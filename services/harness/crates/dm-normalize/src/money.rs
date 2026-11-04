//! Money normalization: every way a Brazilian document writes an amount.

use dm_core::money::DecimalStyle as CoreStyle;
use dm_core::Money;

use crate::NormalizeError;

/// Decimal separator convention of a column or document.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum DecimalStyle {
    /// Decide per value.
    #[default]
    Auto,
    /// `1.234,56`.
    Comma,
    /// `1,234.56`.
    Point,
}

impl From<DecimalStyle> for CoreStyle {
    fn from(s: DecimalStyle) -> Self {
        match s {
            DecimalStyle::Auto => CoreStyle::Auto,
            DecimalStyle::Comma => CoreStyle::Comma,
            DecimalStyle::Point => CoreStyle::Point,
        }
    }
}

/// Parses a pt-BR amount, deciding the decimal separator per value.
///
/// Handles `18.450,00`, `R$ 1.234,56`, `1234.56`, `(1.234,56)` (negative), `1.234,56-`,
/// `-R$ 1,00`, `1.234,56 D` (débito), `R$1.234`, unicode minus and non-breaking spaces.
/// Empty or dash-only cells (`-`, `--`) are errors, not zero: the caller decides.
pub fn money_ptbr(input: &str) -> Result<Money, NormalizeError> {
    money_with_style(input, DecimalStyle::Auto)
}

/// Parses with a known column style (see [`sniff_decimal_style`]).
pub fn money_with_style(input: &str, style: DecimalStyle) -> Result<Money, NormalizeError> {
    let cleaned = pre_clean(input);
    if cleaned.is_empty() {
        return Err(NormalizeError::Money(input.to_string()));
    }
    Money::parse_with_style(&cleaned, style.into())
        .map_err(|_| NormalizeError::Money(input.to_string()))
}

/// Removes decorations that are not part of the number: currency words, `=` prefixes from
/// spreadsheets, trailing `*`, surrounding quotes.
fn pre_clean(input: &str) -> String {
    let mut s = input
        .trim()
        .trim_matches(['"', '\'', '=', '*'])
        .trim()
        .to_string();
    let upper = s.to_uppercase();
    for word in ["REAIS", "REAL", "BRL"] {
        if let Some(rest) = upper.strip_suffix(word) {
            s = s[..rest.len()].trim().to_string();
            break;
        }
    }
    s
}

/// Guesses the decimal style of a column from sample values.
///
/// A value with both separators decides by the last one; otherwise a lone separator followed
/// by one or two digits decides. Ties and no evidence give [`DecimalStyle::Auto`].
pub fn sniff_decimal_style<S: AsRef<str>>(samples: &[S]) -> DecimalStyle {
    let mut comma_votes = 0usize;
    let mut point_votes = 0usize;
    for sample in samples {
        let s: String = sample
            .as_ref()
            .chars()
            .filter(|c| c.is_ascii_digit() || *c == '.' || *c == ',')
            .collect();
        let last_dot = s.rfind('.');
        let last_comma = s.rfind(',');
        match (last_dot, last_comma) {
            (Some(d), Some(c)) => {
                if c > d {
                    comma_votes += 2;
                } else {
                    point_votes += 2;
                }
            }
            (None, Some(c)) => {
                let after = s.len() - c - 1;
                if (1..=2).contains(&after) {
                    comma_votes += 1;
                } else if after == 3 && s.matches(',').count() == 1 {
                    point_votes += 1;
                }
            }
            (Some(d), None) => {
                let after = s.len() - d - 1;
                if (1..=2).contains(&after) {
                    point_votes += 1;
                } else if after == 3 && s.matches('.').count() == 1 {
                    comma_votes += 1;
                }
            }
            (None, None) => {}
        }
    }
    match comma_votes.cmp(&point_votes) {
        std::cmp::Ordering::Greater => DecimalStyle::Comma,
        std::cmp::Ordering::Less => DecimalStyle::Point,
        std::cmp::Ordering::Equal => DecimalStyle::Auto,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn c(v: i64) -> Money {
        Money::from_centavos(v)
    }

    #[test]
    fn real_world_mess() {
        let cases = [
            ("18.450,00", 1_845_000),
            ("R$ 1.234,56", 123_456),
            ("1234.56", 123_456),
            ("(1.234,56)", -123_456),
            ("1.234,56-", -123_456),
            ("R$ -1,00", -100),
            ("1.234,56 D", -123_456),
            ("1.234,56 C", 123_456),
            ("R$1.234", 123_400),
            ("\u{2212}R$ 15,90", -1_590),
            ("= 42,10", 4_210),
            ("\"1.000,00\"", 100_000),
            ("150 reais", 15_000),
            ("3.000,00 BRL", 300_000),
            ("0,01", 1),
            ("12", 1_200),
            ("1,234.56", 123_456),
            ("12.345.678,90", 1_234_567_890),
        ];
        for (input, expected) in cases {
            assert_eq!(
                money_ptbr(input).unwrap_or_else(|e| panic!("{input}: {e}")),
                c(expected),
                "{input}"
            );
        }
        for bad in ["", "-", "--", "abc", "R$", "1.2.3,4,5", "12,34,56"] {
            assert!(money_ptbr(bad).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn column_style_sniffing() {
        assert_eq!(
            sniff_decimal_style(&["1.234,56", "10,00", "3,5"]),
            DecimalStyle::Comma
        );
        assert_eq!(
            sniff_decimal_style(&["1,234.56", "10.00", "3.5"]),
            DecimalStyle::Point
        );
        assert_eq!(
            sniff_decimal_style(&["1.234", "2.345"]),
            DecimalStyle::Comma
        );
        assert_eq!(
            sniff_decimal_style(&["1,234", "2,345"]),
            DecimalStyle::Point
        );
        assert_eq!(sniff_decimal_style(&["12", "34"]), DecimalStyle::Auto);
        assert_eq!(sniff_decimal_style::<&str>(&[]), DecimalStyle::Auto);
        // with a known style, "1.234" is unambiguous
        assert_eq!(
            money_with_style("1.234", DecimalStyle::Comma).unwrap(),
            c(123_400)
        );
        assert_eq!(
            money_with_style("1.23", DecimalStyle::Point).unwrap(),
            c(123)
        );
        assert_eq!(
            money_with_style("1,234", DecimalStyle::Point).unwrap(),
            c(123_400)
        );
    }
}
