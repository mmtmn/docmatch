//! CNPJ check digits, numeric (`12.345.678/0001-95`) and alphanumeric
//! (`12.ABC.345/01DE-35`, issued from July 2026: letters count as `ASCII - 48`).

use serde::{Deserialize, Serialize};

use crate::digits::{all_same, mod11_weighted};
use crate::{strip_mask, CheckResult, Verdict};

const WEIGHTS_FIRST: [u32; 12] = [5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2];
const WEIGHTS_SECOND: [u32; 13] = [6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2];

/// Whether a CNPJ uses only digits or the alphanumeric layout.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CnpjFormat {
    /// Fourteen digits.
    Numeric,
    /// Twelve letters or digits followed by two check digits.
    Alphanumeric,
}

/// Value of a CNPJ character for the check-digit sum (`0-9` as digits, `A-Z` as ASCII − 48).
fn char_value(c: u8) -> Option<u32> {
    match c {
        b'0'..=b'9' | b'A'..=b'Z' => Some(u32::from(c) - 48),
        _ => None,
    }
}

/// Computes the two check digits of a 12-character CNPJ base (digits or uppercase letters).
pub fn compute_dv(base: &[u32; 12]) -> (u32, u32) {
    let d1 = mod11_weighted(base, &WEIGHTS_FIRST);
    let mut with_d1 = [0u32; 13];
    with_d1[..12].copy_from_slice(base);
    with_d1[12] = d1;
    let d2 = mod11_weighted(&with_d1, &WEIGHTS_SECOND);
    (d1, d2)
}

/// Detects the layout of a normalized 14-character CNPJ.
pub fn detect_format(normalized: &str) -> Option<CnpjFormat> {
    if normalized.len() != 14 {
        return None;
    }
    let bytes = normalized.as_bytes();
    if !bytes[12..].iter().all(u8::is_ascii_digit) {
        return None;
    }
    if bytes[..12].iter().all(u8::is_ascii_digit) {
        Some(CnpjFormat::Numeric)
    } else if bytes[..12]
        .iter()
        .all(|b| b.is_ascii_digit() || b.is_ascii_uppercase())
    {
        Some(CnpjFormat::Alphanumeric)
    } else {
        None
    }
}

/// Validates a CNPJ given formatted or bare input, numeric or alphanumeric.
///
/// Lowercase letters are accepted and uppercased. Returns [`Verdict::Malformed`] when the
/// input is not fourteen characters of the right classes after removing the mask.
pub fn cnpj_dv(input: &str) -> CheckResult {
    let normalized = strip_mask(input.trim()).to_ascii_uppercase();
    let Some(_format) = detect_format(&normalized) else {
        return CheckResult::malformed();
    };
    let bytes = normalized.as_bytes();
    let mut base = [0u32; 12];
    for (i, b) in bytes[..12].iter().enumerate() {
        match char_value(*b) {
            Some(v) => base[i] = v,
            None => return CheckResult::malformed(),
        }
    }
    let (d1, d2) = compute_dv(&base);
    let expected = format!("{d1}{d2}");
    let formatted = format!(
        "{}.{}.{}/{}-{}",
        &normalized[..2],
        &normalized[2..5],
        &normalized[5..8],
        &normalized[8..12],
        &normalized[12..]
    );
    let verdict = if all_same(&normalized) || expected != normalized[12..] {
        Verdict::Invalid
    } else {
        Verdict::Valid
    };
    CheckResult {
        verdict,
        digits: normalized,
        formatted: Some(formatted),
        expected_dv: Some(expected),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn valid_numeric_cnpjs() {
        for input in [
            "11.222.333/0001-81",
            "11222333000181",
            "11 222 333 0001 81",
            "45.723.174/0001-10",
        ] {
            let r = cnpj_dv(input);
            assert_eq!(r.verdict, Verdict::Valid, "{input}");
            assert_eq!(r.formatted.as_deref().map(str::len), Some(18));
        }
        assert_eq!(
            cnpj_dv("11222333000181").formatted.as_deref(),
            Some("11.222.333/0001-81")
        );
        assert_eq!(detect_format("11222333000181"), Some(CnpjFormat::Numeric));
    }

    #[test]
    fn valid_alphanumeric_cnpj() {
        // Receita Federal example for the alphanumeric layout.
        let r = cnpj_dv("12.ABC.345/01DE-35");
        assert_eq!(r.verdict, Verdict::Valid);
        assert_eq!(r.digits, "12ABC34501DE35");
        assert_eq!(
            detect_format("12ABC34501DE35"),
            Some(CnpjFormat::Alphanumeric)
        );
        assert_eq!(cnpj_dv("12abc34501de35").verdict, Verdict::Valid);
        assert_eq!(cnpj_dv("12.ABC.345/01DE-36").verdict, Verdict::Invalid);
    }

    #[test]
    fn invalid_cnpjs() {
        assert_eq!(cnpj_dv("11.222.333/0001-82").verdict, Verdict::Invalid);
        assert_eq!(
            cnpj_dv("11.222.333/0001-82").expected_dv.as_deref(),
            Some("81")
        );
        assert_eq!(cnpj_dv("00.000.000/0000-00").verdict, Verdict::Invalid);
        assert_eq!(cnpj_dv("11.111.111/1111-11").verdict, Verdict::Invalid);
    }

    #[test]
    fn malformed_cnpjs() {
        for input in [
            "",
            "1122233300018",
            "112223330001811",
            "11.222.333/0001-8A",
            "ab.cde.fgh/ijkl-mn!",
            "11222333000181x",
        ] {
            assert_eq!(cnpj_dv(input).verdict, Verdict::Malformed, "{input:?}");
        }
    }
}
