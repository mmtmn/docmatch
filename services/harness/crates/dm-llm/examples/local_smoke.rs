//! Opt-in real local-model check. Uses fictional input and never an OpenRouter client.
#![allow(clippy::print_stdout)]

use dm_llm::{build_client_from_env, structured, CompletionRequest, LlmMode};
use schemars::JsonSchema;
use serde::Deserialize;

#[derive(Debug, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
struct Invoice {
    code: String,
    total_cents: i64,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    if LlmMode::from_env()? != LlmMode::Local {
        return Err("set DM_LLM_MODE=local; this check never uses a paid provider".into());
    }
    let client = build_client_from_env()?;
    let mut request = CompletionRequest::new(
        "extraction",
        "Extract the exact fields from the fictional invoice. Return only the requested JSON.",
        "Invoice code QA-42. Total: 1250 cents.",
    );
    request.max_output_tokens = Some(256);
    let (invoice, meter) = structured::<Invoice>(client.as_ref(), &request).await?;
    if invoice.code != "QA-42" || invoice.total_cents != 1250 {
        return Err("the model returned incorrect invoice fields".into());
    }
    if meter.cached || meter.usage.total_tokens == 0 {
        return Err("set DM_LLM_CACHE_MODE=off to verify real model inference".into());
    }
    println!(
        "Local structured extraction passed: model={}, prompt_tokens={}, completion_tokens={}, latency_ms={}",
        meter.model, meter.usage.prompt_tokens, meter.usage.completion_tokens, meter.latency_ms
    );
    Ok(())
}
