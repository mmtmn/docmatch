//! Document numbers: invoices, orders, bills, with leading zeros, prefixes and series.

use serde::{Deserialize, Serialize};

use crate::text::fold_ascii;

/// A normalized document number.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct DocNumber {
    /// Input as given, trimmed.
    pub original: String,
    /// Every digit of the input, in order.
    pub digits: String,
    /// Main number without leading zeros (`088231` → `88231`); `0` when all zeros.
    pub canonical: String,
    /// Prefix such as `NF`, `NFE`, `NFS`, `CTE`, `PC`, `PED`, `DUP`, `BOL`, when present.
    pub prefix: Option<String>,
    /// Series or installment suffix (`1234/1`, `1234-A`, `1234-2/3` keeps `2/3`).
    pub suffix: Option<String>,
}

const PREFIXES: [&str; 20] = [
    "NFSE", "NFS", "NFE", "NF", "NOTA", "CTE", "CT", "PC", "PED", "PEDIDO", "DUP", "DUPL", "BOL",
    "BOLETO", "FAT", "FATURA", "TIT", "TITULO", "OC", "OS",
];

/// Normalizes a document number: strips prefixes (`NF 088231`, `NF-e nº 88231`), splits a
/// series/installment suffix, keeps all digits and removes leading zeros from the main number.
pub fn doc_number(input: &str) -> DocNumber {
    let original = input.trim().to_string();
    let folded = fold_ascii(&original).replace(['º', '°', 'ª'], "");
    let mut rest = folded.as_str();
    let mut prefix = None;
    // "NF-E", "NF E", "NF." etc.
    let normalized_prefix_src = rest
        .replace("NF-E", "NFE")
        .replace("NF E ", "NFE ")
        .replace("CT-E", "CTE");
    rest = normalized_prefix_src.as_str();
    for p in PREFIXES {
        if let Some(after) = rest.strip_prefix(p) {
            let boundary_ok = after.is_empty()
                || !after
                    .chars()
                    .next()
                    .is_some_and(|c| c.is_ascii_alphabetic());
            if boundary_ok {
                prefix = Some(p.to_string());
                rest = after;
                break;
            }
        }
    }
    let rest = rest
        .trim()
        .trim_start_matches(['N', '.', ':', '#', ' ', '-']);
    let digits: String = rest.chars().filter(char::is_ascii_digit).collect();
    // main number = first run of digits; suffix = whatever follows a separator after it
    let mut main = String::new();
    let mut suffix: Option<String> = None;
    let mut seen_digit = false;
    for (i, c) in rest.char_indices() {
        if c.is_ascii_digit() {
            main.push(c);
            seen_digit = true;
        } else if seen_digit {
            let tail = rest[i..]
                .trim_matches(|c: char| c == '/' || c == '-' || c == ' ' || c == '.' || c == ',');
            if !tail.is_empty() {
                suffix = Some(tail.to_string());
            }
            break;
        }
    }
    let canonical = {
        let trimmed = main.trim_start_matches('0');
        if trimmed.is_empty() {
            if main.is_empty() {
                String::new()
            } else {
                "0".to_string()
            }
        } else {
            trimmed.to_string()
        }
    };
    DocNumber {
        original,
        digits,
        canonical,
        prefix,
        suffix,
    }
}

/// True when two document numbers refer to the same main number regardless of leading zeros,
/// prefixes and masks (`NF 088231` == `88231` == `000088231-1`).
pub fn same_doc_number(a: &str, b: &str) -> bool {
    let (a, b) = (doc_number(a), doc_number(b));
    !a.canonical.is_empty() && a.canonical == b.canonical
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn leading_zeros_and_prefixes() {
        let d = doc_number("088231");
        assert_eq!(d.canonical, "88231");
        assert_eq!(d.digits, "088231");
        assert_eq!(d.prefix, None);
        assert_eq!(doc_number("NF 088231").canonical, "88231");
        assert_eq!(doc_number("NF 088231").prefix.as_deref(), Some("NF"));
        assert_eq!(doc_number("NF-e nº 88231").canonical, "88231");
        assert_eq!(doc_number("NF-e nº 88231").prefix.as_deref(), Some("NFE"));
        assert_eq!(doc_number("Nota Fiscal 12").prefix.as_deref(), Some("NOTA"));
        assert_eq!(doc_number("PC 000123").prefix.as_deref(), Some("PC"));
        assert_eq!(doc_number("PC 000123").canonical, "123");
        assert_eq!(doc_number("CT-e 4521").prefix.as_deref(), Some("CTE"));
        assert_eq!(doc_number("0000").canonical, "0");
        assert_eq!(doc_number("").canonical, "");
        assert_eq!(doc_number("abc").canonical, "");
    }

    #[test]
    fn suffixes() {
        let d = doc_number("88231/1");
        assert_eq!(d.canonical, "88231");
        assert_eq!(d.suffix.as_deref(), Some("1"));
        let d = doc_number("NF 088231-2/3");
        assert_eq!(d.canonical, "88231");
        assert_eq!(d.suffix.as_deref(), Some("2/3"));
        assert_eq!(doc_number("1234-A").suffix.as_deref(), Some("A"));
        assert_eq!(doc_number("DUP 1234 B").suffix.as_deref(), Some("B"));
    }

    #[test]
    fn equality() {
        assert!(same_doc_number("088231", "88231"));
        assert!(same_doc_number("NF 088231", "88231/1"));
        assert!(same_doc_number("000088231-1", "NF-e 88231"));
        assert!(!same_doc_number("88231", "88232"));
        assert!(!same_doc_number("", ""));
        assert!(!same_doc_number("NF", "NF"));
    }
}
