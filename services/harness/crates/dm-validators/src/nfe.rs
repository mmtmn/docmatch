//! NF-e / NFC-e / CT-e access key (44 digits) check digit and decoding.

use serde::{Deserialize, Serialize};

use crate::digits::{all_digits, mod11_zero_rule};
use crate::{strip_mask, Verdict};

/// IBGE codes of the Brazilian states, as used in the first two digits of the key.
const UF_CODES: [u8; 27] = [
    11, 12, 13, 14, 15, 16, 17, 21, 22, 23, 24, 25, 26, 27, 28, 29, 31, 32, 33, 35, 41, 42, 43, 50,
    51, 52, 53,
];

/// Decoded fields of an access key.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct NfeKey {
    /// Verdict on the check digit and structure.
    pub verdict: Verdict,
    /// The 44 digits.
    pub digits: String,
    /// IBGE state code (`cUF`).
    pub uf_code: u8,
    /// Year of issue (two digits, `AA`).
    pub year: u8,
    /// Month of issue (`MM`).
    pub month: u8,
    /// Issuer CNPJ (14 digits).
    pub cnpj: String,
    /// Fiscal model: 55 (NF-e), 65 (NFC-e), 57 (CT-e), 58 (MDF-e).
    pub model: u8,
    /// Series.
    pub series: u16,
    /// Document number (`nNF`), without leading zeros.
    pub number: u32,
    /// Issue type (`tpEmis`).
    pub emission_type: u8,
    /// Numeric code (`cNF`).
    pub numeric_code: u32,
    /// Check digit found in the key.
    pub check_digit: u8,
    /// Check digit the first 43 digits require.
    pub expected_check_digit: u8,
    /// Structural problems found (pt-BR), empty when none.
    pub problems: Vec<String>,
}

/// Validates and decodes a 44-digit access key (separators and spaces are ignored).
///
/// Returns `None` when the input is not 44 digits (malformed). Otherwise the verdict is
/// [`Verdict::Valid`] when the mod-11 check digit agrees and the structure is sane, or
/// [`Verdict::Invalid`] with the list of problems.
pub fn nfe_key_dv(input: &str) -> Option<NfeKey> {
    let digits = strip_mask(input.trim());
    if digits.len() != 44 || !all_digits(&digits) {
        return None;
    }
    let expected = mod11_zero_rule(&digits[..43])? as u8;
    let found = digits.as_bytes()[43] - b'0';
    let uf_code: u8 = digits[0..2].parse().ok()?;
    let year: u8 = digits[2..4].parse().ok()?;
    let month: u8 = digits[4..6].parse().ok()?;
    let cnpj = digits[6..20].to_string();
    let model: u8 = digits[20..22].parse().ok()?;
    let series: u16 = digits[22..25].parse().ok()?;
    let number: u32 = digits[25..34].parse().ok()?;
    let emission_type: u8 = digits[34..35].parse().ok()?;
    let numeric_code: u32 = digits[35..43].parse().ok()?;

    let mut problems = Vec::new();
    if expected != found {
        problems.push(format!(
            "dígito verificador {found} difere do esperado {expected}"
        ));
    }
    if !UF_CODES.contains(&uf_code) {
        problems.push(format!("código de UF {uf_code} não existe"));
    }
    if !(1..=12).contains(&month) {
        problems.push(format!("mês {month} inválido"));
    }
    if !matches!(model, 55 | 65 | 57 | 58) {
        problems.push(format!("modelo {model} não é NF-e, NFC-e, CT-e ou MDF-e"));
    }
    if number == 0 {
        problems.push("número do documento é zero".to_string());
    }
    if !(1..=9).contains(&emission_type) {
        problems.push(format!("tipo de emissão {emission_type} inválido"));
    }
    let verdict = if problems.is_empty() {
        Verdict::Valid
    } else {
        Verdict::Invalid
    };
    Some(NfeKey {
        verdict,
        digits,
        uf_code,
        year,
        month,
        cnpj,
        model,
        series,
        number,
        emission_type,
        numeric_code,
        check_digit: found,
        expected_check_digit: expected,
        problems,
    })
}

/// Builds a key from its parts, computing the check digit (used by tests and generators).
#[allow(clippy::too_many_arguments)]
pub fn compose_key(
    uf_code: u8,
    year: u8,
    month: u8,
    cnpj: &str,
    model: u8,
    series: u16,
    number: u32,
    emission_type: u8,
    numeric_code: u32,
) -> Option<String> {
    if cnpj.len() != 14 || !all_digits(cnpj) {
        return None;
    }
    let base = format!(
        "{uf_code:02}{year:02}{month:02}{cnpj}{model:02}{series:03}{number:09}{emission_type:01}{numeric_code:08}"
    );
    if base.len() != 43 {
        return None;
    }
    let dv = mod11_zero_rule(&base)?;
    Some(format!("{base}{dv}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn composes_and_validates() {
        let key = compose_key(35, 24, 7, "11222333000181", 55, 1, 88231, 1, 12_345_678).unwrap();
        assert_eq!(key.len(), 44);
        let parsed = nfe_key_dv(&key).unwrap();
        assert_eq!(parsed.verdict, Verdict::Valid, "{:?}", parsed.problems);
        assert_eq!(parsed.uf_code, 35);
        assert_eq!(parsed.number, 88231);
        assert_eq!(parsed.series, 1);
        assert_eq!(parsed.cnpj, "11222333000181");
        assert_eq!(parsed.model, 55);
        assert_eq!(parsed.numeric_code, 12_345_678);
        assert_eq!(parsed.check_digit, parsed.expected_check_digit);
    }

    #[test]
    fn accepts_separators_and_flags_bad_dv() {
        let key = compose_key(41, 25, 1, "45723174000110", 65, 2, 7, 1, 1).unwrap();
        let spaced: String = key
            .as_bytes()
            .chunks(4)
            .map(|c| std::str::from_utf8(c).unwrap())
            .collect::<Vec<_>>()
            .join(" ");
        assert_eq!(nfe_key_dv(&spaced).unwrap().verdict, Verdict::Valid);
        let mut bad = key.clone();
        let last = bad.pop().unwrap();
        bad.push(if last == '0' { '1' } else { '0' });
        let parsed = nfe_key_dv(&bad).unwrap();
        assert_eq!(parsed.verdict, Verdict::Invalid);
        assert!(parsed.problems[0].contains("dígito verificador"));
    }

    #[test]
    fn structural_problems() {
        let key = compose_key(99, 25, 13, "45723174000110", 10, 2, 0, 0, 1).unwrap();
        let parsed = nfe_key_dv(&key).unwrap();
        assert_eq!(parsed.verdict, Verdict::Invalid);
        assert_eq!(parsed.problems.len(), 5);
    }

    #[test]
    fn malformed_keys() {
        assert!(nfe_key_dv("").is_none());
        assert!(nfe_key_dv("1234").is_none());
        assert!(nfe_key_dv(&"1".repeat(45)).is_none());
        assert!(nfe_key_dv(&format!("{}a", "1".repeat(43))).is_none());
        assert!(compose_key(35, 24, 7, "123", 55, 1, 1, 1, 1).is_none());
    }
}
