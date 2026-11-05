//! Who paid: the payer part of a bank descriptor and the customers it can mean.
//!
//! A customer is a candidate when it shares at least one distinctive token with the payer
//! text (stop words, legal forms and generic business words never count; a truncated last
//! token only reinforces). A payer that equals a customer's name (legal form aside) settles
//! the question; otherwise several candidates make the descriptor ambiguous and only the
//! amount can decide.

use std::collections::BTreeMap;

use dm_normalize::{expand_abbreviation, name_key, NameKey};

use crate::config::EngineConfig;
use crate::sources::Dataset;

const STOP_WORDS: [&str; 13] = [
    "DE", "DA", "DO", "DAS", "DOS", "E", "A", "O", "AS", "OS", "EM", "NA", "NO",
];

/// One customer a descriptor may mean.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Candidate {
    /// Entity index.
    pub entity: usize,
    /// Distinctive tokens shared exactly.
    pub shared: usize,
    /// True when the payer text is the customer's name (legal form aside).
    pub full: bool,
}

/// The payer part of a folded descriptor: transaction prefixes removed (longest first,
/// repeatedly). `None` when nothing name-like is left or the bank says it is unidentified.
pub fn payer_text(folded: &str, cfg: &EngineConfig) -> Option<String> {
    let mut prefixes: Vec<&str> = cfg.descriptor_prefixes.iter().map(String::as_str).collect();
    prefixes.sort_by_key(|p| std::cmp::Reverse(p.len()));
    let mut s = folded.trim().to_string();
    loop {
        let before = s.len();
        for p in &prefixes {
            if s == *p {
                s.clear();
                break;
            }
            if let Some(rest) = s.strip_prefix(p).and_then(|r| r.strip_prefix(' ')) {
                s = rest.trim().to_string();
                break;
            }
        }
        if s.len() == before {
            break;
        }
    }
    if s.is_empty() || !s.chars().any(|c| c.is_ascii_alphabetic()) {
        return None;
    }
    if cfg
        .unidentified_markers
        .iter()
        .any(|m| s.contains(m.as_str()))
    {
        return None;
    }
    Some(s)
}

fn distinctive(key: &NameKey, cfg: &EngineConfig) -> Vec<String> {
    let mut out: Vec<String> = key
        .ordered_tokens
        .iter()
        .map(|t| expand_abbreviation(t).to_string())
        .filter(|t| {
            t.len() >= 2
                && t.chars().any(|c| c.is_ascii_alphabetic())
                && !STOP_WORDS.contains(&t.as_str())
                && !cfg.generic_name_tokens.iter().any(|g| g == t)
        })
        .collect();
    out.dedup();
    out
}

/// Customers the payer text may mean, best first.
pub fn candidates(payer: &str, ds: &Dataset, cfg: &EngineConfig) -> Vec<Candidate> {
    let pk = name_key(payer);
    let ptoks = distinctive(&pk, cfg);
    if ptoks.is_empty() {
        return Vec::new();
    }
    let mut best: BTreeMap<usize, Candidate> = BTreeMap::new();
    for title in &ds.titles {
        let ck = name_key(&title.customer);
        let ctoks = distinctive(&ck, cfg);
        let shared = ptoks.iter().filter(|t| ctoks.contains(t)).count();
        if shared == 0 {
            continue;
        }
        let full = !pk.core.is_empty() && pk.core == ck.core;
        let cand = Candidate {
            entity: title.entity,
            shared,
            full,
        };
        best.entry(title.entity)
            .and_modify(|c| {
                if (cand.full, cand.shared) > (c.full, c.shared) {
                    *c = cand.clone();
                }
            })
            .or_insert(cand);
    }
    let mut out: Vec<Candidate> = best.into_values().collect();
    if out.iter().any(|c| c.full) {
        out.retain(|c| c.full);
    }
    out.sort_by(|a, b| (b.full, b.shared, a.entity).cmp(&(a.full, a.shared, b.entity)));
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prefixes_and_markers() {
        let cfg = EngineConfig::default();
        assert_eq!(
            payer_text("PIX RECEBIDO REDE TRIGO", &cfg).as_deref(),
            Some("REDE TRIGO")
        );
        assert_eq!(
            payer_text("LIQUIDACAO COBRANCA SIMPLES", &cfg),
            None,
            "only prefixes"
        );
        assert_eq!(payer_text("TED RECEBIDA CNPJ 24680135000178", &cfg), None);
        assert_eq!(payer_text("DEPOSITO NAO IDENTIFICADO", &cfg), None);
        assert_eq!(
            payer_text("TED RECEBIDA CONFEITARIA LUA CHEI", &cfg).as_deref(),
            Some("CONFEITARIA LUA CHEI")
        );
    }

    #[test]
    fn generic_words_and_truncation_do_not_create_candidates() {
        let cfg = EngineConfig::default();
        let a = distinctive(
            &name_key("Confeitaria Lua Cheia Comercio de Alimentos"),
            &cfg,
        );
        assert_eq!(a, vec!["CONFEITARIA", "LUA", "CHEIA"]);
        let b = distinctive(&name_key("SABOR DA ROCA FRANQU"), &cfg);
        assert!(!b.contains(&"DA".to_string()));
    }
}
