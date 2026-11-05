//! Format sniffing: kind, encoding, delimiter, quoting, header row, number/date style and
//! header language, from the bytes alone.

use chardetng::{EncodingDetector, Iso2022JpDetection, Utf8Detection};
use dm_normalize::{sniff_decimal_style, DecimalStyle};
use encoding_rs::{Encoding, UTF_16BE, UTF_16LE, UTF_8, WINDOWS_1252};
use serde::{Deserialize, Serialize};

use crate::roles::{header_language_votes, HeaderVotes};

/// What the bytes look like.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SourceKind {
    /// Delimited text (CSV, TSV, `;`-separated exports).
    Csv,
    /// Excel / OpenDocument workbook.
    Spreadsheet,
    /// OFX bank statement.
    Ofx,
    /// NF-e / NFC-e / CT-e XML.
    NfeXml,
    /// Other XML.
    Xml,
    /// RFC 5322 e-mail.
    Eml,
    /// mbox mailbox.
    Mbox,
    /// Text dump of an inbox or a chat thread.
    TxtInbox,
    /// PDF document.
    Pdf,
    /// CNAB 240 remittance/return file.
    Cnab240,
    /// CNAB 400 file.
    Cnab400,
    /// SPED digital bookkeeping (EFD ICMS/IPI, EFD Contribuições, ECD): `|REG|...|` records.
    Sped,
    /// ZIP archive (not an Office/OpenDocument package).
    Zip,
    /// JSON document.
    Json,
    /// Plain text with no recognizable structure.
    Text,
    /// Binary or empty.
    Unknown,
}

impl SourceKind {
    /// pt-BR label for CLI and report output.
    pub fn label_ptbr(self) -> &'static str {
        match self {
            SourceKind::Csv => "texto delimitado (CSV)",
            SourceKind::Spreadsheet => "planilha",
            SourceKind::Ofx => "extrato OFX",
            SourceKind::NfeXml => "XML de NF-e",
            SourceKind::Xml => "XML",
            SourceKind::Eml => "e-mail (.eml)",
            SourceKind::Mbox => "caixa de correio (mbox)",
            SourceKind::TxtInbox => "dump de inbox ou conversa",
            SourceKind::Pdf => "PDF",
            SourceKind::Cnab240 => "CNAB 240",
            SourceKind::Cnab400 => "CNAB 400",
            SourceKind::Sped => "SPED (EFD/ECD)",
            SourceKind::Zip => "arquivo ZIP",
            SourceKind::Json => "JSON",
            SourceKind::Text => "texto simples",
            SourceKind::Unknown => "desconhecido",
        }
    }

    /// Stable snake_case id (the serde name), for JSON reports.
    pub fn id(self) -> &'static str {
        match self {
            SourceKind::Csv => "csv",
            SourceKind::Spreadsheet => "spreadsheet",
            SourceKind::Ofx => "ofx",
            SourceKind::NfeXml => "nfe_xml",
            SourceKind::Xml => "xml",
            SourceKind::Eml => "eml",
            SourceKind::Mbox => "mbox",
            SourceKind::TxtInbox => "txt_inbox",
            SourceKind::Pdf => "pdf",
            SourceKind::Cnab240 => "cnab240",
            SourceKind::Cnab400 => "cnab400",
            SourceKind::Sped => "sped",
            SourceKind::Zip => "zip",
            SourceKind::Json => "json",
            SourceKind::Text => "text",
            SourceKind::Unknown => "unknown",
        }
    }
}

/// Date style of the values.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum DateStyle {
    /// `dd/mm/aaaa`.
    Dmy,
    /// `aaaa-mm-dd`.
    Iso,
    /// Nothing found.
    #[default]
    Unknown,
}

/// Language of the header words.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum HeaderLanguage {
    /// Portuguese.
    Pt,
    /// English.
    En,
    /// Neither or no header.
    #[default]
    Unknown,
}

/// Everything sniffed from the bytes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Sniff {
    /// Kind.
    pub kind: SourceKind,
    /// Encoding name as `encoding_rs` reports it (`UTF-8`, `windows-1252`, `UTF-16LE`).
    pub encoding: String,
    /// True when a byte-order mark was present.
    pub bom: bool,
    /// Delimiter of delimited text.
    pub delimiter: Option<u8>,
    /// Quote character of delimited text.
    pub quote: Option<u8>,
    /// True when the first row is a header.
    pub has_header: bool,
    /// Decimal separator convention of the values.
    pub decimal_style: DecimalStyleTag,
    /// Date convention of the values.
    pub date_style: DateStyle,
    /// Language of the header words.
    pub header_language: HeaderLanguage,
    /// Line ending (`\n`, `\r\n`).
    pub crlf: bool,
    /// Number of lines sampled.
    pub sampled_lines: usize,
}

/// Serializable mirror of [`DecimalStyle`].
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum DecimalStyleTag {
    /// Undecided.
    #[default]
    Auto,
    /// `1.234,56`.
    Comma,
    /// `1,234.56`.
    Point,
}

impl From<DecimalStyle> for DecimalStyleTag {
    fn from(s: DecimalStyle) -> Self {
        match s {
            DecimalStyle::Auto => DecimalStyleTag::Auto,
            DecimalStyle::Comma => DecimalStyleTag::Comma,
            DecimalStyle::Point => DecimalStyleTag::Point,
        }
    }
}

impl From<DecimalStyleTag> for DecimalStyle {
    fn from(s: DecimalStyleTag) -> Self {
        match s {
            DecimalStyleTag::Auto => DecimalStyle::Auto,
            DecimalStyleTag::Comma => DecimalStyle::Comma,
            DecimalStyleTag::Point => DecimalStyle::Point,
        }
    }
}

const SAMPLE_BYTES: usize = 64 * 1024;
const SAMPLE_LINES: usize = 200;

/// Sniffs the bytes. Never fails: unknown input yields [`SourceKind::Unknown`].
pub fn sniff(bytes: &[u8]) -> Sniff {
    let mut out = Sniff {
        kind: SourceKind::Unknown,
        encoding: "UTF-8".to_string(),
        bom: false,
        delimiter: None,
        quote: None,
        has_header: false,
        decimal_style: DecimalStyleTag::Auto,
        date_style: DateStyle::Unknown,
        header_language: HeaderLanguage::Unknown,
        crlf: false,
        sampled_lines: 0,
    };
    if bytes.is_empty() {
        return out;
    }
    if let Some(kind) = binary_kind(bytes) {
        out.kind = kind;
        if kind == SourceKind::Sped {
            // Signed EFD files carry a binary signature after |9999|: sniff the records only.
            let records = crate::sped::record_bytes(bytes);
            out.encoding = detect_encoding(records).0.name().to_string();
            out.crlf = contains(&records[..records.len().min(SAMPLE_BYTES)], b"\r\n");
        }
        return out;
    }
    let (encoding, bom) = detect_encoding(bytes);
    out.encoding = encoding.name().to_string();
    out.bom = bom;
    let (text, _, _) = encoding.decode(&bytes[..bytes.len().min(SAMPLE_BYTES)]);
    let text = text.trim_start_matches('\u{feff}');
    out.crlf = text.contains("\r\n");
    // The sample window cuts the last line of a longer file: only whole lines are sampled,
    // or a 400-position CNAB of a few hundred records would fail the record-length test.
    let whole = if bytes.len() > SAMPLE_BYTES {
        text.rfind('\n').map_or(text, |i| &text[..i])
    } else {
        text
    };
    let lines: Vec<&str> = whole.lines().take(SAMPLE_LINES).collect();
    out.sampled_lines = lines.len();
    if lines.iter().all(|l| l.trim().is_empty()) {
        out.kind = SourceKind::Text;
        return out;
    }
    if let Some(kind) = text_kind(text, &lines) {
        out.kind = kind;
        if kind != SourceKind::Csv {
            return out;
        }
    }
    // Delimited text
    let (delimiter, quote) = detect_delimiter(&lines);
    if let Some(d) = delimiter {
        out.kind = SourceKind::Csv;
        out.delimiter = Some(d);
        out.quote = quote;
        let rows = split_rows(&lines, d, quote);
        out.has_header = looks_like_header(&rows);
        let values: Vec<&str> = rows
            .iter()
            .skip(usize::from(out.has_header))
            .flatten()
            .copied()
            .collect();
        out.decimal_style = sniff_decimal_style(&values).into();
        out.date_style = detect_date_style(&values);
        if out.has_header {
            let votes: HeaderVotes = header_language_votes(&rows[0]);
            out.header_language = match votes.pt.cmp(&votes.en) {
                std::cmp::Ordering::Greater => HeaderLanguage::Pt,
                std::cmp::Ordering::Less => HeaderLanguage::En,
                std::cmp::Ordering::Equal if votes.pt > 0 => HeaderLanguage::Pt,
                std::cmp::Ordering::Equal => HeaderLanguage::Unknown,
            };
        }
    } else {
        if out.kind == SourceKind::Unknown {
            out.kind = if looks_like_inbox(&lines) {
                SourceKind::TxtInbox
            } else {
                SourceKind::Text
            };
        }
        let values: Vec<&str> = lines.iter().flat_map(|l| l.split_whitespace()).collect();
        out.decimal_style = sniff_decimal_style(&values).into();
        out.date_style = detect_date_style(&values);
    }
    out
}

fn binary_kind(bytes: &[u8]) -> Option<SourceKind> {
    if bytes.starts_with(b"%PDF") {
        return Some(SourceKind::Pdf);
    }
    if bytes.starts_with(b"PK\x05\x06") {
        // End of central directory first: an empty archive.
        return Some(SourceKind::Zip);
    }
    if bytes.starts_with(b"PK\x03\x04") {
        return Some(zip_kind(bytes));
    }
    if bytes.starts_with(b"\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1") {
        return Some(SourceKind::Spreadsheet);
    }
    if crate::sped::looks_like_sped(bytes) {
        return Some(SourceKind::Sped);
    }
    // NUL bytes mean binary, except for UTF-16 text announced by its byte-order mark.
    let utf16 = bytes.starts_with(&[0xFF, 0xFE]) || bytes.starts_with(&[0xFE, 0xFF]);
    if !utf16 && bytes[..bytes.len().min(SAMPLE_BYTES)].contains(&0) {
        return Some(SourceKind::Unknown);
    }
    None
}

/// A ZIP is a workbook when its own central directory says so (`xl/workbook.xml`, an ODS
/// `mimetype`); scanning the bytes would also match an archive that merely *contains* a stored
/// workbook. Other Office packages (`.docx`, `.pptx`) are not archives of sources. A damaged
/// archive falls back to the local headers of its first entries.
fn zip_kind(bytes: &[u8]) -> SourceKind {
    let head = &bytes[..bytes.len().min(4096)];
    if contains(
        head,
        b"mimetypeapplication/vnd.oasis.opendocument.spreadsheet",
    ) {
        return SourceKind::Spreadsheet;
    }
    if let Ok(archive) = zip::ZipArchive::new(std::io::Cursor::new(bytes)) {
        let names: Vec<&[u8]> = archive.file_names_raw().collect();
        return if names
            .iter()
            .any(|n| *n == b"xl/workbook.xml" || *n == b"xl/workbook.bin")
        {
            SourceKind::Spreadsheet
        } else if names.contains(&&b"[Content_Types].xml"[..]) {
            SourceKind::Unknown
        } else {
            SourceKind::Zip
        };
    }
    let is_office = contains(head, b"[Content_Types].xml")
        || contains(head, b"xl/")
        || contains(head, b"_rels/");
    if is_office {
        SourceKind::Spreadsheet
    } else {
        SourceKind::Zip
    }
}

fn contains(haystack: &[u8], needle: &[u8]) -> bool {
    !needle.is_empty() && haystack.windows(needle.len()).any(|w| w == needle)
}

/// Encoding with BOM handling, UTF-8 validation and chardetng for the rest.
fn detect_encoding(bytes: &[u8]) -> (&'static Encoding, bool) {
    if bytes.starts_with(&[0xEF, 0xBB, 0xBF]) {
        return (UTF_8, true);
    }
    if bytes.starts_with(&[0xFF, 0xFE]) {
        return (UTF_16LE, true);
    }
    if bytes.starts_with(&[0xFE, 0xFF]) {
        return (UTF_16BE, true);
    }
    if std::str::from_utf8(bytes).is_ok() {
        return (UTF_8, false);
    }
    let mut detector = EncodingDetector::new(Iso2022JpDetection::Deny);
    detector.feed(
        &bytes[..bytes.len().min(SAMPLE_BYTES)],
        bytes.len() <= SAMPLE_BYTES,
    );
    let guess = detector.guess(Some(b"br"), Utf8Detection::Allow);
    // Every single-byte export from Brazil is windows-1252 in practice (a superset of
    // ISO-8859-1 for the printable range); keep the detector's answer for multi-byte guesses.
    if guess.is_single_byte() || guess == UTF_8 {
        (WINDOWS_1252, false)
    } else {
        (guess, false)
    }
}

/// Text formats recognized by their leading structure.
fn text_kind(text: &str, lines: &[&str]) -> Option<SourceKind> {
    let head: String = lines
        .iter()
        .take(20)
        .copied()
        .collect::<Vec<_>>()
        .join("\n");
    let head_upper = head.to_ascii_uppercase();
    let trimmed = text.trim_start();
    if head_upper.contains("OFXHEADER") || head_upper.contains("<OFX>") {
        return Some(SourceKind::Ofx);
    }
    if trimmed.starts_with('<') {
        // Cut on a character boundary: byte SAMPLE_BYTES can fall inside a multi-byte char
        // (and decoded replacement characters are three bytes long).
        let mut end = trimmed.len().min(SAMPLE_BYTES);
        while !trimmed.is_char_boundary(end) {
            end -= 1;
        }
        let upper = trimmed[..end].to_ascii_uppercase();
        if upper.contains("<NFEPROC")
            || upper.contains("<NFE")
            || upper.contains("<INFNFE")
            || upper.contains("<CTEPROC")
            || upper.contains("<CTE")
        {
            return Some(SourceKind::NfeXml);
        }
        return Some(SourceKind::Xml);
    }
    if trimmed.starts_with('{') {
        return Some(SourceKind::Json);
    }
    if let Some(after_bracket) = trimmed.strip_prefix('[') {
        // "[{" / "[\"" / "[]" are JSON; "[05/03 14:30]" is a chat line
        if matches!(
            after_bracket.trim_start().chars().next(),
            Some('{' | '"' | ']')
        ) {
            return Some(SourceKind::Json);
        }
    }
    if text.starts_with("From ") && lines.len() > 1 && is_mail_header_line(lines[1]) {
        return Some(SourceKind::Mbox);
    }
    let header_lines = lines
        .iter()
        .take(10)
        .filter(|l| is_mail_header_line(l))
        .count();
    if header_lines >= 3 && is_mail_header_line(lines[0]) {
        return Some(SourceKind::Eml);
    }
    if let Some(kind) = cnab_kind(lines) {
        return Some(kind);
    }
    if looks_like_inbox(lines) {
        return Some(SourceKind::TxtInbox);
    }
    None
}

fn is_mail_header_line(line: &str) -> bool {
    const HEADERS: [&str; 14] = [
        "From:",
        "To:",
        "Subject:",
        "Date:",
        "Received:",
        "Return-Path:",
        "Delivered-To:",
        "Message-ID:",
        "MIME-Version:",
        "Content-Type:",
        "Cc:",
        "Reply-To:",
        "X-",
        "DKIM-Signature:",
    ];
    HEADERS.iter().any(|h| line.starts_with(h))
}

fn cnab_kind(lines: &[&str]) -> Option<SourceKind> {
    let non_empty: Vec<&str> = lines
        .iter()
        .copied()
        .filter(|l| !l.trim().is_empty())
        .collect();
    if non_empty.len() < 2 {
        return None;
    }
    let all_240 = non_empty
        .iter()
        .all(|l| l.trim_end_matches('\r').chars().count() == 240);
    let all_400 = non_empty
        .iter()
        .all(|l| l.trim_end_matches('\r').chars().count() == 400);
    if all_240 {
        Some(SourceKind::Cnab240)
    } else if all_400 {
        Some(SourceKind::Cnab400)
    } else {
        None
    }
}

/// E-mail dumps and chat exports: block markers, `De:`/`From:` headers, `[dd/mm hh:mm]` lines.
pub(crate) fn looks_like_inbox(lines: &[&str]) -> bool {
    let mut evidence = 0usize;
    for line in lines.iter().take(SAMPLE_LINES) {
        let l = line.trim();
        if crate::inbox::is_block_marker(l) {
            evidence += 3;
        } else if crate::inbox::chat_line_prefix(l).is_some() {
            evidence += 2;
        } else if crate::inbox::header_field(l).is_some() {
            evidence += 1;
        }
    }
    evidence >= 3
}

/// Candidate delimiters and the one whose count is consistent across the sampled lines.
fn detect_delimiter(lines: &[&str]) -> (Option<u8>, Option<u8>) {
    let candidates = [b';', b',', b'\t', b'|'];
    let non_empty: Vec<&str> = lines
        .iter()
        .copied()
        .filter(|l| !l.trim().is_empty())
        .collect();
    if non_empty.is_empty() {
        return (None, None);
    }
    let quote = if non_empty.iter().any(|l| l.contains('"')) {
        Some(b'"')
    } else {
        None
    };
    let mut best: Option<(u8, usize, f64)> = None;
    for d in candidates {
        let counts: Vec<usize> = non_empty
            .iter()
            .map(|l| count_outside_quotes(l, d, quote))
            .collect();
        let with_delim = counts.iter().filter(|c| **c > 0).count();
        if with_delim == 0 {
            continue;
        }
        let mode = mode_of(&counts);
        if mode == 0 {
            continue;
        }
        let consistent = counts.iter().filter(|c| **c == mode).count() as f64 / counts.len() as f64;
        let coverage = with_delim as f64 / counts.len() as f64;
        let score = consistent * coverage;
        // ragged exports (a trailing "TOTAL" line, a note) still count as delimited
        if score < 0.4 {
            continue;
        }
        let better = match best {
            None => true,
            Some((_, _, s)) => {
                score > s + 0.05 || (score >= s - 0.05 && mode > best.map_or(0, |b| b.1))
            }
        };
        if better {
            best = Some((d, mode, score));
        }
    }
    match best {
        Some((d, _, _)) => (Some(d), quote),
        None => (None, None),
    }
}

fn count_outside_quotes(line: &str, delim: u8, quote: Option<u8>) -> usize {
    let mut inside = false;
    let mut n = 0;
    for b in line.bytes() {
        if Some(b) == quote {
            inside = !inside;
        } else if b == delim && !inside {
            n += 1;
        }
    }
    n
}

fn mode_of(counts: &[usize]) -> usize {
    let mut best = (0usize, 0usize);
    let mut sorted = counts.to_vec();
    sorted.sort_unstable();
    let mut i = 0;
    while i < sorted.len() {
        let v = sorted[i];
        let mut j = i;
        while j < sorted.len() && sorted[j] == v {
            j += 1;
        }
        if j - i > best.1 || (j - i == best.1 && v > best.0) {
            best = (v, j - i);
        }
        i = j;
    }
    best.0
}

fn split_rows<'a>(lines: &[&'a str], delim: u8, quote: Option<u8>) -> Vec<Vec<&'a str>> {
    lines
        .iter()
        .filter(|l| !l.trim().is_empty())
        .map(|l| split_line(l, delim, quote))
        .collect()
}

pub(crate) fn split_line(line: &str, delim: u8, quote: Option<u8>) -> Vec<&str> {
    let mut fields = Vec::new();
    let mut inside = false;
    let mut start = 0;
    for (i, b) in line.bytes().enumerate() {
        if Some(b) == quote {
            inside = !inside;
        } else if b == delim && !inside {
            fields.push(line[start..i].trim().trim_matches('"'));
            start = i + 1;
        }
    }
    fields.push(line[start..].trim().trim_matches('"'));
    fields
}

/// A header row is mostly non-numeric while the next rows carry numbers or dates.
fn looks_like_header(rows: &[Vec<&str>]) -> bool {
    let Some(first) = rows.first() else {
        return false;
    };
    let first_numeric = first.iter().filter(|f| is_numeric_like(f)).count();
    let first_text = first
        .iter()
        .filter(|f| !f.is_empty() && !is_numeric_like(f))
        .count();
    if first_text == 0 || first_numeric > first_text {
        return false;
    }
    if rows.len() == 1 {
        return header_language_votes(first).pt + header_language_votes(first).en > 0;
    }
    let body_numeric: usize = rows
        .iter()
        .skip(1)
        .take(20)
        .map(|r| r.iter().filter(|f| is_numeric_like(f)).count())
        .sum();
    body_numeric > 0 || header_language_votes(first).pt + header_language_votes(first).en > 0
}

pub(crate) fn is_numeric_like(field: &str) -> bool {
    let f = field.trim();
    if f.is_empty() {
        return false;
    }
    let digits = f.chars().filter(char::is_ascii_digit).count();
    let letters = f.chars().filter(|c| c.is_alphabetic()).count();
    digits > 0 && letters <= 2 && digits * 2 >= f.len()
}

fn detect_date_style(values: &[&str]) -> DateStyle {
    let mut dmy = 0;
    let mut iso = 0;
    for v in values {
        let v = v.trim();
        let b = v.as_bytes();
        if b.len() >= 10 && b[4] == b'-' && b[7] == b'-' && b[..4].iter().all(u8::is_ascii_digit) {
            iso += 1;
        } else if b.len() >= 8
            && b.len() <= 19
            && (b[2] == b'/' || b[2] == b'-' || b[2] == b'.')
            && b[..2].iter().all(u8::is_ascii_digit)
            && b.get(5).is_some_and(|c| *c == b[2])
        {
            dmy += 1;
        }
    }
    match (dmy, iso) {
        (0, 0) => DateStyle::Unknown,
        (d, i) if d >= i => DateStyle::Dmy,
        _ => DateStyle::Iso,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_and_binary() {
        assert_eq!(sniff(b"").kind, SourceKind::Unknown);
        assert_eq!(sniff(b"%PDF-1.7\n...").kind, SourceKind::Pdf);
        assert_eq!(sniff(b"PK\x03\x04garbage").kind, SourceKind::Zip);
        assert_eq!(sniff(b"PK\x05\x06\x00\x00").kind, SourceKind::Zip);
        // A CNAB 400 longer than the sample window: the cut last line is not sampled.
        let mut long = String::new();
        while long.len() <= SAMPLE_BYTES + 1000 {
            long.push_str(&"1".repeat(400));
            long.push_str("\r\n");
        }
        assert_eq!(sniff(long.as_bytes()).kind, SourceKind::Cnab400);
        assert_eq!(
            sniff(b"PK\x03\x04\x00\x00[Content_Types].xml").kind,
            SourceKind::Spreadsheet
        );
        assert_eq!(
            sniff(b"\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1rest").kind,
            SourceKind::Spreadsheet
        );
    }

    #[test]
    fn zip_kinds_come_from_the_central_directory() {
        use std::io::Write;
        let build = |entries: &[(&str, zip::CompressionMethod)]| {
            let mut w = zip::ZipWriter::new(std::io::Cursor::new(Vec::new()));
            for (name, method) in entries {
                let opts = zip::write::SimpleFileOptions::default().compression_method(*method);
                w.start_file(*name, opts).unwrap();
                w.write_all(b"<x/>").unwrap();
            }
            w.finish().unwrap().into_inner()
        };
        let stored = zip::CompressionMethod::Stored;
        let workbook = build(&[("[Content_Types].xml", stored), ("xl/workbook.xml", stored)]);
        assert_eq!(sniff(&workbook).kind, SourceKind::Spreadsheet);
        // An archive that stores a workbook (with its xl/workbook.xml bytes in the clear) is
        // still an archive.
        let mut outer_entries = vec![("leia-me.txt", stored)];
        outer_entries.push(("planilha.xlsx", stored));
        let mut w = zip::ZipWriter::new(std::io::Cursor::new(Vec::new()));
        for (name, method) in &outer_entries {
            let opts = zip::write::SimpleFileOptions::default().compression_method(*method);
            w.start_file(*name, opts).unwrap();
            w.write_all(if *name == "planilha.xlsx" {
                &workbook
            } else {
                b"oi"
            })
            .unwrap();
        }
        let outer = w.finish().unwrap().into_inner();
        assert_eq!(sniff(&outer).kind, SourceKind::Zip);
        let docx = build(&[
            ("[Content_Types].xml", stored),
            ("word/document.xml", stored),
        ]);
        assert_eq!(sniff(&docx).kind, SourceKind::Unknown);
    }

    #[test]
    fn semicolon_csv_with_pt_headers() {
        let csv = "Data de Vencimento;Fornecedor;Valor;Status\r\n05/03/2025;Empório Bela Vista;1.234,56;Aberto\r\n10/03/2025;Padaria Central;500,00;Pago\r\n";
        let s = sniff(csv.as_bytes());
        assert_eq!(s.kind, SourceKind::Csv);
        assert_eq!(s.delimiter, Some(b';'));
        assert!(s.has_header);
        assert_eq!(s.encoding, "UTF-8");
        assert!(s.crlf);
        assert_eq!(s.decimal_style, DecimalStyleTag::Comma);
        assert_eq!(s.date_style, DateStyle::Dmy);
        assert_eq!(s.header_language, HeaderLanguage::Pt);
    }

    #[test]
    fn comma_csv_with_en_headers_and_quotes() {
        let csv = "due_date,vendor,amount,paid_flag\n2025-03-05,\"Emporio, Bela Vista\",1234.56,N\n2025-03-10,Padaria Central,500.00,Y\n";
        let s = sniff(csv.as_bytes());
        assert_eq!(s.kind, SourceKind::Csv);
        assert_eq!(s.delimiter, Some(b','));
        assert_eq!(s.quote, Some(b'"'));
        assert!(s.has_header);
        assert_eq!(s.decimal_style, DecimalStyleTag::Point);
        assert_eq!(s.date_style, DateStyle::Iso);
        assert_eq!(s.header_language, HeaderLanguage::En);
    }

    #[test]
    fn tab_and_pipe() {
        assert_eq!(sniff(b"a\tb\tc\n1\t2\t3\n").delimiter, Some(b'\t'));
        assert_eq!(sniff(b"a|b|c\n1|2|3\n").delimiter, Some(b'|'));
    }

    #[test]
    fn windows_1252_is_detected() {
        let latin: Vec<u8> = "Emissão;Valor\n05/03/2025;1,00\n"
            .chars()
            .map(|c| if c == 'ã' { 0xE3 } else { c as u8 })
            .collect();
        let s = sniff(&latin);
        assert_eq!(s.encoding, "windows-1252");
        assert_eq!(s.kind, SourceKind::Csv);
        let bom = [&[0xEF, 0xBB, 0xBF][..], b"a;b\n1;2\n"].concat();
        let s = sniff(&bom);
        assert!(s.bom);
        assert_eq!(s.encoding, "UTF-8");
    }

    #[test]
    fn structured_text_kinds() {
        assert_eq!(
            sniff(b"OFXHEADER:100\nDATA:OFXSGML\n<OFX><BANKMSGSRSV1>").kind,
            SourceKind::Ofx
        );
        assert_eq!(
            sniff(b"<?xml version=\"1.0\"?><nfeProc><NFe><infNFe Id=\"NFe1\"/></NFe></nfeProc>")
                .kind,
            SourceKind::NfeXml
        );
        assert_eq!(
            sniff(b"<?xml version=\"1.0\"?><root/>").kind,
            SourceKind::Xml
        );
        assert_eq!(sniff(b"{\"a\": 1}").kind, SourceKind::Json);
        assert_eq!(sniff(b"From: a@example.com\nTo: b@example.com\nSubject: oi\nDate: Mon, 3 Mar 2025 10:00:00 -0300\n\ncorpo\n").kind, SourceKind::Eml);
        assert_eq!(sniff(b"From a@example.com Mon Mar  3 10:00:00 2025\nFrom: a@example.com\nSubject: oi\n\ncorpo\n").kind, SourceKind::Mbox);
        let chat = b"[05/03 14:30] Compras - Rafael: NF 088231 vence amanha\n[05/03 14:31] Financeiro - Ana: ok\n";
        assert_eq!(sniff(chat).kind, SourceKind::TxtInbox);
        let dump = b"--- E-MAIL 1 ---\nDe: compras@example.com\nPara: financeiro@example.com\nAssunto: NF 88231\n\nSegue a nota.\n";
        assert_eq!(sniff(dump).kind, SourceKind::TxtInbox);
        let cnab = format!("{}\n{}\n", "0".repeat(240), "1".repeat(240));
        assert_eq!(sniff(cnab.as_bytes()).kind, SourceKind::Cnab240);
        let cnab = format!("{}\r\n{}\r\n", "0".repeat(400), "1".repeat(400));
        assert_eq!(sniff(cnab.as_bytes()).kind, SourceKind::Cnab400);
        let sped = b"|0000|017|0|01072026|31072026|EMPRESA SINTETICA LTDA|11222333000181||SP|123||3550308||A|1|\r\n|0001|0|\r\n|C100|1|0|P1|55|00|1|123|||05072026|05072026|100,00|0|||100,00|9|||||||||||\r\n";
        assert_eq!(sniff(sped).kind, SourceKind::Sped);
        assert_eq!(
            sniff(b"|a|b|\n|1|2|\n").kind,
            SourceKind::Csv,
            "pipe-delimited text without |0000| stays CSV"
        );
        assert_eq!(
            sniff(b"apenas um texto qualquer sem estrutura\nsegunda linha\n").kind,
            SourceKind::Text
        );
        assert_eq!(sniff(b"   \n\n").kind, SourceKind::Text);
    }

    #[test]
    fn header_detection() {
        assert!(sniff(b"valor;data\n1,00;05/03/2025\n").has_header);
        assert!(!sniff(b"1,00;05/03/2025\n2,00;06/03/2025\n").has_header);
        assert!(sniff(b"Fornecedor;Valor\n").has_header);
    }

    #[test]
    fn markup_sample_cut_respects_char_boundaries() {
        // An 80 KB UTF-8 document starting with '<' and full of multi-byte characters: the
        // sample cut at SAMPLE_BYTES used to land inside a character and panic.
        let mut text = String::from("<");
        while text.len() < 80 * 1024 {
            text.push('ã');
        }
        let _ = sniff(text.as_bytes());
        let mut odd = String::from("< ");
        while odd.len() < 80 * 1024 {
            odd.push('€');
        }
        let _ = sniff(odd.as_bytes());
    }
}
