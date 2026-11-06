//! gRPC server of the harness, over a Unix socket. The API's worker sends the scanned
//! sources and the run options; the deterministic engine (`dm-recon`) reconciles them and
//! the stream reports stages, findings, matches and the summary.

#![forbid(unsafe_code)]

pub mod convert;
pub mod document;
pub mod model;
pub mod service;
pub mod sources;

/// Code generated from `packages/contracts/harness.proto`.
#[allow(
    clippy::all,
    clippy::pedantic,
    missing_docs,
    rust_2018_idioms,
    unreachable_pub
)]
pub mod pb {
    tonic::include_proto!("docmatch.harness.v1");
}

pub use service::{serve, Harness, ServeError, ServerConfig};
