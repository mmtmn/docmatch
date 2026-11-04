//! Biblioteca de ferramentas geradas e seu ciclo de vida (esqueleto; a fase de ferramentas
//! liga geração, testes em sandbox e revisão por modelo de outra família).
#![forbid(unsafe_code)]

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use thiserror::Error;

/// Errors of the tool library.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum ToolError {
    /// A lifecycle transition is not allowed.
    #[error("transição inválida: {from:?} -> {event:?}")]
    InvalidTransition {
        /// Current status.
        from: ToolStatus,
        /// Event attempted.
        event: ToolEvent,
    },
    /// Execution is not wired yet.
    #[error("execução de ferramentas ainda não implementada (fase de ferramentas)")]
    NotImplemented,
}

/// Implementation language of a generated tool.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolLanguage {
    /// Python in a Lambda sandbox.
    Python,
    /// Rust compiled to a sandboxed binary.
    Rust,
    /// SQL run in the DuckDB sandbox.
    Sql,
}

/// Lifecycle status.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum ToolStatus {
    /// Generated, not yet tested.
    #[default]
    Draft,
    /// Tests passed in the sandbox.
    Tested,
    /// Reviewed by a model of another family.
    Reviewed,
    /// Approved for use.
    Approved,
    /// Retired after failures or drift.
    Retired,
    /// Rejected during tests or review.
    Rejected,
}

/// Lifecycle events.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolEvent {
    /// Sandbox tests passed.
    TestsPassed,
    /// Sandbox tests failed.
    TestsFailed,
    /// Reviewer accepted.
    ReviewAccepted,
    /// Reviewer refused.
    ReviewRefused,
    /// A person approved.
    Approve,
    /// Retire.
    Retire,
}

impl ToolStatus {
    /// Applies an event; invalid transitions are errors.
    pub fn advance(self, event: ToolEvent) -> Result<ToolStatus, ToolError> {
        use ToolEvent as E;
        use ToolStatus as S;
        Ok(match (self, event) {
            (S::Draft, E::TestsPassed) => S::Tested,
            (S::Draft | S::Tested, E::TestsFailed) => S::Rejected,
            (S::Tested, E::ReviewAccepted) => S::Reviewed,
            (S::Tested | S::Reviewed, E::ReviewRefused) => S::Rejected,
            (S::Reviewed, E::Approve) => S::Approved,
            (S::Approved | S::Reviewed | S::Tested, E::Retire) => S::Retired,
            (from, event) => return Err(ToolError::InvalidTransition { from, event }),
        })
    }
}

/// A generated tool.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct GeneratedTool {
    /// Id (`tool_` + fingerprint).
    pub id: String,
    /// Human name.
    pub name: String,
    /// What the tool does (pt-BR).
    pub description: String,
    /// Keywords for lookup (formats, relations).
    #[serde(default)]
    pub keywords: Vec<String>,
    /// Language.
    pub language: ToolLanguage,
    /// Version.
    pub version: u32,
    /// SHA-256 of the source.
    pub source_sha256: String,
    /// Status.
    #[serde(default)]
    pub status: ToolStatus,
    /// Model that wrote it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub author_model: Option<String>,
    /// Model that reviewed it (must be of another family).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reviewer_model: Option<String>,
}

/// Fingerprint of a tool source (hex SHA-256).
pub fn fingerprint(source: &str) -> String {
    hex::encode(Sha256::digest(source.as_bytes()))
}

impl GeneratedTool {
    /// New draft from a source.
    pub fn draft(
        name: impl Into<String>,
        description: impl Into<String>,
        language: ToolLanguage,
        source: &str,
    ) -> Self {
        let sha = fingerprint(source);
        GeneratedTool {
            id: format!("tool_{}", &sha[..16]),
            name: name.into(),
            description: description.into(),
            keywords: Vec::new(),
            language,
            version: 1,
            source_sha256: sha,
            status: ToolStatus::Draft,
            author_model: None,
            reviewer_model: None,
        }
    }

    /// True when author and reviewer are of different model families (`vendor/model`).
    pub fn reviewed_by_other_family(&self) -> bool {
        let family = |m: &str| m.split('/').next().unwrap_or("").to_ascii_lowercase();
        match (&self.author_model, &self.reviewer_model) {
            (Some(a), Some(r)) => family(a) != family(r),
            _ => false,
        }
    }
}

/// The library of tools known to a workspace.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ToolLibrary {
    /// Tools.
    pub tools: Vec<GeneratedTool>,
}

impl ToolLibrary {
    /// Adds or replaces (by id) a tool.
    pub fn upsert(&mut self, tool: GeneratedTool) {
        match self.tools.iter_mut().find(|t| t.id == tool.id) {
            Some(existing) => *existing = tool,
            None => self.tools.push(tool),
        }
    }

    /// Approved tools whose name, description or keywords contain every query word
    /// (case-insensitive), most keyword hits first.
    pub fn search(&self, query: &str) -> Vec<&GeneratedTool> {
        let words: Vec<String> = query.split_whitespace().map(str::to_lowercase).collect();
        if words.is_empty() {
            return Vec::new();
        }
        let mut hits: Vec<(usize, &GeneratedTool)> = self
            .tools
            .iter()
            .filter(|t| t.status == ToolStatus::Approved)
            .filter_map(|t| {
                let hay =
                    format!("{} {} {}", t.name, t.description, t.keywords.join(" ")).to_lowercase();
                let all = words.iter().all(|w| hay.contains(w.as_str()));
                all.then(|| {
                    (
                        t.keywords
                            .iter()
                            .filter(|k| words.contains(&k.to_lowercase()))
                            .count(),
                        t,
                    )
                })
            })
            .collect();
        hits.sort_by(|a, b| b.0.cmp(&a.0).then(a.1.name.cmp(&b.1.name)));
        hits.into_iter().map(|(_, t)| t).collect()
    }

    /// Runs a tool on an input.
    ///
    /// TODO(fase de ferramentas): executar na Lambda em sandbox com limites e capturar saída.
    pub fn run(
        &self,
        _tool_id: &str,
        _input: &serde_json::Value,
    ) -> Result<serde_json::Value, ToolError> {
        Err(ToolError::NotImplemented)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lifecycle() {
        let s = ToolStatus::Draft;
        let s = s.advance(ToolEvent::TestsPassed).unwrap();
        assert_eq!(s, ToolStatus::Tested);
        let s = s.advance(ToolEvent::ReviewAccepted).unwrap();
        assert_eq!(s, ToolStatus::Reviewed);
        assert_eq!(s.advance(ToolEvent::Approve).unwrap(), ToolStatus::Approved);
        assert_eq!(
            ToolStatus::Approved.advance(ToolEvent::Retire).unwrap(),
            ToolStatus::Retired
        );
        assert_eq!(
            ToolStatus::Draft.advance(ToolEvent::TestsFailed).unwrap(),
            ToolStatus::Rejected
        );
        assert!(matches!(
            ToolStatus::Draft.advance(ToolEvent::Approve),
            Err(ToolError::InvalidTransition { .. })
        ));
        assert!(matches!(
            ToolStatus::Retired.advance(ToolEvent::TestsPassed),
            Err(ToolError::InvalidTransition { .. })
        ));
    }

    #[test]
    fn library_search_and_families() {
        let mut lib = ToolLibrary::default();
        let mut cnab = GeneratedTool::draft(
            "cnab240_itau",
            "Lê retorno CNAB 240 do Itaú",
            ToolLanguage::Python,
            "def parse(): pass",
        );
        cnab.keywords = vec!["cnab".into(), "240".into(), "itau".into()];
        cnab.status = ToolStatus::Approved;
        cnab.author_model = Some("anthropic/x".into());
        cnab.reviewer_model = Some("openai/y".into());
        assert!(cnab.reviewed_by_other_family());
        let mut draft =
            GeneratedTool::draft("ofx_generic", "Lê OFX", ToolLanguage::Rust, "fn main(){}");
        draft.keywords = vec!["ofx".into()];
        lib.upsert(cnab.clone());
        lib.upsert(draft.clone());
        lib.upsert(cnab.clone());
        assert_eq!(lib.tools.len(), 2);
        assert_eq!(lib.search("cnab 240").len(), 1);
        assert!(lib.search("ofx").is_empty(), "drafts are not searchable");
        assert!(lib.search("").is_empty());
        assert!(lib.search("cnab xlsx").is_empty());
        assert_eq!(
            lib.run("tool_x", &serde_json::json!({})),
            Err(ToolError::NotImplemented)
        );
        assert_eq!(fingerprint("a").len(), 64);
        assert_eq!(
            GeneratedTool::draft("n", "d", ToolLanguage::Sql, "SELECT 1")
                .id
                .len(),
            5 + 16
        );
        let mut same_family = cnab.clone();
        same_family.reviewer_model = Some("anthropic/z".into());
        assert!(!same_family.reviewed_by_other_family());
        assert!(!draft.reviewed_by_other_family());
    }
}
