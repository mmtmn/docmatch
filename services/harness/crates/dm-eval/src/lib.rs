//! Avaliação de casos dourados: compara o esperado (`expected.yaml`) com o obtido e imprime um
//! diff em pt-BR. O caso Vantara real é lido de `DM_VANTARA_PATH`; sem ele, o gêmeo sintético
//! público em `testdata/golden/vantara-twin` (fase 1 gera o gêmeo).
#![forbid(unsafe_code)]

use std::path::{Path, PathBuf};

use dm_core::Money;
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Errors of the evaluator.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum EvalError {
    /// File missing or unreadable.
    #[error("não foi possível ler {0}: {1}")]
    Read(String, String),
    /// YAML does not parse.
    #[error("expected.yaml inválido: {0}")]
    Parse(String),
    /// No file could be located.
    #[error("nenhum expected.yaml encontrado: defina DM_VANTARA_PATH ou gere testdata/golden/vantara-twin")]
    NotFound,
}

/// A bank line and what should explain it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExpectedBankLine {
    /// Line id.
    pub id: String,
    /// Ids of the titles (or tags such as `tarifa`) that explain it; empty means unexplained.
    #[serde(default)]
    pub explained_by: Vec<String>,
    /// Amount, when stated.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub amount: Option<Money>,
}

/// A title and its expected status.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExpectedTitle {
    /// Title id.
    pub id: String,
    /// `paid`, `open`, `partial`, `cancelled`.
    pub status: String,
    /// Amount paid, when stated.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub paid_amount: Option<Money>,
}

/// A finding that must be raised.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExpectedFinding {
    /// Finding code.
    pub code: String,
    /// Kind, when stated.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,
    /// Amount at risk, when stated.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub amount_at_risk: Option<Money>,
    /// Ids of the items the finding must cite.
    #[serde(default)]
    pub evidence_ids: Vec<String>,
}

/// The whole expectation (and, with the same shape, what a run produced).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct Expected {
    /// Bank lines.
    #[serde(default)]
    pub bank_lines: Vec<ExpectedBankLine>,
    /// Titles.
    #[serde(default)]
    pub titles: Vec<ExpectedTitle>,
    /// Findings.
    #[serde(default)]
    pub findings: Vec<ExpectedFinding>,
}

/// What a run produced, in the same shape as [`Expected`].
pub type Actual = Expected;

/// Score of one section.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct SectionScore {
    /// Expected entries.
    pub expected: usize,
    /// Entries matched exactly.
    pub matched: usize,
    /// Expected keys that are missing or differ.
    pub missing: Vec<String>,
    /// Actual keys nobody expected.
    pub extra: Vec<String>,
}

impl SectionScore {
    /// True when every expected entry matched and nothing extra appeared.
    pub fn perfect(&self) -> bool {
        self.matched == self.expected && self.extra.is_empty()
    }
}

/// The whole scorecard.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Scorecard {
    /// Bank lines.
    pub bank_lines: SectionScore,
    /// Titles.
    pub titles: SectionScore,
    /// Findings.
    pub findings: SectionScore,
    /// All sections perfect.
    pub passed: bool,
    /// Human-readable diff in pt-BR (empty when passed).
    pub diff_ptbr: String,
}

fn compare<T, K: Ord + Clone + std::fmt::Display, F: Fn(&T) -> K>(
    expected: &[T],
    actual: &[T],
    key: F,
) -> SectionScore
where
    T: PartialEq,
{
    let mut score = SectionScore {
        expected: expected.len(),
        ..SectionScore::default()
    };
    for e in expected {
        let k = key(e);
        match actual.iter().find(|a| key(a) == k) {
            Some(a) if a == e => score.matched += 1,
            Some(_) => score.missing.push(format!("{k} (difere)")),
            None => score.missing.push(k.to_string()),
        }
    }
    for a in actual {
        let k = key(a);
        if !expected.iter().any(|e| key(e) == k) {
            score.extra.push(k.to_string());
        }
    }
    score
}

/// Compares expected and actual and builds the pt-BR diff.
pub fn score(expected: &Expected, actual: &Actual) -> Scorecard {
    let bank_lines = compare(&expected.bank_lines, &actual.bank_lines, |b| b.id.clone());
    let titles = compare(&expected.titles, &actual.titles, |t| t.id.clone());
    let findings = compare(&expected.findings, &actual.findings, |f| {
        let mut ids = f.evidence_ids.clone();
        ids.sort();
        format!("{}[{}]", f.code, ids.join(","))
    });
    let passed = bank_lines.perfect() && titles.perfect() && findings.perfect();
    let mut diff = String::new();
    for (name, s) in [
        ("linhas bancárias", &bank_lines),
        ("títulos", &titles),
        ("achados", &findings),
    ] {
        diff.push_str(&format!("{name}: {}/{} corretos", s.matched, s.expected));
        if !s.extra.is_empty() {
            diff.push_str(&format!(", {} a mais", s.extra.len()));
        }
        diff.push('\n');
        for m in &s.missing {
            diff.push_str(&format!("  faltou: {m}\n"));
        }
        for x in &s.extra {
            diff.push_str(&format!("  sobrou: {x}\n"));
        }
    }
    Scorecard {
        bank_lines,
        titles,
        findings,
        passed,
        diff_ptbr: if passed { String::new() } else { diff },
    }
}

/// Where an expectation came from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LoadedExpected {
    /// The expectation.
    pub expected: Expected,
    /// File it was read from.
    pub path: PathBuf,
    /// True when it came from `DM_VANTARA_PATH` (private data: never print its values).
    pub private: bool,
}

/// Parses an `expected.yaml`.
pub fn load_expected_from(path: &Path) -> Result<Expected, EvalError> {
    let text = std::fs::read_to_string(path)
        .map_err(|e| EvalError::Read(path.display().to_string(), e.to_string()))?;
    serde_saphyr::from_str(&text).map_err(|e| EvalError::Parse(e.to_string()))
}

/// Locates the twin directory by walking up from `start` to `testdata/golden/vantara-twin`.
pub fn find_twin_dir(start: &Path) -> Option<PathBuf> {
    let mut dir = Some(start);
    while let Some(d) = dir {
        let candidate = d.join("testdata").join("golden").join("vantara-twin");
        if candidate.is_dir() {
            return Some(candidate);
        }
        dir = d.parent();
    }
    None
}

/// Loads `expected.yaml` from `DM_VANTARA_PATH` when set, else from the public twin.
pub fn load_expected() -> Result<LoadedExpected, EvalError> {
    if let Some(private_dir) = std::env::var("DM_VANTARA_PATH")
        .ok()
        .filter(|v| !v.trim().is_empty())
    {
        let path = Path::new(&private_dir).join("expected.yaml");
        return Ok(LoadedExpected {
            expected: load_expected_from(&path)?,
            path,
            private: true,
        });
    }
    let start = std::env::current_dir().map_err(|e| EvalError::Read(".".into(), e.to_string()))?;
    let dir = find_twin_dir(&start).ok_or(EvalError::NotFound)?;
    let path = dir.join("expected.yaml");
    Ok(LoadedExpected {
        expected: load_expected_from(&path)?,
        path,
        private: false,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    const YAML: &str = "bank_lines:\n  - id: b1\n    explained_by: [t1]\n    amount: -184500\n  - id: b2\n    explained_by: [tarifa]\ntitles:\n  - id: t1\n    status: paid\n    paid_amount: \"1.845,00\"\n  - id: t2\n    status: open\nfindings:\n  - code: TITLE_WITHOUT_PAYMENT\n    evidence_ids: [t2]\n    amount_at_risk: 50000\n";

    #[test]
    fn parses_yaml_with_money_in_both_forms() {
        let e: Expected = serde_saphyr::from_str(YAML).unwrap();
        assert_eq!(e.bank_lines[0].amount, Some(Money::from_centavos(-184_500)));
        assert_eq!(e.titles[0].paid_amount, Some(Money::from_centavos(184_500)));
        assert_eq!(
            e.findings[0].amount_at_risk,
            Some(Money::from_centavos(50_000))
        );
        assert_eq!(e.bank_lines[1].explained_by, vec!["tarifa"]);
    }

    #[test]
    fn scores_and_diffs() {
        let expected: Expected = serde_saphyr::from_str(YAML).unwrap();
        let perfect = score(&expected, &expected.clone());
        assert!(perfect.passed);
        assert_eq!(perfect.diff_ptbr, "");
        let mut actual = expected.clone();
        actual.titles[1].status = "paid".into();
        actual.findings.clear();
        actual.bank_lines.push(ExpectedBankLine {
            id: "b3".into(),
            explained_by: vec![],
            amount: None,
        });
        let s = score(&expected, &actual);
        assert!(!s.passed);
        assert_eq!(s.bank_lines.matched, 2);
        assert_eq!(s.bank_lines.extra, vec!["b3"]);
        assert_eq!(s.titles.matched, 1);
        assert_eq!(s.titles.missing, vec!["t2 (difere)"]);
        assert_eq!(s.findings.matched, 0);
        assert_eq!(s.findings.missing, vec!["TITLE_WITHOUT_PAYMENT[t2]"]);
        assert!(s.diff_ptbr.contains("títulos: 1/2 corretos"));
        assert!(s.diff_ptbr.contains("faltou: t2 (difere)"));
        assert!(s.diff_ptbr.contains("sobrou: b3"));
        assert!(s
            .diff_ptbr
            .contains("linhas bancárias: 2/2 corretos, 1 a mais"));
    }

    #[test]
    fn file_loading() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("expected.yaml");
        std::fs::write(&path, YAML).unwrap();
        assert_eq!(load_expected_from(&path).unwrap().titles.len(), 2);
        std::fs::write(&path, ": : bad").unwrap();
        assert!(matches!(
            load_expected_from(&path),
            Err(EvalError::Parse(_))
        ));
        assert!(matches!(
            load_expected_from(&dir.path().join("missing.yaml")),
            Err(EvalError::Read(..))
        ));
        assert_eq!(find_twin_dir(dir.path()), None);
    }

    #[test]
    fn vantara_expected_if_available() {
        // Reads the private case only through the environment and never prints its values.
        if std::env::var("DM_VANTARA_PATH")
            .ok()
            .filter(|v| !v.trim().is_empty())
            .is_none()
        {
            eprintln!("skipped: DM_VANTARA_PATH não definido");
            return;
        }
        let loaded = load_expected().expect("expected.yaml do caso privado");
        assert!(loaded.private);
        assert!(!loaded.expected.bank_lines.is_empty());
    }
}
