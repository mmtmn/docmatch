//! The tonic service and the Unix-socket server.

use std::future::Future;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use dm_recon::EngineConfig;
use prost_types::Timestamp;
use tokio::sync::{mpsc, OwnedSemaphorePermit, Semaphore};
use tokio_stream::wrappers::{ReceiverStream, UnixListenerStream};
use tonic::{Request, Response, Status};

use crate::convert;
use crate::pb::harness_server::{Harness as HarnessApi, HarnessServer};
use crate::pb::{self, run_event::Event};
use crate::sources::materialize;

/// Runs executed at the same time; more wait for a slot.
pub const MAX_CONCURRENT_RUNS: usize = 2;
/// Largest source accepted (packages/contracts/limits.yaml: 50 MB per document).
pub const MAX_SOURCE_BYTES: u64 = 50 * 1024 * 1024;
/// Largest number of sources in one run.
pub const MAX_SOURCES: usize = 100;
/// Run deadline when the request carries none.
pub const DEFAULT_DEADLINE: Duration = Duration::from_secs(30 * 60);

/// Server configuration (from the environment of `.env.example`).
#[derive(Clone, Debug)]
pub struct ServerConfig {
    /// Socket path (`DM_HARNESS_SOCKET`).
    pub socket: PathBuf,
    /// Root of `local://` sources (`DM_MEDIA_LOCAL_DIR`, the API's local storage).
    pub local_root: Option<PathBuf>,
    /// Where private run folders are created.
    pub workdir: PathBuf,
    /// Engine configuration.
    pub engine: EngineConfig,
}

/// Configuration errors and server failures.
#[derive(Debug, thiserror::Error)]
pub enum ServeError {
    /// A required variable is missing.
    #[error("variável obrigatória ausente: {0}")]
    Missing(&'static str),
    /// The socket could not be prepared.
    #[error("socket {0}: {1}")]
    Socket(String, String),
    /// The gRPC server failed.
    #[error("servidor gRPC: {0}")]
    Transport(String),
}

impl ServerConfig {
    /// Reads `DM_HARNESS_SOCKET` (required) and `DM_MEDIA_LOCAL_DIR` (optional).
    pub fn from_env() -> Result<ServerConfig, ServeError> {
        let socket = std::env::var("DM_HARNESS_SOCKET")
            .ok()
            .filter(|s| !s.trim().is_empty())
            .ok_or(ServeError::Missing("DM_HARNESS_SOCKET"))?;
        let local_root = std::env::var("DM_MEDIA_LOCAL_DIR")
            .ok()
            .filter(|s| !s.trim().is_empty())
            .map(PathBuf::from);
        Ok(ServerConfig {
            socket: PathBuf::from(socket),
            local_root,
            workdir: std::env::temp_dir().join("docmatch-harness"),
            engine: EngineConfig::default(),
        })
    }
}

struct Inner {
    cfg: ServerConfig,
    /// A run holds its slot until its blocking work has stopped, not only until its deadline.
    slots: Arc<Semaphore>,
    active: AtomicU32,
    started: SystemTime,
}

/// The harness service.
#[derive(Clone)]
pub struct Harness {
    inner: Arc<Inner>,
}

impl Harness {
    /// A service with `cfg`.
    pub fn new(cfg: ServerConfig) -> Self {
        Harness {
            inner: Arc::new(Inner {
                cfg,
                slots: Arc::new(Semaphore::new(MAX_CONCURRENT_RUNS)),
                active: AtomicU32::new(0),
                started: SystemTime::now(),
            }),
        }
    }
}

fn now() -> Timestamp {
    Timestamp::from(SystemTime::now())
}

fn llm_enabled(options: Option<&pb::RunOptions>) -> bool {
    options.is_some_and(|o| {
        matches!(
            pb::LlmMode::try_from(o.llm_mode).unwrap_or(pb::LlmMode::Unspecified),
            pb::LlmMode::Openrouter | pb::LlmMode::Local
        )
    })
}

/// Tokens of model work forecast per source when models are enabled (the residual after the
/// deterministic cascade; the API prices the quantity).
const RESIDUAL_TOKENS_PER_SOURCE: f64 = 20_000.0;

fn breakdown(req: &pb::EstimateRequest) -> pb::EstimateBreakdown {
    #[allow(clippy::cast_precision_loss)] // at most MAX_SOURCES
    let n = req.sources.len().max(1) as f64;
    let mut items = vec![
        pb::EstimateItem {
            item: pb::EstimateItemKind::Documents as i32,
            quantity: n,
            unit: pb::EstimateUnit::Files as i32,
            ..pb::EstimateItem::default()
        },
        pb::EstimateItem {
            item: pb::EstimateItemKind::ScanFiles as i32,
            quantity: n,
            unit: pb::EstimateUnit::Files as i32,
            ..pb::EstimateItem::default()
        },
    ];
    if llm_enabled(req.options.as_ref()) {
        items.push(pb::EstimateItem {
            item: pb::EstimateItemKind::LlmTokens as i32,
            quantity: n * RESIDUAL_TOKENS_PER_SOURCE,
            unit: pb::EstimateUnit::Tokens as i32,
            note: "resíduo após a cascata determinística".to_string(),
            ..pb::EstimateItem::default()
        });
    }
    pb::EstimateBreakdown {
        lane: req.options.as_ref().map_or(0, |o| o.lane),
        lane_multiplier: 1.0,
        items,
        pricing_version: "harness".to_string(),
        ..pb::EstimateBreakdown::default()
    }
}

/// Sends events with a monotonic `seq`.
struct Emitter {
    tx: mpsc::Sender<Result<pb::RunEvent, Status>>,
    run_id: String,
    seq: u64,
}

impl Emitter {
    /// False when the client went away (the run stops).
    async fn send(&mut self, event: Event) -> bool {
        self.seq += 1;
        let ev = pb::RunEvent {
            run_id: self.run_id.clone(),
            seq: self.seq,
            at: Some(now()),
            event: Some(event),
        };
        self.tx.send(Ok(ev)).await.is_ok()
    }

    async fn stage(&mut self, stage: pb::Stage, message: &str, total: u64) -> bool {
        self.send(Event::StageStarted(pb::StageStarted {
            stage: stage as i32,
            message: message.to_string(),
            total_steps: total,
        }))
        .await
            && self
                .send(Event::Progress(pb::Progress {
                    stage: stage as i32,
                    done: total,
                    total,
                    message: message.to_string(),
                    credits_spent: 0,
                }))
                .await
    }

    async fn fail(&mut self, code: &str, message: String, retryable: bool) {
        let _ = self
            .send(Event::Failed(pb::Failed {
                code: code.to_string(),
                message,
                retryable,
                details: None,
                credits_spent: 0,
            }))
            .await;
    }
}

fn safe_dir_name(run_id: &str) -> String {
    let s: String = run_id
        .chars()
        .filter(|c| c.is_ascii_alphanumeric() || *c == '_' || *c == '-')
        .take(64)
        .collect();
    if s.is_empty() {
        "run".to_string()
    } else {
        s
    }
}

/// What the blocking work of a run shares with it: the cancellation flag it checks between
/// files, archive entries, pages and in the parsers' long loops, and the run's slot, held by
/// every blocking task until it returns.
#[derive(Clone)]
struct Work {
    cancel: Arc<AtomicBool>,
    slot: Arc<OwnedSemaphorePermit>,
}

impl Work {
    /// Runs `f` on a blocking thread under the run's cancellation flag, holding its slot.
    async fn blocking<R: Send + 'static>(
        &self,
        f: impl FnOnce() -> R + Send + 'static,
    ) -> Result<R, tokio::task::JoinError> {
        let work = self.clone();
        tokio::task::spawn_blocking(move || {
            let _slot = work.slot;
            dm_recon::cancel::scope(work.cancel, f)
        })
        .await
    }
}

async fn execute(inner: Arc<Inner>, req: pb::RunRequest, mut out: Emitter) {
    let Ok(slot) = Arc::clone(&inner.slots).acquire_owned().await else {
        return;
    };
    let work = Work {
        cancel: Arc::new(AtomicBool::new(false)),
        slot: Arc::new(slot),
    };
    inner.active.fetch_add(1, Ordering::SeqCst);
    let deadline = req
        .options
        .as_ref()
        .and_then(|o| o.deadline)
        .and_then(|d| SystemTime::try_from(d).ok())
        .and_then(|d| d.duration_since(SystemTime::now()).ok())
        .unwrap_or(DEFAULT_DEADLINE);
    let result = tokio::time::timeout(deadline, run_once(&inner, &req, &mut out, &work)).await;
    // Whatever ended the run (the deadline, the client leaving), work still running on a
    // blocking thread stops at its next check and then gives the slot back.
    work.cancel.store(true, Ordering::Relaxed);
    if result.is_err() {
        out.fail(
            "run_timeout",
            "A execução passou do prazo.".to_string(),
            true,
        )
        .await;
    }
    inner.active.fetch_sub(1, Ordering::SeqCst);
}

async fn run_once(inner: &Inner, req: &pb::RunRequest, out: &mut Emitter, work: &Work) {
    let n = req.sources.len() as u64;
    if req.sources.is_empty() || req.sources.len() > MAX_SOURCES {
        out.fail(
            "run_source_unreadable",
            "A execução precisa de 1 a 100 fontes.".to_string(),
            false,
        )
        .await;
        return;
    }
    if !out
        .stage(pb::Stage::Scan, "Conferindo os arquivos", n)
        .await
    {
        return;
    }
    let run_dir = inner.cfg.workdir.join(safe_dir_name(&req.run_id));
    let prepared = {
        let run_dir = run_dir.clone();
        let sources = req.sources.clone();
        let root = inner.cfg.local_root.clone();
        work.blocking(move || {
            let _ = std::fs::remove_dir_all(&run_dir);
            std::fs::create_dir_all(&run_dir).map_err(|e| e.to_string())?;
            let done = materialize(&sources, &run_dir, root.as_deref(), MAX_SOURCE_BYTES)
                .map_err(|e| e.to_string());
            if dm_recon::cancel::cancelled() {
                let _ = std::fs::remove_dir_all(&run_dir);
            }
            done
        })
        .await
    };
    let materialized = match prepared {
        Ok(Ok(m)) => m,
        Ok(Err(why)) => {
            cleanup(&run_dir).await;
            out.fail("run_source_unreadable", why, false).await;
            return;
        }
        Err(_) => {
            cleanup(&run_dir).await;
            out.fail(
                "internal_error",
                "Falha interna ao preparar as fontes.".to_string(),
                true,
            )
            .await;
            return;
        }
    };
    if !out.stage(pb::Stage::Ingest, "Lendo os arquivos", n).await
        || !out
            .stage(
                pb::Stage::Match,
                "Conciliando extrato, títulos e caixa de entrada",
                n,
            )
            .await
    {
        cleanup(&run_dir).await;
        return;
    }
    let engine = inner.cfg.engine.clone();
    let dir = run_dir.clone();
    // The folder goes with the work that reads it, also when the deadline dropped this future.
    let run = work
        .blocking(move || {
            let run = dm_recon::run_dir(&dir, &engine);
            let _ = std::fs::remove_dir_all(&dir);
            run
        })
        .await;
    let run = match run {
        Ok(Ok(run)) => run,
        Ok(Err(e)) => {
            out.fail("run_source_unreadable", e.to_string(), false)
                .await;
            return;
        }
        Err(_) => {
            cleanup(&run_dir).await;
            out.fail(
                "internal_error",
                "Falha interna no motor de conciliação.".to_string(),
                true,
            )
            .await;
            return;
        }
    };
    let index: convert::SourceIndex = materialized
        .into_iter()
        .map(|m| (m.file_name, m.source_id))
        .collect();
    let findings = convert::findings(&run, &req.run_id, &index);
    let matches = convert::matches(&run, &req.run_id, &index);
    for m in &matches {
        if !out.send(Event::Match(m.clone())).await {
            return;
        }
    }
    for f in &findings {
        if !out.send(Event::Finding(f.clone())).await {
            return;
        }
    }
    if !out
        .stage(pb::Stage::Report, "Montando o relatório", 1)
        .await
    {
        return;
    }
    // The deterministic work was quoted up front; charge what the quote expected, never more
    // than its cap, and let the API refund the rest of the hold.
    let quote = req.quote.as_ref();
    let budget = quote
        .map_or(0, |q| q.cap_credits)
        .max(req.options.as_ref().map_or(0, |o| o.credit_budget));
    let spent = quote.map_or(0, |q| q.expected_credits).min(budget);
    let _ = out
        .send(Event::Completed(pb::Completed {
            summary: Some(convert::summary(&run, &findings, matches.len())),
            credits_spent: spent,
            credits_refunded: 0,
        }))
        .await;
}

async fn cleanup(dir: &Path) {
    let dir = dir.to_path_buf();
    let _ = tokio::task::spawn_blocking(move || std::fs::remove_dir_all(dir)).await;
}

type EventStream = ReceiverStream<Result<pb::RunEvent, Status>>;

#[tonic::async_trait]
impl HarnessApi for Harness {
    async fn estimate(
        &self,
        request: Request<pb::EstimateRequest>,
    ) -> Result<Response<pb::Quote>, Status> {
        let req = request.into_inner();
        if req.sources.len() > MAX_SOURCES {
            return Err(Status::invalid_argument(
                "no máximo 100 fontes por execução",
            ));
        }
        Ok(Response::new(pb::Quote {
            breakdown: Some(breakdown(&req)),
            requires_approval: false,
            created_at: Some(now()),
            ..pb::Quote::default()
        }))
    }

    type RunStream = EventStream;

    async fn run(
        &self,
        request: Request<pb::RunRequest>,
    ) -> Result<Response<Self::RunStream>, Status> {
        let req = request.into_inner();
        if req.run_id.is_empty() {
            return Err(Status::invalid_argument("run_id obrigatório"));
        }
        let (tx, rx) = mpsc::channel(64);
        let out = Emitter {
            tx,
            run_id: req.run_id.clone(),
            seq: 0,
        };
        tokio::spawn(execute(Arc::clone(&self.inner), req, out));
        Ok(Response::new(ReceiverStream::new(rx)))
    }

    type ReplayStream = EventStream;

    async fn replay(
        &self,
        request: Request<pb::ReplayRequest>,
    ) -> Result<Response<Self::ReplayStream>, Status> {
        // The engine is deterministic: a replay is the same run again.
        let r = request.into_inner();
        self.run(Request::new(pb::RunRequest {
            run_id: r.run_id,
            tenant_id: r.tenant_id,
            sources: r.sources,
            options: r.options,
            quote: None,
            rules: r.rules,
            resume: false,
        }))
        .await
    }

    async fn propose_rules(
        &self,
        _request: Request<pb::ProposeRulesRequest>,
    ) -> Result<Response<pb::ProposeRulesResponse>, Status> {
        // Rule learning needs the discovery loop and a model; with neither, nothing to propose.
        Ok(Response::new(pb::ProposeRulesResponse::default()))
    }

    async fn health(
        &self,
        _request: Request<pb::HealthRequest>,
    ) -> Result<Response<pb::HealthResponse>, Status> {
        Ok(Response::new(pb::HealthResponse {
            status: pb::ServingStatus::Serving as i32,
            version: env!("CARGO_PKG_VERSION").to_string(),
            llm_mode: pb::LlmMode::Off as i32,
            llm_reachable: false,
            active_runs: self.inner.active.load(Ordering::SeqCst),
            started_at: Some(Timestamp::from(self.inner.started)),
        }))
    }
}

/// Serves on the configured Unix socket (mode 0600) until `shutdown` resolves.
pub async fn serve(
    cfg: ServerConfig,
    shutdown: impl Future<Output = ()>,
) -> Result<(), ServeError> {
    let path = cfg.socket.clone();
    let shown = path.display().to_string();
    let err = |e: std::io::Error| ServeError::Socket(shown.clone(), e.to_string());
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(err)?;
    }
    if path.exists() {
        std::fs::remove_file(&path).map_err(err)?;
    }
    std::fs::create_dir_all(&cfg.workdir).map_err(err)?;
    let listener = tokio::net::UnixListener::bind(&path).map_err(err)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600)).map_err(err)?;
    }
    let result = tonic::transport::Server::builder()
        .add_service(HarnessServer::new(Harness::new(cfg)))
        .serve_with_incoming_shutdown(UnixListenerStream::new(listener), shutdown)
        .await
        .map_err(|e| ServeError::Transport(e.to_string()));
    let _ = std::fs::remove_file(&path);
    result
}
