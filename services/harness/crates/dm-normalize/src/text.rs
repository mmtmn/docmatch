//! Text helpers shared by the normalizers.

use unicode_normalization::char::is_combining_mark;
use unicode_normalization::UnicodeNormalization;

/// Removes diacritics (`Ação` → `Acao`) by NFD decomposition and dropping combining marks.
pub fn strip_accents(input: &str) -> String {
    input.nfd().filter(|c| !is_combining_mark(*c)).collect()
}

/// Uppercases, strips accents and collapses every run of whitespace to one space.
pub fn fold_ascii(input: &str) -> String {
    squash_spaces(&strip_accents(input).to_uppercase())
}

/// Collapses runs of whitespace (including NBSP) to a single space and trims.
pub fn squash_spaces(input: &str) -> String {
    let mut out = String::with_capacity(input.len());
    let mut pending_space = false;
    for c in input.chars() {
        if c.is_whitespace() || c == '\u{a0}' || c == '\u{202f}' {
            pending_space = !out.is_empty();
        } else {
            if pending_space {
                out.push(' ');
                pending_space = false;
            }
            out.push(c);
        }
    }
    out
}

/// Keeps only ASCII digits.
pub fn digits_only(input: &str) -> String {
    input.chars().filter(char::is_ascii_digit).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strips_accents_and_spaces() {
        assert_eq!(strip_accents("Emissão Ação Ç ü"), "Emissao Acao C u");
        assert_eq!(
            fold_ascii("  Empório\u{a0}Bela   Vista "),
            "EMPORIO BELA VISTA"
        );
        assert_eq!(squash_spaces("a\t\tb\n c"), "a b c");
        assert_eq!(digits_only("12.345.678/0001-95"), "12345678000195");
    }
}
