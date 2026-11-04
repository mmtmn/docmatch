//! CPF check digits.

use crate::digits::{all_digits, all_same, mod11_weighted};
use crate::{strip_mask, CheckResult, Verdict};

const WEIGHTS_FIRST: [u32; 9] = [10, 9, 8, 7, 6, 5, 4, 3, 2];
const WEIGHTS_SECOND: [u32; 10] = [11, 10, 9, 8, 7, 6, 5, 4, 3, 2];

/// Computes the two check digits of a 9-digit CPF base.
pub fn compute_dv(base: &[u32; 9]) -> (u32, u32) {
    let d1 = mod11_weighted(base, &WEIGHTS_FIRST);
    let mut with_d1 = [0u32; 10];
    with_d1[..9].copy_from_slice(base);
    with_d1[9] = d1;
    let d2 = mod11_weighted(&with_d1, &WEIGHTS_SECOND);
    (d1, d2)
}

/// Validates a CPF given formatted (`123.456.789-09`) or bare (`12345678909`) input.
///
/// Returns [`Verdict::Malformed`] for anything that is not eleven digits after removing the
/// mask, [`Verdict::Invalid`] when the check digits disagree or all digits are the same.
pub fn cpf_dv(input: &str) -> CheckResult {
    let digits = strip_mask(input.trim());
    if digits.len() != 11 || !all_digits(&digits) {
        return CheckResult::malformed();
    }
    let values: Vec<u32> = digits.bytes().map(|b| u32::from(b - b'0')).collect();
    let mut base = [0u32; 9];
    base.copy_from_slice(&values[..9]);
    let (d1, d2) = compute_dv(&base);
    let expected = format!("{d1}{d2}");
    let formatted = format!(
        "{}.{}.{}-{}",
        &digits[..3],
        &digits[3..6],
        &digits[6..9],
        &digits[9..]
    );
    let verdict = if all_same(&digits) || expected != digits[9..] {
        Verdict::Invalid
    } else {
        Verdict::Valid
    };
    CheckResult {
        verdict,
        digits,
        formatted: Some(formatted),
        expected_dv: Some(expected),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn valid_cpfs() {
        for input in [
            "111.444.777-35",
            "11144477735",
            " 111 444 777 35 ",
            "529.982.247-25",
        ] {
            let r = cpf_dv(input);
            assert_eq!(r.verdict, Verdict::Valid, "{input}");
            assert_eq!(r.digits.len(), 11);
        }
        assert_eq!(
            cpf_dv("11144477735").formatted.as_deref(),
            Some("111.444.777-35")
        );
    }

    #[test]
    fn invalid_cpfs() {
        assert_eq!(cpf_dv("111.444.777-36").verdict, Verdict::Invalid);
        assert_eq!(cpf_dv("111.444.777-45").verdict, Verdict::Invalid);
        assert_eq!(cpf_dv("111.111.111-11").verdict, Verdict::Invalid);
        assert_eq!(cpf_dv("000.000.000-00").verdict, Verdict::Invalid);
        assert_eq!(cpf_dv("111.444.777-36").expected_dv.as_deref(), Some("35"));
    }

    #[test]
    fn malformed_cpfs() {
        for input in [
            "",
            "123",
            "111.444.777-3",
            "111.444.777-355",
            "abc.def.ghi-jk",
            "1114447773a",
        ] {
            assert_eq!(cpf_dv(input).verdict, Verdict::Malformed, "{input:?}");
        }
    }
}
