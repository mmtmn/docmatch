//! Grafo de evidências sobre DuckDB (esqueleto; a fase 1 do plano o liga à cascata).
//!
//! Tabelas: `item`, `fact`, `fact_arg`, `fact_qual`, `lineage`, `source_reliability`.
//! [`EvidenceGraph::add_item`] e [`EvidenceGraph::add_fact`] gravam; [`EvidenceGraph::query`]
//! roda consultas somente leitura; [`EvidenceGraph::explain`] lista os fatos que citam um
//! item. [`Constraint`] compila restrições para SQL sobre duas tabelas registradas.
//!
//! O banco abre com a mesma blindagem do `dm-sandbox` ([`dm_sandbox::open_hardened`]: sem
//! acesso externo, sem autoload de extensões, configuração travada) e toda consulta passa
//! pelo mesmo validador léxico ([`dm_sandbox::validate_sql`]), porque a fase 1 roda aqui o
//! `body_sql` de regras descobertas pelo modelo.
#![forbid(unsafe_code)]

use dm_core::{Fact, Item};
use dm_sandbox::{open_hardened, validate_sql, SandboxConfig};
use duckdb::types::ValueRef;
use duckdb::{params, Connection};

/// Characters a query may have (same bar as a sandbox probe).
const MAX_QUERY_CHARS: usize = 20_000;
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Errors of the graph.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum GraphError {
    /// DuckDB could not open or create the schema.
    #[error("falha ao abrir o grafo: {0}")]
    Open(String),
    /// A statement failed.
    #[error("erro de consulta: {0}")]
    Query(String),
    /// JSON (de)serialization failed.
    #[error("serialização: {0}")]
    Serde(String),
    /// The query is not read-only.
    #[error("consulta recusada: {0}")]
    Rejected(String),
}

/// JSON columns are stored as text so the graph works without the DuckDB JSON extension.
const SCHEMA: &str = "
CREATE TABLE IF NOT EXISTS item (
  id VARCHAR PRIMARY KEY, kind VARCHAR NOT NULL, source_id VARCHAR NOT NULL,
  locator VARCHAR NOT NULL, fields VARCHAR NOT NULL, time_ref VARCHAR, mentions VARCHAR NOT NULL);
CREATE TABLE IF NOT EXISTS fact (
  id VARCHAR PRIMARY KEY, rel VARCHAR NOT NULL, valid_time VARCHAR, tx_time VARCHAR NOT NULL,
  confidence DOUBLE NOT NULL, rule_id VARCHAR);
CREATE TABLE IF NOT EXISTS fact_arg (
  fact_id VARCHAR NOT NULL, role VARCHAR NOT NULL, item_id VARCHAR NOT NULL, ord INTEGER, qty DECIMAL(28,10));
CREATE TABLE IF NOT EXISTS fact_qual (fact_id VARCHAR NOT NULL, key VARCHAR NOT NULL, value VARCHAR NOT NULL);
CREATE TABLE IF NOT EXISTS lineage (
  fact_id VARCHAR NOT NULL, stage VARCHAR NOT NULL, inputs VARCHAR NOT NULL, tool VARCHAR, model VARCHAR, run_id VARCHAR);
CREATE TABLE IF NOT EXISTS source_reliability (source_id VARCHAR PRIMARY KEY, score DOUBLE NOT NULL, note VARCHAR);
";

/// Rows of a read-only query, every cell rendered as text (`None` for NULL).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct QueryResult {
    /// Column names.
    pub columns: Vec<String>,
    /// Rows.
    pub rows: Vec<Vec<Option<String>>>,
}

/// A fact as seen from one item.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct FactSummary {
    /// Fact id.
    pub id: String,
    /// Relation.
    pub rel: String,
    /// Confidence.
    pub confidence: f64,
    /// Rule, when any.
    pub rule_id: Option<String>,
    /// `(role, item_id)` arguments.
    pub args: Vec<(String, String)>,
}

/// Everything the graph knows about an item.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Explanation {
    /// Item id.
    pub item_id: String,
    /// Whether the item is stored.
    pub known: bool,
    /// Facts citing the item.
    pub facts: Vec<FactSummary>,
}

/// The DuckDB-backed graph.
pub struct EvidenceGraph {
    conn: Connection,
}

fn q(e: duckdb::Error) -> GraphError {
    GraphError::Query(e.to_string())
}

fn to_json<T: Serialize>(v: &T) -> Result<String, GraphError> {
    serde_json::to_string(v).map_err(|e| GraphError::Serde(e.to_string()))
}

/// Serializes a unit-variant enum to its snake_case name.
fn enum_name<T: Serialize>(v: &T) -> Result<String, GraphError> {
    let value = serde_json::to_value(v).map_err(|e| GraphError::Serde(e.to_string()))?;
    value
        .as_str()
        .map(ToString::to_string)
        .ok_or_else(|| GraphError::Serde("enum não é string".to_string()))
}

impl EvidenceGraph {
    /// Opens an empty in-memory graph on a hardened DuckDB with the default sandbox limits.
    pub fn open_in_memory() -> Result<Self, GraphError> {
        EvidenceGraph::open_in_memory_with(&SandboxConfig::default())
    }

    /// Opens an empty in-memory graph on a hardened DuckDB (`memory_limit` and `threads` of
    /// the config apply; the probe limits do not).
    pub fn open_in_memory_with(config: &SandboxConfig) -> Result<Self, GraphError> {
        let conn = open_hardened(config).map_err(|e| GraphError::Open(e.to_string()))?;
        conn.execute_batch(SCHEMA)
            .map_err(|e| GraphError::Open(e.to_string()))?;
        Ok(EvidenceGraph { conn })
    }

    /// Inserts or replaces an item.
    pub fn add_item(&self, item: &Item) -> Result<(), GraphError> {
        let time = match &item.time {
            Some(t) => Some(to_json(t)?),
            None => None,
        };
        self.conn
            .execute(
                "INSERT OR REPLACE INTO item (id, kind, source_id, locator, fields, time_ref, mentions) VALUES (?, ?, ?, ?, ?, ?, ?)",
                params![item.id, enum_name(&item.kind)?, item.source_id, to_json(&item.locator)?, to_json(&item.fields)?, time, to_json(&item.entity_mentions)?],
            )
            .map_err(q)?;
        Ok(())
    }

    /// Inserts or replaces a fact with its arguments, qualifiers and lineage.
    pub fn add_fact(&self, fact: &Fact) -> Result<(), GraphError> {
        let valid_time = match &fact.valid_time {
            Some(t) => Some(to_json(t)?),
            None => None,
        };
        self.conn.execute_batch("BEGIN").map_err(q)?;
        let result = (|| {
            for table in ["fact_arg", "fact_qual", "lineage"] {
                self.conn
                    .execute(
                        &format!("DELETE FROM {table} WHERE fact_id = ?"),
                        params![fact.id],
                    )
                    .map_err(q)?;
            }
            self.conn
                .execute(
                    "INSERT OR REPLACE INTO fact (id, rel, valid_time, tx_time, confidence, rule_id) VALUES (?, ?, ?, ?, ?, ?)",
                    params![fact.id, enum_name(&fact.rel)?, valid_time, fact.tx_time.to_rfc3339(), fact.confidence, fact.rule_id],
                )
                .map_err(q)?;
            for arg in &fact.args {
                self.conn
                    .execute(
                        "INSERT INTO fact_arg (fact_id, role, item_id, ord, qty) VALUES (?, ?, ?, ?, CAST(? AS DECIMAL(28,10)))",
                        params![fact.id, arg.role, arg.item_id, arg.ord, arg.qty.map(|d| d.to_string())],
                    )
                    .map_err(q)?;
            }
            for qual in &fact.qualifiers {
                self.conn
                    .execute(
                        "INSERT INTO fact_qual (fact_id, key, value) VALUES (?, ?, ?)",
                        params![fact.id, qual.key, to_json(&qual.value)?],
                    )
                    .map_err(q)?;
            }
            self.conn
                .execute(
                    "INSERT INTO lineage (fact_id, stage, inputs, tool, model, run_id) VALUES (?, ?, ?, ?, ?, ?)",
                    params![fact.id, fact.lineage.stage, to_json(&fact.lineage.inputs)?, fact.lineage.tool, fact.lineage.model, fact.lineage.run_id],
                )
                .map_err(q)?;
            Ok::<(), GraphError>(())
        })();
        match result {
            Ok(()) => self.conn.execute_batch("COMMIT").map_err(q),
            Err(e) => {
                let _ = self.conn.execute_batch("ROLLBACK");
                Err(e)
            }
        }
    }

    /// Records how much a source can be trusted (`0..=1`).
    pub fn set_source_reliability(
        &self,
        source_id: &str,
        score: f64,
        note: Option<&str>,
    ) -> Result<(), GraphError> {
        self.conn
            .execute("INSERT OR REPLACE INTO source_reliability (source_id, score, note) VALUES (?, ?, ?)", params![source_id, score.clamp(0.0, 1.0), note])
            .map_err(q)?;
        Ok(())
    }

    /// Reliability of a source, when recorded.
    pub fn source_reliability(&self, source_id: &str) -> Result<Option<f64>, GraphError> {
        let mut stmt = self
            .conn
            .prepare("SELECT score FROM source_reliability WHERE source_id = ?")
            .map_err(q)?;
        let mut rows = stmt.query(params![source_id]).map_err(q)?;
        match rows.next().map_err(q)? {
            Some(row) => row.get::<_, f64>(0).map(Some).map_err(q),
            None => Ok(None),
        }
    }

    /// Runs a read-only query: one `SELECT`/`WITH` statement that passes the lexical
    /// validator of `dm-sandbox` (no file functions, settings, extensions, comments or second
    /// statements). The connection itself has no external access, so the validator is the
    /// first line, not the only one.
    pub fn query(&self, sql: &str) -> Result<QueryResult, GraphError> {
        validate_sql(sql, MAX_QUERY_CHARS).map_err(|e| GraphError::Rejected(e.to_string()))?;
        let trimmed = sql.trim().trim_end_matches(';');
        let mut stmt = self.conn.prepare(trimmed).map_err(q)?;
        let mut rows = stmt.query([]).map_err(q)?;
        let mut out = Vec::new();
        let mut width = 0usize;
        while let Some(row) = rows.next().map_err(q)? {
            if width == 0 {
                width = row.as_ref().column_count();
            }
            let mut cells = Vec::with_capacity(width);
            for i in 0..width {
                cells.push(cell_text(row.get_ref(i).map_err(q)?));
            }
            out.push(cells);
        }
        drop(rows);
        Ok(QueryResult {
            columns: stmt.column_names(),
            rows: out,
        })
    }

    /// Facts that cite an item.
    pub fn explain(&self, item_id: &str) -> Result<Explanation, GraphError> {
        let known = self.count_where("item", "id = ?", item_id)? > 0;
        let mut stmt = self
            .conn
            .prepare("SELECT DISTINCT f.id, f.rel, f.confidence, f.rule_id FROM fact f JOIN fact_arg a ON a.fact_id = f.id WHERE a.item_id = ? ORDER BY f.id")
            .map_err(q)?;
        let facts: Vec<(String, String, f64, Option<String>)> = stmt
            .query_map(params![item_id], |r| {
                Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?))
            })
            .map_err(q)?
            .collect::<Result<_, _>>()
            .map_err(q)?;
        let mut out = Vec::with_capacity(facts.len());
        for (id, rel, confidence, rule_id) in facts {
            let mut args_stmt = self.conn.prepare("SELECT role, item_id FROM fact_arg WHERE fact_id = ? ORDER BY ord NULLS LAST, role").map_err(q)?;
            let args: Vec<(String, String)> = args_stmt
                .query_map(params![id], |r| Ok((r.get(0)?, r.get(1)?)))
                .map_err(q)?
                .collect::<Result<_, _>>()
                .map_err(q)?;
            out.push(FactSummary {
                id,
                rel,
                confidence,
                rule_id,
                args,
            });
        }
        Ok(Explanation {
            item_id: item_id.to_string(),
            known,
            facts: out,
        })
    }

    /// Number of items.
    pub fn item_count(&self) -> Result<u64, GraphError> {
        self.count_where("item", "1 = 1", "")
    }

    /// Number of facts.
    pub fn fact_count(&self) -> Result<u64, GraphError> {
        self.count_where("fact", "1 = 1", "")
    }

    fn count_where(&self, table: &str, predicate: &str, param: &str) -> Result<u64, GraphError> {
        let sql = format!("SELECT COUNT(*) FROM {table} WHERE {predicate}");
        if predicate.contains('?') {
            self.conn
                .query_row(&sql, params![param], |r| r.get::<_, i64>(0))
                .map(|n| n.max(0) as u64)
                .map_err(q)
        } else {
            self.conn
                .query_row(&sql, [], |r| r.get::<_, i64>(0))
                .map(|n| n.max(0) as u64)
                .map_err(q)
        }
    }
}

fn cell_text(v: ValueRef<'_>) -> Option<String> {
    match v {
        ValueRef::Null => None,
        ValueRef::Text(t) => Some(String::from_utf8_lossy(t).to_string()),
        ValueRef::Boolean(b) => Some(b.to_string()),
        ValueRef::TinyInt(i) => Some(i.to_string()),
        ValueRef::SmallInt(i) => Some(i.to_string()),
        ValueRef::Int(i) => Some(i.to_string()),
        ValueRef::BigInt(i) => Some(i.to_string()),
        ValueRef::HugeInt(i) => Some(i.to_string()),
        ValueRef::UTinyInt(i) => Some(i.to_string()),
        ValueRef::USmallInt(i) => Some(i.to_string()),
        ValueRef::UInt(i) => Some(i.to_string()),
        ValueRef::UBigInt(i) => Some(i.to_string()),
        ValueRef::Float(f) => Some(f.to_string()),
        ValueRef::Double(f) => Some(f.to_string()),
        ValueRef::Decimal(d) => Some(d.to_string()),
        other => Some(format!("{other:?}")),
    }
}

/// A constraint between two registered tables, compiled to SQL that returns the rows (or
/// pairs) that satisfy it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Constraint {
    /// `left.left_field = right.right_field`.
    Equality {
        /// Left column.
        left_field: String,
        /// Right column.
        right_field: String,
    },
    /// `|left.left_field - right.right_field| <= abs_centavos / 100`.
    Tolerance {
        /// Left money column.
        left_field: String,
        /// Right money column.
        right_field: String,
        /// Tolerance in centavos.
        abs_centavos: i64,
    },
    /// Sum of `left.sum_field` grouped by `left.group_field` equals `right.total_field` where
    /// `right.key_field = left.group_field`.
    Aggregation {
        /// Left grouping column.
        group_field: String,
        /// Left column to sum.
        sum_field: String,
        /// Right key column.
        key_field: String,
        /// Right total column.
        total_field: String,
    },
    /// `left.left_field < right.right_field` (left happens before right).
    TemporalOrder {
        /// Left date column.
        left_field: String,
        /// Right date column.
        right_field: String,
    },
    /// Rows of the left table where `field` is not null.
    Existence {
        /// Column.
        field: String,
    },
    /// Values of `field` that occur exactly once in the left table.
    Uniqueness {
        /// Column.
        field: String,
    },
}

fn ident(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

impl Constraint {
    /// Compiles to a `SELECT` over `left` and `right` (table names, quoted by this function).
    pub fn to_sql(&self, left: &str, right: &str) -> String {
        let (l, r) = (ident(left), ident(right));
        match self {
            Constraint::Equality { left_field, right_field } => format!(
                "SELECT l.rowid AS left_row, r.rowid AS right_row FROM {l} AS l JOIN {r} AS r ON l.{} = r.{}",
                ident(left_field),
                ident(right_field)
            ),
            Constraint::Tolerance { left_field, right_field, abs_centavos } => format!(
                "SELECT l.rowid AS left_row, r.rowid AS right_row, abs(l.{lf} - r.{rf}) AS diff FROM {l} AS l JOIN {r} AS r ON abs(l.{lf} - r.{rf}) <= CAST({abs_centavos} AS DECIMAL(18,2)) / 100",
                lf = ident(left_field),
                rf = ident(right_field)
            ),
            Constraint::Aggregation { group_field, sum_field, key_field, total_field } => format!(
                "SELECT l.{g} AS group_key, SUM(l.{s}) AS parts_total, r.{t} AS whole FROM {l} AS l JOIN {r} AS r ON r.{k} = l.{g} GROUP BY l.{g}, r.{t} HAVING SUM(l.{s}) = r.{t}",
                g = ident(group_field),
                s = ident(sum_field),
                k = ident(key_field),
                t = ident(total_field)
            ),
            Constraint::TemporalOrder { left_field, right_field } => format!(
                "SELECT l.rowid AS left_row, r.rowid AS right_row FROM {l} AS l JOIN {r} AS r ON l.{} < r.{}",
                ident(left_field),
                ident(right_field)
            ),
            Constraint::Existence { field } => format!("SELECT rowid AS left_row FROM {l} WHERE {} IS NOT NULL", ident(field)),
            Constraint::Uniqueness { field } => format!("SELECT {f} AS value FROM {l} GROUP BY {f} HAVING COUNT(*) = 1", f = ident(field)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{DateTime, Utc};
    use dm_core::{FactArg, FactRel, ItemKind, Lineage, Locator, Qualifier, Value};
    use indexmap::IndexMap;

    fn item(id: &str) -> Item {
        let mut fields = IndexMap::new();
        fields.insert("valor".to_string(), Value::Integer(10));
        Item {
            id: id.into(),
            kind: ItemKind::Row,
            source_id: "src".into(),
            locator: Locator::Row {
                table: "t".into(),
                row: 0,
            },
            fields,
            time: None,
            entity_mentions: vec![],
        }
    }

    fn fact(id: &str, a: &str, b: &str) -> Fact {
        Fact {
            id: id.into(),
            rel: FactRel::Matches,
            args: vec![
                FactArg {
                    role: "title".into(),
                    item_id: a.into(),
                    ord: Some(1),
                    qty: None,
                },
                FactArg {
                    role: "payment".into(),
                    item_id: b.into(),
                    ord: Some(2),
                    qty: Some(dm_core::Decimal::ONE),
                },
            ],
            qualifiers: vec![Qualifier {
                key: "rule".into(),
                value: Value::from("exact"),
            }],
            valid_time: None,
            tx_time: DateTime::<Utc>::from_timestamp(1_700_000_000, 0).unwrap(),
            confidence: 0.9,
            rule_id: Some("rule_exact".into()),
            lineage: Lineage {
                stage: "exact_match".into(),
                inputs: vec!["it_a".into()],
                ..Lineage::default()
            },
        }
    }

    #[test]
    fn stores_items_and_facts_and_explains() {
        let g = EvidenceGraph::open_in_memory().unwrap();
        g.add_item(&item("it_a")).unwrap();
        g.add_item(&item("it_b")).unwrap();
        g.add_item(&item("it_a")).unwrap();
        assert_eq!(g.item_count().unwrap(), 2);
        g.add_fact(&fact("fact_1", "it_a", "it_b")).unwrap();
        g.add_fact(&fact("fact_1", "it_a", "it_b")).unwrap();
        assert_eq!(g.fact_count().unwrap(), 1);
        let e = g.explain("it_a").unwrap();
        assert!(e.known);
        assert_eq!(e.facts.len(), 1);
        assert_eq!(e.facts[0].rel, "matches");
        assert_eq!(
            e.facts[0].args,
            vec![
                ("title".to_string(), "it_a".to_string()),
                ("payment".to_string(), "it_b".to_string())
            ]
        );
        assert!(!g.explain("it_zz").unwrap().known);
        let r = g
            .query("SELECT fact_id, key, value FROM fact_qual")
            .unwrap();
        assert_eq!(r.columns, vec!["fact_id", "key", "value"]);
        assert_eq!(r.rows[0][1].as_deref(), Some("rule"));
        let r = g
            .query("SELECT COUNT(*) FROM fact_arg WHERE qty = 1")
            .unwrap();
        assert_eq!(r.rows[0][0].as_deref(), Some("1"));
        assert!(matches!(
            g.query("DELETE FROM fact"),
            Err(GraphError::Rejected(_))
        ));
        assert!(matches!(
            g.query("SELECT 1; SELECT 2"),
            Err(GraphError::Rejected(_))
        ));
        assert!(matches!(
            g.query("SELECT * FROM nope"),
            Err(GraphError::Query(_))
        ));
        // the sandbox validator and the hardened connection both stand between a query and
        // the filesystem
        assert!(matches!(
            g.query("SELECT * FROM read_csv('/etc/passwd')"),
            Err(GraphError::Rejected(_))
        ));
        assert!(matches!(
            g.query("SELECT 1 -- comentário"),
            Err(GraphError::Rejected(_))
        ));
        assert!(
            g.query("SELECT * FROM 'x.csv'").is_err(),
            "sem acesso externo, um arquivo nunca abre"
        );
        let r = g
            .query("SELECT CAST(current_setting('enable_external_access') AS VARCHAR)")
            .unwrap();
        assert_eq!(r.rows[0][0].as_deref(), Some("false"));
        let r = g
            .query("SELECT CAST(current_setting('lock_configuration') AS VARCHAR)")
            .unwrap();
        assert_eq!(r.rows[0][0].as_deref(), Some("true"));
        g.set_source_reliability("src", 1.7, Some("extrato oficial"))
            .unwrap();
        assert_eq!(g.source_reliability("src").unwrap(), Some(1.0));
        assert_eq!(g.source_reliability("other").unwrap(), None);
    }

    #[test]
    fn constraints_compile_and_run() {
        let conn = Connection::open_in_memory().unwrap();
        conn.execute_batch(
            "CREATE TABLE titulos (doc VARCHAR, valor DECIMAL(18,2), venc DATE, pedido VARCHAR);
             CREATE TABLE extrato (doc VARCHAR, valor DECIMAL(18,2), data DATE, total DECIMAL(18,2));
             INSERT INTO titulos VALUES ('88231', 100.00, DATE '2025-03-05', 'PC1'), ('88232', 50.00, DATE '2025-03-06', 'PC1'), ('88233', 50.00, DATE '2025-03-07', 'PC2');
             INSERT INTO extrato VALUES ('88231', 100.01, DATE '2025-03-06', 0), ('PC1', 150.00, DATE '2025-03-10', 150.00);",
        )
        .unwrap();
        let run = |c: &Constraint| -> usize {
            let sql = c.to_sql("titulos", "extrato");
            let mut stmt = conn.prepare(&sql).unwrap();
            let mut rows = stmt.query([]).unwrap();
            let mut n = 0;
            while rows.next().unwrap().is_some() {
                n += 1;
            }
            n
        };
        assert_eq!(
            run(&Constraint::Equality {
                left_field: "doc".into(),
                right_field: "doc".into()
            }),
            1
        );
        assert_eq!(
            run(&Constraint::Tolerance {
                left_field: "valor".into(),
                right_field: "valor".into(),
                abs_centavos: 1
            }),
            1
        );
        assert_eq!(
            run(&Constraint::Tolerance {
                left_field: "valor".into(),
                right_field: "valor".into(),
                abs_centavos: 0
            }),
            0
        );
        assert_eq!(
            run(&Constraint::Aggregation {
                group_field: "pedido".into(),
                sum_field: "valor".into(),
                key_field: "doc".into(),
                total_field: "total".into()
            }),
            1
        );
        assert_eq!(
            run(&Constraint::TemporalOrder {
                left_field: "venc".into(),
                right_field: "data".into()
            }),
            4
        );
        assert_eq!(
            run(&Constraint::Existence {
                field: "pedido".into()
            }),
            3
        );
        assert_eq!(
            run(&Constraint::Uniqueness {
                field: "valor".into()
            }),
            1
        );
        let sql = Constraint::Equality {
            left_field: "a\"b".into(),
            right_field: "c".into(),
        }
        .to_sql("t", "u");
        assert!(sql.contains("\"a\"\"b\""));
    }
}
