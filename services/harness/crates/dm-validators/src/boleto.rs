//! FEBRABAN boleto lines: bank slips (47-digit "linha digitável") and arrecadação slips
//! (48 digits, segments 1–9), plus the 44-digit barcodes behind them.
//!
//! Bank barcode layout (44): bank(3) currency(1) DV(1) factor(4) value(10) free(25).
//! Bank line: `bank+currency+free[0..5]+DV10 | free[5..15]+DV10 | free[15..25]+DV10 |
//! DV11 | factor+value`.
//!
//! Arrecadação barcode (44): product `8`(1) segment(1) value-identifier(1) DV(1) value(11)
//! company(4) free(25). The line is four blocks of eleven digits, each with its own DV
//! computed with the same módulo as the identifier digit (6/7: mod 10; 8/9: mod 11).
//!
//! Due-date factor: 1000 = 2000-07-03 in the first cycle; the counter reached 9999 on
//! 2025-02-21 and restarted at 1000 on 2025-02-22 (second cycle). A factor is therefore
//! ambiguous between two dates ~24.6 years apart; [`parse_boleto_with_reference`] picks the
//! candidate closest to a reference date and reports both.

use chrono::{Days, NaiveDate};
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::digits::{all_digits, mod10, mod11_boleto, mod11_zero_rule};

/// Kind of slip.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BoletoKind {
    /// Bank slip (cobrança), 47-digit line.
    Bank,
    /// Arrecadação slip (utilities, taxes, carnês), 48-digit line.
    Arrecadacao,
}

/// Which cycle of the due-date factor a date belongs to.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum FactorCycle {
    /// Factor 1000 = 2000-07-03 (used until 2025-02-21).
    First,
    /// Factor 1000 = 2025-02-22.
    Second,
}

/// One check digit verification.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FieldCheck {
    /// Field name (`campo1`, `campo2`, `campo3`, `geral`, `bloco1`...).
    pub name: String,
    /// Digits the check digit covers.
    pub digits: String,
    /// Check digit the digits require.
    pub expected_dv: u32,
    /// Check digit found.
    pub found_dv: u32,
    /// True when they agree.
    pub ok: bool,
}

/// Every field of a boleto line, every check-digit verdict, decoded value and due date.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Boleto {
    /// Kind of slip.
    pub kind: BoletoKind,
    /// The line, digits only (47 or 48).
    pub line: String,
    /// The barcode, digits only (44).
    pub barcode: String,
    /// Bank code (bank slips).
    pub bank_code: Option<String>,
    /// Currency code (bank slips; 9 = real).
    pub currency: Option<u8>,
    /// Segment (arrecadação): 1 prefeituras, 2 saneamento, 3 energia/gás, 4 telecom,
    /// 5 órgãos governamentais, 6 carnês, 7 multas de trânsito, 9 uso do banco.
    pub segment: Option<u8>,
    /// Value identifier (arrecadação): 6/8 value in reais, 7/9 reference quantity.
    pub value_identifier: Option<u8>,
    /// Company or organ code (arrecadação, 4 digits).
    pub company_id: Option<String>,
    /// Value field as printed, in centavos (zero means open amount).
    pub value_centavos: i64,
    /// Value in centavos when the slip carries one.
    pub value: Option<i64>,
    /// Due-date factor (bank slips; `0000` means no due date).
    pub due_factor: Option<u16>,
    /// Due date chosen for the factor.
    pub due_date: Option<NaiveDate>,
    /// Cycle of the chosen due date.
    pub due_date_cycle: Option<FactorCycle>,
    /// Every date the factor could mean, by cycle.
    pub due_date_candidates: Vec<(FactorCycle, NaiveDate)>,
    /// Free field (25 digits).
    pub free_field: String,
    /// Per-field check-digit verdicts.
    pub field_checks: Vec<FieldCheck>,
    /// General (barcode) check-digit verdict.
    pub general_check: FieldCheck,
    /// Structural problems (pt-BR).
    pub problems: Vec<String>,
    /// True when every check digit agrees and there are no structural problems.
    pub valid: bool,
}

/// Why a line could not be parsed at all.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum BoletoError {
    /// Not 44, 47 or 48 digits.
    #[error(
        "linha com {0} dígitos; esperado 44 (código de barras), 47 (boleto) ou 48 (arrecadação)"
    )]
    Length(usize),
}

const FIRST_CYCLE_BASE: (i32, u32, u32) = (2000, 7, 3);
const SECOND_CYCLE_BASE: (i32, u32, u32) = (2025, 2, 22);

fn ymd(t: (i32, u32, u32)) -> NaiveDate {
    NaiveDate::from_ymd_opt(t.0, t.1, t.2).unwrap_or(NaiveDate::MIN)
}

/// Dates a due-date factor can mean, one per cycle. Empty for factor 0 (no due date).
pub fn factor_to_dates(factor: u16) -> Vec<(FactorCycle, NaiveDate)> {
    if factor == 0 || factor > 9999 {
        return Vec::new();
    }
    let mut out = Vec::with_capacity(2);
    if factor >= 1000 {
        let offset = Days::new(u64::from(factor - 1000));
        if let Some(d) = ymd(FIRST_CYCLE_BASE).checked_add_days(offset) {
            out.push((FactorCycle::First, d));
        }
        if let Some(d) = ymd(SECOND_CYCLE_BASE).checked_add_days(offset) {
            out.push((FactorCycle::Second, d));
        }
    } else if let Some(d) =
        ymd(FIRST_CYCLE_BASE).checked_sub_days(Days::new(u64::from(1000 - factor)))
    {
        // Factors below 1000 were used before July 2000 (factor 1 = 1997-10-08).
        out.push((FactorCycle::First, d));
    }
    out
}

/// The factor a due date must carry, with its cycle. `None` for dates the factor cannot
/// represent (before 1997-10-08 or beyond the second cycle).
pub fn expected_factor(date: NaiveDate) -> Option<(u16, FactorCycle)> {
    let second = ymd(SECOND_CYCLE_BASE);
    let first = ymd(FIRST_CYCLE_BASE);
    if date >= second {
        let factor = 1000 + (date - second).num_days();
        (factor <= 9999).then_some((factor as u16, FactorCycle::Second))
    } else if date >= first {
        let factor = 1000 + (date - first).num_days();
        Some((factor as u16, FactorCycle::First))
    } else {
        let back = (first - date).num_days();
        (back < 1000).then_some(((1000 - back) as u16, FactorCycle::First))
    }
}

/// Parses a boleto line, choosing the due-date cycle closest to today.
pub fn parse_boleto(input: &str) -> Result<Boleto, BoletoError> {
    parse_boleto_with_reference(input, chrono::Local::now().date_naive())
}

/// Parses a boleto line or barcode; separators, spaces and other characters are ignored.
/// The reference date disambiguates the due-date factor cycle (the candidate closest to it
/// wins).
pub fn parse_boleto_with_reference(
    input: &str,
    reference: NaiveDate,
) -> Result<Boleto, BoletoError> {
    let digits: String = input.chars().filter(char::is_ascii_digit).collect();
    match digits.len() {
        47 => Ok(parse_bank_line(&digits, reference)),
        48 => Ok(parse_arrecadacao_line(&digits)),
        44 => {
            // A leading 8 is the arrecadação product id, but bank codes 8xx exist: when the
            // arrecadação reading fails its own check digits and the bank reading passes,
            // the bank reading wins.
            if digits.starts_with('8') {
                let arrecadacao = parse_arrecadacao_line(&arrecadacao_barcode_to_line(&digits));
                if arrecadacao.valid {
                    return Ok(arrecadacao);
                }
                let bank = parse_bank_line(&bank_barcode_to_line(&digits), reference);
                Ok(if bank.valid { bank } else { arrecadacao })
            } else {
                Ok(parse_bank_line(&bank_barcode_to_line(&digits), reference))
            }
        }
        n => Err(BoletoError::Length(n)),
    }
}

fn check(name: &str, digits: &str, found: u32, expected: Option<u32>) -> FieldCheck {
    let expected_dv = expected.unwrap_or(u32::MAX);
    FieldCheck {
        name: name.to_string(),
        digits: digits.to_string(),
        expected_dv,
        found_dv: found,
        ok: expected == Some(found),
    }
}

fn d(c: u8) -> u32 {
    u32::from(c.saturating_sub(b'0'))
}

fn parse_bank_line(line: &str, reference: NaiveDate) -> Boleto {
    let b = line.as_bytes();
    let field1 = &line[0..9];
    let field2 = &line[10..20];
    let field3 = &line[21..31];
    let general_found = d(b[32]);
    let factor_text = &line[33..37];
    let value_text = &line[37..47];
    let bank_code = line[0..3].to_string();
    let currency = d(b[3]) as u8;
    let free_field = format!("{}{}{}", &line[4..9], field2, field3);
    let barcode = format!(
        "{}{}{}{}{}{}",
        &line[0..4],
        general_found,
        factor_text,
        value_text,
        &line[4..9],
        &line[10..20]
    ) + field3;
    let general_digits = format!("{}{}", &barcode[0..4], &barcode[5..44]);

    let field_checks = vec![
        check("campo1", field1, d(b[9]), mod10(field1)),
        check("campo2", field2, d(b[20]), mod10(field2)),
        check("campo3", field3, d(b[31]), mod10(field3)),
    ];
    let general_check = check(
        "geral",
        &general_digits,
        general_found,
        mod11_boleto(&general_digits),
    );

    let value_centavos: i64 = value_text.parse().unwrap_or(0);
    let due_factor: u16 = factor_text.parse().unwrap_or(0);
    let due_date_candidates = factor_to_dates(due_factor);
    let chosen = due_date_candidates
        .iter()
        .min_by_key(|(_, date)| (*date - reference).num_days().abs())
        .copied();

    let mut problems = Vec::new();
    if currency != 9 {
        problems.push(format!("código de moeda {currency} (esperado 9 = real)"));
    }
    if bank_code == "000" {
        problems.push("código do banco 000".to_string());
    }
    for fc in field_checks.iter().chain(std::iter::once(&general_check)) {
        if !fc.ok {
            problems.push(format!(
                "dígito verificador do {} é {} (esperado {})",
                fc.name, fc.found_dv, fc.expected_dv
            ));
        }
    }
    let valid = problems.is_empty();
    Boleto {
        kind: BoletoKind::Bank,
        line: line.to_string(),
        barcode,
        bank_code: Some(bank_code),
        currency: Some(currency),
        segment: None,
        value_identifier: None,
        company_id: None,
        value_centavos,
        value: (value_centavos > 0).then_some(value_centavos),
        due_factor: Some(due_factor),
        due_date: chosen.map(|(_, date)| date),
        due_date_cycle: chosen.map(|(cycle, _)| cycle),
        due_date_candidates,
        free_field,
        field_checks,
        general_check,
        problems,
        valid,
    }
}

fn parse_arrecadacao_line(line: &str) -> Boleto {
    let b = line.as_bytes();
    let mut barcode = String::with_capacity(44);
    let mut blocks = Vec::with_capacity(4);
    for i in 0..4 {
        let block = &line[i * 12..i * 12 + 11];
        barcode.push_str(block);
        blocks.push((block, d(b[i * 12 + 11])));
    }
    let product = d(b[0]) as u8;
    let segment = d(b[1]) as u8;
    let identifier = d(b[2]) as u8;
    let general_found = d(b[3]);
    let value_text = &barcode[4..15];
    let company_id = barcode[15..19].to_string();
    let free_field = barcode[19..44].to_string();
    let general_digits = format!("{}{}", &barcode[0..3], &barcode[4..44]);

    let modulus: fn(&str) -> Option<u32> = match identifier {
        6 | 7 => mod10,
        8 | 9 => mod11_zero_rule,
        _ => |_| None,
    };
    let field_checks: Vec<FieldCheck> = blocks
        .iter()
        .enumerate()
        .map(|(i, (block, found))| check(&format!("bloco{}", i + 1), block, *found, modulus(block)))
        .collect();
    let general_check = check(
        "geral",
        &general_digits,
        general_found,
        modulus(&general_digits),
    );

    let value_centavos: i64 = value_text.parse().unwrap_or(0);
    let carries_value = matches!(identifier, 6 | 8) && value_centavos > 0;

    let mut problems = Vec::new();
    if product != 8 {
        problems.push(format!("identificador de produto {product} (esperado 8)"));
    }
    if !matches!(segment, 1..=7 | 9) {
        problems.push(format!("segmento {segment} inválido"));
    }
    if !matches!(identifier, 6..=9) {
        problems.push(format!(
            "identificador de valor {identifier} inválido (6 a 9)"
        ));
    }
    for fc in field_checks.iter().chain(std::iter::once(&general_check)) {
        if !fc.ok {
            problems.push(format!(
                "dígito verificador do {} é {} (esperado {})",
                fc.name, fc.found_dv, fc.expected_dv
            ));
        }
    }
    let valid = problems.is_empty();
    Boleto {
        kind: BoletoKind::Arrecadacao,
        line: line.to_string(),
        barcode,
        bank_code: None,
        currency: None,
        segment: Some(segment),
        value_identifier: Some(identifier),
        company_id: Some(company_id),
        value_centavos,
        value: carries_value.then_some(value_centavos),
        due_factor: None,
        due_date: None,
        due_date_cycle: None,
        due_date_candidates: Vec::new(),
        free_field,
        field_checks,
        general_check,
        problems,
        valid,
    }
}

/// Converts a 44-digit bank barcode into its 47-digit line (check digits recomputed).
pub fn bank_barcode_to_line(barcode: &str) -> String {
    let bank_currency = &barcode[0..4];
    let general = &barcode[4..5];
    let factor_value = &barcode[5..19];
    let free = &barcode[19..44];
    let f1 = format!("{bank_currency}{}", &free[0..5]);
    let f2 = &free[5..15];
    let f3 = &free[15..25];
    format!(
        "{f1}{}{f2}{}{f3}{}{general}{factor_value}",
        mod10(&f1).unwrap_or(0),
        mod10(f2).unwrap_or(0),
        mod10(f3).unwrap_or(0)
    )
}

/// Converts a 44-digit arrecadação barcode into its 48-digit line.
pub fn arrecadacao_barcode_to_line(barcode: &str) -> String {
    let identifier = barcode.as_bytes()[2];
    let modulus: fn(&str) -> Option<u32> = match identifier {
        b'8' | b'9' => mod11_zero_rule,
        _ => mod10,
    };
    let mut out = String::with_capacity(48);
    for i in 0..4 {
        let block = &barcode[i * 11..i * 11 + 11];
        out.push_str(block);
        out.push_str(&modulus(block).unwrap_or(0).to_string());
    }
    out
}

/// Builds a valid 47-digit bank line from its parts (tests, generators, synthetic data).
///
/// `bank` has three digits, `free_field` has 25; `value_centavos` must fit ten digits.
pub fn compose_bank_line(
    bank: &str,
    currency: u8,
    factor: u16,
    value_centavos: i64,
    free_field: &str,
) -> Option<String> {
    if bank.len() != 3 || !all_digits(bank) || free_field.len() != 25 || !all_digits(free_field) {
        return None;
    }
    if !(0..=9_999_999_999).contains(&value_centavos) || factor > 9999 || currency > 9 {
        return None;
    }
    let body = format!("{bank}{currency}{factor:04}{value_centavos:010}{free_field}");
    let general = mod11_boleto(&body)?;
    let barcode = format!("{}{general}{}", &body[0..4], &body[4..]);
    Some(bank_barcode_to_line(&barcode))
}

/// Builds a valid 48-digit arrecadação line from its parts.
///
/// `value` has up to eleven digits (centavos for identifiers 6 and 8), `company` four digits
/// and `free_field` 25 digits.
pub fn compose_arrecadacao_line(
    segment: u8,
    identifier: u8,
    value: i64,
    company: &str,
    free_field: &str,
) -> Option<String> {
    if !matches!(segment, 1..=7 | 9) || !matches!(identifier, 6..=9) {
        return None;
    }
    if company.len() != 4
        || !all_digits(company)
        || free_field.len() != 25
        || !all_digits(free_field)
    {
        return None;
    }
    if !(0..=99_999_999_999).contains(&value) {
        return None;
    }
    let body = format!("8{segment}{identifier}{value:011}{company}{free_field}");
    let general = match identifier {
        6 | 7 => mod10(&body)?,
        _ => mod11_zero_rule(&body)?,
    };
    let barcode = format!("{}{general}{}", &body[0..3], &body[3..]);
    Some(arrecadacao_barcode_to_line(&barcode))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn date(y: i32, m: u32, d: u32) -> NaiveDate {
        NaiveDate::from_ymd_opt(y, m, d).unwrap()
    }

    #[test]
    fn factor_cycles() {
        assert_eq!(
            expected_factor(date(2000, 7, 3)),
            Some((1000, FactorCycle::First))
        );
        assert_eq!(
            expected_factor(date(2025, 2, 21)),
            Some((9999, FactorCycle::First))
        );
        assert_eq!(
            expected_factor(date(2025, 2, 22)),
            Some((1000, FactorCycle::Second))
        );
        assert_eq!(
            expected_factor(date(2026, 11, 4)),
            Some((1620, FactorCycle::Second))
        );
        assert_eq!(
            expected_factor(date(1997, 10, 8)),
            Some((1, FactorCycle::First))
        );
        assert_eq!(expected_factor(date(1997, 10, 7)), None);
        assert_eq!(
            expected_factor(date(2049, 10, 13)),
            Some((9999, FactorCycle::Second))
        );
        assert_eq!(expected_factor(date(2049, 10, 14)), None);

        let c = factor_to_dates(1000);
        assert_eq!(
            c,
            vec![
                (FactorCycle::First, date(2000, 7, 3)),
                (FactorCycle::Second, date(2025, 2, 22))
            ]
        );
        assert!(factor_to_dates(0).is_empty());
        assert_eq!(
            factor_to_dates(1),
            vec![(FactorCycle::First, date(1997, 10, 8))]
        );
        // known factor from the FEBRABAN circular: 9999 = 21/02/2025
        assert_eq!(factor_to_dates(9999)[0].1, date(2025, 2, 21));
        // round trip every factor of the second cycle
        for factor in 1000u16..=9999 {
            let (cycle, d) = factor_to_dates(factor)[1];
            assert_eq!(cycle, FactorCycle::Second);
            assert_eq!(expected_factor(d), Some((factor, FactorCycle::Second)));
        }
    }

    #[test]
    fn composes_and_parses_bank_line() {
        let (factor, _) = expected_factor(date(2026, 3, 10)).unwrap();
        let line =
            compose_bank_line("237", 9, factor, 184_500, "3381260007827139500006330").unwrap();
        assert_eq!(line.len(), 47);
        let b = parse_boleto_with_reference(&line, date(2026, 2, 1)).unwrap();
        assert!(b.valid, "{:?}", b.problems);
        assert_eq!(b.kind, BoletoKind::Bank);
        assert_eq!(b.bank_code.as_deref(), Some("237"));
        assert_eq!(b.currency, Some(9));
        assert_eq!(b.value, Some(184_500));
        assert_eq!(b.due_factor, Some(factor));
        assert_eq!(b.due_date, Some(date(2026, 3, 10)));
        assert_eq!(b.due_date_cycle, Some(FactorCycle::Second));
        assert_eq!(b.due_date_candidates.len(), 2);
        assert_eq!(b.free_field, "3381260007827139500006330");
        assert_eq!(b.barcode.len(), 44);
        assert!(b.field_checks.iter().all(|f| f.ok));
        assert!(b.general_check.ok);
        // barcode input yields the same parse
        let from_barcode = parse_boleto_with_reference(&b.barcode, date(2026, 2, 1)).unwrap();
        assert_eq!(from_barcode.line, b.line);
        // formatted input with separators and spaces
        let pretty = format!(
            "{}.{} {}.{} {}.{} {} {}",
            &line[0..5],
            &line[5..10],
            &line[10..15],
            &line[15..21],
            &line[21..26],
            &line[26..32],
            &line[32..33],
            &line[33..47]
        );
        assert_eq!(
            parse_boleto_with_reference(&pretty, date(2026, 2, 1))
                .unwrap()
                .line,
            line
        );
    }

    #[test]
    fn reference_date_picks_the_cycle() {
        let line = compose_bank_line("001", 9, 1000, 100, &"0".repeat(25)).unwrap();
        let old = parse_boleto_with_reference(&line, date(2001, 1, 1)).unwrap();
        assert_eq!(old.due_date, Some(date(2000, 7, 3)));
        assert_eq!(old.due_date_cycle, Some(FactorCycle::First));
        let new = parse_boleto_with_reference(&line, date(2026, 1, 1)).unwrap();
        assert_eq!(new.due_date, Some(date(2025, 2, 22)));
        assert_eq!(new.due_date_cycle, Some(FactorCycle::Second));
        // factor 0000: no due date, open value 0 -> None
        let open = compose_bank_line("001", 9, 0, 0, &"0".repeat(25)).unwrap();
        let p = parse_boleto_with_reference(&open, date(2026, 1, 1)).unwrap();
        assert_eq!(p.due_date, None);
        assert_eq!(p.value, None);
        assert_eq!(p.value_centavos, 0);
        assert!(p.valid);
    }

    #[test]
    fn detects_every_corrupted_check_digit() {
        let line = compose_bank_line("341", 9, 1500, 99_999, "1234567890123456789012345").unwrap();
        for pos in [9usize, 20, 31, 32] {
            let mut bad = line.clone().into_bytes();
            bad[pos] = if bad[pos] == b'9' { b'0' } else { bad[pos] + 1 };
            let bad = String::from_utf8(bad).unwrap();
            let p = parse_boleto_with_reference(&bad, date(2026, 1, 1)).unwrap();
            assert!(!p.valid, "position {pos}");
            assert_eq!(p.problems.len(), 1, "position {pos}: {:?}", p.problems);
            assert!(p.problems[0].contains("dígito verificador"));
        }
        // a wrong value digit breaks the general DV only
        let mut bad = line.clone().into_bytes();
        bad[46] = if bad[46] == b'9' { b'0' } else { bad[46] + 1 };
        let p = parse_boleto_with_reference(&String::from_utf8(bad).unwrap(), date(2026, 1, 1))
            .unwrap();
        assert!(!p.general_check.ok);
        assert!(p.field_checks.iter().all(|f| f.ok));
    }

    #[test]
    fn composes_and_parses_arrecadacao_lines() {
        for identifier in [6u8, 7, 8, 9] {
            let line = compose_arrecadacao_line(
                3,
                identifier,
                12_345,
                "0137",
                "0000000000000000000000001",
            )
            .unwrap();
            assert_eq!(line.len(), 48);
            let b = parse_boleto_with_reference(&line, date(2026, 1, 1)).unwrap();
            assert!(b.valid, "identifier {identifier}: {:?}", b.problems);
            assert_eq!(b.kind, BoletoKind::Arrecadacao);
            assert_eq!(b.segment, Some(3));
            assert_eq!(b.value_identifier, Some(identifier));
            assert_eq!(b.company_id.as_deref(), Some("0137"));
            assert_eq!(b.value_centavos, 12_345);
            assert_eq!(
                b.value,
                if matches!(identifier, 6 | 8) {
                    Some(12_345)
                } else {
                    None
                }
            );
            assert_eq!(b.field_checks.len(), 4);
            assert_eq!(b.due_date, None);
            let from_barcode = parse_boleto_with_reference(&b.barcode, date(2026, 1, 1)).unwrap();
            assert_eq!(from_barcode.line, line);
            // corrupt a block DV
            let mut bad = line.clone().into_bytes();
            bad[11] = if bad[11] == b'9' { b'0' } else { bad[11] + 1 };
            let p = parse_boleto_with_reference(&String::from_utf8(bad).unwrap(), date(2026, 1, 1))
                .unwrap();
            assert!(!p.valid);
            assert!(!p.field_checks[0].ok);
        }
    }

    #[test]
    fn structural_problems_in_arrecadacao() {
        // product digit must be 8; segment 8 is invalid; identifier 5 is invalid
        let line = format!("{}{}", "15500000012345", "0".repeat(34));
        let p = parse_boleto_with_reference(&line, date(2026, 1, 1)).unwrap();
        assert!(!p.valid);
        assert!(p.problems.iter().any(|s| s.contains("produto")));
        assert!(p
            .problems
            .iter()
            .any(|s| s.contains("identificador de valor")));
    }

    #[test]
    fn rejects_bad_lengths_and_bad_parts() {
        assert_eq!(
            parse_boleto_with_reference("", date(2026, 1, 1)),
            Err(BoletoError::Length(0))
        );
        assert_eq!(
            parse_boleto_with_reference("1234", date(2026, 1, 1)),
            Err(BoletoError::Length(4))
        );
        assert!(compose_bank_line("23", 9, 1000, 1, &"0".repeat(25)).is_none());
        assert!(compose_bank_line("237", 9, 1000, -1, &"0".repeat(25)).is_none());
        assert!(compose_bank_line("237", 9, 10_000, 1, &"0".repeat(25)).is_none());
        assert!(compose_bank_line("237", 9, 1000, 1, "123").is_none());
        assert!(compose_arrecadacao_line(8, 6, 1, "0137", &"0".repeat(25)).is_none());
        assert!(compose_arrecadacao_line(3, 5, 1, "0137", &"0".repeat(25)).is_none());
        assert!(compose_arrecadacao_line(3, 6, 1, "13", &"0".repeat(25)).is_none());
    }

    #[test]
    fn known_public_example_line() {
        // Widely published Banco do Brasil example: barcode
        // 00190000090114971860168524522114675860000102656 is its line.
        let b = parse_boleto_with_reference(
            "00190.00009 01149.718601 68524.522114 6 75860000102656",
            date(2018, 6, 1),
        )
        .unwrap();
        assert!(b.valid, "{:?}", b.problems);
        assert_eq!(b.bank_code.as_deref(), Some("001"));
        assert_eq!(b.value, Some(102_656));
        assert_eq!(b.due_factor, Some(7586));
        assert_eq!(b.due_date, Some(date(2018, 7, 15)));
    }
}
