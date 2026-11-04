//! Percentages and rates (`2%`, `2,5 %`, `1% a.m.`, `12% a.a.`, `0,02`).

use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};

use crate::text::{fold_ascii, squash_spaces};
use crate::NormalizeError;

/// Period a rate applies to.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum RatePeriod {
    /// Not stated.
    #[default]
    Unspecified,
    /// Per day (`a.d.`, `ao dia`).
    Day,
    /// Per month (`a.m.`, `ao mês`).
    Month,
    /// Per year (`a.a.`, `ao ano`).
    Year,
}

/// A parsed rate.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct RateParse {
    /// Fraction (`0.02` for 2%).
    pub rate: Decimal,
    /// Period, when stated.
    pub period: RatePeriod,
}

/// Parses a percentage into a fraction: `2%` → `0.02`, `2,5%` → `0.025`.
///
/// Without a `%` sign, values up to 1 are taken as fractions already (`0,02` → `0.02`) and
/// values above 1 as percentages (`2` → `0.02`).
pub fn percent_ptbr(input: &str) -> Result<Decimal, NormalizeError> {
    rate_ptbr(input).map(|r| r.rate)
}

/// Parses a rate with an optional period (`1% a.m.`, `12 % ao ano`, `0,5% ad`).
pub fn rate_ptbr(input: &str) -> Result<RateParse, NormalizeError> {
    let err = || NormalizeError::Percent(input.to_string());
    let folded = fold_ascii(input);
    if folded.is_empty() {
        return Err(err());
    }
    let mut period = RatePeriod::Unspecified;
    let mut text = folded.clone();
    let suffixes: [(&str, RatePeriod); 12] = [
        ("AO MES", RatePeriod::Month),
        ("A.M.", RatePeriod::Month),
        ("A.M", RatePeriod::Month),
        ("AM", RatePeriod::Month),
        ("AO ANO", RatePeriod::Year),
        ("A.A.", RatePeriod::Year),
        ("A.A", RatePeriod::Year),
        ("AA", RatePeriod::Year),
        ("AO DIA", RatePeriod::Day),
        ("A.D.", RatePeriod::Day),
        ("A.D", RatePeriod::Day),
        ("AD", RatePeriod::Day),
    ];
    for (suffix, p) in suffixes {
        if let Some(rest) = text.strip_suffix(suffix) {
            period = p;
            text = rest.trim().to_string();
            break;
        }
    }
    let mut has_percent = false;
    for word in ["POR CENTO", "PORCENTO", "PCT", "%"] {
        if let Some(rest) = text.strip_suffix(word) {
            has_percent = true;
            text = rest.trim().to_string();
            break;
        }
    }
    let text = squash_spaces(&text).replace(' ', "");
    if text.is_empty() {
        return Err(err());
    }
    let negative = text.starts_with('-');
    let body = text.trim_start_matches(['-', '+']);
    if body.is_empty()
        || !body
            .chars()
            .all(|c| c.is_ascii_digit() || c == ',' || c == '.')
    {
        return Err(err());
    }
    // Decimal comma or point; the last separator wins, dots before a comma are thousands.
    let canonical = match (body.rfind('.'), body.rfind(',')) {
        (Some(d), Some(c)) if c > d => body.replace('.', "").replace(',', "."),
        (Some(_), Some(_)) => body.replace(',', ""),
        (None, Some(_)) => body.replace(',', "."),
        _ => body.to_string(),
    };
    let mut value: Decimal = canonical.parse().map_err(|_| err())?;
    if has_percent || value > Decimal::ONE {
        value /= Decimal::ONE_HUNDRED;
    }
    if negative {
        value = -value;
    }
    Ok(RateParse {
        rate: value.normalize(),
        period,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn dec(s: &str) -> Decimal {
        s.parse().unwrap()
    }

    #[test]
    fn percents() {
        assert_eq!(percent_ptbr("2%").unwrap(), dec("0.02"));
        assert_eq!(percent_ptbr("2,5 %").unwrap(), dec("0.025"));
        assert_eq!(percent_ptbr("4.65%").unwrap(), dec("0.0465"));
        assert_eq!(percent_ptbr("4,65%").unwrap(), dec("0.0465"));
        assert_eq!(percent_ptbr("0,02").unwrap(), dec("0.02"));
        assert_eq!(percent_ptbr("2").unwrap(), dec("0.02"));
        assert_eq!(percent_ptbr("1").unwrap(), dec("1"));
        assert_eq!(percent_ptbr("100%").unwrap(), dec("1"));
        assert_eq!(percent_ptbr("2 por cento").unwrap(), dec("0.02"));
        assert_eq!(percent_ptbr("-1,5%").unwrap(), dec("-0.015"));
        assert_eq!(percent_ptbr("1.234,5%").unwrap(), dec("12.345"));
        for bad in ["", "%", "abc", "1..2%", "a.m."] {
            assert!(percent_ptbr(bad).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn rates_with_periods() {
        let r = rate_ptbr("1% a.m.").unwrap();
        assert_eq!(r.rate, dec("0.01"));
        assert_eq!(r.period, RatePeriod::Month);
        assert_eq!(rate_ptbr("12% ao ano").unwrap().period, RatePeriod::Year);
        assert_eq!(rate_ptbr("12 % a.a.").unwrap().rate, dec("0.12"));
        assert_eq!(rate_ptbr("0,033% ao dia").unwrap().period, RatePeriod::Day);
        assert_eq!(rate_ptbr("0,033% ao dia").unwrap().rate, dec("0.00033"));
        assert_eq!(rate_ptbr("2%").unwrap().period, RatePeriod::Unspecified);
        assert!(rate_ptbr("juros de 1% a.m.").is_err());
    }
}
