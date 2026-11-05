//! Worst cases of the document parsers stay within their memory and time bounds.
//!
//! The harness runs inside a 512 MB limit, so each parser must stop at its documented caps
//! instead of growing with what an adversarial file asks for. Every case runs under a global
//! allocator that caps what may be allocated on top of what was already allocated (the
//! input): a parse that needs more fails its allocation and the test process aborts, the last
//! case printed naming the culprit. The output is checked against the caps too, and in a
//! release build each case must also finish within [`MAX_TIME`].
//!
//! One test function runs the cases one after another: the cap is process-wide.

use std::alloc::System;
use std::io::{Cursor, Write};
use std::time::{Duration, Instant};

use cap::Cap;
use dm_core::Table;

#[global_allocator]
static ALLOC: Cap<System> = Cap::new(System, usize::MAX);

/// Bytes a case may allocate on top of its input, at its peak.
const BUDGET: usize = 160 << 20;
/// Time a case may take in a release build.
const MAX_TIME: Duration = Duration::from_secs(10);

/// Runs `f` with at most [`BUDGET`] more bytes allocated at any moment.
fn within<R>(label: &str, f: impl FnOnce() -> R) -> R {
    eprintln!("caso: {label}");
    let base = ALLOC.allocated();
    ALLOC
        .set_limit(base + BUDGET)
        .unwrap_or_else(|()| panic!("limite abaixo do já alocado"));
    let started = Instant::now();
    let out = f();
    let elapsed = started.elapsed();
    ALLOC
        .set_limit(usize::MAX)
        .unwrap_or_else(|()| panic!("limite"));
    eprintln!(
        "caso: {label}: {elapsed:?}, pico do processo {} MB",
        ALLOC.max_allocated() >> 20
    );
    if !cfg!(debug_assertions) {
        assert!(elapsed < MAX_TIME, "{label}: {elapsed:?}");
    }
    out
}

fn cells(tables: &[Table]) -> usize {
    tables.iter().map(|t| t.len() * t.columns.len()).sum()
}

fn text_bytes(tables: &[Table]) -> usize {
    tables
        .iter()
        .flat_map(|t| t.rows.iter())
        .flat_map(|r| r.values.iter())
        .map(|v| v.render().len())
        .sum()
}

fn zip_of(entries: &[(&str, &[u8])]) -> Vec<u8> {
    let mut w = zip::ZipWriter::new(Cursor::new(Vec::new()));
    let opts = zip::write::SimpleFileOptions::default()
        .compression_method(zip::CompressionMethod::Deflated);
    for (path, bytes) in entries {
        w.start_file(*path, opts).unwrap_or_else(|e| panic!("{e}"));
        w.write_all(bytes).unwrap_or_else(|e| panic!("{e}"));
    }
    w.finish().unwrap_or_else(|e| panic!("{e}")).into_inner()
}

fn html_cases() {
    // Two million void elements: the node cap.
    let page = "<br>".repeat(2 << 20);
    let h = within("html: 2 milhões de <br>", || {
        dm_ingest::parse_html(page.as_bytes(), "p", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(h.warnings.iter().any(|w| w.contains("elementos demais")));
    drop(page);
    // Four hundred formatting elements re-opened before every word.
    let mut page = String::from("<p>");
    for k in 0..400 {
        page.push_str(&format!("<b id={k}>"));
    }
    page.push_str(&"</p><p>x".repeat(900_000));
    let h = within("html: formatação reaberta", || {
        dm_ingest::parse_html(page.as_bytes(), "p", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(!h.warnings.is_empty());
    drop(page);
    // Unmatched end tags between a million spans.
    let page = "<span></i>".repeat(800_000);
    let h = within("html: fins sem par", || {
        dm_ingest::parse_html(page.as_bytes(), "p", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(h.warnings.iter().any(|w| w.contains("aninhada demais")));
    drop(page);
    // A table of 130 thousand rows, cells spanning a thousand rows each.
    let page = format!(
        "<table><tr><th>Data</th><th>Histórico</th><th>Valor</th></tr>{}</table>",
        "<tr><td rowspan=1000>10/07/2026</td><td rowspan=1000>PIX RECEBIDO DE CLIENTE</td><td>1,00</td></tr>".repeat(80_000)
    );
    let h = within("html: tabela com rowspan", || {
        dm_ingest::parse_html(page.as_bytes(), "p", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(cells(&h.tables) <= 500_000);
    assert!(text_bytes(&h.tables) <= 8 << 20);
    assert!(h.text.len() <= 8 << 20);
}

fn odt_case() {
    // One 1 KB cell repeated over 100 000 columns in a row repeated 100 000 times.
    let text = "x".repeat(1024);
    let body = format!(
        "<table:table><table:table-row><table:table-cell><text:p>Cliente</text:p></table:table-cell><table:table-cell><text:p>Valor</text:p></table:table-cell><table:table-cell><text:p>Obs</text:p></table:table-cell></table:table-row><table:table-row table:number-rows-repeated=\"100000\"><table:table-cell table:number-columns-repeated=\"100000\"><text:p>{text}</text:p></table:table-cell></table:table-row></table:table>"
    );
    let content = format!(
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?><office:document-content xmlns:office=\"urn:oasis:names:tc:opendocument:xmlns:office:1.0\" xmlns:text=\"urn:oasis:names:tc:opendocument:xmlns:text:1.0\" xmlns:table=\"urn:oasis:names:tc:opendocument:xmlns:table:1.0\"><office:body><office:text>{}</office:text></office:body></office:document-content>",
        body.repeat(8)
    );
    let odt = zip_of(&[
        ("mimetype", b"application/vnd.oasis.opendocument.text"),
        ("content.xml", content.as_bytes()),
    ]);
    let d = within("odt: linhas e células repetidas", || {
        dm_ingest::parse_odt(&odt, "o", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(cells(&d.tables) <= 200_000);
    assert!(text_bytes(&d.tables) <= 8 << 20);
    let parts: usize = d.parts.iter().map(|p| p.text.len()).sum();
    assert!(parts <= 8 << 20 && d.text.len() <= 8 << 20);
}

fn json_cases() {
    // Small on disk, large in memory.
    let doc = format!("[{}{{}}]", "{},".repeat((8 << 20) / 3 - 10));
    let r = within("json: objetos vazios", || {
        dm_ingest::parse_json(doc.as_bytes(), "j", "s")
    });
    assert!(r.is_err());
    drop(doc);
    let doc = format!(
        "[{}{{\"a\":1}}]",
        "{\"data\":\"10/07/2026\",\"valor\":1.5,\"h\":\"PIX\"},".repeat(170_000)
    );
    let p = within("json: registros", || {
        dm_ingest::parse_json(doc.as_bytes(), "j", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(cells(&p.tables) <= 2_000_000);
    drop(doc);
    // 60 MB of JSON Lines whose records keep bringing new keys.
    let mut lines = String::new();
    let mut i = 0usize;
    while lines.len() < (60 << 20) {
        lines.push_str(&format!("{{\"k{}\":1,\"n\":{i},\"t\":\"PIX\"}}\n", i % 256));
        i += 1;
    }
    let p = within("jsonl: chaves novas", || {
        dm_ingest::parse_jsonl(lines.as_bytes(), "j", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(cells(&p.tables) <= 2_000_000);
    assert!(p.warnings.iter().any(|w| w.contains("células por arquivo")));
}

fn xml_case() {
    let mut xml = String::from("<r>");
    while xml.len() < (60 << 20) {
        xml.push_str("<i a=\"1\" b=\"2\" c=\"3\" d=\"4\"/>");
    }
    xml.push_str("</r>");
    let p = within("xml: atributos", || {
        dm_ingest::parse_xml(xml.as_bytes(), "x", "s")
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(cells(&p.tables) <= 2_000_000);
    assert!(p.warnings.iter().any(|w| w.contains("atributos")));
}

fn msg_case() {
    let mut f =
        cfb::CompoundFile::create(Cursor::new(Vec::new())).unwrap_or_else(|e| panic!("{e}"));
    let utf16 = |s: &str| -> Vec<u8> { s.encode_utf16().flat_map(u16::to_le_bytes).collect() };
    for (path, data) in [
        ("/__substg1.0_0037001F", utf16(&"S".repeat(200_000))),
        (
            "/__substg1.0_1000001F",
            utf16(&"linha do corpo\r\n".repeat(1_500_000)),
        ),
        ("/__properties_version1.0", vec![0u8; 32]),
    ] {
        let mut s = f.create_stream(path).unwrap_or_else(|e| panic!("{e}"));
        s.write_all(&data).unwrap_or_else(|e| panic!("{e}"));
    }
    f.flush().unwrap_or_else(|e| panic!("{e}"));
    let bytes = f.into_inner().into_inner();
    let p = within("msg: corpo e assunto enormes", || {
        dm_ingest::parse_msg(&bytes, true, &dm_ingest::archive::ZipLimits::default())
    })
    .unwrap_or_else(|e| panic!("{e}"));
    assert!(p.message.subject.chars().count() <= 4096);
    assert!(p.message.body.len() <= 8 << 20);
}

#[test]
fn worst_cases_stay_within_their_bounds() {
    html_cases();
    odt_case();
    json_cases();
    xml_case();
    msg_case();
}
