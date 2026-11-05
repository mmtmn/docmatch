//! Materializes a run's sources into a private run folder.
//!
//! The harness never opens a path the request names. A source arrives as
//! `local://<bucket>/<key>`: the key is resolved under the configured local root, the result
//! is canonicalized and must stay inside it (no `..`, no symlink escapes), the size is capped
//! and the SHA-256 the scanner recorded must match. Remote schemes (`s3://`, `r2://`) are
//! refused here: the worker materializes them before calling, so the harness needs no network.

use std::collections::BTreeSet;
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};

use crate::pb;

/// Why a source could not be read.
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum SourceError {
    /// The URI scheme is not one the harness reads.
    #[error("fonte {0}: esquema não suportado (o worker deve materializar fontes remotas)")]
    Scheme(String),
    /// The local root is not configured.
    #[error("fonte {0}: armazenamento local não configurado no harness")]
    NoLocalRoot(String),
    /// The path escapes the local root or does not exist.
    #[error("fonte {0}: caminho inválido ou fora da pasta permitida")]
    Path(String),
    /// The file is larger than allowed.
    #[error("fonte {0}: arquivo maior que o permitido ({1} bytes)")]
    TooLarge(String, u64),
    /// The content does not match the recorded checksum.
    #[error("fonte {0}: conteúdo não confere com o SHA-256 registrado")]
    Checksum(String),
    /// Reading or writing failed.
    #[error("fonte {0}: falha de leitura ({1})")]
    Io(String, String),
}

/// One source copied into the run folder.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Materialized {
    /// Upload id from the request.
    pub source_id: String,
    /// File name inside the run folder.
    pub file_name: String,
}

/// A file name safe inside the run folder: last path component only, characters outside
/// `[A-Za-z0-9._-]` replaced, never hidden, at most 120 bytes, extension kept.
pub fn safe_file_name(name: &str, fallback: &str) -> String {
    let last = name.rsplit(['/', '\\']).next().unwrap_or_default();
    let mut out: String = last
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-') {
                c
            } else {
                '_'
            }
        })
        .collect();
    out = out.trim_start_matches('.').to_string();
    if out.is_empty() {
        out = fallback
            .chars()
            .filter(|c| c.is_ascii_alphanumeric() || *c == '_')
            .collect();
    }
    if out.len() > 120 {
        let (stem, ext) = out
            .rsplit_once('.')
            .map_or((out.as_str(), ""), |(s, e)| (s, e));
        let ext: String = ext.chars().take(10).collect();
        let keep = 120usize.saturating_sub(ext.len() + 1);
        out = format!("{}.{ext}", &stem[..keep.min(stem.len())]);
    }
    out
}

fn resolve(uri: &str, id: &str, local_root: Option<&Path>) -> Result<PathBuf, SourceError> {
    let Some(rest) = uri.strip_prefix("local://") else {
        let scheme = uri.split_once("://").map_or("?", |(s, _)| s);
        return Err(SourceError::Scheme(format!("{id} ({scheme})")));
    };
    let root = local_root.ok_or_else(|| SourceError::NoLocalRoot(id.to_string()))?;
    // `local://<bucket>/<key>`: the bucket is a label, the key a relative path under the root.
    let key = rest.split_once('/').map_or("", |(_, k)| k);
    if key.is_empty()
        || key.starts_with('/')
        || key
            .split('/')
            .any(|p| p == ".." || p == "." || p.is_empty())
    {
        return Err(SourceError::Path(id.to_string()));
    }
    let root = root
        .canonicalize()
        .map_err(|_| SourceError::NoLocalRoot(id.to_string()))?;
    let path = root
        .join(key)
        .canonicalize()
        .map_err(|_| SourceError::Path(id.to_string()))?;
    if !path.starts_with(&root) || !path.is_file() {
        return Err(SourceError::Path(id.to_string()));
    }
    Ok(path)
}

fn copy_checked(
    from: &Path,
    to: &Path,
    src: &pb::Source,
    max_bytes: u64,
) -> Result<(), SourceError> {
    let io = |e: std::io::Error| SourceError::Io(src.id.clone(), e.kind().to_string());
    let meta = fs::metadata(from).map_err(io)?;
    if meta.len() > max_bytes {
        return Err(SourceError::TooLarge(src.id.clone(), max_bytes));
    }
    let mut bytes = Vec::with_capacity(usize::try_from(meta.len()).unwrap_or(0));
    fs::File::open(from)
        .map_err(io)?
        .take(max_bytes + 1)
        .read_to_end(&mut bytes)
        .map_err(io)?;
    if bytes.len() as u64 > max_bytes {
        return Err(SourceError::TooLarge(src.id.clone(), max_bytes));
    }
    if !src.sha256.is_empty()
        && !hex::encode(Sha256::digest(&bytes)).eq_ignore_ascii_case(&src.sha256)
    {
        return Err(SourceError::Checksum(src.id.clone()));
    }
    fs::write(to, &bytes).map_err(io)
}

/// Copies every source into `run_dir` and returns where each went.
pub fn materialize(
    sources: &[pb::Source],
    run_dir: &Path,
    local_root: Option<&Path>,
    max_bytes: u64,
) -> Result<Vec<Materialized>, SourceError> {
    let mut out = Vec::with_capacity(sources.len());
    let mut used = BTreeSet::new();
    for (i, src) in sources.iter().enumerate() {
        let from = resolve(&src.uri, &src.id, local_root)?;
        let mut name = safe_file_name(&src.file_name, &format!("fonte_{}", i + 1));
        if !used.insert(name.clone()) {
            name = format!("{}_{name}", i + 1);
            used.insert(name.clone());
        }
        copy_checked(&from, &run_dir.join(&name), src, max_bytes)?;
        out.push(Materialized {
            source_id: src.id.clone(),
            file_name: name,
        });
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn source(id: &str, uri: &str, name: &str, sha: &str) -> pb::Source {
        pb::Source {
            id: id.into(),
            uri: uri.into(),
            file_name: name.into(),
            sha256: sha.into(),
            ..pb::Source::default()
        }
    }

    #[test]
    fn names_are_sanitized() {
        assert_eq!(safe_file_name("../../etc/passwd", "x"), "passwd");
        assert_eq!(safe_file_name("..\\win.ini", "x"), "win.ini");
        assert_eq!(safe_file_name(".bashrc", "x"), "bashrc");
        assert_eq!(
            safe_file_name("extrato julho.csv", "x"),
            "extrato_julho.csv"
        );
        assert_eq!(safe_file_name("", "fonte_1"), "fonte_1");
        assert!(safe_file_name(&("a".repeat(300) + ".csv"), "x").len() <= 120);
    }

    #[test]
    fn escapes_schemes_and_checksums_are_refused() {
        let root = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
        let run = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
        fs::create_dir_all(root.path().join("quarantine")).unwrap_or_else(|e| panic!("{e}"));
        fs::write(root.path().join("quarantine/a.csv"), b"x;y\n1;2\n")
            .unwrap_or_else(|e| panic!("{e}"));
        let ok = hex::encode(Sha256::digest(b"x;y\n1;2\n"));
        let r = Some(root.path());
        let got = materialize(
            &[source("upl_1", "local://b/quarantine/a.csv", "a.csv", &ok)],
            run.path(),
            r,
            1024,
        )
        .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(got[0].file_name, "a.csv");
        for (uri, want) in [
            ("local://b/../../etc/passwd", "Path"),
            ("local://b/quarantine/../../../etc/passwd", "Path"),
            ("local://b//etc/passwd", "Path"),
            ("local://b/nao-existe.csv", "Path"),
            ("s3://bucket/key", "Scheme"),
            ("file:///etc/passwd", "Scheme"),
        ] {
            let err = materialize(&[source("upl_2", uri, "x.csv", "")], run.path(), r, 1024)
                .err()
                .unwrap_or_else(|| panic!("{uri} deveria falhar"));
            assert!(format!("{err:?}").starts_with(want), "{uri}: {err:?}");
        }
        let bad = materialize(
            &[source(
                "upl_3",
                "local://b/quarantine/a.csv",
                "a.csv",
                &"0".repeat(64),
            )],
            run.path(),
            r,
            1024,
        );
        assert_eq!(bad, Err(SourceError::Checksum("upl_3".into())));
        let big = materialize(
            &[source("upl_4", "local://b/quarantine/a.csv", "a.csv", "")],
            run.path(),
            r,
            3,
        );
        assert_eq!(big, Err(SourceError::TooLarge("upl_4".into(), 3)));
        assert!(matches!(
            materialize(
                &[source("upl_5", "local://b/quarantine/a.csv", "a.csv", "")],
                run.path(),
                None,
                1024
            ),
            Err(SourceError::NoLocalRoot(_))
        ));
    }

    #[cfg(unix)]
    #[test]
    fn symlinks_out_of_the_root_are_refused() {
        let root = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
        let outside = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
        let run = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
        fs::write(outside.path().join("segredo.txt"), b"x").unwrap_or_else(|e| panic!("{e}"));
        std::os::unix::fs::symlink(
            outside.path().join("segredo.txt"),
            root.path().join("link.csv"),
        )
        .unwrap_or_else(|e| panic!("{e}"));
        let err = materialize(
            &[source("upl_1", "local://b/link.csv", "link.csv", "")],
            run.path(),
            Some(root.path()),
            1024,
        );
        assert_eq!(err, Err(SourceError::Path("upl_1".into())));
    }
}
