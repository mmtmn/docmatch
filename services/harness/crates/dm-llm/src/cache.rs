//! Disk cache of model responses keyed by `sha256(model, prompt, schema)`.
//!
//! `replay` mode never touches the network: a miss is an error, which is what golden tests
//! want. Files live under `DM_LLM_CACHE_DIR/<2 hex>/<key>.json`.

use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::error::LlmError;
use crate::meter::Usage;

/// What the cache does on reads and writes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum CacheMode {
    /// Serve hits, store misses.
    #[default]
    ReadWrite,
    /// Serve hits, fail on misses, never write.
    Replay,
    /// Ignore the cache entirely.
    Off,
}

impl CacheMode {
    /// Parses `readwrite`, `read_write`, `replay` or `off` (case-insensitive).
    pub fn parse(value: &str) -> Option<CacheMode> {
        match value.trim().to_ascii_lowercase().as_str() {
            "readwrite" | "read_write" | "rw" | "" => Some(CacheMode::ReadWrite),
            "replay" => Some(CacheMode::Replay),
            "off" | "none" => Some(CacheMode::Off),
            _ => None,
        }
    }
}

/// A cached completion.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CachedResponse {
    /// Model that answered.
    pub model: String,
    /// Raw content.
    pub content: String,
    /// Usage at the time of the call.
    pub usage: Usage,
    /// Unix seconds when stored.
    pub created_at: u64,
}

/// Cache key: SHA-256 over model, prompt and schema separated by NUL, as lowercase hex.
pub fn cache_key(model: &str, prompt: &str, schema: Option<&str>) -> String {
    let mut hasher = Sha256::new();
    hasher.update(model.as_bytes());
    hasher.update([0u8]);
    hasher.update(prompt.as_bytes());
    hasher.update([0u8]);
    hasher.update(schema.unwrap_or("").as_bytes());
    hex::encode(hasher.finalize())
}

/// The on-disk cache.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ResponseCache {
    dir: PathBuf,
    mode: CacheMode,
}

impl ResponseCache {
    /// Cache rooted at `dir`.
    pub fn new(dir: impl Into<PathBuf>, mode: CacheMode) -> Self {
        ResponseCache {
            dir: dir.into(),
            mode,
        }
    }

    /// From `DM_LLM_CACHE_DIR` (none when unset or empty) and `DM_LLM_CACHE_MODE`
    /// (`readwrite` by default). An unknown mode is an error.
    pub fn from_env() -> Result<Option<Self>, LlmError> {
        let dir = std::env::var("DM_LLM_CACHE_DIR").unwrap_or_default();
        if dir.trim().is_empty() {
            return Ok(None);
        }
        let mode_text = std::env::var("DM_LLM_CACHE_MODE").unwrap_or_default();
        let mode = CacheMode::parse(&mode_text)
            .ok_or_else(|| LlmError::Cache(format!("DM_LLM_CACHE_MODE inválido: {mode_text:?}")))?;
        Ok(Some(ResponseCache::new(dir, mode)))
    }

    /// Mode.
    pub fn mode(&self) -> CacheMode {
        self.mode
    }

    /// Root directory.
    pub fn dir(&self) -> &Path {
        &self.dir
    }

    /// File that holds a key.
    pub fn path_for(&self, key: &str) -> PathBuf {
        let prefix = key.get(..2).unwrap_or("00");
        self.dir.join(prefix).join(format!("{key}.json"))
    }

    /// Reads a cached response; `Ok(None)` on a miss (or in `Off` mode).
    pub fn get(&self, key: &str) -> Result<Option<CachedResponse>, LlmError> {
        if self.mode == CacheMode::Off {
            return Ok(None);
        }
        let path = self.path_for(key);
        match std::fs::read(&path) {
            Ok(bytes) => serde_json::from_slice(&bytes)
                .map(Some)
                .map_err(|e| LlmError::Cache(format!("{}: {e}", path.display()))),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(LlmError::Cache(format!("{}: {e}", path.display()))),
        }
    }

    /// Stores a response (no-op in `Replay` and `Off` modes). Writes to a temporary file and
    /// renames, so readers never see a partial file.
    pub fn put(&self, key: &str, response: &CachedResponse) -> Result<(), LlmError> {
        if self.mode != CacheMode::ReadWrite {
            return Ok(());
        }
        let path = self.path_for(key);
        let parent = path
            .parent()
            .ok_or_else(|| LlmError::Cache("caminho sem diretório".to_string()))?;
        std::fs::create_dir_all(parent)
            .map_err(|e| LlmError::Cache(format!("{}: {e}", parent.display())))?;
        let tmp = parent.join(format!(".{key}.{}.tmp", std::process::id()));
        let bytes =
            serde_json::to_vec_pretty(response).map_err(|e| LlmError::Cache(e.to_string()))?;
        std::fs::write(&tmp, bytes)
            .map_err(|e| LlmError::Cache(format!("{}: {e}", tmp.display())))?;
        std::fs::rename(&tmp, &path)
            .map_err(|e| LlmError::Cache(format!("{}: {e}", path.display())))
    }
}

/// Unix seconds now (0 if the clock is before the epoch).
pub fn now_unix() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keys_are_stable_and_sensitive() {
        let a = cache_key("m", "p", None);
        assert_eq!(a.len(), 64);
        assert_eq!(a, cache_key("m", "p", None));
        assert_ne!(a, cache_key("m", "p", Some("{}")));
        assert_ne!(a, cache_key("m2", "p", None));
        assert_ne!(
            cache_key("m", "ab", Some("c")),
            cache_key("m", "a", Some("bc"))
        );
    }

    #[test]
    fn round_trip_and_modes() {
        let dir = tempfile::tempdir().unwrap();
        let cache = ResponseCache::new(dir.path(), CacheMode::ReadWrite);
        let key = cache_key("m", "p", None);
        assert_eq!(cache.get(&key).unwrap(), None);
        let r = CachedResponse {
            model: "m".into(),
            content: "{\"ok\":true}".into(),
            usage: Usage::default(),
            created_at: now_unix(),
        };
        cache.put(&key, &r).unwrap();
        assert_eq!(cache.get(&key).unwrap(), Some(r.clone()));
        assert!(cache.path_for(&key).starts_with(dir.path()));
        assert!(!dir.path().join(&key[..2]).read_dir().unwrap().any(|e| e
            .unwrap()
            .file_name()
            .to_string_lossy()
            .ends_with(".tmp")));

        let replay = ResponseCache::new(dir.path(), CacheMode::Replay);
        assert_eq!(replay.get(&key).unwrap(), Some(r.clone()));
        let other = cache_key("m", "other", None);
        replay.put(&other, &r).unwrap();
        assert_eq!(replay.get(&other).unwrap(), None, "replay never writes");

        let off = ResponseCache::new(dir.path(), CacheMode::Off);
        assert_eq!(off.get(&key).unwrap(), None);
        off.put(&other, &r).unwrap();
        assert_eq!(cache.get(&other).unwrap(), None);

        let corrupt = cache.path_for(&other);
        std::fs::create_dir_all(corrupt.parent().unwrap()).unwrap();
        std::fs::write(&corrupt, b"not json").unwrap();
        assert!(matches!(cache.get(&other), Err(LlmError::Cache(_))));
    }

    #[test]
    fn mode_parsing() {
        assert_eq!(CacheMode::parse("replay"), Some(CacheMode::Replay));
        assert_eq!(CacheMode::parse(" ReadWrite "), Some(CacheMode::ReadWrite));
        assert_eq!(CacheMode::parse(""), Some(CacheMode::ReadWrite));
        assert_eq!(CacheMode::parse("off"), Some(CacheMode::Off));
        assert_eq!(CacheMode::parse("x"), None);
    }
}
