//! Errors of the model client.

use thiserror::Error;

/// Everything that can go wrong talking to a model (or deciding not to).
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum LlmError {
    /// The client is in `off` mode: deterministic stages continue, model stages are skipped.
    #[error("modelo indisponível (DM_LLM_MODE=off): {0}")]
    NotAvailable(String),
    /// `config/models.yaml` could not be found or parsed.
    #[error("configuração de modelos: {0}")]
    Config(String),
    /// The role is not in the configuration.
    #[error("papel de modelo desconhecido: {0}")]
    UnknownRole(String),
    /// A required environment variable is missing.
    #[error("variável de ambiente obrigatória ausente: {0}")]
    MissingEnv(String),
    /// Every attempt (and fallback) failed.
    #[error("falha ao chamar o modelo após {attempts} tentativa(s): {detail}")]
    Http {
        /// Attempts made across models.
        attempts: u32,
        /// Last error.
        detail: String,
    },
    /// The server answered something that is not a chat completion.
    #[error("resposta inválida do modelo: {0}")]
    BadResponse(String),
    /// Structured output did not match the schema or was not JSON.
    #[error("saída estruturada inválida: {0}")]
    BadStructured(String),
    /// Disk cache failure.
    #[error("cache de respostas: {0}")]
    Cache(String),
    /// Replay mode and the response is not cached.
    #[error("cache miss em modo replay (chave {0})")]
    ReplayMiss(String),
}
