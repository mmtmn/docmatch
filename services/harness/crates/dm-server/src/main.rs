//! `dm-server`: the harness gRPC server on the Unix socket of `DM_HARNESS_SOCKET`.

#![forbid(unsafe_code)]
#![allow(clippy::print_stderr)]

use std::process::ExitCode;

use dm_server::{serve, ServerConfig};

#[tokio::main]
async fn main() -> ExitCode {
    // `--socket <path>` (as the systemd unit passes it) wins over DM_HARNESS_SOCKET.
    let args: Vec<String> = std::env::args().skip(1).collect();
    let socket_arg = args
        .iter()
        .position(|a| a == "--socket")
        .and_then(|i| args.get(i + 1))
        .cloned();
    if let Some(a) = args
        .iter()
        .find(|a| *a != "--socket" && Some(*a) != socket_arg.as_ref())
    {
        eprintln!("dm-server: argumento desconhecido: {a} (uso: dm-server [--socket CAMINHO])");
        return ExitCode::from(2);
    }
    let cfg = match ServerConfig::from_env_with_socket(socket_arg) {
        Ok(cfg) => cfg,
        Err(e) => {
            eprintln!("dm-server: {e}. Consulte .env.example na raiz do repositório.");
            return ExitCode::from(2);
        }
    };
    if cfg.local_root.is_none() {
        eprintln!("dm-server: DM_MEDIA_LOCAL_DIR vazio; fontes local:// serão recusadas");
    }
    eprintln!("dm-server: ouvindo em {}", cfg.socket.display());
    let shutdown = async {
        #[cfg(unix)]
        {
            use tokio::signal::unix::{signal, SignalKind};
            if let Ok(mut term) = signal(SignalKind::terminate()) {
                tokio::select! {
                    _ = term.recv() => {}
                    _ = tokio::signal::ctrl_c() => {}
                }
                return;
            }
        }
        let _ = tokio::signal::ctrl_c().await;
    };
    match serve(cfg, shutdown).await {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("dm-server: {e}");
            ExitCode::FAILURE
        }
    }
}
