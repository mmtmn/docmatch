//! The server over a real Unix socket, driven by a tonic client like the Go worker.

use std::path::{Path, PathBuf};

use dm_recon::EngineConfig;
use dm_server::pb::harness_client::HarnessClient;
use dm_server::pb::{self, run_event::Event};
use dm_server::{serve, ServerConfig};
use hyper_util::rt::TokioIo;
use sha2::{Digest, Sha256};
use tokio::net::UnixStream;
use tokio_stream::StreamExt;
use tonic::transport::{Channel, Endpoint, Uri};
use tower::service_fn;

fn twin_dir() -> PathBuf {
    Path::new(
        &std::env::var("CARGO_MANIFEST_DIR")
            .expect("cargo define CARGO_MANIFEST_DIR ao rodar os testes"),
    )
    .join("../../../../testdata/golden/vantara-twin")
}

/// A short directory for the socket (Unix socket paths are limited to ~108 bytes).
fn socket_dir() -> tempfile::TempDir {
    let base = std::env::temp_dir();
    let base = if base.as_os_str().len() > 60 {
        PathBuf::from("/tmp")
    } else {
        base
    };
    tempfile::Builder::new()
        .prefix("dmh")
        .tempdir_in(base)
        .unwrap_or_else(|e| panic!("{e}"))
}

struct Harness {
    _sock_dir: tempfile::TempDir,
    root: tempfile::TempDir,
    socket: PathBuf,
    stop: Option<tokio::sync::oneshot::Sender<()>>,
    task: Option<tokio::task::JoinHandle<()>>,
}

impl Harness {
    async fn start() -> Harness {
        let sock_dir = socket_dir();
        let socket = sock_dir.path().join("h.sock");
        let root = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
        let work = root.path().join("work");
        let cfg = ServerConfig {
            socket: socket.clone(),
            local_root: Some(root.path().to_path_buf()),
            workdir: work,
            engine: EngineConfig::default(),
        };
        let (tx, rx) = tokio::sync::oneshot::channel::<()>();
        let task = tokio::spawn(async move {
            let _ = serve(cfg, async {
                let _ = rx.await;
            })
            .await;
        });
        for _ in 0..100 {
            if socket.exists() {
                break;
            }
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
        Harness {
            _sock_dir: sock_dir,
            root,
            socket,
            stop: Some(tx),
            task: Some(task),
        }
    }

    async fn client(&self) -> HarnessClient<Channel> {
        let path = self.socket.clone();
        let channel =
            Endpoint::try_from("http://harness.invalid")
                .unwrap_or_else(|e| panic!("{e}"))
                .connect_with_connector(service_fn(move |_: Uri| {
                    let path = path.clone();
                    async move {
                        Ok::<_, std::io::Error>(TokioIo::new(UnixStream::connect(path).await?))
                    }
                }))
                .await
                .unwrap_or_else(|e| panic!("{e}"));
        HarnessClient::new(channel)
    }

    /// Copies the twin into the local root, as the API's local storage keeps uploads.
    fn twin_sources(&self) -> Vec<pb::Source> {
        let quarantine = self.root.path().join("quarantine");
        std::fs::create_dir_all(&quarantine).unwrap_or_else(|e| panic!("{e}"));
        let mut names: Vec<String> = std::fs::read_dir(twin_dir())
            .unwrap_or_else(|e| panic!("{e}"))
            .filter_map(Result::ok)
            .map(|e| e.file_name().to_string_lossy().to_string())
            .filter(|n| {
                Path::new(n)
                    .extension()
                    .is_some_and(|x| x.eq_ignore_ascii_case("csv") || x.eq_ignore_ascii_case("txt"))
            })
            .collect();
        names.sort();
        names
            .iter()
            .enumerate()
            .map(|(i, name)| {
                let bytes = std::fs::read(twin_dir().join(name)).unwrap_or_else(|e| panic!("{e}"));
                let key = format!("k{i}");
                std::fs::write(quarantine.join(&key), &bytes).unwrap_or_else(|e| panic!("{e}"));
                pb::Source {
                    id: format!("upl_{i:026}"),
                    kind: pb::SourceKind::Table as i32,
                    uri: format!("local://docmatch-media-dev/quarantine/{key}"),
                    sha256: hex::encode(Sha256::digest(&bytes)),
                    file_name: name.clone(),
                    size_bytes: bytes.len() as u64,
                    ..pb::Source::default()
                }
            })
            .collect()
    }

    async fn stop(mut self) {
        if let Some(tx) = self.stop.take() {
            let _ = tx.send(());
        }
        if let Some(task) = self.task.take() {
            let _ = task.await;
        }
    }
}

async fn collect(client: &mut HarnessClient<Channel>, req: pb::RunRequest) -> Vec<pb::RunEvent> {
    let mut stream = client
        .run(req)
        .await
        .unwrap_or_else(|e| panic!("{e}"))
        .into_inner();
    let mut events = Vec::new();
    while let Some(ev) = stream.next().await {
        events.push(ev.unwrap_or_else(|e| panic!("{e}")));
    }
    events
}

fn run_request(run_id: &str, sources: Vec<pb::Source>) -> pb::RunRequest {
    pb::RunRequest {
        run_id: run_id.into(),
        tenant_id: "org_1".into(),
        sources,
        options: Some(pb::RunOptions {
            llm_mode: pb::LlmMode::Off as i32,
            ..pb::RunOptions::default()
        }),
        quote: Some(pb::Quote {
            expected_credits: 40,
            cap_credits: 50,
            ..pb::Quote::default()
        }),
        ..pb::RunRequest::default()
    }
}

#[tokio::test]
async fn reconciles_the_twin_over_the_socket() {
    let h = Harness::start().await;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = std::fs::metadata(&h.socket)
            .unwrap_or_else(|e| panic!("{e}"))
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o600, "socket precisa ser 0600");
    }
    let mut client = h.client().await;

    let health = client
        .health(pb::HealthRequest {})
        .await
        .unwrap_or_else(|e| panic!("{e}"))
        .into_inner();
    assert_eq!(health.status, pb::ServingStatus::Serving as i32);

    let sources = h.twin_sources();
    let quote = client
        .estimate(pb::EstimateRequest {
            tenant_id: "org_1".into(),
            sources: sources.clone(),
            options: None,
        })
        .await
        .unwrap_or_else(|e| panic!("{e}"))
        .into_inner();
    let docs = quote
        .breakdown
        .as_ref()
        .and_then(|b| {
            b.items
                .iter()
                .find(|i| i.item == pb::EstimateItemKind::Documents as i32)
        })
        .map(|i| i.quantity);
    assert_eq!(docs, Some(4.0));

    let events = collect(&mut client, run_request("run_twin", sources)).await;
    let seqs: Vec<u64> = events.iter().map(|e| e.seq).collect();
    assert!(
        seqs.windows(2).all(|w| w[1] == w[0] + 1),
        "seq precisa ser contínuo: {seqs:?}"
    );
    let findings: Vec<&pb::Finding> = events
        .iter()
        .filter_map(|e| match &e.event {
            Some(Event::Finding(f)) => Some(f),
            _ => None,
        })
        .collect();
    assert_eq!(findings.len(), 23);
    let partial = findings
        .iter()
        .find(|f| f.code == "recon.partial_payment")
        .unwrap_or_else(|| panic!("sem pagamento parcial"));
    assert_eq!(partial.amount_at_risk, 4_900_000);
    assert!(partial
        .evidence
        .iter()
        .all(|e| e.source_id.starts_with("upl_")));
    assert!(findings
        .iter()
        .all(|f| !f.title.is_empty() && !f.explanation.is_empty()));
    let matches = events
        .iter()
        .filter(|e| matches!(e.event, Some(Event::Match(_))))
        .count();
    assert!(matches >= 15, "casamentos: {matches}");
    let Some(Event::Completed(done)) = events.last().and_then(|e| e.event.clone()) else {
        panic!("último evento não é completed: {:?}", events.last());
    };
    let summary = done.summary.unwrap_or_default();
    assert_eq!(
        (
            summary.findings_total,
            summary.lines_total,
            summary.lines_matched
        ),
        (23, 18, 17)
    );
    assert_eq!(done.credits_spent, 40);
    h.stop().await;
}

#[tokio::test]
async fn refuses_sources_it_must_not_read() {
    let h = Harness::start().await;
    let mut client = h.client().await;
    for (uri, why) in [
        ("local://b/../../../etc/passwd", "escape"),
        ("s3://bucket/key", "esquema remoto"),
        ("file:///etc/passwd", "arquivo arbitrário"),
    ] {
        let source = pb::Source {
            id: "upl_x".into(),
            uri: uri.into(),
            file_name: "x.csv".into(),
            ..pb::Source::default()
        };
        let events = collect(&mut client, run_request("run_bad", vec![source])).await;
        match events.last().and_then(|e| e.event.clone()) {
            Some(Event::Failed(f)) => assert_eq!(f.code, "run_source_unreadable", "{why}: {f:?}"),
            other => panic!("{why}: esperava failed, veio {other:?}"),
        }
        assert!(!events
            .iter()
            .any(|e| matches!(e.event, Some(Event::Finding(_)))));
    }
    // A wrong checksum is refused too.
    let mut sources = h.twin_sources();
    sources[0].sha256 = "0".repeat(64);
    let events = collect(&mut client, run_request("run_sha", sources)).await;
    assert!(
        matches!(events.last().and_then(|e| e.event.clone()), Some(Event::Failed(f)) if f.code == "run_source_unreadable")
    );
    h.stop().await;
}

/// The deadline stops the work itself: the engine, reading heavy files on a blocking thread,
/// sees the cancellation at its next check and lets its folder go; the server then takes the
/// next run.
#[tokio::test]
async fn the_deadline_stops_the_blocking_work() {
    let h = Harness::start().await;
    let mut sources = h.twin_sources();
    let quarantine = h.root.path().join("quarantine");
    let page = format!(
        "<html><body>{}</body></html>",
        "<div>linha de um relatório longo</div>".repeat(80_000)
    );
    for i in 0..12 {
        let key = format!("pesado{i}");
        std::fs::write(quarantine.join(&key), page.as_bytes()).unwrap_or_else(|e| panic!("{e}"));
        sources.push(pb::Source {
            id: format!("upl_p{i:025}"),
            kind: pb::SourceKind::Table as i32,
            uri: format!("local://docmatch-media-dev/quarantine/{key}"),
            sha256: hex::encode(Sha256::digest(page.as_bytes())),
            file_name: format!("{i:02}_relatorio.html"),
            size_bytes: page.len() as u64,
            ..pb::Source::default()
        });
    }
    let mut req = run_request("run_prazo", sources);
    if let Some(o) = req.options.as_mut() {
        o.deadline = Some(prost_types::Timestamp::from(
            std::time::SystemTime::now() + std::time::Duration::from_millis(400),
        ));
    }
    let mut client = h.client().await;
    let started = std::time::Instant::now();
    let events = collect(&mut client, req).await;
    let failed = events.iter().find_map(|e| match &e.event {
        Some(Event::Failed(f)) => Some(f.code.clone()),
        _ => None,
    });
    assert_eq!(failed.as_deref(), Some("run_timeout"), "{events:?}");
    // The engine stopped: its run folder is gone soon after, not when all files are read.
    let folder = h.root.path().join("work").join("run_prazo");
    let mut waited = 0;
    while folder.exists() && waited < 200 {
        tokio::time::sleep(std::time::Duration::from_millis(50)).await;
        waited += 1;
    }
    assert!(!folder.exists(), "a pasta da execução continuou em uso");
    assert!(
        started.elapsed() < std::time::Duration::from_secs(20),
        "{:?}",
        started.elapsed()
    );
    // The slots are free again.
    let events = collect(&mut client, run_request("run_depois", h.twin_sources())).await;
    assert!(
        events
            .iter()
            .any(|e| matches!(e.event, Some(Event::Completed(_)))),
        "{events:?}"
    );
    h.stop().await;
}
