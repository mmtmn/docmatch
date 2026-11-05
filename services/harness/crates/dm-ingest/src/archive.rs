//! ZIP containers: every entry re-enters the dispatcher as its own source.
//!
//! The limits are the shared ones of `packages/contracts/limits.yaml` (`files.zip_*`, through
//! the generated `packages/contracts/gen/limits.rs`), applied the same way the API's upload
//! scanner applies them, so an archive the API accepted is never refused here and a crafted
//! one is refused in both places: too many entries, too much uncompressed data (declared and
//! actually read), a compression ratio above the limit, a zip inside the zip (depth 1) or a
//! path that is absolute or climbs out with `..`. Entries are read in memory, one at a time,
//! never past the declared size.

use std::io::{Cursor, Read};

use zip::ZipArchive;

use crate::IngestError;

#[allow(dead_code)] // the generated module carries every shared limit; only files.zip_* is read here
#[path = "../../../../../packages/contracts/gen/limits.rs"]
mod limits;

/// Shared ZIP limits.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ZipLimits {
    /// Entries per archive.
    pub max_entries: usize,
    /// Uncompressed bytes per archive.
    pub max_uncompressed: u64,
    /// Uncompressed / compressed, per entry and for the whole archive.
    pub max_ratio: u64,
    /// Archive nesting; 1 means no archive inside an archive.
    pub max_depth: usize,
}

impl Default for ZipLimits {
    fn default() -> Self {
        ZipLimits {
            max_entries: limits::files::ZIP_MAX_ENTRIES as usize,
            max_uncompressed: u64::from(limits::files::ZIP_MAX_UNCOMPRESSED_MB) << 20,
            max_ratio: u64::from(limits::files::ZIP_MAX_RATIO),
            max_depth: limits::files::ZIP_MAX_DEPTH as usize,
        }
    }
}

/// One entry, read.
#[derive(Debug)]
pub struct ZipEntry {
    /// Path inside the archive, `/`-separated, never absolute and never with `..`.
    pub path: String,
    /// Content.
    pub bytes: Vec<u8>,
}

/// Entries that are noise in every archive made on a Mac or by a file manager.
pub(crate) fn is_noise(path: &str) -> bool {
    path.starts_with("__MACOSX/")
        || path
            .rsplit('/')
            .next()
            .is_some_and(|n| n.starts_with('.') || n.eq_ignore_ascii_case("thumbs.db"))
}

fn refuse(reason: String) -> IngestError {
    IngestError::Unrecognized(format!("arquivo zip recusado: {reason}"))
}

pub(crate) fn safe_path(name: &str) -> Option<String> {
    let unified = name.replace('\\', "/");
    // The same test as the upload scanner (`..` or absolute): nothing is written to disk, so
    // a drive letter is just part of a name.
    if unified.starts_with('/') || unified.split('/').any(|part| part == "..") {
        return None;
    }
    Some(clean_name(unified.trim_start_matches("./")))
}

/// Characters kept of a name that came from inside a file (an archive entry path, an
/// attachment name): names end up in warnings, table names and reports. ZIP and 7z entries
/// and `.msg` attachments (`msg::safe_name`) are cleaned by the same rule.
pub(crate) const MAX_NAME_CHARS: usize = 200;

/// A name without control characters and at most [`MAX_NAME_CHARS`] long; a longer one keeps
/// its start and its end (the extension).
pub(crate) fn clean_name(raw: &str) -> String {
    let chars: Vec<char> = raw.chars().filter(|c| !c.is_control()).collect();
    if chars.len() <= MAX_NAME_CHARS {
        return chars.into_iter().collect();
    }
    let head = MAX_NAME_CHARS / 2;
    let tail = MAX_NAME_CHARS - head - 1;
    let mut out: String = chars[..head].iter().collect();
    out.push('…');
    out.extend(&chars[chars.len() - tail..]);
    out
}

/// Entry name as written (UTF-8, or CP437 decoded by the zip crate); invalid bytes are
/// replaced rather than refused.
fn entry_name<R: Read>(file: &zip::read::ZipFile<'_, R>) -> String {
    file.name().map_or_else(
        |_| String::from_utf8_lossy(file.name_raw()).into_owned(),
        std::borrow::Cow::into_owned,
    )
}

/// Opens an archive and reads its entries under the limits. Returns the entries (sorted by
/// path) and pt-BR warnings for entries that were skipped.
pub fn read_zip(
    bytes: &[u8],
    lim: &ZipLimits,
) -> Result<(Vec<ZipEntry>, Vec<String>), IngestError> {
    let mut archive = ZipArchive::new(Cursor::new(bytes)).map_err(|e| IngestError::Parse {
        format: "zip",
        detail: format!("arquivo zip ilegível ({e})"),
    })?;
    let count = archive.len();
    if count > lim.max_entries {
        return Err(refuse(format!(
            "{count} entradas (máximo {})",
            lim.max_entries
        )));
    }
    let max_mb = lim.max_uncompressed >> 20;
    // First pass: the declared sizes and names, exactly as the upload scanner checks them.
    let mut declared_total: u64 = 0;
    let mut encrypted = vec![false; count];
    for (i, flag) in encrypted.iter_mut().enumerate() {
        let file = archive.by_index_raw(i).map_err(|e| IngestError::Parse {
            format: "zip",
            detail: e.to_string(),
        })?;
        *flag = file.encrypted();
        declared_total = declared_total.saturating_add(file.size());
        if declared_total > lim.max_uncompressed {
            return Err(refuse(format!("descompactado acima de {max_mb} MB")));
        }
        let compressed = file.compressed_size();
        if compressed > 0 && file.size() / compressed > lim.max_ratio {
            return Err(refuse(format!(
                "razão de compressão acima de {}x",
                lim.max_ratio
            )));
        }
        let name = entry_name(&file);
        if lim.max_depth <= 1 && name.to_ascii_lowercase().ends_with(".zip") {
            return Err(refuse("zip dentro de zip".to_string()));
        }
        if safe_path(&name).is_none() {
            return Err(refuse("caminho perigoso dentro do zip".to_string()));
        }
    }
    let size = bytes.len() as u64;
    if size > 0 && declared_total / size > lim.max_ratio {
        return Err(refuse(format!(
            "razão de compressão acima de {}x",
            lim.max_ratio
        )));
    }
    // Second pass: read, never past the declared size nor the archive budget.
    let mut entries = Vec::new();
    let mut warnings = Vec::new();
    let mut read_total: u64 = 0;
    for (i, locked) in encrypted.into_iter().enumerate() {
        if crate::cancel::cancelled() {
            warnings.push(crate::cancel::STOPPED.to_string());
            break;
        }
        if locked {
            // The zip crate refuses to open it without the password; say so in pt-BR.
            let name = archive
                .by_index_raw(i)
                .map(|f| entry_name(&f))
                .unwrap_or_default();
            if !is_noise(&name) && !name.ends_with('/') {
                warnings.push(format!(
                    "{}: entrada protegida por senha não foi aberta",
                    clean_name(&name)
                ));
            }
            continue;
        }
        let mut file = match archive.by_index(i) {
            Ok(f) => f,
            Err(e) => {
                warnings.push(format!("entrada {} ilegível: {e}", i + 1));
                continue;
            }
        };
        if file.is_dir() {
            continue;
        }
        let Some(path) = safe_path(&entry_name(&file)) else {
            continue;
        };
        if is_noise(&path) {
            continue;
        }
        let declared = file.size();
        let budget = lim.max_uncompressed.saturating_sub(read_total);
        let cap = declared.min(budget);
        let mut buf = Vec::with_capacity(usize::try_from(cap.min(16 << 20)).unwrap_or(0));
        let read = (&mut file)
            .take(cap.saturating_add(1))
            .read_to_end(&mut buf);
        match read {
            Ok(n) if n as u64 > cap => {
                return Err(refuse(format!(
                    "{path}: conteúdo maior que o declarado ou acima de {max_mb} MB"
                )));
            }
            Ok(n) => {
                read_total = read_total.saturating_add(n as u64);
                entries.push(ZipEntry { path, bytes: buf });
            }
            Err(e) => warnings.push(format!("{path}: não foi possível descompactar ({e})")),
        }
    }
    entries.sort_by(|a, b| a.path.cmp(&b.path));
    unique_paths(&mut entries);
    Ok((entries, warnings))
}

/// Entries whose cleaned paths collide (two long names cut to the same) get ` (2)`, ` (3)`…
/// before the extension, so each stays a source of its own.
pub(crate) fn unique_paths(entries: &mut [ZipEntry]) {
    let mut seen = std::collections::HashSet::new();
    for e in entries.iter_mut() {
        if seen.insert(e.path.clone()) {
            continue;
        }
        let (stem, ext) = match e.path.rsplit_once('.') {
            Some((stem, ext)) if !ext.contains('/') => (stem.to_string(), format!(".{ext}")),
            _ => (e.path.clone(), String::new()),
        };
        let mut k = 2;
        while !seen.insert(format!("{stem} ({k}){ext}")) {
            k += 1;
        }
        e.path = format!("{stem} ({k}){ext}");
    }
}

/// Reads one member of a ZIP package (the `word/document.xml` of a DOCX, the `content.xml` of
/// an ODT) under the shared limits: entry count, compression ratio and at most `max_bytes`
/// (declared and actually read). `Ok(None)` when the package has no such member.
pub fn read_zip_member(
    bytes: &[u8],
    path: &str,
    max_bytes: u64,
    lim: &ZipLimits,
) -> Result<Option<Vec<u8>>, IngestError> {
    let mut archive = ZipArchive::new(Cursor::new(bytes)).map_err(|e| IngestError::Parse {
        format: "zip",
        detail: format!("pacote zip ilegível ({e})"),
    })?;
    if archive.len() > lim.max_entries {
        return Err(refuse(format!(
            "{} entradas (máximo {})",
            archive.len(),
            lim.max_entries
        )));
    }
    let mut file = match archive.by_name(path) {
        Ok(f) => f,
        Err(zip::result::ZipError::FileNotFound) => return Ok(None),
        Err(e) => {
            return Err(IngestError::Parse {
                format: "zip",
                detail: format!("{path}: {e}"),
            })
        }
    };
    let declared = file.size();
    let max_mb = (max_bytes >> 20).max(1);
    if declared > max_bytes {
        return Err(refuse(format!("{path} acima de {max_mb} MB")));
    }
    let compressed = file.compressed_size();
    if compressed > 0 && declared / compressed > lim.max_ratio {
        return Err(refuse(format!(
            "razão de compressão acima de {}x",
            lim.max_ratio
        )));
    }
    let mut buf = Vec::with_capacity(usize::try_from(declared.min(16 << 20)).unwrap_or(0));
    match (&mut file)
        .take(declared.saturating_add(1))
        .read_to_end(&mut buf)
    {
        Ok(n) if n as u64 > declared => {
            Err(refuse(format!("{path}: conteúdo maior que o declarado")))
        }
        Ok(_) => Ok(Some(buf)),
        Err(e) => Err(IngestError::Parse {
            format: "zip",
            detail: format!("{path}: não foi possível descompactar ({e})"),
        }),
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::io::Write;
    use zip::write::SimpleFileOptions;
    use zip::ZipWriter;

    /// Builds an archive from `(path, bytes)` pairs, deflated.
    pub(crate) fn zip_of(entries: &[(&str, &[u8])]) -> Vec<u8> {
        let mut w = ZipWriter::new(Cursor::new(Vec::new()));
        let opts =
            SimpleFileOptions::default().compression_method(zip::CompressionMethod::Deflated);
        for (path, bytes) in entries {
            if path.ends_with('/') {
                w.add_directory(*path, opts).unwrap();
            } else {
                w.start_file(*path, opts).unwrap();
                w.write_all(bytes).unwrap();
            }
        }
        w.finish().unwrap().into_inner()
    }

    #[test]
    fn limits_come_from_the_shared_contract() {
        let l = ZipLimits::default();
        assert_eq!(l.max_entries, 1000);
        assert_eq!(l.max_uncompressed, 200 << 20);
        assert_eq!(l.max_ratio, 100);
        assert_eq!(l.max_depth, 1);
    }

    #[test]
    fn reads_entries_sorted_and_skips_noise() {
        let z = zip_of(&[
            ("b/extrato.csv", b"data;valor\n01/07/2026;1,00\n"),
            ("a.txt", b"oi"),
            ("pasta/", b""),
            ("__MACOSX/._a.txt", b"x"),
            (".DS_Store", b"x"),
        ]);
        let (entries, warnings) = read_zip(&z, &ZipLimits::default()).unwrap();
        assert!(warnings.is_empty(), "{warnings:?}");
        let paths: Vec<&str> = entries.iter().map(|e| e.path.as_str()).collect();
        assert_eq!(paths, vec!["a.txt", "b/extrato.csv"]);
        assert_eq!(entries[0].bytes, b"oi");
    }

    #[test]
    fn refuses_what_the_upload_scanner_refuses() {
        let lim = ZipLimits::default();
        let nested = zip_of(&[("dentro.zip", b"PK")]);
        assert!(
            matches!(read_zip(&nested, &lim), Err(IngestError::Unrecognized(m)) if m.contains("zip dentro de zip"))
        );
        let climbing = zip_of(&[("../fora.csv", b"a")]);
        assert!(
            matches!(read_zip(&climbing, &lim), Err(IngestError::Unrecognized(m)) if m.contains("caminho perigoso"))
        );
        let absolute = zip_of(&[("/etc/x.csv", b"a")]);
        assert!(
            matches!(read_zip(&absolute, &lim), Err(IngestError::Unrecognized(m)) if m.contains("caminho perigoso"))
        );
        let bomb = zip_of(&[("zeros.csv", &vec![b'0'; 2 << 20])]);
        assert!(
            matches!(read_zip(&bomb, &lim), Err(IngestError::Unrecognized(m)) if m.contains("razão de compressão"))
        );
        let small = ZipLimits {
            max_entries: 1,
            ..lim
        };
        let two = zip_of(&[("a.csv", b"a"), ("b.csv", b"b")]);
        assert!(
            matches!(read_zip(&two, &small), Err(IngestError::Unrecognized(m)) if m.contains("2 entradas"))
        );
        let tiny = ZipLimits {
            max_uncompressed: 3,
            ..lim
        };
        assert!(read_zip(&two, &tiny).is_ok());
        let four = zip_of(&[("a.csv", b"aa"), ("b.csv", b"bb")]);
        assert!(
            matches!(read_zip(&four, &tiny), Err(IngestError::Unrecognized(m)) if m.contains("acima de 0 MB"))
        );
        assert!(matches!(
            read_zip(b"PK\x03\x04lixo", &lim),
            Err(IngestError::Parse { .. })
        ));
    }

    #[test]
    fn encrypted_entries_are_skipped_with_a_warning() {
        let mut z = zip_of(&[("a.csv", b"a;b\n1;2\n"), ("segredo.csv", b"x;y\n")]);
        // Set the "encrypted" flag (bit 0 of the general purpose flags) of the second entry,
        // in its local header and in the central directory.
        let mut seen_local = 0;
        let mut seen_central = 0;
        let mut i = 0;
        while i + 10 < z.len() {
            if z[i..].starts_with(b"PK\x03\x04") {
                seen_local += 1;
                if seen_local == 2 {
                    z[i + 6] |= 1;
                }
            } else if z[i..].starts_with(b"PK\x01\x02") {
                seen_central += 1;
                if seen_central == 2 {
                    z[i + 8] |= 1;
                }
            }
            i += 1;
        }
        let (entries, warnings) = read_zip(&z, &ZipLimits::default()).unwrap();
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].path, "a.csv");
        assert_eq!(
            warnings,
            vec!["segredo.csv: entrada protegida por senha não foi aberta".to_string()]
        );
    }

    #[test]
    fn path_safety() {
        assert_eq!(safe_path("a/b.csv").as_deref(), Some("a/b.csv"));
        assert_eq!(safe_path("./a.csv").as_deref(), Some("a.csv"));
        assert_eq!(safe_path("a\\b.csv").as_deref(), Some("a/b.csv"));
        assert_eq!(safe_path("C:/x.csv").as_deref(), Some("C:/x.csv"));
        assert_eq!(safe_path("a/../../x"), None);
        assert_eq!(
            safe_path("a\u{0}b\n/c\u{1b}[31m.csv").as_deref(),
            Some("ab/c[31m.csv")
        );
        let long = format!("{}.csv", "x".repeat(10_000));
        let cleaned = safe_path(&long).unwrap_or_default();
        assert_eq!(cleaned.chars().count(), MAX_NAME_CHARS);
        assert!(cleaned.ends_with("x.csv") && cleaned.contains('…'));
        let mut entries = vec![
            ZipEntry {
                path: "a/x.csv".into(),
                bytes: Vec::new(),
            },
            ZipEntry {
                path: "a/x.csv".into(),
                bytes: Vec::new(),
            },
            ZipEntry {
                path: "a/x.csv".into(),
                bytes: Vec::new(),
            },
        ];
        unique_paths(&mut entries);
        let paths: Vec<&str> = entries.iter().map(|e| e.path.as_str()).collect();
        assert_eq!(paths, vec!["a/x.csv", "a/x (2).csv", "a/x (3).csv"]);
        assert!(is_noise("__MACOSX/x"));
        assert!(is_noise("pasta/.hidden"));
        assert!(!is_noise("pasta/extrato.pdf"));
    }
}
