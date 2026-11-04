//! Descoberta de regras como hipóteses executáveis (esqueleto; a fase 1 liga o laço com o
//! modelo de descoberta).
//!
//! Uma [`Hypothesis`] é uma [`Rule`] com SQL compilado. Ela é reproduzida sobre o histórico
//! confirmado ([`replay_on_confirmed`]), experimentada num conjunto reservado
//! ([`experiment_on_holdout`]), pontuada por simplicidade ([`simplicity_score`]), deduplicada
//! pelo hash mascarado do que seleciona ([`output_hash`], [`dedup_by_output`]) e paga com um
//! orçamento repartido ([`allocate`]).

#![forbid(unsafe_code)]

use dm_core::{stable_id, Money, Rule};
use dm_sandbox::Sandbox;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use thiserror::Error;

/// Errors of the discovery stage.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum DiscoverError {
    /// The sandbox refused or failed the probe.
    #[error("sandbox: {0}")]
    Sandbox(String),
    /// The probe did not return the columns a hypothesis must produce.
    #[error("sonda sem as colunas esperadas (hit, confirmed, amount): {0}")]
    Shape(String),
}

/// A rule plus the SQL it compiles to. The SQL must return one row per candidate with the
/// columns `hit` (BOOLEAN: the rule fires), `confirmed` (BOOLEAN: a person confirmed the
/// pairing) and `amount` (money, centavos or decimal).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Hypothesis {
    /// Deterministic id from the rule id and the SQL.
    pub id: String,
    /// The rule.
    pub rule: Rule,
    /// Compiled SQL.
    pub compiled_sql: String,
    /// Exceptions the rule needs to hold (counts against simplicity).
    pub exceptions: u32,
}

impl Hypothesis {
    /// Builds a hypothesis.
    pub fn new(rule: Rule, compiled_sql: impl Into<String>, exceptions: u32) -> Self {
        let compiled_sql = compiled_sql.into();
        Hypothesis {
            id: stable_id("hyp", &[&rule.id, &compiled_sql]),
            rule,
            compiled_sql,
            exceptions,
        }
    }
}

/// Replay over confirmed history.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ReplayResult {
    /// Hypothesis id.
    pub hypothesis_id: String,
    /// Candidate rows evaluated.
    pub rows: usize,
    /// Rows the rule fired on that were confirmed.
    pub confirmed_hits: usize,
    /// Rows the rule fired on that were not confirmed.
    pub false_positives: usize,
    /// Money of the confirmed hits.
    pub money_explained: Money,
}

/// Experiment on held-out rows.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct HoldoutResult {
    /// Hypothesis id.
    pub hypothesis_id: String,
    /// Rows evaluated.
    pub rows: usize,
    /// `confirmed hits / hits` (1.0 when the rule never fires).
    pub precision: f64,
    /// `confirmed hits / confirmed rows` (1.0 when nothing is confirmed).
    pub recall: f64,
}

struct RowEval {
    hit: bool,
    confirmed: bool,
    amount: Money,
}

fn as_bool(v: &serde_json::Value) -> Option<bool> {
    match v {
        serde_json::Value::Bool(b) => Some(*b),
        serde_json::Value::Number(n) => n.as_i64().map(|i| i != 0),
        serde_json::Value::String(s) => match s.to_ascii_lowercase().as_str() {
            "true" | "t" | "1" | "sim" => Some(true),
            "false" | "f" | "0" | "nao" | "não" => Some(false),
            _ => None,
        },
        _ => None,
    }
}

fn as_money(v: &serde_json::Value) -> Money {
    match v {
        serde_json::Value::Number(n) => n.as_i64().map_or(Money::ZERO, Money::from_centavos),
        serde_json::Value::String(s) => {
            Money::parse_with_style(s, dm_core::money::DecimalStyle::Point).unwrap_or(Money::ZERO)
        }
        _ => Money::ZERO,
    }
}

fn run(h: &Hypothesis, sandbox: &Sandbox) -> Result<Vec<RowEval>, DiscoverError> {
    let result = sandbox
        .run_probe(&h.compiled_sql)
        .map_err(|e| DiscoverError::Sandbox(e.to_string()))?;
    let col = |name: &str| {
        result
            .columns
            .iter()
            .position(|c| c.eq_ignore_ascii_case(name))
            .ok_or_else(|| {
                DiscoverError::Shape(format!("coluna {name} ausente em {:?}", result.columns))
            })
    };
    let (hit, confirmed, amount) = (col("hit")?, col("confirmed")?, col("amount")?);
    result
        .rows
        .iter()
        .map(|row| {
            let get = |i: usize| row.get(i).cloned().unwrap_or(serde_json::Value::Null);
            Ok(RowEval {
                hit: as_bool(&get(hit))
                    .ok_or_else(|| DiscoverError::Shape("hit não é booleano".to_string()))?,
                confirmed: as_bool(&get(confirmed))
                    .ok_or_else(|| DiscoverError::Shape("confirmed não é booleano".to_string()))?,
                amount: as_money(&get(amount)),
            })
        })
        .collect()
}

/// Replays the hypothesis over the confirmed history registered in the sandbox.
///
/// TODO(fase 1): a tabela de histórico confirmado vem do ledger do workspace; aqui o SQL
/// compilado já aponta para ela.
pub fn replay_on_confirmed(
    h: &Hypothesis,
    sandbox: &Sandbox,
) -> Result<ReplayResult, DiscoverError> {
    let rows = run(h, sandbox)?;
    let mut out = ReplayResult {
        hypothesis_id: h.id.clone(),
        rows: rows.len(),
        confirmed_hits: 0,
        false_positives: 0,
        money_explained: Money::ZERO,
    };
    for r in &rows {
        if r.hit && r.confirmed {
            out.confirmed_hits += 1;
            out.money_explained += r.amount.abs();
        } else if r.hit {
            out.false_positives += 1;
        }
    }
    Ok(out)
}

/// Experiments on held-out rows: precision and recall of the rule.
pub fn experiment_on_holdout(
    h: &Hypothesis,
    sandbox: &Sandbox,
) -> Result<HoldoutResult, DiscoverError> {
    let rows = run(h, sandbox)?;
    let hits = rows.iter().filter(|r| r.hit).count();
    let confirmed = rows.iter().filter(|r| r.confirmed).count();
    let both = rows.iter().filter(|r| r.hit && r.confirmed).count();
    let ratio = |num: usize, den: usize| {
        if den == 0 {
            1.0
        } else {
            num as f64 / den as f64
        }
    };
    Ok(HoldoutResult {
        hypothesis_id: h.id.clone(),
        rows: rows.len(),
        precision: ratio(both, hits),
        recall: ratio(both, confirmed),
    })
}

/// Description length of a hypothesis plus a penalty per exception; lower is simpler.
///
/// Length = SQL tokens + natural-language words / 4; each exception costs ten tokens.
pub fn simplicity_score(h: &Hypothesis) -> f64 {
    let sql_tokens = h
        .compiled_sql
        .split(|c: char| !(c.is_alphanumeric() || c == '_' || c == '.'))
        .filter(|t| !t.is_empty())
        .count();
    let words = h.rule.body_nl.split_whitespace().count();
    sql_tokens as f64 + words as f64 / 4.0 + 10.0 * f64::from(h.exceptions)
}

/// Hash of which candidate rows a hypothesis selects (the constants in its SQL do not
/// matter, only the selection), so two phrasings of the same rule collapse.
pub fn output_hash(hits: &[bool]) -> String {
    let mut hasher = Sha256::new();
    for (i, hit) in hits.iter().enumerate() {
        if *hit {
            hasher.update(i.to_le_bytes());
        }
    }
    hex::encode(hasher.finalize())
}

/// Hit pattern of a hypothesis, for [`output_hash`].
pub fn hit_pattern(h: &Hypothesis, sandbox: &Sandbox) -> Result<Vec<bool>, DiscoverError> {
    Ok(run(h, sandbox)?.into_iter().map(|r| r.hit).collect())
}

/// Keeps the simplest hypothesis per output hash, in order of appearance.
pub fn dedup_by_output(hypotheses: Vec<(Hypothesis, String)>) -> Vec<Hypothesis> {
    let mut kept: Vec<(String, Hypothesis)> = Vec::new();
    for (h, hash) in hypotheses {
        match kept.iter_mut().find(|(k, _)| *k == hash) {
            Some((_, existing)) => {
                if simplicity_score(&h) < simplicity_score(existing) {
                    *existing = h;
                }
            }
            None => kept.push((hash, h)),
        }
    }
    kept.into_iter().map(|(_, h)| h).collect()
}

/// Budget for one discovery round.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Budget {
    /// Model tokens for the round.
    pub total_tokens: u64,
    /// Hypotheses to evaluate at most.
    pub max_hypotheses: usize,
    /// Sandbox time for the round.
    pub max_sandbox_ms: u64,
}

impl Default for Budget {
    fn default() -> Self {
        Budget {
            total_tokens: 50_000,
            max_hypotheses: 20,
            max_sandbox_ms: 30_000,
        }
    }
}

/// Splits `total` across `weights` proportionally (largest-remainder rounding, so the parts
/// add up to `total` exactly). Non-positive weights get nothing.
pub fn allocate(total: u64, weights: &[f64]) -> Vec<u64> {
    let positive: Vec<f64> = weights
        .iter()
        .map(|w| if w.is_finite() && *w > 0.0 { *w } else { 0.0 })
        .collect();
    let sum: f64 = positive.iter().sum();
    if sum <= 0.0 || total == 0 {
        return vec![0; weights.len()];
    }
    let exact: Vec<f64> = positive.iter().map(|w| total as f64 * w / sum).collect();
    let mut parts: Vec<u64> = exact.iter().map(|e| e.floor() as u64).collect();
    let mut remainder = total - parts.iter().sum::<u64>();
    let mut order: Vec<usize> = (0..parts.len()).collect();
    order.sort_by(|a, b| {
        (exact[*b] - exact[*b].floor())
            .partial_cmp(&(exact[*a] - exact[*a].floor()))
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(a.cmp(b))
    });
    for i in order {
        if remainder == 0 {
            break;
        }
        if positive[i] > 0.0 {
            parts[i] += 1;
            remainder -= 1;
        }
    }
    parts
}

#[cfg(test)]
mod tests {
    use super::*;
    use dm_core::{
        Column, ColumnRole, RuleProvenance, RuleScope, RuleStats, RuleStatus, Table, Value,
    };

    fn rule(id: &str, nl: &str) -> Rule {
        Rule {
            id: id.into(),
            scope: RuleScope::Global,
            title: "t".into(),
            body_nl: nl.into(),
            body_sql: String::new(),
            provenance: RuleProvenance::Builtin,
            stats: RuleStats::default(),
            version: 1,
            status: RuleStatus::Candidate,
        }
    }

    fn sandbox_with_candidates() -> Sandbox {
        let sb = Sandbox::with_defaults().unwrap();
        let mut t = Table::new(
            "c",
            "s",
            vec![
                Column::new("hit", ColumnRole::Unknown),
                Column::new("confirmed", ColumnRole::Unknown),
                Column::new("amount", ColumnRole::Amount),
            ],
        );
        for (hit, confirmed, amount) in [
            (true, true, 10_000),
            (true, false, 5_000),
            (false, true, 7_000),
            (false, false, 1),
        ] {
            t.push_row(vec![
                Value::Bool(hit),
                Value::Bool(confirmed),
                Value::Money(Money::from_centavos(amount)),
            ])
            .unwrap();
        }
        sb.register_table("candidates", &t).unwrap();
        sb
    }

    #[test]
    fn replay_and_holdout() {
        let sb = sandbox_with_candidates();
        let h = Hypothesis::new(
            rule("r1", "paga com multa"),
            "SELECT hit, confirmed, amount FROM candidates",
            0,
        );
        let r = replay_on_confirmed(&h, &sb).unwrap();
        assert_eq!(r.rows, 4);
        assert_eq!(r.confirmed_hits, 1);
        assert_eq!(r.false_positives, 1);
        assert_eq!(r.money_explained, Money::from_centavos(10_000));
        let x = experiment_on_holdout(&h, &sb).unwrap();
        assert!((x.precision - 0.5).abs() < 1e-9);
        assert!((x.recall - 0.5).abs() < 1e-9);
        assert_eq!(
            hit_pattern(&h, &sb).unwrap(),
            vec![true, true, false, false]
        );
        let bad = Hypothesis::new(rule("r2", ""), "SELECT 1 AS x", 0);
        assert!(matches!(
            replay_on_confirmed(&bad, &sb),
            Err(DiscoverError::Shape(_))
        ));
        let refused = Hypothesis::new(rule("r3", ""), "DROP TABLE candidates", 0);
        assert!(matches!(
            replay_on_confirmed(&refused, &sb),
            Err(DiscoverError::Sandbox(_))
        ));
    }

    #[test]
    fn simplicity_dedup_and_budget() {
        let a = Hypothesis::new(
            rule("a", "regra curta"),
            "SELECT hit, confirmed, amount FROM c",
            0,
        );
        let b = Hypothesis::new(
            rule("b", "uma regra muito mais longa e cheia de palavras"),
            "SELECT hit, confirmed, amount FROM c WHERE 1 = 1 AND 2 = 2",
            2,
        );
        assert!(simplicity_score(&a) < simplicity_score(&b));
        let same = output_hash(&[true, false, true]);
        assert_eq!(same, output_hash(&[true, false, true]));
        assert_ne!(same, output_hash(&[true, true, true]));
        let kept = dedup_by_output(vec![
            (b.clone(), same.clone()),
            (a.clone(), same.clone()),
            (b.clone(), "other".into()),
        ]);
        assert_eq!(kept.len(), 2);
        assert_eq!(kept[0].id, a.id, "the simplest phrasing wins");
        assert_eq!(kept[1].id, b.id);
        assert_eq!(allocate(10, &[1.0, 1.0, 1.0]), vec![4, 3, 3]);
        assert_eq!(allocate(7, &[0.0, 2.0, -1.0, f64::NAN]), vec![0, 7, 0, 0]);
        assert_eq!(allocate(0, &[1.0]), vec![0]);
        assert_eq!(allocate(5, &[]), Vec::<u64>::new());
        assert_eq!(allocate(100, &[3.0, 1.0]).iter().sum::<u64>(), 100);
        assert_eq!(Budget::default().max_hypotheses, 20);
    }
}
