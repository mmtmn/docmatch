//! Small pt-BR text helpers for finding titles and explanations.

use chrono::{Datelike, NaiveDate, Weekday};
use dm_core::Money;
use rust_decimal::Decimal;

const MASC: [&str; 21] = [
    "zero",
    "um",
    "dois",
    "três",
    "quatro",
    "cinco",
    "seis",
    "sete",
    "oito",
    "nove",
    "dez",
    "onze",
    "doze",
    "treze",
    "catorze",
    "quinze",
    "dezesseis",
    "dezessete",
    "dezoito",
    "dezenove",
    "vinte",
];

/// A count in words, masculine (`seis títulos`); digits above twenty.
pub fn count_masc(n: usize) -> String {
    MASC.get(n)
        .map_or_else(|| n.to_string(), |w| (*w).to_string())
}

/// A count in words, feminine (`duas notas`); digits above twenty.
pub fn count_fem(n: usize) -> String {
    match n {
        1 => "uma".to_string(),
        2 => "duas".to_string(),
        _ => count_masc(n),
    }
}

/// First letter uppercase.
pub fn capitalize(s: &str) -> String {
    let mut chars = s.chars();
    chars.next().map_or_else(String::new, |c| {
        c.to_uppercase().collect::<String>() + chars.as_str()
    })
}

/// Day of the week (`domingo`, `segunda-feira`).
pub fn weekday(d: NaiveDate) -> &'static str {
    match d.weekday() {
        Weekday::Mon => "segunda-feira",
        Weekday::Tue => "terça-feira",
        Weekday::Wed => "quarta-feira",
        Weekday::Thu => "quinta-feira",
        Weekday::Fri => "sexta-feira",
        Weekday::Sat => "sábado",
        Weekday::Sun => "domingo",
    }
}

/// A percentage with a decimal comma and no trailing zeros (`5`, `1,5`, `4,65`).
pub fn pct(value: Decimal) -> String {
    value.normalize().to_string().replace('.', ",")
}

/// `part / whole` as a percentage rounded to two decimals.
pub fn ratio_pct(part: Money, whole: Money) -> Decimal {
    if whole.is_zero() {
        return Decimal::ZERO;
    }
    (part.as_decimal() * Decimal::ONE_HUNDRED / whole.as_decimal()).round_dp(2)
}

/// `dd/mm/aaaa`.
pub fn date(d: NaiveDate) -> String {
    d.format("%d/%m/%Y").to_string()
}

/// The payment channel a bank descriptor names, with its article (`uma TED`, `um Pix`).
pub fn channel(folded: &str) -> (&'static str, &'static str) {
    let words: Vec<&str> = folded.split_whitespace().collect();
    let has = |w: &str| words.contains(&w);
    if has("PIX") {
        ("um", "Pix")
    } else if has("TED") {
        ("uma", "TED")
    } else if has("DOC") {
        ("um", "DOC")
    } else if words.iter().any(|w| w.starts_with("DEPOSITO")) {
        ("um", "depósito")
    } else if words.iter().any(|w| w.starts_with("LIQUIDACAO")) {
        ("um", "boleto")
    } else {
        ("um", "pagamento")
    }
}

/// Plural helper: `1 dia`, `2 dias`.
pub fn days(n: i64) -> String {
    if n.abs() == 1 {
        format!("{n} dia")
    } else {
        format!("{n} dias")
    }
}

/// Plural helper for business days: `1 dia útil`, `2 dias úteis`.
pub fn business_days(n: i64) -> String {
    if n.abs() == 1 {
        format!("{n} dia útil")
    } else {
        format!("{n} dias úteis")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn words_and_numbers() {
        assert_eq!(capitalize(&count_masc(6)), "Seis");
        assert_eq!(count_fem(2), "duas");
        assert_eq!(count_masc(42), "42");
        assert_eq!(pct(Decimal::new(465, 2)), "4,65");
        assert_eq!(pct(Decimal::new(500, 2)), "5");
        assert_eq!(channel("TED RECEBIDA X"), ("uma", "TED"));
        assert_eq!(days(1), "1 dia");
        assert_eq!(business_days(2), "2 dias úteis");
        let d = NaiveDate::from_ymd_opt(2026, 7, 12).unwrap_or_default();
        assert_eq!(weekday(d), "domingo");
    }
}
