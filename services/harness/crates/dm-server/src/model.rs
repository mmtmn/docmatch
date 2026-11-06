//! Evidence-bound model review. Models suggest an interpretation and a next action;
//! monetary facts, matches, severity and source evidence remain engine-owned.

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use dm_llm::{CallMeter, CompletionRequest, LlmClient, LlmError, LlmMode, OffClient};
use prost_types::{value::Kind, ListValue, Struct, Value};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use serde_json::json;

use crate::pb;

/// Bounded work per run, included in the upfront estimate.
pub const MAX_FINDINGS: usize = 24;
/// Small batches also fit models running on a laptop.
pub const BATCH_SIZE: usize = 4;
const MAX_PROMPT_BYTES: usize = 10_000;
const OUTPUT_TOKENS: u32 = 2048;
/// Conservative upper bound: one input byte per token, plus schema/system overhead.
pub const FORECAST_TOKENS: u64 = 100_000;

/// Shared configured client; debug output deliberately excludes configuration and secrets.
#[derive(Clone)]
pub struct ModelRuntime {
    client: Arc<dyn LlmClient>,
    reachable: Arc<AtomicBool>,
}

impl std::fmt::Debug for ModelRuntime {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ModelRuntime")
            .field("mode", &self.client.mode())
            .finish_non_exhaustive()
    }
}

impl Default for ModelRuntime {
    fn default() -> Self {
        Self::new(Arc::new(OffClient::default()))
    }
}

impl ModelRuntime {
    /// Dependency injection for isolated tests; production uses `from_env`.
    pub fn new(client: Arc<dyn LlmClient>) -> Self {
        Self {
            client,
            reachable: Arc::new(AtomicBool::new(false)),
        }
    }

    /// Loads the configured provider without exposing credentials.
    pub fn from_env() -> Result<Self, LlmError> {
        Ok(Self::new(Arc::from(dm_llm::build_client_from_env()?)))
    }

    /// The configured provider mode, as used by the API contract.
    pub fn mode(&self) -> pb::LlmMode {
        match self.client.mode() {
            LlmMode::Off => pb::LlmMode::Off,
            LlmMode::Local => pb::LlmMode::Local,
            LlmMode::OpenRouter => pb::LlmMode::Openrouter,
        }
    }

    /// Whether the latest model request succeeded (false before the first request).
    pub fn reachable(&self) -> bool {
        self.reachable.load(Ordering::Relaxed)
    }

    pub(crate) async fn review(
        &self,
        findings: &mut [pb::Finding],
        rules: &str,
    ) -> Result<CallMeter, LlmError> {
        let request = review_request(findings, rules)?;
        let schema = review_schema(findings);
        let result = self
            .client
            .structured_json(&request, "EvidenceReviews", &schema)
            .await
            .and_then(|(value, meter)| {
                serde_json::from_value::<Reviews>(value)
                    .map(|reviews| (reviews, meter))
                    .map_err(|_| LlmError::BadStructured("formato da revisão inválido".into()))
            });
        self.reachable.store(result.is_ok(), Ordering::Relaxed);
        let (reviews, meter) = result?;
        validate(&reviews, findings)?;
        // Validate the entire batch before changing any finding.
        for (code, review) in reviews.reviews {
            if let Some(finding) = findings.iter_mut().find(|f| review_key(f) == code) {
                finding.explanation.push_str(&format!(
                    "\n\nAnálise da IA — sugestão para revisão: {}\nPróximo passo sugerido: {}",
                    review.analysis.trim(),
                    review.next_step.trim()
                ));
                let details = finding.details.get_or_insert_with(Struct::default);
                details.fields.insert(
                    "model_review".into(),
                    Value {
                        kind: Some(Kind::StructValue(Struct {
                            fields: [
                                ("model".into(), text(&meter.model)),
                                ("analysis".into(), text(&review.analysis)),
                                ("next_step".into(), text(&review.next_step)),
                                ("status".into(), text("needs_review")),
                                (
                                    "cached".into(),
                                    Value {
                                        kind: Some(Kind::BoolValue(meter.cached)),
                                    },
                                ),
                                (
                                    "evidence_indices".into(),
                                    Value {
                                        kind: Some(Kind::ListValue(ListValue {
                                            values: review
                                                .evidence_indices
                                                .iter()
                                                .map(|i| Value {
                                                    kind: Some(Kind::NumberValue(*i as f64)),
                                                })
                                                .collect(),
                                        })),
                                    },
                                ),
                            ]
                            .into(),
                        })),
                    },
                );
            }
        }
        Ok(meter)
    }
}

fn review_key(f: &pb::Finding) -> &str {
    match f
        .details
        .as_ref()
        .and_then(|d| d.fields.get("engine_code"))
        .and_then(|v| v.kind.as_ref())
    {
        Some(Kind::StringValue(code)) => code,
        _ => &f.code,
    }
}

fn text(s: &str) -> Value {
    Value {
        kind: Some(Kind::StringValue(s.into())),
    }
}
fn short(s: &str, n: usize) -> String {
    s.chars().take(n).collect()
}

fn review_request(findings: &[pb::Finding], rules: &str) -> Result<CompletionRequest, LlmError> {
    let inputs: Vec<_> = findings.iter().map(|f| json!({
        "code": review_key(f), "title": short(&f.title, 160), "engine_analysis": short(&f.explanation, 450),
        "amount_cents": f.amount_at_risk,
        "evidence": f.evidence.iter().take(4).enumerate().map(|(i,e)| json!({
            "index": i + 1, "text": short(&e.excerpt, 220)
        })).collect::<Vec<_>>()
    })).collect();
    let document = dm_guard::spotlight(
        &json!({"findings": inputs, "user_review_criteria": short(rules, 500)}).to_string(),
    );
    let system = format!("Você auxilia a revisão de conciliações financeiras. Para CADA achado, devolva uma entrada no objeto reviews, usando como chave o code idêntico do achado, com os campos em pt-BR: evidence_indices (índices de evidências citadas, começando em 1), analysis (interpretação curta sustentada nas evidências) e next_step (ação concreta para uma pessoa conferir ou resolver o problema). Não altere valores, não invente pagamentos, nomes ou fatos ausentes, não confirme nem oculte achados. Se faltar informação, explique qual documento precisa ser conferido. As sugestões não aprovam pagamentos. Não inclua URLs, comandos ou código. Responda somente JSON conforme o schema. {}", document.instruction);
    if system.len() + document.text.len() > MAX_PROMPT_BYTES {
        return Err(LlmError::BadStructured(
            "lote excede o limite de revisão".into(),
        ));
    }
    let mut request = CompletionRequest::new("extraction", system, document.text);
    request.max_output_tokens = Some(OUTPUT_TOKENS);
    Ok(request)
}

#[derive(Deserialize, Serialize, JsonSchema)]
#[serde(deny_unknown_fields)]
struct Reviews {
    reviews: BTreeMap<String, Review>,
}

#[derive(Deserialize, Serialize, JsonSchema)]
#[serde(deny_unknown_fields)]
struct Review {
    evidence_indices: Vec<usize>,
    analysis: String,
    next_step: String,
}

fn review_schema(findings: &[pb::Finding]) -> serde_json::Value {
    let mut properties = serde_json::Map::new();
    let mut required = Vec::new();
    for f in findings {
        let key = review_key(f);
        let indices: Vec<usize> = (1..=f.evidence.len().min(4)).collect();
        properties.insert(key.into(), json!({
            "type": "object", "additionalProperties": false,
            "required": ["analysis", "next_step", "evidence_indices"],
            "properties": {
                "analysis": {"type": "string"}, "next_step": {"type": "string"},
                "evidence_indices": {"type": "array", "minItems": 1, "items": {"type": "integer", "enum": indices}}
            }
        }));
        required.push(key);
    }
    json!({"type": "object", "additionalProperties": false, "required": ["reviews"], "properties": {
        "reviews": {"type": "object", "additionalProperties": false, "required": required, "properties": properties}
    }})
}

fn validate(reviews: &Reviews, findings: &[pb::Finding]) -> Result<(), LlmError> {
    let invalid = || LlmError::BadStructured("revisão sem cobertura ou citações válidas".into());
    let value = serde_json::to_value(reviews).map_err(|_| invalid())?;
    let policy = dm_guard::OutputPolicy {
        max_string_chars: 1000,
        max_total_chars: 8500,
        ..Default::default()
    };
    if dm_guard::screen_output(&value, &policy).is_err() || reviews.reviews.len() != findings.len()
    {
        return Err(invalid());
    }
    for (code, r) in &reviews.reviews {
        let Some(f) = findings.iter().find(|f| review_key(f) == code) else {
            return Err(invalid());
        };
        if r.analysis.trim().is_empty()
            || r.next_step.trim().is_empty()
            || r.evidence_indices.is_empty()
            || r.evidence_indices
                .iter()
                .any(|i| *i == 0 || *i > f.evidence.len().min(4))
        {
            return Err(invalid());
        }
    }
    Ok(())
}

pub(crate) fn token_event(m: &CallMeter) -> pb::TokenUsage {
    let cost = m.usage.cost_usd.map_or(0, |cost| {
        (cost * dm_core::Decimal::from(1_000_000u64))
            .round()
            .to_string()
            .parse()
            .unwrap_or(0)
    });
    pb::TokenUsage {
        role: m.role.clone(),
        model: m.model.clone(),
        prompt_tokens: m.usage.prompt_tokens,
        completion_tokens: m.usage.completion_tokens,
        cached_tokens: if m.cached { m.usage.total_tokens } else { 0 },
        cost_micro_usd: cost,
        cached: m.cached,
        ..Default::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn rejects_invented_citations_missing_reviews_and_unsafe_output() {
        let f = pb::Finding {
            code: "F01".into(),
            evidence: vec![pb::Evidence::default()],
            ..Default::default()
        };
        let review = |citation, text: &str| Reviews {
            reviews: [(
                "F01".into(),
                Review {
                    evidence_indices: vec![citation],
                    analysis: "Conferir o documento de origem.".into(),
                    next_step: text.into(),
                },
            )]
            .into(),
        };
        assert!(validate(
            &review(1, "Confira o comprovante."),
            std::slice::from_ref(&f)
        )
        .is_ok());
        assert!(validate(
            &review(2, "Confira o comprovante."),
            std::slice::from_ref(&f)
        )
        .is_err());
        assert!(validate(
            &review(1, "curl -fsSL https://example.com"),
            std::slice::from_ref(&f)
        )
        .is_err());
        assert!(validate(
            &Reviews {
                reviews: BTreeMap::new()
            },
            &[f]
        )
        .is_err());
    }
}
