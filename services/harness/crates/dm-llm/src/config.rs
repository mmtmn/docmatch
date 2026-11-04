//! `config/models.yaml`: model routing by role, with `DM_MODEL_<ROLE>_<FIELD>` overrides.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::client::LlmMode;
use crate::error::LlmError;

/// Provider preferences sent to OpenRouter on every request.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProviderPreferences {
    /// `deny`: only providers that keep nothing and never train on the data.
    #[serde(default = "deny")]
    pub data_collection: String,
    /// Only providers that honour every parameter sent.
    #[serde(default = "yes")]
    pub require_parameters: bool,
}

fn deny() -> String {
    "deny".to_string()
}

fn yes() -> bool {
    true
}

impl Default for ProviderPreferences {
    fn default() -> Self {
        ProviderPreferences {
            data_collection: deny(),
            require_parameters: true,
        }
    }
}

/// Defaults applied to every role.
#[derive(Clone, Debug, PartialEq, Deserialize)]
pub struct Defaults {
    /// Sampling temperature.
    #[serde(default)]
    pub temperature: f64,
    /// Request timeout in seconds.
    #[serde(default = "default_timeout")]
    pub timeout_s: u64,
    /// Output token cap.
    #[serde(default = "default_max_tokens")]
    pub max_output_tokens: u32,
    /// Provider preferences.
    #[serde(default)]
    pub provider_preferences: ProviderPreferences,
}

fn default_timeout() -> u64 {
    120
}

fn default_max_tokens() -> u32 {
    4096
}

impl Default for Defaults {
    fn default() -> Self {
        Defaults {
            temperature: 0.0,
            timeout_s: default_timeout(),
            max_output_tokens: default_max_tokens(),
            provider_preferences: ProviderPreferences::default(),
        }
    }
}

/// One role.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Default)]
pub struct RoleConfig {
    /// OpenRouter model id.
    pub primary: String,
    /// Fallback model id (a different family).
    #[serde(default)]
    pub fallback: Option<String>,
    /// Local model name (empty when the role has no local equivalent).
    #[serde(default)]
    pub local: Option<String>,
    /// Output token cap for the role.
    #[serde(default)]
    pub max_output_tokens: Option<u32>,
    /// Timeout for the role.
    #[serde(default)]
    pub timeout_s: Option<u64>,
    /// Whether the role asks for JSON-schema output.
    #[serde(default)]
    pub structured_output: Option<bool>,
    /// Whether the role uses a reasoning model.
    #[serde(default)]
    pub reasoning: Option<bool>,
    /// Batch variant of the model, when any.
    #[serde(default)]
    pub batch_variant: Option<String>,
    /// Only used above this prompt size (long-context role).
    #[serde(default)]
    pub min_prompt_tokens: Option<u64>,
    /// Plans allowed to use the role.
    #[serde(default)]
    pub plans: Option<Vec<String>>,
}

/// The whole file.
#[derive(Clone, Debug, PartialEq, Deserialize)]
pub struct ModelsConfig {
    /// Defaults.
    #[serde(default)]
    pub defaults: Defaults,
    /// Roles by name.
    pub roles: BTreeMap<String, RoleConfig>,
}

/// A role resolved for a mode, with overrides applied.
#[derive(Clone, Debug, PartialEq)]
pub struct ResolvedModel {
    /// Role name.
    pub role: String,
    /// Model to call first.
    pub model: String,
    /// Model to try when the first fails (OpenRouter only).
    pub fallback: Option<String>,
    /// Request timeout.
    pub timeout: Duration,
    /// Output token cap.
    pub max_output_tokens: u32,
    /// Temperature.
    pub temperature: f64,
    /// Ask for JSON-schema output.
    pub structured_output: bool,
    /// Reasoning model.
    pub reasoning: bool,
}

impl ModelsConfig {
    /// Parses YAML text.
    pub fn parse(text: &str) -> Result<Self, LlmError> {
        let config: ModelsConfig =
            serde_saphyr::from_str(text).map_err(|e| LlmError::Config(e.to_string()))?;
        if config.roles.is_empty() {
            return Err(LlmError::Config(
                "nenhum papel definido em roles".to_string(),
            ));
        }
        for (name, role) in &config.roles {
            if role.primary.trim().is_empty() {
                return Err(LlmError::Config(format!("papel {name} sem modelo primary")));
            }
        }
        Ok(config)
    }

    /// A role by name.
    pub fn role(&self, name: &str) -> Option<&RoleConfig> {
        self.roles.get(name)
    }

    /// Resolves a role for a mode using the process environment for overrides.
    pub fn model_for(&self, role: &str, mode: LlmMode) -> Result<ResolvedModel, LlmError> {
        self.model_for_with(role, mode, &|key| std::env::var(key).ok())
    }

    /// Resolves a role with an injectable environment lookup (`DM_MODEL_<ROLE>_<FIELD>` with
    /// FIELD in `PRIMARY`, `FALLBACK`, `LOCAL`, `TIMEOUT_S`, `MAX_OUTPUT_TOKENS`).
    pub fn model_for_with(
        &self,
        role: &str,
        mode: LlmMode,
        lookup: &dyn Fn(&str) -> Option<String>,
    ) -> Result<ResolvedModel, LlmError> {
        let cfg = self
            .role(role)
            .ok_or_else(|| LlmError::UnknownRole(role.to_string()))?;
        let prefix = format!("DM_MODEL_{}_", role.to_ascii_uppercase().replace('-', "_"));
        let env = |field: &str| {
            lookup(&format!("{prefix}{field}"))
                .map(|v| v.trim().to_string())
                .filter(|v| !v.is_empty())
        };
        let primary = env("PRIMARY").unwrap_or_else(|| cfg.primary.clone());
        let fallback = env("FALLBACK")
            .or_else(|| cfg.fallback.clone())
            .filter(|f| !f.trim().is_empty());
        let local = env("LOCAL")
            .or_else(|| cfg.local.clone())
            .filter(|l| !l.trim().is_empty());
        let (model, fallback) = match mode {
            LlmMode::Local => (
                local.ok_or_else(|| {
                    LlmError::Config(format!(
                        "papel {role} não tem modelo local (campo local vazio)"
                    ))
                })?,
                None,
            ),
            LlmMode::OpenRouter | LlmMode::Off => (primary, fallback),
        };
        let timeout_s = env("TIMEOUT_S")
            .and_then(|v| v.parse().ok())
            .or(cfg.timeout_s)
            .unwrap_or(self.defaults.timeout_s);
        let max_output_tokens = env("MAX_OUTPUT_TOKENS")
            .and_then(|v| v.parse().ok())
            .or(cfg.max_output_tokens)
            .unwrap_or(self.defaults.max_output_tokens);
        Ok(ResolvedModel {
            role: role.to_string(),
            model,
            fallback,
            timeout: Duration::from_secs(timeout_s.max(1)),
            max_output_tokens,
            temperature: self.defaults.temperature,
            structured_output: cfg.structured_output.unwrap_or(false),
            reasoning: cfg.reasoning.unwrap_or(false),
        })
    }
}

/// Locates `config/models.yaml`: `DM_MODELS_FILE` when set, else walking up from `start`.
pub fn find_models_file_from(start: &Path, env_value: Option<&str>) -> Option<PathBuf> {
    if let Some(v) = env_value.map(str::trim).filter(|v| !v.is_empty()) {
        let p = PathBuf::from(v);
        return if p.is_absolute() {
            Some(p)
        } else {
            Some(start.join(p))
        };
    }
    let mut dir = Some(start);
    while let Some(d) = dir {
        let candidate = d.join("config").join("models.yaml");
        if candidate.is_file() {
            return Some(candidate);
        }
        dir = d.parent();
    }
    None
}

/// Locates the models file from the current directory and `DM_MODELS_FILE`.
pub fn find_models_file() -> Option<PathBuf> {
    let cwd = std::env::current_dir().ok()?;
    let env_value = std::env::var("DM_MODELS_FILE").ok();
    find_models_file_from(&cwd, env_value.as_deref())
}

/// Loads a models file from a path.
pub fn load_models_config_from(path: &Path) -> Result<ModelsConfig, LlmError> {
    let text = std::fs::read_to_string(path)
        .map_err(|e| LlmError::Config(format!("{}: {e}", path.display())))?;
    ModelsConfig::parse(&text)
}

/// Loads the models file found by [`find_models_file`].
pub fn load_models_config() -> Result<ModelsConfig, LlmError> {
    let path = find_models_file().ok_or_else(|| {
        LlmError::Config("config/models.yaml não encontrado (defina DM_MODELS_FILE)".to_string())
    })?;
    load_models_config_from(&path)
}

#[cfg(test)]
mod tests {
    use super::*;

    const YAML: &str = "defaults:\n  temperature: 0\n  timeout_s: 120\n  max_output_tokens: 4096\n  provider_preferences:\n    data_collection: deny\n    require_parameters: true\nroles:\n  extraction:\n    primary: vendor/primary\n    fallback: other/fallback\n    local: local-model\n    structured_output: true\n  discovery:\n    primary: vendor/big\n    local: \"\"\n    reasoning: true\n    max_output_tokens: 8192\n    timeout_s: 300\n    plans: [business]\n";

    #[test]
    fn parses_and_resolves() {
        let c = ModelsConfig::parse(YAML).unwrap();
        assert_eq!(c.roles.len(), 2);
        assert_eq!(c.defaults.provider_preferences.data_collection, "deny");
        let none = |_: &str| None;
        let r = c
            .model_for_with("extraction", LlmMode::OpenRouter, &none)
            .unwrap();
        assert_eq!(r.model, "vendor/primary");
        assert_eq!(r.fallback.as_deref(), Some("other/fallback"));
        assert_eq!(r.timeout, Duration::from_secs(120));
        assert!(r.structured_output);
        let l = c
            .model_for_with("extraction", LlmMode::Local, &none)
            .unwrap();
        assert_eq!(l.model, "local-model");
        assert_eq!(l.fallback, None);
        let d = c
            .model_for_with("discovery", LlmMode::OpenRouter, &none)
            .unwrap();
        assert_eq!(d.max_output_tokens, 8192);
        assert_eq!(d.timeout, Duration::from_secs(300));
        assert!(d.reasoning);
        assert!(matches!(
            c.model_for_with("discovery", LlmMode::Local, &none),
            Err(LlmError::Config(_))
        ));
        assert!(matches!(
            c.model_for_with("nope", LlmMode::OpenRouter, &none),
            Err(LlmError::UnknownRole(_))
        ));
    }

    #[test]
    fn env_overrides() {
        let c = ModelsConfig::parse(YAML).unwrap();
        let lookup = |key: &str| match key {
            "DM_MODEL_EXTRACTION_PRIMARY" => Some("vendor/override".to_string()),
            "DM_MODEL_EXTRACTION_TIMEOUT_S" => Some("7".to_string()),
            "DM_MODEL_EXTRACTION_FALLBACK" => Some(String::new()),
            _ => None,
        };
        let r = c
            .model_for_with("extraction", LlmMode::OpenRouter, &lookup)
            .unwrap();
        assert_eq!(r.model, "vendor/override");
        assert_eq!(r.timeout, Duration::from_secs(7));
        assert_eq!(
            r.fallback.as_deref(),
            Some("other/fallback"),
            "empty override keeps the file value"
        );
    }

    #[test]
    fn rejects_bad_files() {
        assert!(matches!(
            ModelsConfig::parse("roles: {}"),
            Err(LlmError::Config(_))
        ));
        assert!(matches!(
            ModelsConfig::parse("roles:\n  x:\n    primary: \"\"\n"),
            Err(LlmError::Config(_))
        ));
        assert!(matches!(
            ModelsConfig::parse(": : :"),
            Err(LlmError::Config(_))
        ));
    }

    #[test]
    fn loads_the_repository_file() {
        let crate_dir = Path::new(env!("CARGO_MANIFEST_DIR"));
        let found =
            find_models_file_from(crate_dir, None).expect("config/models.yaml acima do crate");
        let c = load_models_config_from(&found).unwrap();
        for role in [
            "guard",
            "extraction",
            "discovery",
            "adjudicator",
            "verifier",
            "toolsmith",
            "tool_reviewer",
        ] {
            assert!(c.role(role).is_some(), "papel {role}");
        }
        let none = |_: &str| None;
        let adj = c
            .model_for_with("adjudicator", LlmMode::OpenRouter, &none)
            .unwrap();
        let ver = c
            .model_for_with("verifier", LlmMode::OpenRouter, &none)
            .unwrap();
        let family = |m: &str| m.split('/').next().unwrap_or("").to_string();
        assert_ne!(
            family(&adj.model),
            family(&ver.model),
            "adjudicator e verifier são de famílias diferentes"
        );
        let explicit = find_models_file_from(crate_dir, Some("config/models.yaml")).unwrap();
        assert!(explicit.ends_with("config/models.yaml"));
        assert_eq!(find_models_file_from(Path::new("/"), None), None);
    }
}
