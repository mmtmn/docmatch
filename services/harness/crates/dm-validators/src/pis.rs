//! PIS/PASEP/NIT check digit.

use crate::digits::{all_digits, all_same, mod11_weighted};
use crate::{strip_mask, CheckResult, Verdict};

const WEIGHTS: [u32; 10] = [3, 2, 9, 8, 7, 6, 5, 4, 3, 2];

/// Validates a PIS/PASEP number (`123.45678.90-1` or bare eleven digits).
pub fn validate_pis(input: &str) -> CheckResult {
    let digits = strip_mask(input.trim());
    if digits.len() != 11 || !all_digits(&digits) {
        return CheckResult::malformed();
    }
    let values: Vec<u32> = digits.bytes().map(|b| u32::from(b - b'0')).collect();
    let expected = mod11_weighted(&values[..10], &WEIGHTS);
    let expected_s = expected.to_string();
    let formatted = format!(
        "{}.{}.{}-{}",
        &digits[..3],
        &digits[3..8],
        &digits[8..10],
        &digits[10..]
    );
    let verdict = if all_same(&digits) || expected_s != digits[10..] {
        Verdict::Invalid
    } else {
        Verdict::Valid
    };
    CheckResult {
        verdict,
        digits,
        formatted: Some(formatted),
        expected_dv: Some(expected_s),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validates_pis() {
        // the mask shape is irrelevant: eleven digits are checked whatever the grouping
        assert_eq!(validate_pis("120.4567.891-4").verdict, Verdict::Invalid);
        // 1204567891: 1*3+2*2+0*9+4*8+5*7+6*6+7*5+8*4+9*3+1*2 = 3+4+0+32+35+36+35+32+27+2 = 206; 206%11=8 -> 3
        assert_eq!(validate_pis("120.45678.91-3").verdict, Verdict::Valid);
        assert_eq!(
            validate_pis("12045678913").formatted.as_deref(),
            Some("120.45678.91-3")
        );
        assert_eq!(validate_pis("120.45678.91-4").verdict, Verdict::Invalid);
        assert_eq!(validate_pis("111.11111.11-1").verdict, Verdict::Invalid);
        assert_eq!(validate_pis("").verdict, Verdict::Malformed);
        assert_eq!(validate_pis("1204567891x").verdict, Verdict::Malformed);
    }
}
