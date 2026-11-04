//! Contrato de serviço do harness (esqueleto). O gRPC sobre socket Unix chega com o
//! `harness.proto` de outro PR; aqui fica o trait que a implementação do servidor e o worker
//! Go compartilham em espírito.
#![forbid(unsafe_code)]

use async_trait::async_trait;
use dm_core::Finding;
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Errors a service call can return.
#[derive(Debug, Clone, PartialEq, Eq, Error, Serialize, Deserialize)]
pub enum ServiceError {
    /// The request is malformed.
    #[error("requisição inválida: {0}")]
    InvalidRequest(String),
    /// The run failed.
    #[error("execução falhou: {0}")]
    RunFailed(String),
    /// Not implemented yet.
    #[error("ainda não implementado: {0}")]
    NotImplemented(&'static str),
}

/// A file to reconcile.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct InputFile {
    /// Original file name.
    pub name: String,
    /// Local path the worker downloaded it to.
    pub path: String,
}

/// Options of a run.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct RunOptions {
    /// Model tokens the run may spend (0 = deterministic only).
    pub token_budget: u64,
    /// Rules text from the user (pt-BR).
    #[serde(default)]
    pub rules_text: String,
    /// Reference year for dates without a year.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reference_year: Option<i32>,
}

/// A reconciliation request.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct RunRequest {
    /// Run id.
    pub run_id: String,
    /// Workspace id.
    pub workspace_id: String,
    /// Files.
    pub files: Vec<InputFile>,
    /// Options.
    #[serde(default)]
    pub options: RunOptions,
}

/// A reconciliation response.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, Default)]
pub struct RunResponse {
    /// Run id.
    pub run_id: String,
    /// Findings.
    pub findings: Vec<Finding>,
    /// Warnings (pt-BR).
    pub warnings: Vec<String>,
    /// Model tokens spent.
    pub tokens_spent: u64,
}

/// Health of the service.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct HealthStatus {
    /// True when ready to accept runs.
    pub ok: bool,
    /// Harness version.
    pub version: String,
}

/// The harness as the worker sees it.
#[async_trait]
pub trait HarnessService: Send + Sync {
    /// Runs a reconciliation.
    async fn run(&self, request: RunRequest) -> Result<RunResponse, ServiceError>;
    /// Health check.
    async fn health(&self) -> HealthStatus;
}

/// Service that validates requests and returns empty results.
///
/// TODO(fase 1): substituir pela cascata completa (ingestão -> casamento -> grafo -> relatório)
/// e expor via gRPC em socket Unix conforme `packages/contracts/harness.proto`.
#[derive(Debug, Default, Clone, Copy)]
pub struct NoopService;

#[async_trait]
impl HarnessService for NoopService {
    async fn run(&self, request: RunRequest) -> Result<RunResponse, ServiceError> {
        if request.run_id.trim().is_empty() {
            return Err(ServiceError::InvalidRequest("run_id vazio".to_string()));
        }
        if request.files.is_empty() {
            return Err(ServiceError::InvalidRequest("nenhum arquivo".to_string()));
        }
        Ok(RunResponse {
            run_id: request.run_id,
            findings: Vec::new(),
            warnings: vec!["cascata ainda não ligada (fase 1)".to_string()],
            tokens_spent: 0,
        })
    }

    async fn health(&self) -> HealthStatus {
        HealthStatus {
            ok: true,
            version: env!("CARGO_PKG_VERSION").to_string(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn noop_service_validates_and_answers() {
        let rt = tokio::runtime::Builder::new_current_thread()
            .build()
            .unwrap();
        rt.block_on(async {
            let svc = NoopService;
            assert!(svc.health().await.ok);
            let bad = RunRequest {
                run_id: String::new(),
                workspace_id: "w".into(),
                files: vec![],
                options: RunOptions::default(),
            };
            assert!(matches!(
                svc.run(bad).await,
                Err(ServiceError::InvalidRequest(_))
            ));
            let ok = RunRequest {
                run_id: "run_1".into(),
                workspace_id: "w".into(),
                files: vec![InputFile {
                    name: "a.csv".into(),
                    path: "/tmp/a.csv".into(),
                }],
                options: RunOptions {
                    token_budget: 0,
                    ..RunOptions::default()
                },
            };
            let r = svc.run(ok).await.unwrap();
            assert_eq!(r.run_id, "run_1");
            assert!(r.findings.is_empty());
            assert_eq!(r.tokens_spent, 0);
        });
    }
}
