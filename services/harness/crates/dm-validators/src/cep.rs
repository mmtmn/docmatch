//! CEP (postal code) format check.

use crate::digits::{all_digits, all_same};
use crate::{CheckResult, Verdict};

/// Checks the format of a CEP (`01310-100` or `01310100`).
///
/// There is no check digit: the verdict is [`Verdict::Valid`] for eight digits that are not
/// all the same, [`Verdict::Invalid`] for `00000-000`-like values and [`Verdict::Malformed`]
/// for anything else.
pub fn validate_cep(input: &str) -> CheckResult {
    let trimmed = input.trim();
    let digits: String = trimmed
        .chars()
        .filter(|c| !matches!(c, '-' | '.' | ' '))
        .collect();
    if digits.len() != 8 || !all_digits(&digits) {
        return CheckResult::malformed();
    }
    // Only one hyphen, and only before the last three digits, is a well-formed mask.
    let hyphens = trimmed.matches('-').count();
    if hyphens > 1 || (hyphens == 1 && trimmed.find('-') != Some(trimmed.len() - 4)) {
        return CheckResult::malformed();
    }
    let formatted = format!("{}-{}", &digits[..5], &digits[5..]);
    let verdict = if all_same(&digits) {
        Verdict::Invalid
    } else {
        Verdict::Valid
    };
    CheckResult {
        verdict,
        digits,
        formatted: Some(formatted),
        expected_dv: None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validates_cep() {
        assert_eq!(validate_cep("01310-100").verdict, Verdict::Valid);
        assert_eq!(validate_cep("01310100").verdict, Verdict::Valid);
        assert_eq!(
            validate_cep("01310100").formatted.as_deref(),
            Some("01310-100")
        );
        assert_eq!(validate_cep("01.310-100").verdict, Verdict::Valid);
        assert_eq!(validate_cep("00000-000").verdict, Verdict::Invalid);
        assert_eq!(validate_cep("0131-0100").verdict, Verdict::Malformed);
        assert_eq!(validate_cep("01310-10").verdict, Verdict::Malformed);
        assert_eq!(validate_cep("").verdict, Verdict::Malformed);
        assert_eq!(validate_cep("abcde-fgh").verdict, Verdict::Malformed);
    }
}
