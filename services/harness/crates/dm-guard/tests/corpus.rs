//! Corpus-driven gate for the injection detector. Every `tests/data/injection/*.txt` case
//! states its expectation on the first line (`# expected: injection` or `# expected: benign`).
//!
//! The gate fails hard when the corpus is missing or smaller than the brief requires, when
//! detection on injections is below 95%, when any benign look-alike is flagged, when a
//! category of the detector has no detected case, or when the document screen reports any
//! action. A missing corpus is a failure, never a skip.

use std::path::{Path, PathBuf};

use dm_core::{FindingKind, Item, ItemKind, Locator, Money, Value};
use dm_guard::{screen_document, screen_request, Category};
use indexmap::IndexMap;

/// Minimum number of cases (injections plus benign look-alikes).
const MIN_CASES: usize = 40;
/// Minimum number of injection cases.
const MIN_INJECTIONS: usize = 30;
/// Minimum number of benign look-alikes.
const MIN_BENIGN: usize = 10;
/// Minimum detection rate on injection cases.
const MIN_DETECTION: f64 = 0.95;

/// Every category the detector claims; each needs at least one detected case.
const ALL_CATEGORIES: [Category; 7] = [
    Category::InstructionOverride,
    Category::SystemPromptDisclosure,
    Category::RolePlay,
    Category::Exfiltration,
    Category::ToolCallInjection,
    Category::OutputManipulation,
    Category::HiddenMarkup,
];

struct Case {
    name: String,
    injection: bool,
    text: String,
}

fn corpus_dir() -> PathBuf {
    Path::new(
        &std::env::var("CARGO_MANIFEST_DIR")
            .expect("cargo define CARGO_MANIFEST_DIR ao rodar os testes"),
    )
    .join("tests/data/injection")
}

fn load_cases() -> Vec<Case> {
    let dir = corpus_dir();
    let entries = std::fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("corpus ausente em {}: {e}", dir.display()));
    let mut cases = Vec::new();
    for entry in entries {
        let path = entry
            .unwrap_or_else(|e| panic!("listando {}: {e}", dir.display()))
            .path();
        if path.extension().and_then(|e| e.to_str()) != Some("txt") {
            continue;
        }
        let content = std::fs::read_to_string(&path)
            .unwrap_or_else(|e| panic!("lendo {}: {e}", path.display()));
        let mut lines = content.lines();
        let header = lines.next().unwrap_or("").trim().to_ascii_lowercase();
        let injection = match header.as_str() {
            "# expected: injection" => true,
            "# expected: benign" => false,
            other => panic!(
                "{}: a primeira linha deve ser `# expected: injection` ou `# expected: benign`, é {other:?}",
                path.display()
            ),
        };
        let text: String = lines.collect::<Vec<_>>().join("\n");
        assert!(
            !text.trim().is_empty(),
            "{}: caso sem texto",
            path.display()
        );
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
fn corpus_is_present_and_large_enough() {
    let cases = load_cases();
    assert!(
        !cases.is_empty(),
        "corpus vazio em {}: o portão de detecção não pode rodar",
        corpus_dir().display()
    );
    let injections = cases.iter().filter(|c| c.injection).count();
    let benign = cases.len() - injections;
    assert!(
        cases.len() >= MIN_CASES,
        "o corpus precisa de pelo menos {MIN_CASES} casos, tem {}",
        cases.len()
    );
    assert!(
        injections >= MIN_INJECTIONS,
        "o corpus precisa de pelo menos {MIN_INJECTIONS} injeções, tem {injections}"
    );
    assert!(
        benign >= MIN_BENIGN,
        "o corpus precisa de pelo menos {MIN_BENIGN} casos benignos parecidos com injeção, tem {benign}"
    );
}

#[test]
fn corpus_detection_rate_and_zero_actions() {
    let cases = load_cases();
    assert!(
        cases.len() >= MIN_CASES,
        "corpus incompleto ({} casos); veja corpus_is_present_and_large_enough",
        cases.len()
    );
    let injections: Vec<&Case> = cases.iter().filter(|c| c.injection).collect();
    let benign: Vec<&Case> = cases.iter().filter(|c| !c.injection).collect();

    // Detection on injections, and which categories the detected cases exercise.
    let mut detected = 0usize;
    let mut missed: Vec<&str> = Vec::new();
    let mut categories_seen: Vec<Category> = Vec::new();
    for case in &injections {
        let verdict = screen_request(&case.text);
        if verdict.suspicious {
            detected += 1;
            for category in verdict.categories() {
                if !categories_seen.contains(&category) {
                    categories_seen.push(category);
                }
            }
        } else {
            missed.push(case.name.as_str());
        }
    }
    let rate = detected as f64 / injections.len() as f64;
    eprintln!(
        "corpus: {} injeções, {} benignos; detecção {:.1}% ({detected}/{}); não detectados: {missed:?}",
        injections.len(),
        benign.len(),
        rate * 100.0,
        injections.len()
    );
    assert!(
        rate >= MIN_DETECTION,
        "detecção de {:.1}% abaixo de {:.0}%; não detectados: {missed:?}",
        rate * 100.0,
        MIN_DETECTION * 100.0
    );
    let categories_missing: Vec<&str> = ALL_CATEGORIES
        .iter()
        .filter(|c| !categories_seen.contains(c))
        .map(|c| c.label_ptbr())
        .collect();
    assert!(
        categories_missing.is_empty(),
        "categorias do detector sem caso detectado no corpus: {categories_missing:?}"
    );

    // Benign look-alikes never trip the detector.
    let false_positives: Vec<String> = benign
        .iter()
        .map(|c| (c, screen_request(&c.text)))
        .filter(|(_, v)| v.suspicious)
        .map(|(c, v)| {
            format!(
                "{} (score {:.1}: {})",
                c.name,
                v.score,
                v.hits
                    .iter()
                    .map(|h| h.id.as_str())
                    .collect::<Vec<_>>()
                    .join(", ")
            )
        })
        .collect();
    assert!(
        false_positives.is_empty(),
        "falsos positivos: {false_positives:?}"
    );

    // The document screen reports, it never acts: one finding per flagged item, no actions.
    let items: Vec<Item> = cases.iter().map(item_for).collect();
    let screen = screen_document(&items);
    assert!(
        screen.actions.is_empty(),
        "o guard nunca executa nada: {:?}",
        screen.actions
    );
    assert_eq!(screen.screened_items, cases.len());
    assert_eq!(screen.flagged_items, detected + false_positives.len());
    assert_eq!(screen.findings.len(), screen.flagged_items);
    for finding in &screen.findings {
        assert_eq!(finding.kind, FindingKind::InjectedInstructions);
        assert_eq!(finding.amount_at_risk, Money::ZERO);
        assert!(
            finding
                .explanation
                .contains("Nenhuma instrução vinda de documento é executada"),
            "{}",
            finding.explanation
        );
    }
}
