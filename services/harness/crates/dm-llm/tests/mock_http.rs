//! HTTP behaviour against a mock server: headers, provider preferences, retries, fallback,
//! timeouts, structured output, the disk cache and replay mode. No real network, ever.

use std::time::Duration;

use dm_llm::{
    structured, CacheMode, CompletionRequest, HttpLlmClient, LlmClient, LlmError, LlmMode,
    ModelsConfig, OffClient, ResponseCache, RetryPolicy,
};
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::json;
use wiremock::matchers::{body_partial_json, header, method, path};
use wiremock::{Mock, MockServer, ResponseTemplate};

const YAML: &str = "defaults:\n  temperature: 0\n  timeout_s: 1\n  max_output_tokens: 256\n  provider_preferences:\n    data_collection: deny\n    require_parameters: true\nroles:\n  extraction:\n    primary: test/primary\n    fallback: test/fallback\n    local: local-model\n    structured_output: true\n  guard:\n    primary: test/guard\n    local: \"\"\n";

fn models() -> ModelsConfig {
    ModelsConfig::parse(YAML).unwrap()
}

#[tokio::test]
async fn remote_models_can_omit_unsupported_temperature() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(200).set_body_json(completion("ok", "test/primary")))
        .expect(1)
        .mount(&server)
        .await;
    let mut config = models();
    config
        .roles
        .get_mut("extraction")
        .unwrap()
        .temperature_supported = Some(false);
    let client =
        HttpLlmClient::openrouter(server.uri(), "test-key", config, None, fast_retry()).unwrap();
    client.complete(&request()).await.unwrap();
    let requests = server.received_requests().await.unwrap();
    let body: serde_json::Value = requests[0].body_json().unwrap();
    assert!(body.get("temperature").is_none());
    assert_eq!(body["provider"]["require_parameters"], true);
}

fn fast_retry() -> RetryPolicy {
    RetryPolicy {
        max_attempts: 3,
        base_delay: Duration::from_millis(1),
        max_jitter: Duration::from_millis(1),
    }
}

fn completion(content: &str, model: &str) -> serde_json::Value {
    json!({
        "id": "gen-1",
        "model": model,
        "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
        "usage": {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15, "cost": 0.0001}
    })
}

fn request() -> CompletionRequest {
    CompletionRequest::new("extraction", "Você extrai campos.", "NF 88231 de R$ 10,00")
}

#[tokio::test]
async fn openrouter_sends_auth_provider_prefs_and_model() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/chat/completions"))
        .and(header("authorization", "Bearer test-key"))
        .and(header("x-title", "docmatch"))
        .and(body_partial_json(json!({"model": "test/primary", "provider": {"data_collection": "deny", "require_parameters": true}, "usage": {"include": true}, "temperature": 0.0, "max_tokens": 256})))
        .respond_with(ResponseTemplate::new(200).set_body_json(completion("ok", "test/primary")))
        .expect(1)
        .mount(&server)
        .await;
    let client = HttpLlmClient::openrouter(server.uri(), "test-key", models(), None, fast_retry())
        .unwrap()
        .with_app_headers(None, Some("docmatch".to_string()));
    let r = client.complete(&request()).await.unwrap();
    assert_eq!(r.content, "ok");
    assert_eq!(r.model, "test/primary");
    assert_eq!(r.meter.usage.total_tokens, 15);
    assert_eq!(r.meter.attempts, 1);
    assert!(!r.meter.cached);
    assert_eq!(client.meter_totals().total_tokens, 15);
    assert_eq!(client.mode(), LlmMode::OpenRouter);
}

#[tokio::test]
async fn retries_on_5xx_then_succeeds() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(500))
        .up_to_n_times(2)
        .expect(2)
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(completion("after retries", "test/primary")),
        )
        .expect(1)
        .mount(&server)
        .await;
    let client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), None, fast_retry()).unwrap();
    let r = client.complete(&request()).await.unwrap();
    assert_eq!(r.content, "after retries");
    assert_eq!(r.meter.attempts, 3);
}

#[tokio::test]
async fn falls_back_to_second_model_on_4xx() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(body_partial_json(json!({"model": "test/primary"})))
        .respond_with(ResponseTemplate::new(400).set_body_string("modelo indisponível"))
        .expect(1)
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(body_partial_json(json!({"model": "test/fallback"})))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(completion("from fallback", "test/fallback")),
        )
        .expect(1)
        .mount(&server)
        .await;
    let client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), None, fast_retry()).unwrap();
    let r = client.complete(&request()).await.unwrap();
    assert_eq!(r.model, "test/fallback");
    assert_eq!(
        r.meter.attempts, 2,
        "one failed attempt on the primary, one on the fallback"
    );
}

#[tokio::test]
async fn gives_up_after_retries_and_fallback() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(503))
        .expect(6)
        .mount(&server)
        .await;
    let client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), None, fast_retry()).unwrap();
    let err = client.complete(&request()).await.unwrap_err();
    assert!(matches!(err, LlmError::Http { attempts: 6, .. }), "{err:?}");
}

#[tokio::test]
async fn times_out_slow_responses() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_json(completion("late", "test/guard"))
                .set_delay(Duration::from_millis(2500)),
        )
        .mount(&server)
        .await;
    let client = HttpLlmClient::openrouter(
        server.uri(),
        "k",
        models(),
        None,
        RetryPolicy {
            max_attempts: 1,
            ..fast_retry()
        },
    )
    .unwrap();
    let started = std::time::Instant::now();
    let err = client
        .complete(&CompletionRequest::new("guard", "s", "u"))
        .await
        .unwrap_err();
    assert!(matches!(err, LlmError::Http { attempts: 1, .. }), "{err:?}");
    assert!(
        started.elapsed() < Duration::from_secs(2),
        "timeout of 1 s must fire before the 2,5 s delay"
    );
}

#[derive(Debug, Deserialize, JsonSchema, PartialEq)]
struct Extracted {
    invoice: String,
    amount_centavos: i64,
}

#[tokio::test]
async fn structured_output_parses_json_and_sends_schema() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(body_partial_json(json!({"response_format": {"type": "json_schema", "json_schema": {"name": "Extracted", "strict": true}}})))
        .respond_with(ResponseTemplate::new(200).set_body_json(completion("```json\n{\"invoice\": \"88231\", \"amount_centavos\": 1000}\n```", "test/primary")))
        .expect(1)
        .mount(&server)
        .await;
    let client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), None, fast_retry()).unwrap();
    let (parsed, meter) = structured::<Extracted>(&client, &request()).await.unwrap();
    assert_eq!(
        parsed,
        Extracted {
            invoice: "88231".into(),
            amount_centavos: 1000
        }
    );
    assert_eq!(meter.model, "test/primary");
}

#[tokio::test]
async fn structured_output_rejects_non_json() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(completion("isto não é JSON", "test/primary")),
        )
        .mount(&server)
        .await;
    let client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), None, fast_retry()).unwrap();
    let err = structured::<Extracted>(&client, &request())
        .await
        .unwrap_err();
    assert!(matches!(err, LlmError::BadStructured(_)), "{err:?}");
}

#[tokio::test]
async fn cache_serves_second_call_and_replay_fails_on_miss() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(completion("cached answer", "test/primary")),
        )
        .expect(1)
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let cache = ResponseCache::new(dir.path(), CacheMode::ReadWrite);
    let client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), Some(cache), fast_retry()).unwrap();
    let first = client.complete(&request()).await.unwrap();
    let second = client.complete(&request()).await.unwrap();
    assert_eq!(first.content, second.content);
    assert!(!first.meter.cached);
    assert!(second.meter.cached);
    assert_eq!(second.meter.attempts, 0);
    let totals = client.meter_totals();
    assert_eq!(totals.calls, 2);
    assert_eq!(totals.cached_calls, 1);
    assert_eq!(totals.total_tokens, 15, "cached calls add no tokens");

    // replay: the cached prompt is served, a new prompt is a hard error and never hits the server
    let replay = ResponseCache::new(dir.path(), CacheMode::Replay);
    let replay_client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), Some(replay), fast_retry()).unwrap();
    assert_eq!(
        replay_client.complete(&request()).await.unwrap().content,
        "cached answer"
    );
    let miss = replay_client
        .complete(&CompletionRequest::new("extraction", "outro", "prompt"))
        .await
        .unwrap_err();
    assert!(matches!(miss, LlmError::ReplayMiss(_)), "{miss:?}");
}

#[tokio::test]
async fn local_client_uses_local_model_without_auth_or_provider_prefs() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .and(body_partial_json(json!({"model": "local-model"})))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(completion("local ok", "local-model")),
        )
        .expect(1)
        .mount(&server)
        .await;
    let client =
        HttpLlmClient::local(format!("{}/v1", server.uri()), models(), None, fast_retry()).unwrap();
    let r = client.complete(&request()).await.unwrap();
    assert_eq!(r.content, "local ok");
    assert_eq!(client.mode(), LlmMode::Local);
    let received = server.received_requests().await.unwrap();
    assert_eq!(received.len(), 1);
    assert!(received[0].headers.get("authorization").is_none());
    let body: serde_json::Value = serde_json::from_slice(&received[0].body).unwrap();
    assert!(body.get("provider").is_none());
    // a role without a local model cannot be used locally
    let err = client
        .complete(&CompletionRequest::new("guard", "s", "u"))
        .await
        .unwrap_err();
    assert!(matches!(err, LlmError::Config(_)));
}

#[tokio::test]
async fn off_client_is_not_available() {
    let client = OffClient::default();
    assert_eq!(client.mode(), LlmMode::Off);
    assert!(matches!(
        client.complete(&request()).await,
        Err(LlmError::NotAvailable(_))
    ));
    assert!(matches!(
        structured::<Extracted>(&client, &request()).await,
        Err(LlmError::NotAvailable(_))
    ));
    assert_eq!(client.meter_totals().calls, 0);
}

#[tokio::test]
async fn unknown_role_and_bad_response() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({"choices": []})))
        .mount(&server)
        .await;
    let client =
        HttpLlmClient::openrouter(server.uri(), "k", models(), None, fast_retry()).unwrap();
    assert!(matches!(
        client
            .complete(&CompletionRequest::new("nope", "s", "u"))
            .await,
        Err(LlmError::UnknownRole(_))
    ));
    assert!(matches!(
        client.complete(&request()).await,
        Err(LlmError::BadResponse(_))
    ));
    assert!(matches!(
        HttpLlmClient::openrouter(" ", "k", models(), None, fast_retry()),
        Err(LlmError::Config(_))
    ));
}

#[test]
fn building_a_client_installs_the_ring_crypto_provider() {
    // reqwest is built with `rustls-no-provider`: the client constructor installs `ring`
    // before the first TLS handshake, so HTTPS endpoints work without aws-lc-sys.
    let client =
        HttpLlmClient::local("http://localhost:1/v1", models(), None, fast_retry()).unwrap();
    drop(client);
    assert!(HttpLlmClient::crypto_provider_installed());
}

#[tokio::test]
async fn local_reasoning_control_is_optional_and_separates_cached_answers() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(200).set_body_json(completion("ok", "local-model")))
        .expect(2)
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let cache = ResponseCache::new(dir.path(), CacheMode::ReadWrite);
    let plain = HttpLlmClient::local(
        format!("{}/v1", server.uri()),
        models(),
        Some(cache.clone()),
        fast_retry(),
    )
    .unwrap();
    let direct = HttpLlmClient::local(
        format!("{}/v1", server.uri()),
        models(),
        Some(cache),
        fast_retry(),
    )
    .unwrap()
    .with_local_reasoning_effort(Some(" none ".to_string()));
    assert!(!plain.complete(&request()).await.unwrap().meter.cached);
    assert!(!direct.complete(&request()).await.unwrap().meter.cached);
    assert!(direct.complete(&request()).await.unwrap().meter.cached);
    let requests = server.received_requests().await.unwrap();
    let first: serde_json::Value = requests[0].body_json().unwrap();
    let second: serde_json::Value = requests[1].body_json().unwrap();
    assert!(first.get("reasoning_effort").is_none());
    assert_eq!(second["reasoning_effort"], "none");
    assert!(requests
        .iter()
        .all(|r| !r.headers.contains_key("authorization")));
}
