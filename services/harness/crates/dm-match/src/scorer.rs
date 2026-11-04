//! Fellegi–Sunter record linkage: per-field agreement weights `ln(m/u)` and disagreement
//! weights `ln((1-m)/(1-u))`, summed into a match score with two thresholds.

use dm_normalize::{date_ptbr, money_ptbr, name_key, prefix_match, DateContext};
use indexmap::IndexMap;
use serde::{Deserialize, Serialize};

/// Field values of one record, by field name. Missing or empty fields contribute nothing.
pub type FieldMap = IndexMap<String, String>;

/// How two values of a field are compared.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Agreement {
    /// Equal after trimming and uppercasing.
    Exact,
    /// Jaro–Winkler similarity of the normalized names at or above the threshold.
    JaroWinkler {
        /// Minimum similarity (`0.9` by default).
        threshold: f64,
    },
    /// Overlap of name tokens (Jaccard) at or above the threshold.
    TokenSet {
        /// Minimum Jaccard overlap.
        min_overlap: f64,
    },
    /// Truncated-descriptor prefix match (either side may be the truncated one).
    Prefix,
    /// Money within an absolute tolerance in centavos.
    AmountTolerance {
        /// Maximum difference.
        centavos: i64,
    },
    /// Dates within a window of calendar days.
    DateWindow {
        /// Maximum distance in days.
        days: i64,
    },
}

/// A field and its probabilities: `m` = P(agree | match), `u` = P(agree | non-match).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct FieldSpec {
    /// Field name on both records.
    pub name: String,
    /// Comparison.
    pub agreement: Agreement,
    /// Probability of agreement among true matches.
    pub m: f64,
    /// Probability of agreement among non-matches.
    pub u: f64,
}

impl FieldSpec {
    /// New spec; `m` and `u` are clamped away from 0 and 1 so the logs are finite.
    pub fn new(name: impl Into<String>, agreement: Agreement, m: f64, u: f64) -> Self {
        FieldSpec {
            name: name.into(),
            agreement,
            m: m.clamp(1e-6, 1.0 - 1e-6),
            u: u.clamp(1e-6, 1.0 - 1e-6),
        }
    }

    /// Weight added when the field agrees (`ln(m/u)`, in bits when divided by `ln 2`).
    pub fn agree_weight(&self) -> f64 {
        (self.m / self.u).ln()
    }

    /// Weight added when the field disagrees (`ln((1-m)/(1-u))`, negative).
    pub fn disagree_weight(&self) -> f64 {
        ((1.0 - self.m) / (1.0 - self.u)).ln()
    }
}

/// Outcome.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Decision {
    /// Score at or above the upper threshold.
    Match,
    /// Between the thresholds: needs review or more evidence.
    Possible,
    /// Below the lower threshold.
    NonMatch,
}

/// Contribution of one field.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct FieldScore {
    /// Field name.
    pub name: String,
    /// `Some(true)` agreed, `Some(false)` disagreed, `None` missing on a side.
    pub agreed: Option<bool>,
    /// Weight contributed.
    pub weight: f64,
}

/// A scored pair.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Score {
    /// Sum of the field weights.
    pub total: f64,
    /// Per-field detail.
    pub fields: Vec<FieldScore>,
    /// Decision against the thresholds.
    pub decision: Decision,
}

/// The scorer: field specs plus the two thresholds.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Scorer {
    /// Fields.
    pub fields: Vec<FieldSpec>,
    /// Score at or above which a pair is a match.
    pub upper: f64,
    /// Score below which a pair is a non-match.
    pub lower: f64,
}

impl Default for Scorer {
    /// Sensible defaults for titles versus bank lines: amount, dates, document number,
    /// counterparty name (three agreement functions) and tax id.
    fn default() -> Self {
        Scorer {
            fields: vec![
                FieldSpec::new(
                    "amount",
                    Agreement::AmountTolerance { centavos: 0 },
                    0.95,
                    0.01,
                ),
                FieldSpec::new("date", Agreement::DateWindow { days: 3 }, 0.90, 0.05),
                FieldSpec::new("doc_number", Agreement::Exact, 0.90, 0.001),
                FieldSpec::new(
                    "name",
                    Agreement::JaroWinkler { threshold: 0.9 },
                    0.85,
                    0.02,
                ),
                FieldSpec::new("name", Agreement::TokenSet { min_overlap: 0.6 }, 0.80, 0.05),
                FieldSpec::new("name", Agreement::Prefix, 0.70, 0.02),
                FieldSpec::new("tax_id", Agreement::Exact, 0.97, 0.0005),
            ],
            upper: 6.0,
            lower: 0.0,
        }
    }
}

impl Scorer {
    /// Scores a pair of records.
    pub fn score(&self, a: &FieldMap, b: &FieldMap) -> Score {
        let mut total = 0.0;
        let mut fields = Vec::with_capacity(self.fields.len());
        for spec in &self.fields {
            let (va, vb) = (
                a.get(&spec.name)
                    .map(|s| s.trim())
                    .filter(|s| !s.is_empty()),
                b.get(&spec.name)
                    .map(|s| s.trim())
                    .filter(|s| !s.is_empty()),
            );
            let agreed = match (va, vb) {
                (Some(x), Some(y)) => Some(agrees(&spec.agreement, x, y)),
                _ => None,
            };
            let weight = match agreed {
                Some(true) => spec.agree_weight(),
                Some(false) => spec.disagree_weight(),
                None => 0.0,
            };
            total += weight;
            fields.push(FieldScore {
                name: spec.name.clone(),
                agreed,
                weight,
            });
        }
        let decision = if total >= self.upper {
            Decision::Match
        } else if total < self.lower {
            Decision::NonMatch
        } else {
            Decision::Possible
        };
        Score {
            total,
            fields,
            decision,
        }
    }
}

/// Applies an agreement function to two raw values.
pub fn agrees(agreement: &Agreement, a: &str, b: &str) -> bool {
    match agreement {
        Agreement::Exact => a.trim().eq_ignore_ascii_case(b.trim()),
        Agreement::JaroWinkler { threshold } => {
            let (ka, kb) = (name_key(a).core, name_key(b).core);
            !ka.is_empty() && !kb.is_empty() && strsim::jaro_winkler(&ka, &kb) >= *threshold
        }
        Agreement::TokenSet { min_overlap } => {
            let (ta, tb) = (name_key(a).tokens, name_key(b).tokens);
            if ta.is_empty() || tb.is_empty() {
                return false;
            }
            let inter = ta.iter().filter(|t| tb.contains(t)).count() as f64;
            let union = (ta.len() + tb.len()) as f64 - inter;
            union > 0.0 && inter / union >= *min_overlap
        }
        Agreement::Prefix => prefix_match(a, b),
        Agreement::AmountTolerance { centavos } => match (money_ptbr(a), money_ptbr(b)) {
            (Ok(x), Ok(y)) => x.abs().abs_diff(y.abs()).centavos() <= *centavos,
            _ => false,
        },
        Agreement::DateWindow { days } => {
            let ctx = DateContext::default();
            match (date_ptbr(a, &ctx), date_ptbr(b, &ctx)) {
                (Ok(x), Ok(y)) => (x.date - y.date).num_days().abs() <= *days,
                _ => false,
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rec(pairs: &[(&str, &str)]) -> FieldMap {
        pairs
            .iter()
            .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
            .collect()
    }

    #[test]
    fn weights_have_the_expected_signs() {
        let f = FieldSpec::new("x", Agreement::Exact, 0.9, 0.1);
        assert!(f.agree_weight() > 0.0);
        assert!(f.disagree_weight() < 0.0);
        assert!((f.agree_weight() - (9.0f64).ln()).abs() < 1e-9);
        let clamped = FieldSpec::new("x", Agreement::Exact, 1.0, 0.0);
        assert!(clamped.agree_weight().is_finite());
    }

    #[test]
    fn title_vs_bank_line() {
        let scorer = Scorer::default();
        let title = rec(&[
            ("amount", "18.450,00"),
            ("date", "20/03/2025"),
            ("doc_number", "88231"),
            ("name", "Empório Bela Vista Comércio de Alimentos Ltda"),
            ("tax_id", "11222333000181"),
        ]);
        let bank = rec(&[
            ("amount", "-18450.00"),
            ("date", "21/03/2025"),
            ("name", "EMPORIO BELA VISTA COM DE ALIM"),
        ]);
        let s = scorer.score(&title, &bank);
        assert_eq!(s.decision, Decision::Match, "{s:?}");
        assert!(s
            .fields
            .iter()
            .any(|f| f.name == "doc_number" && f.agreed.is_none()));
        assert!(s
            .fields
            .iter()
            .filter(|f| f.name == "name")
            .any(|f| f.agreed == Some(true)));
        let other = rec(&[
            ("amount", "-18450.00"),
            ("date", "21/03/2025"),
            ("name", "PADARIA CENTRAL NORTE"),
        ]);
        let s2 = scorer.score(&title, &other);
        assert!(s2.total < s.total);
        let unrelated = rec(&[
            ("amount", "-99,00"),
            ("date", "01/01/2025"),
            ("name", "TARIFA BANCARIA"),
            ("tax_id", "45723174000110"),
        ]);
        assert_eq!(
            scorer.score(&title, &unrelated).decision,
            Decision::NonMatch
        );
    }

    #[test]
    fn agreement_functions() {
        assert!(agrees(&Agreement::Exact, " abc ", "ABC"));
        assert!(agrees(
            &Agreement::JaroWinkler { threshold: 0.9 },
            "Empório Bela Vista Ltda",
            "EMPORIO BELA VISTA"
        ));
        assert!(!agrees(
            &Agreement::JaroWinkler { threshold: 0.95 },
            "Padaria Central",
            "Distribuidora Nordeste"
        ));
        assert!(agrees(
            &Agreement::TokenSet { min_overlap: 0.6 },
            "BELA VISTA EMPORIO",
            "Emporio Bela Vista"
        ));
        assert!(!agrees(&Agreement::TokenSet { min_overlap: 0.6 }, "", "x"));
        assert!(agrees(
            &Agreement::Prefix,
            "EMPORIO BELA VISTA COM DE A",
            "Empório Bela Vista Comércio de Alimentos Ltda"
        ));
        assert!(agrees(
            &Agreement::AmountTolerance { centavos: 1 },
            "100,00",
            "-100.01"
        ));
        assert!(!agrees(
            &Agreement::AmountTolerance { centavos: 0 },
            "100,00",
            "100,01"
        ));
        assert!(!agrees(
            &Agreement::AmountTolerance { centavos: 0 },
            "x",
            "100,01"
        ));
        assert!(agrees(
            &Agreement::DateWindow { days: 3 },
            "20/03/2025",
            "2025-03-23"
        ));
        assert!(!agrees(
            &Agreement::DateWindow { days: 2 },
            "20/03/2025",
            "2025-03-23"
        ));
        assert!(!agrees(
            &Agreement::DateWindow { days: 2 },
            "hoje",
            "2025-03-23"
        ));
    }

    #[test]
    fn possible_zone() {
        let scorer = Scorer {
            fields: vec![
                FieldSpec::new("a", Agreement::Exact, 0.9, 0.1),
                FieldSpec::new("b", Agreement::Exact, 0.9, 0.1),
            ],
            upper: 4.0,
            lower: 0.0,
        };
        let s = scorer.score(
            &rec(&[("a", "1"), ("b", "2")]),
            &rec(&[("a", "1"), ("b", "3")]),
        );
        assert_eq!(s.decision, Decision::Possible);
        assert_eq!(s.fields[0].agreed, Some(true));
        assert_eq!(s.fields[1].agreed, Some(false));
        let empty = scorer.score(&rec(&[]), &rec(&[]));
        assert!(empty.total.abs() < f64::EPSILON);
        assert_eq!(empty.decision, Decision::Possible);
    }
}
