//! The `LlmClient` trait and its three implementations: OpenRouter, OpenAI-compatible local
//! endpoint and off.

use std::sync::{Mutex, Once};
use std::time::{Duration, Instant};

use async_trait::async_trait;
use schemars::JsonSchema;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::cache::{cache_key, now_unix, CacheMode, CachedResponse, ResponseCache};
use crate::config::{load_models_config, ModelsConfig};
use crate::error::LlmError;
use crate::meter::{CallMeter, Meter, MeterTotals, Usage};

/// Public OpenRouter API base, used when `DM_OPENROUTER_BASE_URL` is not set.
pub const DEFAULT_OPENROUTER_BASE_URL: &str = "https://openrouter.ai/api/v1";

/// Where completions come from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LlmMode {
    /// OpenRouter with the role's primary/fallback models.
    OpenRouter,
    /// OpenAI-compatible local server with the role's local model.
    Local,
    /// No model: every call returns [`LlmError::NotAvailable`].
    Off,
}

impl LlmMode {
    /// Parses `openrouter`, `local` or `off` (empty means off).
    pub fn parse(value: &str) -> Option<LlmMode> {
        match value.trim().to_ascii_lowercase().as_str() {
            "openrouter" => Some(LlmMode::OpenRouter),
            "local" => Some(LlmMode::Local),
            "off" | "" => Some(LlmMode::Off),
            _ => None,
        }
    }

    /// From `DM_LLM_MODE`.
    pub fn from_env() -> Result<LlmMode, LlmError> {
        let value = std::env::var("DM_LLM_MODE").unwrap_or_default();
        LlmMode::parse(&value)
            .ok_or_else(|| LlmError::Config(format!("DM_LLM_MODE inválido: {value:?}")))
    }
}

/// A completion request: role (decides the model), system and user messages.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct CompletionRequest {
    /// Role from `config/models.yaml`.
    pub role: String,
    /// System message (already spotlight-aware).
    pub system: String,
    /// User message (document text goes here, spotlighted).
    pub user: String,
    /// Temperature override.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f64>,
    /// Output token override.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_output_tokens: Option<u32>,
}

impl CompletionRequest {
    /// Request with the role defaults.
    pub fn new(
        role: impl Into<String>,
        system: impl Into<String>,
        user: impl Into<String>,
    ) -> Self {
        CompletionRequest {
            role: role.into(),
            system: system.into(),
            user: user.into(),
            temperature: None,
            max_output_tokens: None,
        }
    }
}

/// A completion.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CompletionResponse {
    /// Text content.
    pub content: String,
    /// Model that answered.
    pub model: String,
    /// Provider finish reason.
    pub finish_reason: Option<String>,
    /// Metering.
    pub meter: CallMeter,
}

/// Retries with exponential backoff and jitter.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RetryPolicy {
    /// Attempts per model (1 = no retry).
    pub max_attempts: u32,
    /// Delay before the second attempt; doubles each time.
    pub base_delay: Duration,
    /// Random extra delay added to each wait.
    pub max_jitter: Duration,
}

impl Default for RetryPolicy {
    fn default() -> Self {
        RetryPolicy {
            max_attempts: 3,
            base_delay: Duration::from_millis(500),
            max_jitter: Duration::from_millis(250),
        }
    }
}

impl RetryPolicy {
    fn delay(&self, attempt: u32) -> Duration {
        let factor = 2u32.saturating_pow(attempt.saturating_sub(1));
        let jitter_ms = u64::try_from(self.max_jitter.as_millis()).unwrap_or(0);
        let jitter = if jitter_ms == 0 {
            0
        } else {
            rand::random_range(0..=jitter_ms)
        };
        self.base_delay.saturating_mul(factor) + Duration::from_millis(jitter)
    }
}

/// A model client. Object-safe; the generic [`structured`] helper deserializes schemas.
#[async_trait]
pub trait LlmClient: Send + Sync {
    /// Mode.
    fn mode(&self) -> LlmMode;
    /// Plain text completion.
    async fn complete(&self, req: &CompletionRequest) -> Result<CompletionResponse, LlmError>;
    /// JSON completion constrained by a JSON schema; returns the parsed JSON value.
    async fn structured_json(
        &self,
        req: &CompletionRequest,
        schema_name: &str,
        schema: &Value,
    ) -> Result<(Value, CallMeter), LlmError>;
    /// Totals so far.
    fn meter_totals(&self) -> MeterTotals;
}

/// Structured completion into any `T: JsonSchema + Deserialize`.
pub async fn structured<T: JsonSchema + DeserializeOwned>(
    client: &dyn LlmClient,
    req: &CompletionRequest,
) -> Result<(T, CallMeter), LlmError> {
    let schema = schemars::schema_for!(T);
    let name = T::schema_name().to_string();
    let schema_value =
        serde_json::to_value(schema).map_err(|e| LlmError::BadStructured(e.to_string()))?;
    let (value, meter) = client.structured_json(req, &name, &schema_value).await?;
    let parsed =
        serde_json::from_value::<T>(value).map_err(|e| LlmError::BadStructured(e.to_string()))?;
    Ok((parsed, meter))
}

/// The `off` client.
#[derive(Default)]
pub struct OffClient {
    meter: Mutex<Meter>,
}

#[async_trait]
impl LlmClient for OffClient {
    fn mode(&self) -> LlmMode {
        LlmMode::Off
    }

    async fn complete(&self, req: &CompletionRequest) -> Result<CompletionResponse, LlmError> {
        Err(LlmError::NotAvailable(format!(
            "papel {} não executado",
            req.role
        )))
    }

    async fn structured_json(
        &self,
        req: &CompletionRequest,
        _schema_name: &str,
        _schema: &Value,
    ) -> Result<(Value, CallMeter), LlmError> {
        Err(LlmError::NotAvailable(format!(
            "papel {} não executado",
            req.role
        )))
    }

    fn meter_totals(&self) -> MeterTotals {
        self.meter.lock().map(|m| m.totals()).unwrap_or_default()
    }
}

/// HTTP client for OpenRouter or an OpenAI-compatible local server.
pub struct HttpLlmClient {
    mode: LlmMode,
    base_url: String,
    api_key: Option<String>,
    http: reqwest::Client,
    models: ModelsConfig,
    cache: Option<ResponseCache>,
    retry: RetryPolicy,
    meter: Mutex<Meter>,
    app_referer: Option<String>,
    app_title: Option<String>,
}

struct ChatOutcome {
    content: String,
    model: String,
    finish_reason: Option<String>,
    meter: CallMeter,
}

struct SendError {
    retryable: bool,
    detail: String,
}

impl HttpLlmClient {
    /// OpenRouter client.
    pub fn openrouter(
        base_url: impl Into<String>,
        api_key: impl Into<String>,
        models: ModelsConfig,
        cache: Option<ResponseCache>,
        retry: RetryPolicy,
    ) -> Result<Self, LlmError> {
        Self::build(
            LlmMode::OpenRouter,
            base_url.into(),
            Some(api_key.into()),
            models,
            cache,
            retry,
        )
    }

    /// Local OpenAI-compatible client (no API key).
    pub fn local(
        base_url: impl Into<String>,
        models: ModelsConfig,
        cache: Option<ResponseCache>,
        retry: RetryPolicy,
    ) -> Result<Self, LlmError> {
        Self::build(LlmMode::Local, base_url.into(), None, models, cache, retry)
    }

    /// Installs `ring` as the process-level rustls crypto provider, once. reqwest is built
    /// with `rustls-no-provider` (no aws-lc-sys, no cmake), so the provider must exist before
    /// the first TLS handshake; a provider installed earlier by the host process is kept.
    fn ensure_crypto_provider() {
        static ONCE: Once = Once::new();
        ONCE.call_once(|| {
            let _ = rustls::crypto::ring::default_provider().install_default();
        });
    }

    fn build(
        mode: LlmMode,
        base_url: String,
        api_key: Option<String>,
        models: ModelsConfig,
        cache: Option<ResponseCache>,
        retry: RetryPolicy,
    ) -> Result<Self, LlmError> {
        if base_url.trim().is_empty() {
            return Err(LlmError::Config("URL base do modelo vazia".to_string()));
        }
        Self::ensure_crypto_provider();
        let http = reqwest::Client::builder()
            .build()
            .map_err(|e| LlmError::Config(e.to_string()))?;
        Ok(HttpLlmClient {
            mode,
            base_url,
            api_key,
            http,
            models,
            cache,
            retry: RetryPolicy {
                max_attempts: retry.max_attempts.max(1),
                ..retry
            },
            meter: Mutex::new(Meter::default()),
            app_referer: None,
            app_title: None,
        })
    }

    /// True when a process-level rustls crypto provider is installed (always, after any
    /// client was built).
    pub fn crypto_provider_installed() -> bool {
        rustls::crypto::CryptoProvider::get_default().is_some()
    }

    /// Sets the `HTTP-Referer` and `X-Title` headers OpenRouter uses for attribution.
    pub fn with_app_headers(mut self, referer: Option<String>, title: Option<String>) -> Self {
        self.app_referer = referer.filter(|r| !r.trim().is_empty());
        self.app_title = title.filter(|t| !t.trim().is_empty());
        self
    }

    /// Models configuration in use.
    pub fn models(&self) -> &ModelsConfig {
        &self.models
    }

    fn endpoint(&self) -> String {
        format!("{}/chat/completions", self.base_url.trim_end_matches('/'))
    }

    fn record(&self, meter: &CallMeter) {
        if let Ok(mut m) = self.meter.lock() {
            m.record(meter.clone());
        }
    }

    fn request_body(
        &self,
        model: &str,
        req: &CompletionRequest,
        temperature: f64,
        max_tokens: u32,
        response_format: Option<&Value>,
    ) -> Value {
        let mut body = json!({
            "model": model,
            "messages": [
                {"role": "system", "content": req.system},
                {"role": "user", "content": req.user}
            ],
            "temperature": temperature,
            "max_tokens": max_tokens,
        });
        if let Some(rf) = response_format {
            body["response_format"] = rf.clone();
        }
        if self.mode == LlmMode::OpenRouter {
            let prefs = &self.models.defaults.provider_preferences;
            body["provider"] = json!({"data_collection": prefs.data_collection, "require_parameters": prefs.require_parameters});
            body["usage"] = json!({"include": true});
        }
        body
    }

    async fn send(&self, body: &Value, timeout: Duration) -> Result<Value, SendError> {
        let mut request = self.http.post(self.endpoint()).timeout(timeout).json(body);
        if let Some(key) = &self.api_key {
            request = request.bearer_auth(key);
        }
        if let Some(r) = &self.app_referer {
            request = request.header("HTTP-Referer", r);
        }
        if let Some(t) = &self.app_title {
            request = request.header("X-Title", t);
        }
        let response = request.send().await.map_err(|e| SendError {
            retryable: e.is_timeout() || e.is_connect() || e.is_request(),
            detail: e.to_string(),
        })?;
        let status = response.status();
        let text = response.text().await.map_err(|e| SendError {
            retryable: true,
            detail: e.to_string(),
        })?;
        if status.is_success() {
            serde_json::from_str(&text).map_err(|e| SendError {
                retryable: false,
                detail: format!("JSON inválido na resposta: {e}"),
            })
        } else {
            let retryable =
                status.as_u16() == 429 || status.as_u16() == 408 || status.is_server_error();
            Err(SendError {
                retryable,
                detail: format!(
                    "HTTP {status}: {}",
                    text.chars().take(200).collect::<String>()
                ),
            })
        }
    }

    async fn chat(
        &self,
        req: &CompletionRequest,
        response_format: Option<Value>,
        schema_fingerprint: Option<&str>,
    ) -> Result<ChatOutcome, LlmError> {
        let resolved = self.models.model_for(&req.role, self.mode)?;
        let temperature = req.temperature.unwrap_or(resolved.temperature);
        let max_tokens = req.max_output_tokens.unwrap_or(resolved.max_output_tokens);
        let prompt_fingerprint = serde_json::to_string(&json!({"system": req.system, "user": req.user, "temperature": temperature, "max_tokens": max_tokens})).map_err(|e| LlmError::BadStructured(e.to_string()))?;
        let mut candidates = vec![resolved.model.clone()];
        if let Some(f) = &resolved.fallback {
            candidates.push(f.clone());
        }
        let started = Instant::now();
        let mut attempts_total = 0u32;
        let mut last_error = String::new();
        for model in &candidates {
            let key = cache_key(model, &prompt_fingerprint, schema_fingerprint);
            if let Some(cache) = &self.cache {
                if let Some(hit) = cache.get(&key)? {
                    let meter = CallMeter {
                        role: req.role.clone(),
                        model: hit.model.clone(),
                        usage: hit.usage.clone(),
                        cached: true,
                        attempts: 0,
                        latency_ms: started.elapsed().as_millis() as u64,
                    };
                    self.record(&meter);
                    return Ok(ChatOutcome {
                        content: hit.content,
                        model: hit.model,
                        finish_reason: None,
                        meter,
                    });
                }
                if cache.mode() == CacheMode::Replay {
                    return Err(LlmError::ReplayMiss(key));
                }
            }
            let body = self.request_body(
                model,
                req,
                temperature,
                max_tokens,
                response_format.as_ref(),
            );
            for attempt in 1..=self.retry.max_attempts {
                attempts_total += 1;
                match self.send(&body, resolved.timeout).await {
                    Ok(value) => {
                        let (content, finish_reason, usage, answered_model) =
                            parse_completion(&value)?;
                        let model_name = answered_model.unwrap_or_else(|| model.clone());
                        if let Some(cache) = &self.cache {
                            cache.put(
                                &key,
                                &CachedResponse {
                                    model: model_name.clone(),
                                    content: content.clone(),
                                    usage: usage.clone(),
                                    created_at: now_unix(),
                                },
                            )?;
                        }
                        let meter = CallMeter {
                            role: req.role.clone(),
                            model: model_name.clone(),
                            usage,
                            cached: false,
                            attempts: attempts_total,
                            latency_ms: started.elapsed().as_millis() as u64,
                        };
                        self.record(&meter);
                        return Ok(ChatOutcome {
                            content,
                            model: model_name,
                            finish_reason,
                            meter,
                        });
                    }
                    Err(err) => {
                        tracing::warn!(model = %model, attempt, retryable = err.retryable, "chamada ao modelo falhou: {}", err.detail);
                        last_error = err.detail;
                        if !err.retryable {
                            break;
                        }
                        if attempt < self.retry.max_attempts {
                            tokio::time::sleep(self.retry.delay(attempt)).await;
                        }
                    }
                }
            }
        }
        Err(LlmError::Http {
            attempts: attempts_total,
            detail: last_error,
        })
    }
}

/// Reads an OpenAI-style completion: content (string or text parts), finish reason, usage
/// and the model that answered.
fn parse_completion(
    value: &Value,
) -> Result<(String, Option<String>, Usage, Option<String>), LlmError> {
    if let Some(err) = value.get("error") {
        return Err(LlmError::BadResponse(err.to_string()));
    }
    let choice = value
        .get("choices")
        .and_then(|c| c.get(0))
        .ok_or_else(|| LlmError::BadResponse("sem choices".to_string()))?;
    let message = choice
        .get("message")
        .ok_or_else(|| LlmError::BadResponse("choice sem message".to_string()))?;
    let content = match message.get("content") {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Array(parts)) => parts
            .iter()
            .filter_map(|p| p.get("text").and_then(Value::as_str))
            .collect::<Vec<_>>()
            .join(""),
        _ => return Err(LlmError::BadResponse("message sem content".to_string())),
    };
    let finish_reason = choice
        .get("finish_reason")
        .and_then(Value::as_str)
        .map(String::from);
    let usage = value
        .get("usage")
        .map(Usage::from_openai_json)
        .unwrap_or_default();
    let model = value.get("model").and_then(Value::as_str).map(String::from);
    Ok((content, finish_reason, usage, model))
}

/// Strips ```json fences a model may wrap its JSON in.
fn strip_fences(content: &str) -> &str {
    let trimmed = content.trim();
    let Some(rest) = trimmed.strip_prefix("```") else {
        return trimmed;
    };
    let rest = rest.strip_prefix("json").unwrap_or(rest);
    rest.trim().strip_suffix("```").unwrap_or(rest).trim()
}

#[async_trait]
impl LlmClient for HttpLlmClient {
    fn mode(&self) -> LlmMode {
        self.mode
    }

    async fn complete(&self, req: &CompletionRequest) -> Result<CompletionResponse, LlmError> {
        let out = self.chat(req, None, None).await?;
        Ok(CompletionResponse {
            content: out.content,
            model: out.model,
            finish_reason: out.finish_reason,
            meter: out.meter,
        })
    }

    async fn structured_json(
        &self,
        req: &CompletionRequest,
        schema_name: &str,
        schema: &Value,
    ) -> Result<(Value, CallMeter), LlmError> {
        let response_format = json!({"type": "json_schema", "json_schema": {"name": schema_name, "strict": true, "schema": schema}});
        let fingerprint =
            serde_json::to_string(schema).map_err(|e| LlmError::BadStructured(e.to_string()))?;
        let out = self
            .chat(req, Some(response_format), Some(&fingerprint))
            .await?;
        let value: Value = serde_json::from_str(strip_fences(&out.content)).map_err(|e| {
            LlmError::BadStructured(format!(
                "{e}: {}",
                out.content.chars().take(120).collect::<String>()
            ))
        })?;
        Ok((value, out.meter))
    }

    fn meter_totals(&self) -> MeterTotals {
        self.meter.lock().map(|m| m.totals()).unwrap_or_default()
    }
}

fn required_env(name: &str) -> Result<String, LlmError> {
    std::env::var(name)
        .ok()
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| LlmError::MissingEnv(name.to_string()))
}

/// Builds the client described by the environment (`DM_LLM_MODE`, `OPENROUTER_API_KEY`,
/// `DM_OPENROUTER_BASE_URL`, `DM_LOCAL_LLM_BASE_URL`, `DM_MODELS_FILE`, `DM_LLM_CACHE_DIR`,
/// `DM_LLM_CACHE_MODE`, `DM_PUBLIC_BASE_URL`).
pub fn build_client_from_env() -> Result<Box<dyn LlmClient>, LlmError> {
    let mode = LlmMode::from_env()?;
    let cache = ResponseCache::from_env()?;
    match mode {
        LlmMode::Off => Ok(Box::new(OffClient::default())),
        LlmMode::OpenRouter => {
            let key = required_env("OPENROUTER_API_KEY")?;
            let base = std::env::var("DM_OPENROUTER_BASE_URL")
                .ok()
                .filter(|v| !v.trim().is_empty())
                .unwrap_or_else(|| DEFAULT_OPENROUTER_BASE_URL.to_string());
            let models = load_models_config()?;
            let client =
                HttpLlmClient::openrouter(base, key, models, cache, RetryPolicy::default())?
                    .with_app_headers(
                        std::env::var("DM_PUBLIC_BASE_URL").ok(),
                        Some("docmatch".to_string()),
                    );
            Ok(Box::new(client))
        }
        LlmMode::Local => {
            let base = required_env("DM_LOCAL_LLM_BASE_URL")?;
            let models = load_models_config()?;
            Ok(Box::new(HttpLlmClient::local(
                base,
                models,
                cache,
                RetryPolicy::default(),
            )?))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mode_parsing_and_fences() {
        assert_eq!(LlmMode::parse("OpenRouter"), Some(LlmMode::OpenRouter));
        assert_eq!(LlmMode::parse(""), Some(LlmMode::Off));
        assert_eq!(LlmMode::parse("x"), None);
        assert_eq!(strip_fences("```json\n{\"a\":1}\n```"), "{\"a\":1}");
        assert_eq!(strip_fences(" {\"a\":1} "), "{\"a\":1}");
        assert_eq!(strip_fences("```\n[1]\n```"), "[1]");
    }

    #[test]
    fn completion_parsing() {
        let v = json!({"choices": [{"message": {"content": [{"type": "text", "text": "a"}, {"type": "text", "text": "b"}]}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 1, "completion_tokens": 2}, "model": "m"});
        let (content, finish, usage, model) = parse_completion(&v).unwrap();
        assert_eq!(content, "ab");
        assert_eq!(finish.as_deref(), Some("stop"));
        assert_eq!(usage.total_tokens, 3);
        assert_eq!(model.as_deref(), Some("m"));
        assert!(matches!(
            parse_completion(&json!({"error": {"message": "x"}})),
            Err(LlmError::BadResponse(_))
        ));
        assert!(matches!(
            parse_completion(&json!({"choices": []})),
            Err(LlmError::BadResponse(_))
        ));
        assert!(matches!(
            parse_completion(&json!({"choices": [{"message": {}}]})),
            Err(LlmError::BadResponse(_))
        ));
    }

    #[test]
    fn retry_delays_grow() {
        let p = RetryPolicy {
            max_attempts: 3,
            base_delay: Duration::from_millis(10),
            max_jitter: Duration::ZERO,
        };
        assert_eq!(p.delay(1), Duration::from_millis(10));
        assert_eq!(p.delay(2), Duration::from_millis(20));
        assert_eq!(p.delay(3), Duration::from_millis(40));
        let j = RetryPolicy {
            max_attempts: 3,
            base_delay: Duration::from_millis(10),
            max_jitter: Duration::from_millis(5),
        };
        let d = j.delay(1);
        assert!(d >= Duration::from_millis(10) && d <= Duration::from_millis(15));
    }
}
