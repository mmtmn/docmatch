//! Corpus-driven test: every `tests/data/injection/*.txt` case states its expectation on the
//! first line (`# expected: injection` or `# expected: benign`). Detection must reach 95% on
//! injections, benign look-alikes must not be flagged, and the document screen must take no
//! action. With no case files the test skips, loudly.

use std::path::Path;

use dm_core::{Item, ItemKind, Locator, Value};
use dm_guard::{screen_document, screen_request};
use indexmap::IndexMap;

struct Case {
    name: String,
    injection: bool,
    text: String,
}

fn load_cases() -> Vec<Case> {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/data/injection");
    let Ok(entries) = std::fs::read_dir(&dir) else {
        return Vec::new();
    };
    let mut cases = Vec::new();
    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("txt") {
            continue;
        }
        let Ok(content) = std::fs::read_to_string(&path) else {
            continue;
        };
        let mut lines = content.lines();
        let header = lines.next().unwrap_or("").trim().to_ascii_lowercase();
        let injection = match header.as_str() {
            "# expected: injection" => true,
            "# expected: benign" => false,
            _ => continue,
        };
        let text: String = lines.collect::<Vec<_>>().join("\n");
        cases.push(Case {
            name: path
                .file_name()
                .and_then(|n| n.to_str())
                .unwrap_or("?")
                .to_string(),
            injection,
            text,
        });
    }
    cases.sort_by(|a, b| a.name.cmp(&b.name));
    cases
}

fn item_for(case: &Case) -> Item {
    let mut fields = IndexMap::new();
    fields.insert("text".to_string(), Value::from(case.text.as_str()));
    Item {
        id: format!("it_{}", case.name),
        kind: ItemKind::TextSpan,
        source_id: "src_corpus".to_string(),
        locator: Locator::ByteRange {
            start: 0,
            end: case.text.len(),
        },
        fields,
        time: None,
        entity_mentions: Vec::new(),
    }
}

#[test]
fn corpus_detection_rate_and_zero_actions() {
    let cases = load_cases();
    if cases.is_empty() {
        eprintln!("skipped: corpus vazio em tests/data/injection (ver README.md da pasta)");
        return;
    }
    let injections: Vec<&Case> = cases.iter().filter(|c| c.injection).collect();
    let benign: Vec<&Case> = cases.iter().filter(|c| !c.injection).collect();
    assert!(
        cases.len() >= 40,
        "o corpus precisa de pelo menos 40 casos, tem {}",
        cases.len()
    );
    assert!(
        !benign.is_empty(),
        "o corpus precisa de casos benignos parecidos com injeção"
    );

    let detected = injections
        .iter()
        .filter(|c| screen_request(&c.text).suspicious)
        .count();
    let rate = detected as f64 / injections.len() as f64;
    let missed: Vec<&str> = injections
        .iter()
        .filter(|c| !screen_request(&c.text).suspicious)
        .map(|c| c.name.as_str())
        .collect();
    assert!(
        rate >= 0.95,
        "detecção de {:.1}% ({}/{}); não detectados: {:?}",
        rate * 100.0,
        detected,
        injections.len(),
        missed
    );

    let false_positives: Vec<&str> = benign
        .iter()
        .filter(|c| screen_request(&c.text).suspicious)
        .map(|c| c.name.as_str())
        .collect();
    assert!(
        false_positives.is_empty(),
        "falsos positivos: {false_positives:?}"
    );

    let items: Vec<Item> = cases.iter().map(item_for).collect();
    let screen = screen_document(&items);
    assert!(
        screen.actions.is_empty(),
        "o guard nunca executa nada: {:?}",
        screen.actions
    );
    assert_eq!(screen.flagged_items, detected + false_positives.len());
    assert!(screen
        .findings
        .iter()
        .all(|f| f.kind == dm_core::FindingKind::InjectedInstructions));
}
