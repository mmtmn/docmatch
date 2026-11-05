//! The golden format: what a case's `expected.yaml` holds and what `dm run` writes as
//! `result.json`. Both parse into [`Golden`], so the scorer compares like with like.
//!
//! Amounts are centavos ([`Brl`]); ids that YAML may read as numbers (tax ids, nosso número)
//! go through [`FlexStr`].

use std::collections::BTreeMap;
use std::fmt;

use chrono::NaiveDate;
use rust_decimal::prelude::ToPrimitive;
use rust_decimal::Decimal;
use serde::de::{self, Visitor};
use serde::{Deserialize, Deserializer, Serialize, Serializer};

/// An amount in centavos. Serializes as a JSON number in reais (`24300.0`, `1475.15`) and
/// reads numbers or strings in reais.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Brl(pub i64);

impl Brl {
    /// The value as a decimal number of reais.
    pub fn to_decimal(self) -> Decimal {
        Decimal::new(self.0, 2)
    }

    /// From a decimal number of reais, rounded half away from zero to centavos.
    pub fn from_decimal(value: Decimal) -> Self {
        let cents = (value * Decimal::ONE_HUNDRED)
            .round_dp_with_strategy(0, rust_decimal::RoundingStrategy::MidpointAwayFromZero);
        Brl(cents.to_i64().unwrap_or(if cents.is_sign_negative() {
            i64::MIN
        } else {
            i64::MAX
        }))
    }

    /// Absolute difference in centavos.
    pub fn abs_diff(self, other: Brl) -> u64 {
        self.0.abs_diff(other.0)
    }
}

impl fmt::Display for Brl {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_decimal())
    }
}

impl Serialize for Brl {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        // Centavos up to 2^53 convert exactly; the shortest round-trip rendering of the f64
        // is the two-decimal value (1475.15, 24300.0).
        #[allow(clippy::cast_precision_loss)]
        let reais = self.0 as f64 / 100.0;
        serializer.serialize_f64(reais)
    }
}

struct BrlVisitor;

impl Visitor<'_> for BrlVisitor {
    type Value = Brl;

    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("um valor em reais (número ou texto)")
    }

    fn visit_i64<E: de::Error>(self, v: i64) -> Result<Brl, E> {
        v.checked_mul(100)
            .map(Brl)
            .ok_or_else(|| E::custom("valor fora do intervalo"))
    }

    fn visit_u64<E: de::Error>(self, v: u64) -> Result<Brl, E> {
        i64::try_from(v)
            .ok()
            .and_then(|v| v.checked_mul(100))
            .map(Brl)
            .ok_or_else(|| E::custom("valor fora do intervalo"))
    }

    fn visit_f64<E: de::Error>(self, v: f64) -> Result<Brl, E> {
        if !v.is_finite() || v.abs() > 9.0e15 {
            return Err(E::custom("valor fora do intervalo"));
        }
        // Bounded above, so the cast cannot truncate.
        #[allow(clippy::cast_possible_truncation)]
        let cents = (v * 100.0).round() as i64;
        Ok(Brl(cents))
    }

    fn visit_str<E: de::Error>(self, v: &str) -> Result<Brl, E> {
        let t = v.trim();
        if let Ok(d) = t.parse::<Decimal>() {
            return Ok(Brl::from_decimal(d));
        }
        dm_core::Money::parse_brl(t)
            .map(|m| Brl(m.centavos()))
            .map_err(|_| E::custom(format!("valor inválido: {v}")))
    }
}

impl<'de> Deserialize<'de> for Brl {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        deserializer.deserialize_any(BrlVisitor)
    }
}

/// A string that YAML may have read as a number (`000098765001`, `21456789000123`).
#[derive(Clone, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct FlexStr(pub String);

impl FlexStr {
    /// The text.
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl Serialize for FlexStr {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&self.0)
    }
}

struct FlexVisitor;

impl Visitor<'_> for FlexVisitor {
    type Value = FlexStr;

    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("texto ou número")
    }

    fn visit_str<E: de::Error>(self, v: &str) -> Result<FlexStr, E> {
        Ok(FlexStr(v.to_string()))
    }

    fn visit_string<E: de::Error>(self, v: String) -> Result<FlexStr, E> {
        Ok(FlexStr(v))
    }

    fn visit_i64<E: de::Error>(self, v: i64) -> Result<FlexStr, E> {
        Ok(FlexStr(v.to_string()))
    }

    fn visit_u64<E: de::Error>(self, v: u64) -> Result<FlexStr, E> {
        Ok(FlexStr(v.to_string()))
    }

    fn visit_f64<E: de::Error>(self, v: f64) -> Result<FlexStr, E> {
        Ok(FlexStr(v.to_string()))
    }

    fn visit_bool<E: de::Error>(self, v: bool) -> Result<FlexStr, E> {
        Ok(FlexStr(v.to_string()))
    }
}

impl<'de> Deserialize<'de> for FlexStr {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        deserializer.deserialize_any(FlexVisitor)
    }
}

/// Reporting period.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Period {
    /// First day.
    pub start: NaiveDate,
    /// Last day.
    pub end: NaiveDate,
}

/// One receivable title and what happened to it.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct GTitle {
    /// Id within the case (`P01`, `O03`).
    pub id: String,
    /// Source key (see [`Golden::sources`]).
    pub source: String,
    /// Document reference as in the source (`NF 200311`, `FAT-2026-0411`).
    #[serde(rename = "ref")]
    pub reference: FlexStr,
    /// Customer name as in the source.
    #[serde(default)]
    pub customer: FlexStr,
    /// Tax id, digits.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tax_id: Option<FlexStr>,
    /// Due date.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub due: Option<NaiveDate>,
    /// Face amount.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub amount: Option<Brl>,
    /// `paid`, `partial`, `open` or `duplicate`.
    pub status: String,
    /// Boleto number, when the title has one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub nosso_numero: Option<FlexStr>,
    /// Due date rolled to the next business day, when it fell on a weekend or holiday.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub effective_due: Option<NaiveDate>,
    /// Date of the (last) payment.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub paid_on: Option<NaiveDate>,
    /// Cash received, fines included.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub paid_amount: Option<Brl>,
    /// What is still open (a withheld tax counts).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub open_amount: Option<Brl>,
    /// Calendar days after the due date (negative: early).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub days_late: Option<i64>,
    /// Business days after the rolled due date.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub business_days_late: Option<i64>,
    /// `late`, `early`, `overdue`, `fine_received`, `withholding_pending`, `proposed`, `intercompany`.
    #[serde(default)]
    pub flags: Vec<String>,
    /// Id of the title this one duplicates.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duplicate_of: Option<String>,
}

/// One bank statement line and how it was explained.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct GLine {
    /// One-based line number in the statement.
    pub n: usize,
    /// Posting date.
    pub date: NaiveDate,
    /// Amount (always positive; see `side`).
    pub amount: Brl,
    /// `C` (credit) or `D` (debit).
    pub side: String,
    /// `receipt`, `fee`, `supplier_payment` or `unapplied`.
    pub kind: String,
    /// Title ids (or `AP-<invoice>`) the line pays.
    #[serde(default)]
    pub matched: Vec<FlexStr>,
    /// Rule that explained the line.
    pub rule: String,
    /// True when the match is only a proposal that needs confirmation.
    #[serde(default, skip_serializing_if = "is_false")]
    pub proposed: bool,
    /// Codes of the findings about this line.
    #[serde(default)]
    pub findings: Vec<String>,
    /// Statement description.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
}

#[allow(clippy::trivially_copy_pass_by_ref)] // serde's skip_serializing_if passes a reference
fn is_false(b: &bool) -> bool {
    !*b
}

/// One finding.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct GFinding {
    /// Code within the case (`F01`...).
    pub code: String,
    /// Stable kind (`partial_payment`, `ap_boleto_invalid`...).
    pub kind: String,
    /// `info`, `low`, `medium`, `high` or `critical`.
    pub severity: String,
    /// Money at stake.
    pub amount_at_risk_brl: Brl,
    /// References: `bank:<n>`, `title:<id>`, `email:<n>`, `chat:<AAAA-MM-DDTHH:MM>`.
    #[serde(default)]
    pub evidence: Vec<String>,
    /// One-line title in pt-BR.
    #[serde(default)]
    pub title_ptbr: String,
    /// Short explanation in pt-BR.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub explanation_ptbr: String,
}

/// A whole case: the expected answer or a run's result.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Golden {
    /// Format version.
    #[serde(default)]
    pub version: u32,
    /// Case name.
    #[serde(default)]
    pub case: String,
    /// Stable run id derived from the input hashes (results only).
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub run_id: String,
    /// Reporting period.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub period: Option<Period>,
    /// Source key → file name.
    #[serde(default)]
    pub sources: BTreeMap<String, String>,
    /// File name → SHA-256 of its bytes (results only).
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub inputs: BTreeMap<String, String>,
    /// Totals in reais.
    #[serde(default)]
    pub totals_brl: BTreeMap<String, Brl>,
    /// Titles.
    #[serde(default)]
    pub titles: Vec<GTitle>,
    /// Bank lines.
    #[serde(default)]
    pub bank_lines: Vec<GLine>,
    /// Findings.
    #[serde(default)]
    pub findings: Vec<GFinding>,
    /// Estimated uncharged late fees by title id.
    #[serde(default)]
    pub late_fee_estimates_brl: BTreeMap<String, Brl>,
}

impl Golden {
    /// Parses a YAML (`expected.yaml`) or JSON (`result.json`) document.
    pub fn parse(text: &str) -> Result<Golden, String> {
        let trimmed = text.trim_start();
        if trimmed.starts_with('{') {
            serde_json::from_str(text).map_err(|e| format!("JSON inválido: {e}"))
        } else {
            serde_saphyr::from_str(text).map_err(|e| format!("YAML inválido: {e}"))
        }
    }

    /// Pretty JSON with a trailing newline (deterministic: fields in declaration order,
    /// maps sorted).
    pub fn to_json(&self) -> String {
        let mut s = serde_json::to_string_pretty(self).unwrap_or_else(|_| "{}".to_string());
        s.push('\n');
        s
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn brl_reads_numbers_and_strings() {
        let g: Golden =
            Golden::parse("totals_brl: { a: 24300.00, b: 0, c: 1475.15, d: '19.890,00' }\n")
                .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(g.totals_brl["a"], Brl(2_430_000));
        assert_eq!(g.totals_brl["b"], Brl(0));
        assert_eq!(g.totals_brl["c"], Brl(147_515));
        assert!(Golden::parse("totals_brl: { d: abc }\n").is_err());
    }

    #[test]
    fn flex_ids_keep_leading_zeros_when_quoted_and_read_numbers() {
        let g = Golden::parse(
            "titles:\n  - { id: T1, source: s, ref: NF 1, status: open, tax_id: 21456789000123, nosso_numero: '000098765001' }\n",
        )
        .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(
            g.titles[0].tax_id.as_ref().map(FlexStr::as_str),
            Some("21456789000123")
        );
        assert_eq!(
            g.titles[0].nosso_numero.as_ref().map(FlexStr::as_str),
            Some("000098765001")
        );
    }

    #[test]
    fn json_round_trip_is_stable() {
        let mut g = Golden::default();
        g.totals_brl.insert("x".into(), Brl(147_515));
        let json = g.to_json();
        assert!(json.contains("1475.15"));
        let back = Golden::parse(&json).unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(back, g);
    }
}
