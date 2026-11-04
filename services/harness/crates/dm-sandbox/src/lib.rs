//! DuckDB em memória, sem acesso externo, para sondas SQL somente leitura.
//!
//! O banco abre com `enable_external_access=false`, sem autoload de extensões, com limite de
//! memória, poucas threads, diretório temporário desativado e `lock_configuration=true`, de
//! modo que nenhuma consulta consegue reabilitar nada. [`Sandbox::run_probe`] aceita uma única
//! instrução `SELECT`/`WITH` depois de passar pelo validador léxico de [`validate`], roda em
//! outra thread e é interrompida ao estourar o tempo.
#![forbid(unsafe_code)]

pub mod validate;

use std::sync::mpsc;
use std::time::{Duration, Instant};

use dm_core::{ColumnRole, Table, Value};
use duckdb::types::{TimeUnit, ValueRef};
use duckdb::{params_from_iter, Config, Connection};
use serde::{Deserialize, Serialize};
use thiserror::Error;

pub use validate::{validate_sql, SqlRejection};

/// Sandbox limits.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SandboxConfig {
    /// DuckDB memory limit (`512MB`).
    pub memory_limit: String,
    /// Query timeout.
    pub timeout: Duration,
    /// DuckDB threads.
    pub threads: i64,
    /// Rows returned at most by a probe; more sets `truncated`.
    pub max_rows: usize,
    /// Characters a probe may have.
    pub max_sql_chars: usize,
}

impl Default for SandboxConfig {
    fn default() -> Self {
        SandboxConfig {
            memory_limit: "512MB".to_string(),
            timeout: Duration::from_secs(5),
            threads: 2,
            max_rows: 10_000,
            max_sql_chars: 20_000,
        }
    }
}

/// Errors of the sandbox.
#[derive(Debug, Clone, PartialEq, Eq, Error, Serialize, Deserialize)]
pub enum SandboxError {
    /// The SQL was refused before execution.
    #[error("consulta recusada: {0}")]
    Rejected(SqlRejection),
    /// DuckDB failed to open or configure the database.
    #[error("falha ao abrir o sandbox: {0}")]
    Open(String),
    /// DuckDB returned an error while running.
    #[error("erro na consulta: {0}")]
    Query(String),
    /// The query exceeded the timeout and was interrupted.
    #[error("consulta interrompida após {0} ms")]
    Timeout(u64),
    /// A table name is not a plain identifier.
    #[error("nome de tabela inválido: {0:?}")]
    TableName(String),
}

/// Rows of a probe, as JSON values (decimals as strings, dates as ISO strings).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ProbeResult {
    /// Column names.
    pub columns: Vec<String>,
    /// Rows.
    pub rows: Vec<Vec<serde_json::Value>>,
    /// True when `max_rows` cut the result.
    pub truncated: bool,
    /// Wall time.
    pub elapsed_ms: u64,
}

/// An in-memory DuckDB with the hardening applied.
pub struct Sandbox {
    conn: Connection,
    config: SandboxConfig,
}

/// Opens an in-memory DuckDB connection with the hardening applied: external access and
/// extension autoload off, memory and thread limits, no temp files, configuration locked.
/// The sandbox and the evidence graph (`dm-graph`) both open their databases here, so there
/// is a single definition of "hardened"; nothing run on the connection afterwards can undo
/// it, because `lock_configuration` is the last setting applied.
pub fn open_hardened(config: &SandboxConfig) -> Result<Connection, SandboxError> {
    let open = |e: duckdb::Error| SandboxError::Open(e.to_string());
    let flags = Config::default()
        .enable_external_access(false)
        .map_err(open)?
        .enable_autoload_extension(false)
        .map_err(open)?
        .max_memory(&config.memory_limit)
        .map_err(open)?
        .threads(config.threads)
        .map_err(open)?;
    let conn = Connection::open_in_memory_with_flags(flags).map_err(open)?;
    // No spilling to disk: an empty temp_directory disables temp files; builds that refuse
    // the empty string get a zero-byte cap instead.
    if conn.execute_batch("SET temp_directory=''").is_err() {
        conn.execute_batch("SET max_temp_directory_size='0 bytes'")
            .map_err(open)?;
    }
    // Belt and braces: settings that may be unknown to a given DuckDB build are optional.
    for optional in [
        "SET disabled_filesystems='LocalFileSystem,HTTPFileSystem,S3FileSystem'",
        "SET allow_persistent_secrets=false",
        "SET enable_object_cache=false",
    ] {
        let _ = conn.execute_batch(optional);
    }
    conn.execute_batch("SET lock_configuration=true")
        .map_err(open)?;
    Ok(conn)
}

impl Sandbox {
    /// Opens a hardened in-memory database (see [`open_hardened`]).
    pub fn new(config: SandboxConfig) -> Result<Self, SandboxError> {
        let conn = open_hardened(&config)?;
        Ok(Sandbox { conn, config })
    }

    /// Opens with the default limits.
    pub fn with_defaults() -> Result<Self, SandboxError> {
        Sandbox::new(SandboxConfig::default())
    }

    /// Current value of a DuckDB setting (for tests and diagnostics).
    pub fn setting(&self, name: &str) -> Result<String, SandboxError> {
        self.conn
            .query_row("SELECT CAST(current_setting(?) AS VARCHAR)", [name], |r| {
                r.get::<_, String>(0)
            })
            .map_err(|e| SandboxError::Query(e.to_string()))
    }

    /// Registers a [`Table`] as a DuckDB table with typed columns: money as `DECIMAL(18,2)`,
    /// dates as `DATE`/`TIMESTAMP`, integers as `BIGINT`, booleans, everything else `VARCHAR`.
    pub fn register_table(&self, name: &str, table: &Table) -> Result<(), SandboxError> {
        if name.is_empty()
            || !name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
            || name.starts_with(|c: char| c.is_ascii_digit())
        {
            return Err(SandboxError::TableName(name.to_string()));
        }
        let types: Vec<&'static str> = (0..table.columns.len())
            .map(|i| sql_type(table, i))
            .collect();
        let column_defs: Vec<String> = table
            .columns
            .iter()
            .zip(&types)
            .map(|(c, t)| format!("\"{}\" {t}", c.name.replace('"', "\"\"")))
            .collect();
        let create = format!("CREATE TABLE \"{name}\" ({})", column_defs.join(", "));
        let q = |e: duckdb::Error| SandboxError::Query(e.to_string());
        self.conn
            .execute_batch(&format!("DROP TABLE IF EXISTS \"{name}\"; {create}"))
            .map_err(q)?;
        if table.rows.is_empty() {
            return Ok(());
        }
        let placeholders: Vec<String> = types.iter().map(|t| format!("CAST(? AS {t})")).collect();
        let insert = format!(
            "INSERT INTO \"{name}\" VALUES ({})",
            placeholders.join(", ")
        );
        self.conn.execute_batch("BEGIN").map_err(q)?;
        let result = (|| {
            let mut stmt = self.conn.prepare(&insert).map_err(q)?;
            for record in &table.rows {
                let params: Vec<Option<String>> = record
                    .values
                    .iter()
                    .zip(&types)
                    .map(|(v, t)| param_text(v, t))
                    .collect();
                stmt.execute(params_from_iter(params.iter())).map_err(q)?;
            }
            Ok::<(), SandboxError>(())
        })();
        match result {
            Ok(()) => self.conn.execute_batch("COMMIT").map_err(q),
            Err(e) => {
                let _ = self.conn.execute_batch("ROLLBACK");
                Err(e)
            }
        }
    }

    /// Runs one read-only statement with the timeout, after validation.
    pub fn run_probe(&self, sql: &str) -> Result<ProbeResult, SandboxError> {
        validate_sql(sql, self.config.max_sql_chars).map_err(SandboxError::Rejected)?;
        let worker = self
            .conn
            .try_clone()
            .map_err(|e| SandboxError::Open(e.to_string()))?;
        let handle = worker.interrupt_handle();
        let sql_owned = sql.trim().trim_end_matches(';').to_string();
        let max_rows = self.config.max_rows;
        let (tx, rx) = mpsc::channel();
        let started = Instant::now();
        std::thread::spawn(move || {
            // A panic inside the driver (an untyped NULL column, an exotic type) must not take
            // the sandbox down: it becomes a query error.
            let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                execute(&worker, &sql_owned, max_rows)
            }))
            .unwrap_or_else(|_| {
                Err(SandboxError::Query(
                    "falha interna ao ler o resultado (tipo de coluna sem suporte)".to_string(),
                ))
            });
            let _ = tx.send(result);
        });
        match rx.recv_timeout(self.config.timeout) {
            Ok(result) => result.map(|(columns, rows, truncated)| ProbeResult {
                columns,
                rows,
                truncated,
                elapsed_ms: started.elapsed().as_millis() as u64,
            }),
            Err(mpsc::RecvTimeoutError::Timeout) => {
                handle.interrupt();
                // Give the worker a moment to observe the interrupt before reporting.
                let _ = rx.recv_timeout(Duration::from_secs(5));
                Err(SandboxError::Timeout(self.config.timeout.as_millis() as u64))
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => Err(SandboxError::Query(
                "thread da consulta terminou sem resposta".to_string(),
            )),
        }
    }

    /// Limits in effect.
    pub fn config(&self) -> &SandboxConfig {
        &self.config
    }
}

type Executed = Result<(Vec<String>, Vec<Vec<serde_json::Value>>, bool), SandboxError>;

fn execute(conn: &Connection, sql: &str, max_rows: usize) -> Executed {
    let q = |e: duckdb::Error| SandboxError::Query(e.to_string());
    let mut stmt = conn.prepare(sql).map_err(q)?;
    let mut rows = stmt.query([]).map_err(q)?;
    let mut out = Vec::new();
    let mut truncated = false;
    let mut width = 0;
    while let Some(row) = rows.next().map_err(q)? {
        if out.len() >= max_rows {
            truncated = true;
            break;
        }
        if width == 0 {
            width = row.as_ref().column_count();
        }
        let mut values = Vec::with_capacity(width);
        for i in 0..width {
            let v = row.get_ref(i).map_err(q)?;
            values.push(json_value(v));
        }
        out.push(values);
    }
    drop(rows);
    let columns = stmt.column_names();
    Ok((columns, out, truncated))
}

fn json_value(v: ValueRef<'_>) -> serde_json::Value {
    use serde_json::Value as J;
    match v {
        ValueRef::Null => J::Null,
        ValueRef::Boolean(b) => J::Bool(b),
        ValueRef::TinyInt(i) => J::from(i),
        ValueRef::SmallInt(i) => J::from(i),
        ValueRef::Int(i) => J::from(i),
        ValueRef::BigInt(i) => J::from(i),
        ValueRef::UTinyInt(i) => J::from(i),
        ValueRef::USmallInt(i) => J::from(i),
        ValueRef::UInt(i) => J::from(i),
        ValueRef::UBigInt(i) => J::from(i),
        ValueRef::HugeInt(i) => J::String(i.to_string()),
        ValueRef::UHugeInt(i) => J::String(i.to_string()),
        ValueRef::Float(f) => serde_json::Number::from_f64(f64::from(f)).map_or(J::Null, J::Number),
        ValueRef::Double(f) => serde_json::Number::from_f64(f).map_or(J::Null, J::Number),
        ValueRef::Decimal(d) => J::String(d.to_string()),
        ValueRef::Text(t) => J::String(String::from_utf8_lossy(t).to_string()),
        ValueRef::Blob(b) => J::String(format!("<blob {} bytes>", b.len())),
        ValueRef::Date32(days) => J::String(
            chrono::DateTime::from_timestamp(i64::from(days) * 86_400, 0)
                .map_or_else(|| days.to_string(), |d| d.format("%Y-%m-%d").to_string()),
        ),
        ValueRef::Timestamp(unit, value) => J::String(timestamp_string(unit, value)),
        ValueRef::Time64(unit, value) => J::String(time_string(unit, value)),
        other => J::String(format!("{other:?}")),
    }
}

fn unit_to_micros(unit: TimeUnit, value: i64) -> i64 {
    match unit {
        TimeUnit::Second => value.saturating_mul(1_000_000),
        TimeUnit::Millisecond => value.saturating_mul(1_000),
        TimeUnit::Microsecond => value,
        TimeUnit::Nanosecond => value / 1_000,
    }
}

fn timestamp_string(unit: TimeUnit, value: i64) -> String {
    let micros = unit_to_micros(unit, value);
    chrono::DateTime::from_timestamp_micros(micros).map_or_else(
        || value.to_string(),
        |d| d.format("%Y-%m-%dT%H:%M:%S%.6f").to_string(),
    )
}

fn time_string(unit: TimeUnit, value: i64) -> String {
    let micros = unit_to_micros(unit, value);
    let secs = (micros / 1_000_000) as u32;
    let sub = (micros % 1_000_000) as u32;
    chrono::NaiveTime::from_num_seconds_from_midnight_opt(secs, sub * 1_000).map_or_else(
        || value.to_string(),
        |t| t.format("%H:%M:%S%.6f").to_string(),
    )
}

/// SQL type of a column from its role and values.
fn sql_type(table: &Table, col: usize) -> &'static str {
    let column = &table.columns[col];
    let values = table
        .rows
        .iter()
        .filter_map(|r| r.values.get(col))
        .filter(|v| !v.is_null());
    let mut all_money = true;
    let mut all_date = true;
    let mut all_datetime = true;
    let mut all_int = true;
    let mut all_bool = true;
    let mut any = false;
    for v in values {
        any = true;
        all_money &= matches!(v, Value::Money(_));
        all_date &= matches!(v, Value::Date(_));
        all_datetime &= matches!(v, Value::DateTime(_) | Value::Date(_));
        all_int &= matches!(v, Value::Integer(_));
        all_bool &= matches!(v, Value::Bool(_));
    }
    if !any {
        return match column.role {
            ColumnRole::Amount => "DECIMAL(18,2)",
            ColumnRole::Date => "DATE",
            _ => "VARCHAR",
        };
    }
    if all_money {
        "DECIMAL(18,2)"
    } else if all_date {
        "DATE"
    } else if all_datetime {
        "TIMESTAMP"
    } else if all_int {
        "BIGINT"
    } else if all_bool {
        "BOOLEAN"
    } else {
        "VARCHAR"
    }
}

/// Text form of a value for a `CAST(? AS type)` parameter.
fn param_text(v: &Value, sql_type: &str) -> Option<String> {
    match v {
        Value::Null => None,
        Value::Text(s) if s.trim().is_empty() => None,
        Value::Text(s) => Some(s.clone()),
        Value::Money(m) => Some(m.as_decimal().to_string()),
        Value::Number(d) => Some(d.to_string()),
        Value::Integer(i) => Some(i.to_string()),
        Value::Bool(b) => Some(b.to_string()),
        Value::Date(d) => Some(if sql_type == "TIMESTAMP" {
            d.and_hms_opt(0, 0, 0).map_or_else(
                || d.to_string(),
                |dt| dt.format("%Y-%m-%d %H:%M:%S").to_string(),
            )
        } else {
            d.format("%Y-%m-%d").to_string()
        }),
        Value::DateTime(dt) => Some(dt.format("%Y-%m-%d %H:%M:%S").to_string()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use dm_core::{Column, Money};

    fn sample_table() -> Table {
        let mut t = Table::new(
            "titulos",
            "src",
            vec![
                Column::new("Vencimento", ColumnRole::Date),
                Column::new("Fornecedor", ColumnRole::Name),
                Column::new("Valor", ColumnRole::Amount),
                Column::new("Pago", ColumnRole::Status),
            ],
        );
        t.push_row(vec![
            Value::Date(chrono::NaiveDate::from_ymd_opt(2025, 3, 5).unwrap()),
            Value::from("Padaria Central"),
            Value::Money(Money::from_centavos(1_845_000)),
            Value::Bool(false),
        ])
        .unwrap();
        t.push_row(vec![
            Value::Null,
            Value::from("Empório Bela Vista"),
            Value::Money(Money::from_centavos(-99)),
            Value::Bool(true),
        ])
        .unwrap();
        t
    }

    #[test]
    fn hardened_settings_are_locked() {
        let sb = Sandbox::with_defaults().unwrap();
        assert_eq!(sb.setting("enable_external_access").unwrap(), "false");
        assert_eq!(sb.setting("lock_configuration").unwrap(), "true");
        assert_eq!(sb.setting("autoinstall_known_extensions").unwrap(), "false");
        assert_eq!(sb.setting("threads").unwrap(), "2");
    }

    #[test]
    fn registers_tables_with_types_and_probes_them() {
        let sb = Sandbox::with_defaults().unwrap();
        sb.register_table("titulos", &sample_table()).unwrap();
        let r = sb
            .run_probe(
                "SELECT vencimento, fornecedor, valor, pago FROM titulos ORDER BY valor DESC",
            )
            .unwrap();
        assert_eq!(r.columns, vec!["vencimento", "fornecedor", "valor", "pago"]);
        assert_eq!(r.rows.len(), 2);
        assert_eq!(r.rows[0][0], serde_json::json!("2025-03-05"));
        assert_eq!(r.rows[0][1], serde_json::json!("Padaria Central"));
        assert_eq!(r.rows[0][2], serde_json::json!("18450.00"));
        assert_eq!(r.rows[0][3], serde_json::json!(false));
        assert_eq!(r.rows[1][0], serde_json::Value::Null);
        assert!(!r.truncated);
        let sum = sb
            .run_probe("SELECT SUM(valor) AS total FROM titulos")
            .unwrap();
        assert_eq!(sum.rows[0][0], serde_json::json!("18449.01"));
        // re-registering replaces the table
        sb.register_table(
            "titulos",
            &Table::new("x", "s", vec![Column::new("a", ColumnRole::Unknown)]),
        )
        .unwrap();
        assert_eq!(
            sb.run_probe("SELECT count(*) FROM titulos").unwrap().rows[0][0],
            serde_json::json!(0)
        );
        assert!(matches!(
            sb.register_table("bad name", &sample_table()),
            Err(SandboxError::TableName(_))
        ));
        assert!(matches!(
            sb.register_table("1x", &sample_table()),
            Err(SandboxError::TableName(_))
        ));
    }

    #[test]
    fn truncation_and_errors() {
        let sb = Sandbox::new(SandboxConfig {
            max_rows: 3,
            ..SandboxConfig::default()
        })
        .unwrap();
        let r = sb.run_probe("SELECT * FROM range(10)").unwrap();
        assert_eq!(r.rows.len(), 3);
        assert!(r.truncated);
        assert!(matches!(
            sb.run_probe("SELECT * FROM nao_existe"),
            Err(SandboxError::Query(_))
        ));
        assert!(matches!(
            sb.run_probe("DROP TABLE x"),
            Err(SandboxError::Rejected(_))
        ));
        assert_eq!(sb.config().max_rows, 3);
    }

    #[test]
    fn timeout_interrupts_long_queries() {
        let sb = Sandbox::new(SandboxConfig {
            timeout: Duration::from_millis(300),
            ..SandboxConfig::default()
        })
        .unwrap();
        let started = Instant::now();
        let r = sb.run_probe(
            "SELECT count(*) FROM range(200000) a, range(200000) b WHERE a.range + b.range = -1",
        );
        assert!(matches!(r, Err(SandboxError::Timeout(300))), "{r:?}");
        assert!(started.elapsed() < Duration::from_secs(10));
        // the sandbox still works afterwards
        assert_eq!(
            sb.run_probe("SELECT 1").unwrap().rows[0][0],
            serde_json::json!(1)
        );
    }

    #[test]
    fn value_conversions() {
        let sb = Sandbox::with_defaults().unwrap();
        let r = sb.run_probe("SELECT 1::TINYINT, 2::SMALLINT, 3::INTEGER, 4::BIGINT, 5::HUGEINT, 1.5::DOUBLE, 2.50::DECIMAL(10,2), 'x', DATE '2025-03-05', TIMESTAMP '2025-03-05 10:11:12', TIME '10:11:12', NULL::INTEGER, true, 'ab'::BLOB, [1,2]").unwrap();
        let row = &r.rows[0];
        assert_eq!(row[0], serde_json::json!(1));
        assert_eq!(row[4], serde_json::json!("5"));
        assert_eq!(row[5], serde_json::json!(1.5));
        assert_eq!(row[6], serde_json::json!("2.50"));
        assert_eq!(row[7], serde_json::json!("x"));
        assert_eq!(row[8], serde_json::json!("2025-03-05"));
        assert_eq!(row[9], serde_json::json!("2025-03-05T10:11:12.000000"));
        assert_eq!(row[10], serde_json::json!("10:11:12.000000"));
        assert_eq!(row[11], serde_json::Value::Null);
        assert_eq!(row[12], serde_json::json!(true));
        assert_eq!(row[13], serde_json::json!("<blob 2 bytes>"));
        assert!(row[14].is_string());
        // an untyped NULL column makes the driver panic; the sandbox turns that into an error
        assert!(matches!(
            sb.run_probe("SELECT NULL"),
            Err(SandboxError::Query(_))
        ));
        assert_eq!(
            sb.run_probe("SELECT 2").unwrap().rows[0][0],
            serde_json::json!(2)
        );
    }
}
