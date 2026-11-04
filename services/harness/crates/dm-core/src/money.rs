//! BRL money: a [`Decimal`] with centavo precision and pt-BR formatting. Never a float.

use std::fmt;
use std::iter::Sum;
use std::ops::{Add, AddAssign, Neg, Sub, SubAssign};
use std::str::FromStr;

use rust_decimal::prelude::ToPrimitive;
use rust_decimal::{Decimal, RoundingStrategy};
use schemars::JsonSchema;
use serde::{de, Deserialize, Deserializer, Serialize, Serializer};

use crate::error::CoreError;

/// A monetary amount in BRL with exactly two decimal places.
///
/// Serializes as an integer number of centavos (`12345` is R$ 123,45): every consumer (Go,
/// TypeScript, PostgreSQL, the LLM schemas) handles integers exactly. The deserializer also
/// accepts decimal strings such as `"123.45"`, `"123,45"` or `"R$ 1.234,56"` so hand-written
/// fixtures stay readable. JSON floats are rejected on purpose.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Default)]
pub struct Money(Decimal);

/// How a decimal separator should be interpreted when parsing.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema, Default)]
#[serde(rename_all = "snake_case")]
pub enum DecimalStyle {
    /// Decide from the string itself (see [`Money::parse_brl`]).
    #[default]
    Auto,
    /// `1.234,56`: comma is the decimal separator, dot groups thousands.
    Comma,
    /// `1,234.56`: dot is the decimal separator, comma groups thousands.
    Point,
}

impl Money {
    /// Zero reais.
    pub const ZERO: Money = Money(Decimal::ZERO);

    /// Builds an amount from an integer number of centavos.
    pub fn from_centavos(centavos: i64) -> Self {
        Money(Decimal::new(centavos, 2))
    }

    /// Builds an amount from a decimal, rounding half away from zero to centavos
    /// (the commercial rounding used on Brazilian invoices and bank statements).
    pub fn from_decimal(value: Decimal) -> Self {
        Money(value.round_dp_with_strategy(2, RoundingStrategy::MidpointAwayFromZero))
    }

    /// Builds an amount from a decimal that must already be exact in centavos.
    pub fn try_from_decimal_exact(value: Decimal) -> Result<Self, CoreError> {
        let rounded = value.round_dp_with_strategy(2, RoundingStrategy::ToZero);
        if rounded != value.normalize() && rounded != value {
            return Err(CoreError::SubCentavoPrecision(value.to_string()));
        }
        Ok(Money(rounded))
    }

    /// Builds an amount from whole reais (no centavos).
    pub fn from_reais(reais: i64) -> Self {
        Money(Decimal::new(reais, 0).round_dp(2))
    }

    /// The amount as an integer number of centavos, saturating at `i64::MIN`/`i64::MAX`
    /// (an amount near `Decimal::MAX` cannot be scaled by 100; it never panics here).
    pub fn centavos(self) -> i64 {
        let saturated = if self.0.is_sign_negative() {
            i64::MIN
        } else {
            i64::MAX
        };
        self.0
            .round_dp(2)
            .checked_mul(Decimal::ONE_HUNDRED)
            .and_then(|scaled| scaled.trunc().to_i64())
            .unwrap_or(saturated)
    }

    /// The underlying decimal with scale 2.
    pub fn as_decimal(self) -> Decimal {
        self.0.round_dp(2)
    }

    /// Absolute value.
    pub fn abs(self) -> Self {
        Money(self.0.abs())
    }

    /// True when the amount is exactly zero.
    pub fn is_zero(self) -> bool {
        self.0.is_zero()
    }

    /// True when the amount is strictly negative.
    pub fn is_negative(self) -> bool {
        self.0.is_sign_negative() && !self.0.is_zero()
    }

    /// True when the amount is strictly positive.
    pub fn is_positive(self) -> bool {
        self.0.is_sign_positive() && !self.0.is_zero()
    }

    /// Checked addition.
    pub fn checked_add(self, other: Money) -> Result<Money, CoreError> {
        self.0
            .checked_add(other.0)
            .map(Money)
            .ok_or(CoreError::Overflow)
    }

    /// Checked subtraction.
    pub fn checked_sub(self, other: Money) -> Result<Money, CoreError> {
        self.0
            .checked_sub(other.0)
            .map(Money)
            .ok_or(CoreError::Overflow)
    }

    /// Saturating addition: clamps at the `Decimal` range instead of panicking.
    pub fn saturating_add(self, other: Money) -> Money {
        Money(self.0.saturating_add(other.0))
    }

    /// Sums with overflow detection ([`CoreError::Overflow`]): report totals and other
    /// aggregates over untrusted inputs use this instead of [`Sum`], which saturates.
    pub fn checked_sum<I: IntoIterator<Item = Money>>(iter: I) -> Result<Money, CoreError> {
        iter.into_iter().try_fold(Money::ZERO, Money::checked_add)
    }

    /// Multiplies by a rate (`0.02` for 2%) and rounds half away from zero to centavos,
    /// saturating at the `Decimal` range instead of panicking.
    pub fn mul_rate(self, rate: Decimal) -> Money {
        match self.0.checked_mul(rate) {
            Some(product) => Money::from_decimal(product),
            None if self.0.is_sign_negative() != rate.is_sign_negative() => Money(Decimal::MIN),
            None => Money(Decimal::MAX),
        }
    }

    /// Takes a percentage (`2` for 2%) of the amount, rounded to centavos.
    pub fn percent(self, percent: Decimal) -> Money {
        self.mul_rate(percent / Decimal::ONE_HUNDRED)
    }

    /// Absolute difference between two amounts.
    pub fn abs_diff(self, other: Money) -> Money {
        Money((self.0 - other.0).abs())
    }

    /// Parses a pt-BR monetary string.
    ///
    /// Accepts `R$`, `BRL`, spaces and non-breaking spaces, thousands separators, decimal comma
    /// or decimal point, leading or trailing minus, parentheses for negatives and the `D`/`C`
    /// suffixes of bank statements (`D` is negative). With [`DecimalStyle::Auto`] the last
    /// separator that appears decides, and a lone dot followed by exactly three digits
    /// (`1.234`) is a thousands separator, as in every Brazilian document.
    pub fn parse_brl(input: &str) -> Result<Money, CoreError> {
        Money::parse_with_style(input, DecimalStyle::Auto)
    }

    /// Parses with an explicit decimal style (see [`DecimalStyle`]).
    pub fn parse_with_style(input: &str, style: DecimalStyle) -> Result<Money, CoreError> {
        let err = || CoreError::InvalidMoney(input.to_string());
        let mut s: String = input
            .chars()
            .filter(|c| !c.is_whitespace() && *c != '\u{a0}' && *c != '\u{202f}')
            .collect();
        if s.is_empty() {
            return Err(err());
        }
        if ["--", "-+", "+-", "++"].iter().any(|p| s.contains(p)) {
            return Err(err());
        }
        let mut negative = false;
        // Bank statement suffixes: "1.234,56 D" (débito) / "1.234,56 C" (crédito).
        if let Some(rest) = s.strip_suffix(['D', 'd']) {
            negative = true;
            s = rest.to_string();
        } else if let Some(rest) = s.strip_suffix(['C', 'c']) {
            s = rest.to_string();
        }
        // Currency marks and signs come in any order ("-R$ 1,00", "R$ -1,00", "(R$ 1,00)"):
        // peel them until only the number is left.
        loop {
            let before = s.len();
            let upper = s.to_uppercase();
            for prefix in ["R$", "BRL", "RS$", "R $"] {
                if let Some(rest) = upper.strip_prefix(prefix) {
                    s = s[s.len() - rest.len()..].to_string();
                    break;
                }
            }
            if let Some(rest) = s.strip_prefix('(').and_then(|r| r.strip_suffix(')')) {
                negative = !negative;
                s = rest.to_string();
            }
            if let Some(rest) = s.strip_prefix(['-', '\u{2212}']) {
                negative = !negative;
                s = rest.to_string();
            } else if let Some(rest) = s.strip_prefix('+') {
                s = rest.to_string();
            }
            if let Some(rest) = s.strip_suffix(['-', '\u{2212}']) {
                negative = !negative;
                s = rest.to_string();
            }
            if s.len() == before {
                break;
            }
        }
        if s.is_empty()
            || !s
                .chars()
                .all(|c| c.is_ascii_digit() || c == '.' || c == ',')
        {
            return Err(err());
        }
        let canonical = canonical_decimal(&s, style).ok_or_else(err)?;
        let mut value = Decimal::from_str(&canonical).map_err(|_| err())?;
        if negative {
            value = -value;
        }
        if value.scale() > 2 {
            // Documents never carry sub-centavo precision; treat extra digits as a typo guard.
            let rounded = value.round_dp_with_strategy(2, RoundingStrategy::ToZero);
            if rounded != value {
                return Err(CoreError::SubCentavoPrecision(input.to_string()));
            }
            value = rounded;
        }
        Ok(Money(value.round_dp(2)))
    }

    /// Formats as `1.234,56` (no currency sign); negatives get a leading minus.
    pub fn format_plain(self) -> String {
        let value = self.as_decimal();
        let negative = value.is_sign_negative() && !value.is_zero();
        let abs = value.abs();
        let text = format!("{abs:.2}");
        let (int_part, frac_part) = text.split_once('.').unwrap_or((&text, "00"));
        let mut grouped = String::with_capacity(int_part.len() + int_part.len() / 3);
        let digits: Vec<char> = int_part.chars().collect();
        for (i, c) in digits.iter().enumerate() {
            if i > 0 && (digits.len() - i).is_multiple_of(3) {
                grouped.push('.');
            }
            grouped.push(*c);
        }
        let sign = if negative { "-" } else { "" };
        format!("{sign}{grouped},{frac_part}")
    }

    /// Formats as `R$ 1.234,56`; negatives as `-R$ 1.234,56`.
    pub fn format_brl(self) -> String {
        let plain = self.format_plain();
        match plain.strip_prefix('-') {
            Some(rest) => format!("-R$ {rest}"),
            None => format!("R$ {plain}"),
        }
    }
}

/// Turns the digits-and-separators body of a monetary string into `1234.56` form.
///
/// With [`DecimalStyle::Auto`]: when both separators appear the last one is the decimal
/// separator; a single separator followed by exactly three digits (`1.234`, `1,234`) is a
/// thousands group, because money never carries three decimals; repeated separators of one
/// kind are thousands groups (`1.234.567`).
fn canonical_decimal(body: &str, style: DecimalStyle) -> Option<String> {
    let dots = body.matches('.').count();
    let commas = body.matches(',').count();
    let decimal_sep: Option<char> = match style {
        DecimalStyle::Comma => Some(','),
        DecimalStyle::Point => Some('.'),
        DecimalStyle::Auto => match (body.rfind('.'), body.rfind(',')) {
            (Some(d), Some(c)) => Some(if d > c { '.' } else { ',' }),
            (Some(p), None) => single_separator_role(body, p, dots),
            (None, Some(p)) => single_separator_role(body, p, commas),
            (None, None) => None,
        },
    };
    let (int_text, frac_text) = match decimal_sep.and_then(|sep| body.rfind(sep)) {
        Some(idx) => (&body[..idx], &body[idx + 1..]),
        None => (body, ""),
    };
    let group_sep = match decimal_sep {
        Some('.') => ',',
        Some(_) => '.',
        None if dots > 0 => '.',
        None => ',',
    };
    if let Some(sep) = decimal_sep {
        if int_text.contains(sep) {
            return None;
        }
    }
    if frac_text.contains(['.', ',']) {
        return None;
    }
    // Whatever remains in the integer part must be digits with well-formed thousands groups.
    let groups: Vec<&str> = int_text.split(group_sep).collect();
    if groups.len() > 1
        && (groups[0].is_empty() || groups[0].len() > 3 || groups[1..].iter().any(|g| g.len() != 3))
    {
        return None;
    }
    let int_digits: String = int_text.chars().filter(char::is_ascii_digit).collect();
    let int_digits = if int_digits.is_empty() {
        "0".to_string()
    } else {
        int_digits
    };
    if frac_text.is_empty() {
        Some(int_digits)
    } else {
        Some(format!("{int_digits}.{frac_text}"))
    }
}

/// Role of the only kind of separator present in a number.
fn single_separator_role(body: &str, last_pos: usize, count: usize) -> Option<char> {
    let after = &body[last_pos + 1..];
    let before = &body[..last_pos];
    // "1.234" groups thousands; "0,125" cannot (no grouping starts with a bare zero).
    let thousands_group = count == 1 && after.len() == 3 && !before.is_empty() && before != "0";
    if thousands_group || count > 1 {
        None
    } else {
        body[last_pos..].chars().next()
    }
}

impl fmt::Display for Money {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.format_plain())
    }
}

impl fmt::Debug for Money {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Money({})", self.format_plain())
    }
}

impl FromStr for Money {
    type Err = CoreError;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        Money::parse_brl(s)
    }
}

impl From<Money> for Decimal {
    fn from(value: Money) -> Self {
        value.as_decimal()
    }
}

impl Add for Money {
    type Output = Money;
    fn add(self, rhs: Money) -> Money {
        Money(self.0 + rhs.0)
    }
}

impl AddAssign for Money {
    fn add_assign(&mut self, rhs: Money) {
        self.0 += rhs.0;
    }
}

impl Sub for Money {
    type Output = Money;
    fn sub(self, rhs: Money) -> Money {
        Money(self.0 - rhs.0)
    }
}

impl SubAssign for Money {
    fn sub_assign(&mut self, rhs: Money) {
        self.0 -= rhs.0;
    }
}

impl Neg for Money {
    type Output = Money;
    fn neg(self) -> Money {
        Money(-self.0)
    }
}

/// Sums saturate at the `Decimal` range instead of panicking (`+` and `-` keep the `Decimal`
/// semantics and panic on overflow, which needs amounts beyond 10^26 reais). Aggregates that
/// must report overflow use [`Money::checked_sum`].
impl Sum for Money {
    fn sum<I: Iterator<Item = Money>>(iter: I) -> Money {
        iter.fold(Money::ZERO, Money::saturating_add)
    }
}

impl<'a> Sum<&'a Money> for Money {
    fn sum<I: Iterator<Item = &'a Money>>(iter: I) -> Money {
        iter.fold(Money::ZERO, |acc, m| acc.saturating_add(*m))
    }
}

impl Serialize for Money {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_i64(self.centavos())
    }
}

impl<'de> Deserialize<'de> for Money {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct MoneyVisitor;

        impl de::Visitor<'_> for MoneyVisitor {
            type Value = Money;

            fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str("inteiro em centavos ou string decimal (\"1.234,56\")")
            }

            fn visit_i64<E: de::Error>(self, v: i64) -> Result<Money, E> {
                Ok(Money::from_centavos(v))
            }

            fn visit_u64<E: de::Error>(self, v: u64) -> Result<Money, E> {
                i64::try_from(v)
                    .map(Money::from_centavos)
                    .map_err(E::custom)
            }

            fn visit_f64<E: de::Error>(self, v: f64) -> Result<Money, E> {
                Err(E::custom(format!(
                    "valor monetário deve ser inteiro em centavos ou string decimal, não float ({v})"
                )))
            }

            fn visit_str<E: de::Error>(self, v: &str) -> Result<Money, E> {
                Money::parse_brl(v).map_err(E::custom)
            }
        }

        deserializer.deserialize_any(MoneyVisitor)
    }
}

impl JsonSchema for Money {
    fn schema_name() -> std::borrow::Cow<'static, str> {
        "Money".into()
    }

    fn schema_id() -> std::borrow::Cow<'static, str> {
        "dm_core::Money".into()
    }

    fn json_schema(_gen: &mut schemars::SchemaGenerator) -> schemars::Schema {
        schemars::json_schema!({
            "type": "integer",
            "format": "int64",
            "description": "Valor em centavos de real (12345 = R$ 123,45)."
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rust_decimal::prelude::FromPrimitive;

    fn m(c: i64) -> Money {
        Money::from_centavos(c)
    }

    #[test]
    fn parses_common_brazilian_formats() {
        let cases = [
            ("18.450,00", 1_845_000),
            ("R$ 1.234,56", 123_456),
            ("R$1.234,56", 123_456),
            ("1234.56", 123_456),
            ("1234,56", 123_456),
            ("1.234", 123_400),
            ("1,234.56", 123_456),
            ("0,50", 50),
            (",50", 50),
            ("(1.234,56)", -123_456),
            ("1.234,56-", -123_456),
            ("-1.234,56", -123_456),
            ("R$ -1.234,56", -123_456),
            ("-R$ 1.234,56", -123_456),
            ("1.234,56 D", -123_456),
            ("1.234,56 C", 123_456),
            ("  42  ", 4_200),
            ("1.234.567,89", 123_456_789),
            ("1.234.567", 123_456_700),
            ("12.5", 1_250),
            ("12,5", 1_250),
            ("BRL 10,00", 1_000),
            ("R$\u{a0}3.000,00", 300_000),
            ("\u{2212}5,00", -500),
            ("0", 0),
            ("0,00", 0),
        ];
        for (input, centavos) in cases {
            let got = Money::parse_brl(input).unwrap_or_else(|e| panic!("{input}: {e}"));
            assert_eq!(got.centavos(), centavos, "input {input:?}");
        }
    }

    #[test]
    fn rejects_garbage() {
        for input in [
            "",
            "abc",
            "1.23.4",
            "1,23,4",
            "12.345,6,7",
            "R$",
            "1..2",
            "--1",
        ] {
            assert!(Money::parse_brl(input).is_err(), "should reject {input:?}");
        }
    }

    #[test]
    fn rejects_sub_centavo_precision() {
        // a lone separator followed by three digits is a thousands group, never 3 decimals
        assert!(matches!(Money::parse_brl("1,234"), Ok(x) if x.centavos() == 123_400));
        assert!(matches!(Money::parse_brl("1,230"), Ok(x) if x.centavos() == 123_000));
        assert!(matches!(
            Money::parse_brl("1,2345"),
            Err(CoreError::SubCentavoPrecision(_))
        ));
        assert!(matches!(
            Money::parse_brl("0,125"),
            Err(CoreError::SubCentavoPrecision(_))
        ));
        // trailing zeros beyond centavos are fine
        assert!(matches!(Money::parse_brl("1,2300"), Ok(x) if x.centavos() == 123));
    }

    #[test]
    fn explicit_styles() {
        assert!(matches!(
            Money::parse_with_style("1.234", DecimalStyle::Point),
            Err(CoreError::SubCentavoPrecision(_))
        ));
        assert_eq!(
            Money::parse_with_style("1.23", DecimalStyle::Point).unwrap(),
            m(123)
        );
        assert_eq!(
            Money::parse_with_style("1.234", DecimalStyle::Comma).unwrap(),
            m(123_400)
        );
        assert_eq!(
            Money::parse_with_style("1,234", DecimalStyle::Point).unwrap(),
            m(123_400)
        );
        assert_eq!(
            Money::parse_with_style("1,234.56", DecimalStyle::Point).unwrap(),
            m(123_456)
        );
        assert_eq!(
            Money::parse_with_style("1.234,56", DecimalStyle::Comma).unwrap(),
            m(123_456)
        );
        assert!(Money::parse_with_style("1,234.5", DecimalStyle::Comma).is_err());
        assert!(Money::parse_with_style("1.234,5", DecimalStyle::Point).is_err());
    }

    #[test]
    fn formats_pt_br() {
        assert_eq!(m(123_456).format_plain(), "1.234,56");
        assert_eq!(m(123_456).format_brl(), "R$ 1.234,56");
        assert_eq!(m(-123_456).format_brl(), "-R$ 1.234,56");
        assert_eq!(m(5).format_plain(), "0,05");
        assert_eq!(m(0).format_plain(), "0,00");
        assert_eq!(m(100_000_000).format_plain(), "1.000.000,00");
        assert_eq!(m(99_999).format_plain(), "999,99");
        assert_eq!(m(-7).to_string(), "-0,07");
    }

    #[test]
    fn round_trips_through_format() {
        for c in [
            0,
            1,
            99,
            100,
            123_456,
            -123_456,
            100_000_000,
            i64::from(u32::MAX),
        ] {
            assert_eq!(Money::parse_brl(&m(c).format_brl()).unwrap(), m(c));
            assert_eq!(Money::parse_brl(&m(c).format_plain()).unwrap(), m(c));
        }
    }

    #[test]
    fn sums_saturate_and_checked_sum_reports_overflow() {
        let huge = Money::from_decimal(Decimal::MAX);
        // `Sum` never panics: it clamps at the Decimal range
        let total: Money = [huge, huge, m(1)].iter().sum();
        assert_eq!(total, huge);
        assert_eq!((-huge).saturating_add(-huge), -huge);
        // `checked_sum` reports what `Sum` hides (MAX + 0.01 is MAX in rust_decimal, which
        // drops the centavo rather than overflow, so the overflow needs two huge terms)
        assert_eq!(Money::checked_sum([m(1), m(2), m(3)]), Ok(m(6)));
        assert_eq!(Money::checked_sum([]), Ok(Money::ZERO));
        assert_eq!(Money::checked_sum([huge, huge]), Err(CoreError::Overflow));
        assert_eq!(huge.checked_add(huge), Err(CoreError::Overflow));
        assert_eq!((-huge).checked_sub(huge), Err(CoreError::Overflow));
        // centavos and rates saturate instead of panicking on absurd amounts
        assert_eq!(huge.centavos(), i64::MAX);
        assert_eq!((-huge).centavos(), i64::MIN);
        assert_eq!(huge.mul_rate(Decimal::new(2, 0)), huge);
        assert_eq!(huge.mul_rate(Decimal::new(-2, 0)), -huge);
        assert!(huge.mul_rate(Decimal::new(1, 2)).is_positive());
    }

    #[test]
    fn arithmetic_and_rates() {
        assert_eq!(m(100) + m(250), m(350));
        assert_eq!(m(100) - m(250), m(-150));
        assert_eq!(-m(100), m(-100));
        assert_eq!(m(-100).abs(), m(100));
        assert_eq!(m(100_000).percent(Decimal::new(2, 0)), m(2_000));
        assert_eq!(m(100_000).mul_rate(Decimal::new(15, 3)), m(1_500));
        // rounding half away from zero: 100,00 * 0,00125 = 0,125 -> 0,13
        assert_eq!(m(10_000).mul_rate(Decimal::new(125, 5)), m(13));
        assert_eq!(m(-10_000).mul_rate(Decimal::new(125, 5)), m(-13));
        assert_eq!([m(1), m(2), m(3)].iter().sum::<Money>(), m(6));
        assert_eq!(m(5).abs_diff(m(12)), m(7));
    }

    #[test]
    fn decimal_conversions() {
        assert_eq!(
            Money::from_decimal(Decimal::from_f64(10.005).unwrap()),
            m(1_001)
        );
        assert_eq!(Money::from_decimal(Decimal::new(10_004, 3)), m(1_000));
        assert!(Money::try_from_decimal_exact(Decimal::new(10_005, 3)).is_err());
        assert_eq!(
            Money::try_from_decimal_exact(Decimal::new(1_000, 2)).unwrap(),
            m(1_000)
        );
        assert_eq!(
            Money::try_from_decimal_exact(Decimal::new(10, 0)).unwrap(),
            m(1_000)
        );
        assert_eq!(Money::from_reais(7), m(700));
        assert_eq!(m(123_456).as_decimal(), Decimal::new(123_456, 2));
    }

    #[test]
    fn serde_is_integer_centavos() {
        let json = serde_json::to_string(&m(123_456)).unwrap();
        assert_eq!(json, "123456");
        assert_eq!(serde_json::from_str::<Money>("123456").unwrap(), m(123_456));
        assert_eq!(
            serde_json::from_str::<Money>("\"1.234,56\"").unwrap(),
            m(123_456)
        );
        assert_eq!(
            serde_json::from_str::<Money>("\"1234.56\"").unwrap(),
            m(123_456)
        );
        assert_eq!(serde_json::from_str::<Money>("-5").unwrap(), m(-5));
        assert!(serde_json::from_str::<Money>("1234.56").is_err());
    }

    #[test]
    fn json_schema_is_integer() {
        let schema = schemars::schema_for!(Money);
        let value = serde_json::to_value(schema).unwrap();
        assert_eq!(value["type"], "integer");
    }

    #[test]
    fn ordering_and_hash() {
        use std::collections::HashSet;
        assert!(m(1) < m(2));
        assert!(m(-1) < m(0));
        let set: HashSet<Money> = [m(1), m(1), m(2)].into_iter().collect();
        assert_eq!(set.len(), 2);
    }
}
