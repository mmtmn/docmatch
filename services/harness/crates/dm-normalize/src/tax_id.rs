//! CNPJ/CPF normalization: mask removal, type detection and check-digit status.

use dm_validators::{cnpj_dv, cpf_dv, Verdict};
use serde::{Deserialize, Serialize};

use crate::text::digits_only;

/// Which identifier a value is.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TaxIdKind {
    /// Legal entity.
    Cnpj,
    /// Natural person.
    Cpf,
    /// Neither (wrong length or characters).
    Unknown,
}

/// A normalized tax id.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct TaxId {
    /// Kind.
    pub kind: TaxIdKind,
    /// Normalized characters (digits, or uppercase alphanumeric for the new CNPJ).
    pub digits: String,
    /// Canonical mask.
    pub formatted: Option<String>,
    /// Check-digit verdict.
    pub verdict: Verdict,
    /// True when leading zeros lost by a spreadsheet were restored.
    pub padded: bool,
    /// CNPJ root (first eight characters), for matching branches of one company.
    pub root: Option<String>,
}

/// Normalizes a CNPJ or CPF in any mask, restoring leading zeros that spreadsheets drop
/// (a 13-digit value that validates as a 14-digit CNPJ once padded, same for CPF).
pub fn tax_id(input: &str) -> TaxId {
    let trimmed = input.trim();
    let digits = digits_only(trimmed);
    let alnum: String = trimmed
        .chars()
        .filter(char::is_ascii_alphanumeric)
        .collect::<String>()
        .to_ascii_uppercase();

    // Alphanumeric CNPJ (letters present, 14 characters).
    if alnum.len() == 14 && alnum.chars().any(|c| c.is_ascii_alphabetic()) {
        let r = cnpj_dv(&alnum);
        if r.verdict != Verdict::Malformed {
            return TaxId {
                kind: TaxIdKind::Cnpj,
                root: Some(r.digits[..8].to_string()),
                digits: r.digits,
                formatted: r.formatted,
                verdict: r.verdict,
                padded: false,
            };
        }
    }

    let candidates: [(usize, TaxIdKind); 2] = [(14, TaxIdKind::Cnpj), (11, TaxIdKind::Cpf)];
    for (len, kind) in candidates {
        if digits.len() == len {
            return build(kind, &digits, false);
        }
    }
    // Leading zeros dropped: pad and accept only when the check digits then agree.
    if (9..14).contains(&digits.len()) && !digits.is_empty() {
        for (len, kind) in candidates {
            if digits.len() < len {
                let padded = format!("{digits:0>len$}");
                let candidate = build(kind, &padded, true);
                if candidate.verdict == Verdict::Valid {
                    return candidate;
                }
            }
        }
    }
    TaxId {
        kind: TaxIdKind::Unknown,
        digits,
        formatted: None,
        verdict: Verdict::Malformed,
        padded: false,
        root: None,
    }
}

fn build(kind: TaxIdKind, digits: &str, padded: bool) -> TaxId {
    let r = match kind {
        TaxIdKind::Cnpj => cnpj_dv(digits),
        TaxIdKind::Cpf => cpf_dv(digits),
        TaxIdKind::Unknown => unreachable!("only CNPJ/CPF are built"),
    };
    let root = (kind == TaxIdKind::Cnpj && r.digits.len() == 14).then(|| r.digits[..8].to_string());
    TaxId {
        kind,
        digits: r.digits,
        formatted: r.formatted,
        verdict: r.verdict,
        padded,
        root,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn masked_vs_bare() {
        let a = tax_id("11.222.333/0001-81");
        let b = tax_id("11222333000181");
        assert_eq!(a, b);
        assert_eq!(a.kind, TaxIdKind::Cnpj);
        assert_eq!(a.verdict, Verdict::Valid);
        assert_eq!(a.formatted.as_deref(), Some("11.222.333/0001-81"));
        assert_eq!(a.root.as_deref(), Some("11222333"));
        let c = tax_id("111.444.777-35");
        assert_eq!(c.kind, TaxIdKind::Cpf);
        assert_eq!(c.verdict, Verdict::Valid);
        assert_eq!(c.root, None);
    }

    #[test]
    fn restores_leading_zeros() {
        // a CNPJ starting with 0 loses its zero in a numeric spreadsheet column
        let (d1, d2) = dm_validators::cnpj::compute_dv(&[0, 1, 2, 3, 4, 5, 6, 7, 0, 0, 0, 1]);
        let cnpj = format!("012345670001{d1}{d2}");
        let t = tax_id(&cnpj[1..]);
        assert_eq!(t.kind, TaxIdKind::Cnpj);
        assert_eq!(t.verdict, Verdict::Valid);
        assert!(t.padded);
        assert_eq!(t.digits, cnpj);
        // CPF 012.345.678-90 -> "1234567890"
        let base = dm_validators::cpf::compute_dv(&[0, 1, 2, 3, 4, 5, 6, 7, 8]);
        let cpf = format!("012345678{}{}", base.0, base.1);
        let t = tax_id(&cpf[1..]);
        assert_eq!(t.kind, TaxIdKind::Cpf);
        assert!(t.padded);
        assert_eq!(t.digits, cpf);
    }

    #[test]
    fn invalid_and_unknown() {
        let t = tax_id("11.222.333/0001-82");
        assert_eq!(t.kind, TaxIdKind::Cnpj);
        assert_eq!(t.verdict, Verdict::Invalid);
        let u = tax_id("12345");
        assert_eq!(u.kind, TaxIdKind::Unknown);
        assert_eq!(u.verdict, Verdict::Malformed);
        assert_eq!(tax_id("").kind, TaxIdKind::Unknown);
        let alpha = tax_id("12.ABC.345/01DE-35");
        assert_eq!(alpha.kind, TaxIdKind::Cnpj);
        assert_eq!(alpha.verdict, Verdict::Valid);
        assert_eq!(alpha.root.as_deref(), Some("12ABC345"));
    }
}
