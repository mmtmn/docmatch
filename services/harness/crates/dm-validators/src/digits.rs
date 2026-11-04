//! Shared check-digit arithmetic (módulo 10 and módulo 11 in their Brazilian variants).

/// Parses an ASCII digit.
pub(crate) fn digit(c: u8) -> Option<u32> {
    if c.is_ascii_digit() {
        Some(u32::from(c - b'0'))
    } else {
        None
    }
}

/// True when every byte is an ASCII digit and the slice is not empty.
pub(crate) fn all_digits(s: &str) -> bool {
    !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit())
}

/// True when all characters are the same (`111.111.111-11`), which the DV math accepts
/// but Receita Federal does not.
pub(crate) fn all_same(s: &str) -> bool {
    let mut chars = s.chars();
    match chars.next() {
        Some(first) => chars.all(|c| c == first),
        None => true,
    }
}

/// Módulo 10 (FEBRABAN): weights 2,1,2,1... from the right; products ≥ 10 have their digits
/// summed; the digit is what completes the next multiple of ten.
pub fn mod10(digits: &str) -> Option<u32> {
    if !all_digits(digits) {
        return None;
    }
    let mut sum = 0;
    for (i, b) in digits.bytes().rev().enumerate() {
        let d = digit(b)?;
        let weight = if i % 2 == 0 { 2 } else { 1 };
        let product = d * weight;
        sum += if product >= 10 { product - 9 } else { product };
    }
    Some((10 - sum % 10) % 10)
}

/// Weighted sum with weights `2..=9` cycling from the right (the módulo 11 of boletos,
/// NF-e keys and arrecadação).
pub fn mod11_sum_2_to_9(digits: &str) -> Option<u32> {
    if !all_digits(digits) {
        return None;
    }
    let mut sum = 0;
    for (i, b) in digits.bytes().rev().enumerate() {
        let weight = 2 + (i % 8) as u32;
        sum += digit(b)? * weight;
    }
    Some(sum)
}

/// Módulo 11 of the general check digit of bank boletos: `11 - (sum % 11)`, with 0, 10 and 11
/// mapped to 1.
pub fn mod11_boleto(digits: &str) -> Option<u32> {
    let sum = mod11_sum_2_to_9(digits)?;
    let dv = 11 - (sum % 11);
    Some(if dv == 0 || dv >= 10 { 1 } else { dv })
}

/// Módulo 11 of arrecadação slips and NF-e keys: `11 - (sum % 11)`, with remainders 0 and 1
/// giving 0 (and remainder 10 giving 1).
pub fn mod11_zero_rule(digits: &str) -> Option<u32> {
    let sum = mod11_sum_2_to_9(digits)?;
    let rem = sum % 11;
    Some(if rem < 2 { 0 } else { 11 - rem })
}

/// Módulo 11 with explicit descending weights (CPF, CNPJ, PIS): `11 - (sum % 11)`, with
/// results of 10 or 11 giving 0.
pub fn mod11_weighted(values: &[u32], weights: &[u32]) -> u32 {
    let sum: u32 = values.iter().zip(weights.iter()).map(|(v, w)| v * w).sum();
    let dv = 11 - (sum % 11);
    if dv >= 10 {
        0
    } else {
        dv
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mod10_matches_febraban_examples() {
        // From the FEBRABAN layout manual: field "001905009" -> DV 5
        assert_eq!(mod10("001905009"), Some(5));
        assert_eq!(mod10("4014481606"), Some(9));
        assert_eq!(mod10("0680935031"), Some(4));
        assert_eq!(mod10(""), None);
        assert_eq!(mod10("12a"), None);
    }

    #[test]
    fn mod11_rules() {
        // remainder 0 -> 1 (boleto) / 0 (zero rule)
        assert_eq!(mod11_boleto("0"), Some(1));
        assert_eq!(mod11_zero_rule("0"), Some(0));
        // 2*1 = 2 -> rem 2 -> dv 9 for both
        assert_eq!(mod11_boleto("1"), Some(9));
        assert_eq!(mod11_zero_rule("1"), Some(9));
        // 9*2 + 8*3 + 7*4 + 6*5 + 5*6 + 4*7 + 3*8 + 2*9 + 1*2 (weights restart after 9)
        assert_eq!(
            mod11_sum_2_to_9("123456789"),
            Some(18 + 24 + 28 + 30 + 30 + 28 + 24 + 18 + 2)
        );
        assert_eq!(mod11_boleto("x"), None);
    }

    #[test]
    fn weighted_mod11() {
        // CPF 111.444.777-35: first DV
        let v = [1, 1, 1, 4, 4, 4, 7, 7, 7];
        assert_eq!(mod11_weighted(&v, &[10, 9, 8, 7, 6, 5, 4, 3, 2]), 3);
        assert!(all_same("1111"));
        assert!(!all_same("1112"));
        assert!(all_same(""));
    }
}
