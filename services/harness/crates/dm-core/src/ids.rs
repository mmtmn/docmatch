//! Identifier conventions and a deterministic id helper.
//!
//! Ids are plain strings so they travel unchanged through JSON, SQL and gRPC. They are
//! deterministic: the same source bytes and locator always produce the same id, which keeps
//! golden tests and the LLM replay cache stable across runs.

use sha2::{Digest, Sha256};

/// Id of an [`crate::Item`] (`it_` prefix).
pub type ItemId = String;
/// Id of a source document (`src_` prefix).
pub type SourceId = String;
/// Id of a [`crate::Rule`] (`rule_` prefix).
pub type RuleId = String;

/// Builds a deterministic id: `<prefix>_<first 16 hex chars of sha256(parts joined by NUL)>`.
pub fn stable_id(prefix: &str, parts: &[&str]) -> String {
    let mut hasher = Sha256::new();
    for (i, part) in parts.iter().enumerate() {
        if i > 0 {
            hasher.update([0u8]);
        }
        hasher.update(part.as_bytes());
    }
    let digest = hasher.finalize();
    format!("{prefix}_{}", &hex::encode(digest)[..16])
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_are_deterministic_and_prefixed() {
        let a = stable_id("it", &["src_1", "row", "3"]);
        let b = stable_id("it", &["src_1", "row", "3"]);
        let c = stable_id("it", &["src_1", "row", "4"]);
        assert_eq!(a, b);
        assert_ne!(a, c);
        assert!(a.starts_with("it_"));
        assert_eq!(a.len(), 3 + 16);
        // joining with NUL means ["ab", "c"] differs from ["a", "bc"]
        assert_ne!(stable_id("x", &["ab", "c"]), stable_id("x", &["a", "bc"]));
    }
}
