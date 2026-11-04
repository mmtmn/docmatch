//! Cliente de modelos de linguagem do harness.
//!
//! Três modos ([`LlmMode`]): OpenRouter (com preferências de provedor `data_collection=deny`,
//! modelo por papel de `config/models.yaml`, tentativas com jitter e tempo limite), um endpoint
//! local compatível com a API da OpenAI (`DM_LOCAL_LLM_BASE_URL`) e desligado, que devolve
//! [`LlmError::NotAvailable`] para que os estágios determinísticos sigam sem modelo. Toda
//! resposta pode ser guardada em disco ([`ResponseCache`]) com chave `sha256(modelo, prompt,
//! schema)`; o modo *replay* falha em cache miss e é o que os testes dourados usam. Nenhum
//! teste deste crate toca a rede: tudo roda contra um servidor HTTP simulado.
#![forbid(unsafe_code)]

pub mod cache;
pub mod client;
pub mod config;
pub mod error;
pub mod meter;

pub use cache::{cache_key, CacheMode, CachedResponse, ResponseCache};
pub use client::{
    build_client_from_env, structured, CompletionRequest, CompletionResponse, HttpLlmClient,
    LlmClient, LlmMode, OffClient, RetryPolicy,
};
pub use config::{
    find_models_file, load_models_config, load_models_config_from, ModelsConfig, ResolvedModel,
    RoleConfig,
};
pub use error::LlmError;
pub use meter::{CallMeter, Meter, MeterTotals, Usage};
