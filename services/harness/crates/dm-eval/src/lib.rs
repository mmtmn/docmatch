//! Scores a run against a golden case and reports the differences in pt-BR.
//!
//! Ids are never compared directly: titles are matched by (source file, document
//! reference) and sources by file name, so an engine may number titles and name sources as
//! it likes. 100% means every bank line (kind, matched titles, rule, proposal flag), every
//! title (status, flags, payment date, paid and open amounts), every finding (kind present
//! exactly once, amount within one centavo, same evidence) and every total agree, with no
//! extra findings.

#![forbid(unsafe_code)]

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use dm_recon::{run_dir, Brl, EngineConfig, GFinding, GTitle, Golden};
use sha2::{Digest, Sha256};

/// Errors of the golden runner.
#[derive(Debug, thiserror::Error)]
pub enum EvalError {
    /// Reading a file failed.
    #[error("leitura de {0}: {1}")]
    Io(String, String),
    /// A document did not parse.
    #[error("{0}: {1}")]
    Parse(String, String),
    /// The engine failed.
    #[error("motor: {0}")]
    Engine(String),
}

/// Correct items out of the expected ones.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Section {
    /// Items that agree.
    pub ok: usize,
    /// Expected items.
    pub total: usize,
}

impl Section {
    fn perfect(self) -> bool {
        self.ok == self.total
    }
}

/// The score of one run.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Scorecard {
    /// Bank lines.
    pub lines: Section,
    /// Titles.
    pub titles: Section,
    /// Findings.
    pub findings: Section,
    /// Findings the engine raised that the case does not expect.
    pub extra_findings: usize,
    /// Totals.
    pub totals: Section,
    /// Every difference, in pt-BR.
    pub diffs: Vec<String>,
}

impl Scorecard {
    /// True for a 100% score.
    pub fn perfect(&self) -> bool {
        self.lines.perfect()
            && self.titles.perfect()
            && self.findings.perfect()
            && self.totals.perfect()
            && self.extra_findings == 0
    }

    /// `18/18 linhas, 17/17 títulos, 23/23 achados` (with `+N extras` when the engine raised
    /// findings the case does not expect).
    pub fn summary(&self) -> String {
        let mut s = format!(
            "{}/{} linhas, {}/{} títulos, {}/{} achados",
            self.lines.ok,
            self.lines.total,
            self.titles.ok,
            self.titles.total,
            self.findings.ok,
            self.findings.total
        );
        if self.extra_findings > 0 {
            s.push_str(&format!(" (+{} extras)", self.extra_findings));
        }
        s
    }

    /// The summary plus totals and extras, then every difference.
    pub fn report(&self) -> String {
        let mut s = format!(
            "{} · totais {}/{} · achados extras {}\n",
            self.summary(),
            self.totals.ok,
            self.totals.total,
            self.extra_findings
        );
        for d in &self.diffs {
            s.push_str("  - ");
            s.push_str(d);
            s.push('\n');
        }
        s
    }
}

fn norm_ref(s: &str) -> String {
    dm_normalize::fold_ascii(s)
}

/// Canonical title keys (source file + reference) of one side.
struct Side<'a> {
    canon: BTreeMap<&'a str, String>,
}

impl<'a> Side<'a> {
    fn new(golden: &'a Golden) -> Self {
        let canon = golden
            .titles
            .iter()
            .map(|t| (t.id.as_str(), title_key(golden, t)))
            .collect();
        Side { canon }
    }

    fn evidence(&self, refs: &[String]) -> BTreeSet<String> {
        refs.iter()
            .map(|r| match r.strip_prefix("title:") {
                Some(id) => format!(
                    "title:{}",
                    self.canon
                        .get(id)
                        .cloned()
                        .unwrap_or_else(|| id.to_string())
                ),
                None => r.clone(),
            })
            .collect()
    }

    fn matched(&self, ids: &[dm_recon::FlexStr]) -> BTreeSet<String> {
        ids.iter()
            .map(|m| {
                self.canon
                    .get(m.as_str())
                    .cloned()
                    .unwrap_or_else(|| m.as_str().to_string())
            })
            .collect()
    }
}

fn source_file<'a>(g: &'a Golden, key: &'a str) -> &'a str {
    g.sources.get(key).map_or(key, String::as_str)
}

fn title_key(g: &Golden, t: &GTitle) -> String {
    format!(
        "{}#{}",
        source_file(g, &t.source),
        norm_ref(t.reference.as_str())
    )
}

fn amount(a: Option<Brl>) -> Brl {
    a.unwrap_or_default()
}

fn show_set(s: &BTreeSet<String>) -> String {
    s.iter().cloned().collect::<Vec<_>>().join(", ")
}

/// Scores `actual` against `expected`.
pub fn score(expected: &Golden, actual: &Golden) -> Scorecard {
    let exp = Side::new(expected);
    let act = Side::new(actual);
    let mut card = Scorecard::default();

    // Bank lines, by number.
    let actual_lines: BTreeMap<usize, &dm_recon::GLine> =
        actual.bank_lines.iter().map(|l| (l.n, l)).collect();
    card.lines.total = expected.bank_lines.len();
    for e in &expected.bank_lines {
        let Some(a) = actual_lines.get(&e.n) else {
            card.diffs
                .push(format!("linha {}: ausente no resultado", e.n));
            continue;
        };
        let (em, am) = (exp.matched(&e.matched), act.matched(&a.matched));
        if e.kind == a.kind && e.rule == a.rule && e.proposed == a.proposed && em == am {
            card.lines.ok += 1;
        } else {
            card.diffs.push(format!(
                "linha {}: esperado {} / {} / [{}]{}, obtido {} / {} / [{}]{}",
                e.n,
                e.kind,
                e.rule,
                show_set(&em),
                if e.proposed { " (proposta)" } else { "" },
                a.kind,
                a.rule,
                show_set(&am),
                if a.proposed { " (proposta)" } else { "" },
            ));
        }
    }

    // Titles, by source file and reference.
    let actual_titles: BTreeMap<String, &GTitle> = actual
        .titles
        .iter()
        .map(|t| (title_key(actual, t), t))
        .collect();
    card.titles.total = expected.titles.len();
    for e in &expected.titles {
        let key = title_key(expected, e);
        let Some(a) = actual_titles.get(&key) else {
            card.diffs
                .push(format!("título {} ({key}): ausente no resultado", e.id));
            continue;
        };
        let ef: BTreeSet<&str> = e.flags.iter().map(String::as_str).collect();
        let af: BTreeSet<&str> = a.flags.iter().map(String::as_str).collect();
        let mut problems = Vec::new();
        if e.status != a.status {
            problems.push(format!("status {} ≠ {}", e.status, a.status));
        }
        if ef != af {
            problems.push(format!(
                "flags [{}] ≠ [{}]",
                e.flags.join(", "),
                a.flags.join(", ")
            ));
        }
        if e.paid_on != a.paid_on {
            problems.push(format!("pago em {:?} ≠ {:?}", e.paid_on, a.paid_on));
        }
        if amount(e.paid_amount).abs_diff(amount(a.paid_amount)) > 1 {
            problems.push(format!(
                "pago {} ≠ {}",
                amount(e.paid_amount),
                amount(a.paid_amount)
            ));
        }
        if amount(e.open_amount).abs_diff(amount(a.open_amount)) > 1 {
            problems.push(format!(
                "aberto {} ≠ {}",
                amount(e.open_amount),
                amount(a.open_amount)
            ));
        }
        if problems.is_empty() {
            card.titles.ok += 1;
        } else {
            card.diffs.push(format!(
                "título {} ({}): {}",
                e.id,
                a.id,
                problems.join("; ")
            ));
        }
    }

    // Findings: each expected one needs an actual one of the same kind, amount and evidence.
    let mut used = vec![false; actual.findings.len()];
    card.findings.total = expected.findings.len();
    for e in &expected.findings {
        let ee = exp.evidence(&e.evidence);
        let hit = actual.findings.iter().enumerate().position(|(i, a)| {
            !used[i]
                && a.kind == e.kind
                && a.amount_at_risk_brl.abs_diff(e.amount_at_risk_brl) <= 1
                && act.evidence(&a.evidence) == ee
        });
        if let Some(i) = hit {
            used[i] = true;
            card.findings.ok += 1;
            continue;
        }
        let same_kind: Vec<&GFinding> = actual
            .findings
            .iter()
            .enumerate()
            .filter(|(i, a)| !used[*i] && a.kind == e.kind)
            .map(|(_, a)| a)
            .collect();
        match same_kind.first() {
            Some(a) => card.diffs.push(format!(
                "achado {} ({}): esperado {} com [{}], obtido {} ({}) com [{}]",
                e.code,
                e.kind,
                e.amount_at_risk_brl,
                show_set(&ee),
                a.amount_at_risk_brl,
                a.code,
                show_set(&act.evidence(&a.evidence))
            )),
            None => card
                .diffs
                .push(format!("achado {} ({}): não encontrado", e.code, e.kind)),
        }
    }
    for (i, a) in actual.findings.iter().enumerate() {
        if !used[i] && !expected.findings.iter().any(|e| e.kind == a.kind) {
            card.extra_findings += 1;
            card.diffs.push(format!(
                "achado extra {} ({}): {} com [{}]",
                a.code,
                a.kind,
                a.amount_at_risk_brl,
                show_set(&act.evidence(&a.evidence))
            ));
        } else if !used[i] {
            let expected_count = expected
                .findings
                .iter()
                .filter(|e| e.kind == a.kind)
                .count();
            let actual_count = actual.findings.iter().filter(|x| x.kind == a.kind).count();
            if actual_count > expected_count {
                card.extra_findings += 1;
                card.diffs.push(format!(
                    "achado extra {} ({}): tipo repetido",
                    a.code, a.kind
                ));
            }
        }
    }

    // Totals; `<source>_open` keys follow the source file.
    card.totals.total = expected.totals_brl.len();
    for (key, value) in &expected.totals_brl {
        let actual_key = key
            .strip_suffix("_open")
            .and_then(|src| expected.sources.get(src))
            .and_then(|file| {
                actual
                    .sources
                    .iter()
                    .find(|(_, f)| *f == file)
                    .map(|(k, _)| format!("{k}_open"))
            })
            .unwrap_or_else(|| key.clone());
        match actual.totals_brl.get(&actual_key) {
            Some(v) if v.abs_diff(*value) <= 1 => card.totals.ok += 1,
            Some(v) => card
                .diffs
                .push(format!("total {key}: esperado {value}, obtido {v}")),
            None => card.diffs.push(format!("total {key}: ausente")),
        }
    }
    card
}

/// Result of running a golden case several times.
#[derive(Clone, Debug)]
pub struct GoldenRun {
    /// Case folder.
    pub dir: PathBuf,
    /// Score of the first run.
    pub scorecard: Scorecard,
    /// Number of runs.
    pub runs: usize,
    /// True when every run produced byte-identical JSON.
    pub identical: bool,
    /// SHA-256 of the first run's JSON.
    pub hash: String,
    /// The first run's result.
    pub result: Golden,
}

/// Reads and parses `expected.yaml` (or a result JSON) from a path.
pub fn load(path: &Path) -> Result<Golden, EvalError> {
    let text = std::fs::read_to_string(path)
        .map_err(|e| EvalError::Io(path.display().to_string(), e.to_string()))?;
    Golden::parse(&text).map_err(|e| EvalError::Parse(path.display().to_string(), e))
}

/// Runs the engine on `dir` `runs` times and scores the first run against `dir/expected.yaml`.
pub fn run_golden(dir: &Path, cfg: &EngineConfig, runs: usize) -> Result<GoldenRun, EvalError> {
    let expected = load(&dir.join("expected.yaml"))?;
    let mut hashes = BTreeSet::new();
    let mut first: Option<(Golden, String)> = None;
    for _ in 0..runs.max(1) {
        let run = run_dir(dir, cfg).map_err(|e| EvalError::Engine(e.to_string()))?;
        let json = run.result.to_json();
        let hash = hex::encode(Sha256::digest(json.as_bytes()));
        hashes.insert(hash.clone());
        if first.is_none() {
            first = Some((run.result, hash));
        }
    }
    let (result, hash) = first.unwrap_or_default();
    Ok(GoldenRun {
        dir: dir.to_path_buf(),
        scorecard: score(&expected, &result),
        runs: runs.max(1),
        identical: hashes.len() == 1,
        hash,
        result,
    })
}

/// Walks up from `start` looking for `testdata/golden/vantara-twin`.
pub fn find_twin_dir(start: &Path) -> Option<PathBuf> {
    let mut dir = Some(start);
    while let Some(d) = dir {
        let candidate = d.join("testdata/golden/vantara-twin");
        if candidate.join("expected.yaml").is_file() {
            return Some(candidate);
        }
        dir = d.parent();
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn case() -> Golden {
        Golden::parse(
            r"
sources: { erp: a.csv, bank: b.csv }
totals_brl: { erp_open: 100.00, bank_credits: 100.00 }
titles:
  - { id: T01, source: erp, ref: NF 1, status: paid, paid_on: 2026-07-10, paid_amount: 100, open_amount: 0, flags: [late] }
bank_lines:
  - { n: 1, date: 2026-07-10, amount: 100, side: C, kind: receipt, matched: [T01], rule: name_amount, findings: [F01] }
findings:
  - { code: F01, kind: paid_late_no_fee, severity: low, amount_at_risk_brl: 2.00, evidence: ['bank:1', 'title:T01'] }
",
        )
        .unwrap_or_else(|e| panic!("{e}"))
    }

    #[test]
    fn identical_cases_score_100() {
        let c = case();
        let s = score(&c, &c);
        assert!(s.perfect(), "{}", s.report());
        assert_eq!(s.summary(), "1/1 linhas, 1/1 títulos, 1/1 achados");
    }

    #[test]
    fn ids_and_source_keys_are_mapped_by_file_and_reference() {
        let e = case();
        let mut a = case();
        a.sources = [
            ("sistema".to_string(), "a.csv".to_string()),
            ("banco".to_string(), "b.csv".to_string()),
        ]
        .into();
        a.totals_brl = [
            ("sistema_open".to_string(), Brl(10_000)),
            ("bank_credits".to_string(), Brl(10_000)),
        ]
        .into();
        a.titles[0].id = "X9".into();
        a.titles[0].source = "sistema".into();
        a.bank_lines[0].matched = vec![dm_recon::FlexStr("X9".into())];
        a.findings[0].evidence = vec!["bank:1".into(), "title:X9".into()];
        let s = score(&e, &a);
        assert!(s.perfect(), "{}", s.report());
    }

    #[test]
    fn wrong_amount_missing_and_extra_findings_are_reported() {
        let e = case();
        let mut a = case();
        a.findings[0].amount_at_risk_brl = Brl(300);
        a.findings.push(GFinding {
            code: "F02".into(),
            kind: "unapplied_cash".into(),
            ..GFinding::default()
        });
        a.titles[0].status = "partial".into();
        a.bank_lines[0].rule = "none".into();
        let s = score(&e, &a);
        assert!(!s.perfect());
        assert_eq!(
            (s.findings.ok, s.extra_findings, s.titles.ok, s.lines.ok),
            (0, 1, 0, 0)
        );
        assert!(s.report().contains("achado extra F02"));
    }

    #[test]
    fn a_repeated_kind_is_an_extra() {
        let e = case();
        let mut a = case();
        let mut dup = a.findings[0].clone();
        dup.code = "F02".into();
        dup.amount_at_risk_brl = Brl(1);
        a.findings.push(dup);
        let s = score(&e, &a);
        assert_eq!(s.extra_findings, 1);
        assert!(!s.perfect());
    }
}
