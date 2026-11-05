//! Deterministic reconciliation engine: receivables ledgers, the bank statement and the
//! payables inbox of one case folder in, the golden-format result out.
//!
//! ```text
//! sources → cascade → title outcomes → receivables + payables controls → totals → Golden
//! ```
//!
//! Everything is a pure function of the input bytes and the [`EngineConfig`]: no clock, no
//! randomness, no network. The run id is derived from the input hashes, so ten runs over the
//! same folder produce byte-identical JSON.

#![forbid(unsafe_code)]

pub mod ap;
pub mod cascade;
pub mod config;
pub mod golden;
pub mod names;
pub mod probes;
pub mod ptbr;
pub mod sources;
pub mod titles;

use std::collections::BTreeMap;
use std::path::Path;

use dm_core::Money;
use sha2::{Digest, Sha256};

pub use config::{ApConfig, EngineConfig};
pub use golden::{Brl, FlexStr, GFinding, GLine, GTitle, Golden, Period};

use cascade::{Cascade, LineKind};
use probes::Finding;
use sources::Dataset;
use titles::{Status, TitleOutcome};

/// Engine errors.
#[derive(Debug, thiserror::Error)]
pub enum ReconError {
    /// A file or folder could not be read.
    #[error("leitura: {0}")]
    Io(String),
    /// Nothing usable in the folder; the second field lists why, file by file.
    #[error(
        "nenhuma fonte reconhecida em {0} (esperado: títulos a receber e/ou extrato bancário){1}"
    )]
    NoInputs(String, String),
}

/// Everything a run computed, for the report and `dm explain`.
#[derive(Clone, Debug)]
pub struct Run {
    /// The golden-format result.
    pub result: Golden,
    /// The sources as read.
    pub dataset: Dataset,
    /// Line outcomes.
    pub cascade: Cascade,
    /// Title outcomes.
    pub outcomes: Vec<TitleOutcome>,
}

/// Reconciles every input file of `dir`.
pub fn run_dir(dir: &Path, cfg: &EngineConfig) -> Result<Run, ReconError> {
    let ds = sources::load_dir(dir, cfg)?;
    if ds.titles.is_empty() && ds.lines.is_empty() {
        let why = if ds.warnings.is_empty() {
            String::new()
        } else {
            format!(": {}", ds.warnings.join("; "))
        };
        return Err(ReconError::NoInputs(dir.display().to_string(), why));
    }
    let invoices = ap::invoice_numbers(&ds);
    let cascade = cascade::run(&ds, &invoices, cfg);
    let outcomes = titles::outcomes(&ds, &cascade, cfg);
    let mut findings = probes::ar_findings(&ds, &cascade, &outcomes, cfg);
    findings.extend(ap::ap_findings(&ds, &cascade, cfg));
    findings.extend(ap::injection_findings(&ds));
    findings.extend(probes::aggregate_findings(&ds, &outcomes));
    let result = assemble(&ds, &cascade, &outcomes, findings);
    Ok(Run {
        result,
        dataset: ds,
        cascade,
        outcomes,
    })
}

fn brl(m: Money) -> Brl {
    Brl(m.centavos())
}

fn assemble(
    ds: &Dataset,
    cascade: &Cascade,
    outcomes: &[TitleOutcome],
    mut findings: Vec<Finding>,
) -> Golden {
    findings.sort_by(|a, b| {
        (a.section, a.anchor, a.order, &a.evidence).cmp(&(
            b.section,
            b.anchor,
            b.order,
            &b.evidence,
        ))
    });
    let digits = findings.len().to_string().len().max(2);
    let coded: Vec<(String, &Finding)> = findings
        .iter()
        .enumerate()
        .map(|(i, f)| (format!("F{:0digits$}", i + 1), f))
        .collect();

    let mut run_hasher = Sha256::new();
    for (file, hash) in &ds.inputs {
        run_hasher.update(file.as_bytes());
        run_hasher.update(b"\0");
        run_hasher.update(hash.as_bytes());
        run_hasher.update(b"\n");
    }
    let run_id = format!("run_{}", &hex::encode(run_hasher.finalize())[..16]);

    let titles: Vec<GTitle> = ds
        .titles
        .iter()
        .zip(outcomes)
        .map(|(t, o)| {
            let duplicate = o.status == Status::Duplicate;
            GTitle {
                id: t.id.clone(),
                source: ds.ledgers[t.ledger].key.clone(),
                reference: FlexStr(t.reference.clone()),
                customer: FlexStr(t.customer.clone()),
                tax_id: t.tax_id.clone().map(FlexStr),
                due: Some(t.due),
                amount: Some(brl(t.amount)),
                status: o.status.code().to_string(),
                nosso_numero: t.nosso_numero_raw.clone().map(FlexStr),
                effective_due: o.effective_due,
                paid_on: o.paid_on,
                paid_amount: (!duplicate && o.status != Status::Open).then(|| brl(o.paid_amount)),
                open_amount: Some(brl(o.open_amount)),
                days_late: o.days_late,
                business_days_late: o.business_days_late,
                flags: o.flags.iter().map(ToString::to_string).collect(),
                duplicate_of: o.duplicate_of.map(|d| ds.titles[d].id.clone()),
            }
        })
        .collect();

    let bank_lines: Vec<GLine> = ds
        .lines
        .iter()
        .zip(&cascade.lines)
        .map(|(l, lo)| {
            let mut matched: Vec<FlexStr> = lo
                .applied
                .iter()
                .map(|a| FlexStr(ds.titles[a.title].id.clone()))
                .collect();
            matched.extend(lo.ap_refs.iter().cloned().map(FlexStr));
            GLine {
                n: l.n,
                date: l.date,
                amount: brl(l.amount),
                side: l.side.code().to_string(),
                kind: lo.kind.code().to_string(),
                matched,
                rule: lo.rule.to_string(),
                proposed: lo.proposed,
                findings: coded
                    .iter()
                    .filter(|(_, f)| f.cites_line(l.n))
                    .map(|(c, _)| c.clone())
                    .collect(),
                description: l.description.clone(),
            }
        })
        .collect();

    let late_fee_estimates_brl: BTreeMap<String, Brl> = ds
        .titles
        .iter()
        .zip(outcomes)
        .filter_map(|(t, o)| o.fee_estimate.map(|fee| (t.id.clone(), brl(fee))))
        .collect();

    Golden {
        version: 1,
        case: ds.case.clone(),
        run_id,
        period: ds.period,
        sources: ds.sources.clone(),
        inputs: ds.inputs.clone(),
        totals_brl: totals(ds, cascade, outcomes),
        titles,
        bank_lines,
        findings: coded
            .iter()
            .map(|(code, f)| GFinding {
                code: code.clone(),
                kind: f.kind.to_string(),
                severity: f.severity.to_string(),
                amount_at_risk_brl: brl(f.amount),
                evidence: f.evidence.clone(),
                title_ptbr: f.title.clone(),
                explanation_ptbr: f.explanation.clone(),
            })
            .collect(),
        late_fee_estimates_brl,
    }
}

fn sum<I: IntoIterator<Item = Money>>(it: I) -> Money {
    it.into_iter().fold(Money::ZERO, Money::saturating_add)
}

fn totals(ds: &Dataset, cascade: &Cascade, outcomes: &[TitleOutcome]) -> BTreeMap<String, Brl> {
    let mut t = BTreeMap::new();
    for (i, l) in ds.ledgers.iter().enumerate() {
        t.insert(
            format!("{}_open", l.key),
            brl(ds.ledger_open.get(i).copied().unwrap_or(Money::ZERO)),
        );
    }
    let side = |s: sources::Side| sum(ds.lines.iter().filter(|l| l.side == s).map(|l| l.amount));
    let of_kind = |k: LineKind| {
        sum(ds
            .lines
            .iter()
            .zip(&cascade.lines)
            .filter(|(l, lo)| lo.kind == k && l.side == sources::Side::Credit)
            .map(|(l, _)| l.amount))
    };
    let applied = cascade.lines.iter().flat_map(|lo| lo.applied.iter());
    let principal = sum(applied.clone().map(|a| a.principal));
    let fines = sum(applied.map(|a| a.fine));
    let open = sum(outcomes
        .iter()
        .filter(|o| o.status != Status::Duplicate)
        .map(|o| o.open_amount));
    let duplicated = sum(ds
        .titles
        .iter()
        .zip(outcomes)
        .filter(|(_, o)| o.status == Status::Duplicate)
        .map(|(title, _)| title.amount));
    let fees = sum(ds
        .lines
        .iter()
        .zip(&cascade.lines)
        .filter(|(_, lo)| lo.kind == LineKind::Fee)
        .map(|(l, _)| l.amount));
    t.insert("bank_credits".into(), brl(side(sources::Side::Credit)));
    t.insert("bank_debits".into(), brl(side(sources::Side::Debit)));
    t.insert("applied_principal".into(), brl(principal));
    t.insert("fines_received".into(), brl(fines));
    t.insert("unapplied_cash".into(), brl(of_kind(LineKind::Unapplied)));
    t.insert("true_open_balance".into(), brl(open));
    t.insert(
        "naive_open_balance".into(),
        brl(open.saturating_add(duplicated)),
    );
    t.insert("unbooked_bank_fees".into(), brl(fees));
    t.insert(
        "uncharged_late_fees_estimate".into(),
        brl(sum(outcomes.iter().filter_map(|o| o.fee_estimate))),
    );
    t
}
