//! Turns an engine run (`dm_recon::Run`) into the contract's findings, matches and summary.
//!
//! Evidence references of the engine (`bank:<n>`, `title:<id>`, `email:<n>`,
//! `chat:<time>`) become `Evidence` with the upload id of the file they came from, a row
//! locator where there is one, a pt-BR label and an excerpt for the side-by-side view.

use std::collections::{BTreeMap, BTreeSet};

use dm_recon::golden::GFinding;
use dm_recon::Run;
use prost_types::{value::Kind, ListValue, Struct, Value};

use crate::pb;

/// File name in the run folder → upload id.
pub type SourceIndex = BTreeMap<String, String>;

fn brl(centavos: i64) -> String {
    let neg = centavos < 0;
    let c = centavos.unsigned_abs();
    let reais = (c / 100).to_string();
    let mut grouped = String::new();
    for (i, ch) in reais.chars().enumerate() {
        if i > 0 && (reais.len() - i).is_multiple_of(3) {
            grouped.push('.');
        }
        grouped.push(ch);
    }
    format!("{}R$ {grouped},{:02}", if neg { "-" } else { "" }, c % 100)
}

fn source_of(run: &Run, key: &str, index: &SourceIndex) -> String {
    run.result
        .sources
        .get(key)
        .and_then(|file| index.get(file))
        .cloned()
        .unwrap_or_default()
}

/// Structured fields of an evidence row, for clients that render tables: `date` (ISO),
/// `description`, `amount` (centavos, negative for debits) and `document`.
fn values(date: String, description: &str, amount: Option<i64>, document: &str) -> Option<Struct> {
    let mut fields = BTreeMap::new();
    fields.insert("date".to_string(), text(&date));
    fields.insert("description".to_string(), text(description));
    if let Some(a) = amount {
        // Centavos fit an f64 exactly up to 2^53.
        #[allow(clippy::cast_precision_loss)]
        let n = a as f64;
        fields.insert(
            "amount".to_string(),
            Value {
                kind: Some(Kind::NumberValue(n)),
            },
        );
    }
    if !document.is_empty() {
        fields.insert("document".to_string(), text(document));
    }
    Some(Struct { fields })
}

fn row(n: usize) -> Option<pb::Locator> {
    Some(pb::Locator {
        locator: Some(pb::locator::Locator::Row(pb::RowLocator {
            sheet: String::new(),
            row: u32::try_from(n).unwrap_or(u32::MAX),
            row_end: 0,
            column: String::new(),
        })),
    })
}

/// Evidence for one engine reference; `None` when the reference points nowhere.
pub fn evidence(run: &Run, reference: &str, index: &SourceIndex) -> Option<pb::Evidence> {
    let ds = &run.dataset;
    let (kind, value) = reference.split_once(':')?;
    match kind {
        "bank" => {
            let n: usize = value.parse().ok()?;
            let line = ds.lines.get(n.checked_sub(1)?)?;
            Some(pb::Evidence {
                source_id: source_of(run, "bank", index),
                locator: row(n),
                excerpt: format!(
                    "{} · {} · {}",
                    line.date.format("%d/%m/%Y"),
                    line.description,
                    brl(line.amount.centavos())
                ),
                label: format!("Linha {n} do extrato"),
                values: values(
                    line.date.format("%Y-%m-%d").to_string(),
                    &line.description,
                    Some(if line.side == dm_recon::sources::Side::Debit {
                        -line.amount.centavos()
                    } else {
                        line.amount.centavos()
                    }),
                    line.document.as_deref().unwrap_or_default(),
                ),
            })
        }
        "title" => {
            let t = ds.titles.iter().find(|t| t.id == value)?;
            let key = &ds.ledgers.get(t.ledger)?.key;
            Some(pb::Evidence {
                source_id: source_of(run, key, index),
                locator: row(t.row + 1),
                excerpt: format!(
                    "{} · venc. {} · {}",
                    t.customer,
                    t.due.format("%d/%m/%Y"),
                    brl(t.amount.centavos())
                ),
                label: if t.reference.is_empty() {
                    value.to_string()
                } else {
                    t.reference.clone()
                },
                values: values(
                    t.due.format("%Y-%m-%d").to_string(),
                    &t.customer,
                    Some(t.amount.centavos()),
                    &t.reference,
                ),
            })
        }
        "email" => {
            let n: usize = value.parse().ok()?;
            let e = ds.emails.get(n.checked_sub(1)?)?;
            Some(pb::Evidence {
                source_id: source_of(run, "ap_inbox", index),
                locator: None,
                excerpt: format!("{} — {}", e.subject, e.from),
                label: format!("E-mail {n}"),
                values: values(
                    e.at.map(|a| a.format("%Y-%m-%dT%H:%M").to_string())
                        .unwrap_or_default(),
                    &e.subject,
                    None,
                    &e.from,
                ),
            })
        }
        "chat" => {
            let m = ds
                .chat
                .iter()
                .find(|m| m.at.format("%Y-%m-%dT%H:%M").to_string() == value)?;
            Some(pb::Evidence {
                source_id: source_of(run, "ap_inbox", index),
                locator: None,
                excerpt: m.text.clone(),
                label: format!("Mensagem de {} ({})", m.at.format("%d/%m %H:%M"), m.author),
                values: values(
                    m.at.format("%Y-%m-%dT%H:%M").to_string(),
                    &m.text,
                    None,
                    &m.author,
                ),
            })
        }
        _ => None,
    }
}

fn severity(s: &str) -> pb::Severity {
    match s {
        "info" => pb::Severity::Info,
        "low" => pb::Severity::Low,
        "medium" => pb::Severity::Medium,
        "high" => pb::Severity::High,
        "critical" => pb::Severity::Critical,
        _ => pb::Severity::Unspecified,
    }
}

/// The contract's coarse taxonomy for an engine finding kind.
pub fn finding_kind(kind: &str) -> pb::FindingKind {
    match kind {
        "duplicate_title_across_erps" | "customer_master_duplicate" => pb::FindingKind::Duplicate,
        "partial_payment" => pb::FindingKind::Partial,
        "unapplied_cash"
        | "overdue_open"
        | "unbooked_bank_fees"
        | "ap_invoice_without_payment_or_approval" => pb::FindingKind::Missing,
        "ap_invoice_already_paid" => pb::FindingKind::Contradiction,
        "ap_boleto_invalid" | "ap_sender_or_bank_details_changed" | "injected_instructions" => {
            pb::FindingKind::Anomaly
        }
        "withholding_suspected"
        | "overpayment_fine_received"
        | "paid_late_no_fee"
        | "uncharged_late_fees"
        | "ap_three_way_match_failed"
        | "ap_premature_recharge" => pb::FindingKind::Mismatch,
        _ => pb::FindingKind::Note,
    }
}

fn confidence(kind: &str) -> (f64, pb::ConfidenceLabel) {
    match kind {
        "injected_instructions" => (0.5, pb::ConfidenceLabel::NeedsYou),
        "amount_only_proposal" => (0.6, pb::ConfidenceLabel::Probable),
        "ambiguous_name_match" | "withholding_suspected" | "ap_three_way_match_failed" => {
            (0.8, pb::ConfidenceLabel::Probable)
        }
        _ => (0.97, pb::ConfidenceLabel::Confirmed),
    }
}

fn text(s: &str) -> Value {
    Value {
        kind: Some(Kind::StringValue(s.to_string())),
    }
}

/// Every finding of the run, in engine order.
pub fn findings(run: &Run, run_id: &str, index: &SourceIndex) -> Vec<pb::Finding> {
    run.result
        .findings
        .iter()
        .map(|f: &GFinding| {
            let evidence: Vec<pb::Evidence> = f
                .evidence
                .iter()
                .filter_map(|r| evidence(run, r, index))
                .collect();
            let source_ids: Vec<String> = evidence
                .iter()
                .map(|e| e.source_id.clone())
                .filter(|s| !s.is_empty())
                .collect::<BTreeSet<_>>()
                .into_iter()
                .collect();
            let (conf, label) = confidence(&f.kind);
            let details = Struct {
                fields: [
                    ("engine_code".to_string(), text(&f.code)),
                    (
                        "references".to_string(),
                        Value {
                            kind: Some(Kind::ListValue(ListValue {
                                values: f.evidence.iter().map(|r| text(r)).collect(),
                            })),
                        },
                    ),
                ]
                .into_iter()
                .collect(),
            };
            pb::Finding {
                id: String::new(),
                run_id: run_id.to_string(),
                code: format!("recon.{}", f.kind),
                kind: finding_kind(&f.kind) as i32,
                severity: severity(&f.severity) as i32,
                amount_at_risk: f.amount_at_risk_brl.0,
                currency: "BRL".to_string(),
                title: f.title_ptbr.clone(),
                explanation: if f.explanation_ptbr.is_empty() {
                    f.title_ptbr.clone()
                } else {
                    f.explanation_ptbr.clone()
                },
                confidence: conf,
                confidence_label: label as i32,
                evidence,
                status: pb::FindingStatus::Open as i32,
                rule_id: String::new(),
                match_id: String::new(),
                source_ids,
                details: Some(details),
                created_at: None,
            }
        })
        .filter(|f| !f.evidence.is_empty())
        .collect()
}

fn match_kind(rule: &str) -> (pb::MatchKind, f64) {
    if rule.contains("subset_sum") {
        (pb::MatchKind::Sum, 0.95)
    } else if rule.contains("amount_only") {
        (pb::MatchKind::Fuzzy, 0.6)
    } else if rule.contains("fuzzy") {
        (pb::MatchKind::Fuzzy, 0.85)
    } else if rule.contains("partial") || rule.contains("withholding") || rule.contains("fine") {
        (pb::MatchKind::Tolerance, 0.9)
    } else if rule.contains("name") {
        (pb::MatchKind::Fuzzy, 0.9)
    } else if rule == "bank_fee_pattern" {
        (pb::MatchKind::Rule, 0.95)
    } else {
        (pb::MatchKind::Exact, 0.99)
    }
}

fn rule_ptbr(rule: &str) -> &'static str {
    match rule {
        "nosso_numero_exact" => "Nosso número do boleto e valor iguais",
        "nosso_numero_partial" => "Nosso número do boleto com valor parcial",
        "tax_id_in_text" => "CNPJ no histórico e valor igual",
        "tax_id_amount_fine" => "CNPJ no histórico e valor com multa contratual",
        "name_amount_due" => "Nome do pagador, valor e data do vencimento",
        "name_amount" => "Nome do pagador e valor",
        "name_amount_fine" => "Nome do pagador e valor com multa contratual",
        "name_amount_withholding" => "Nome do pagador e valor líquido de retenção",
        "fuzzy_name_unique_amount" => "Nome ambíguo resolvido por valor único",
        "subset_sum_same_customer" => "Soma de títulos do mesmo cliente",
        "amount_only_unique" => "Valor único (proposta a confirmar)",
        "document_in_text" => "Número da nota citado no histórico",
        "bank_fee_pattern" => "Tarifa bancária",
        _ => "Casamento",
    }
}

/// One match per explained bank line (titles or payables references on the right).
pub fn matches(run: &Run, run_id: &str, index: &SourceIndex) -> Vec<pb::Match> {
    run.result
        .bank_lines
        .iter()
        .filter(|l| !l.matched.is_empty())
        .filter_map(|l| {
            let left = evidence(run, &format!("bank:{}", l.n), index)?;
            let right: Vec<pb::Evidence> = l
                .matched
                .iter()
                .map(|m| {
                    let id = m.as_str();
                    if id.starts_with("AP-") {
                        Some(pb::Evidence {
                            source_id: source_of(run, "ap_inbox", index),
                            locator: None,
                            excerpt: format!(
                                "Nota {} da caixa de contas a pagar",
                                id.trim_start_matches("AP-")
                            ),
                            label: id.to_string(),
                            values: None,
                        })
                    } else {
                        evidence(run, &format!("title:{id}"), index)
                    }
                })
                .collect::<Option<Vec<_>>>()?;
            let (kind, confidence) = match_kind(&l.rule);
            Some(pb::Match {
                id: String::new(),
                run_id: run_id.to_string(),
                kind: kind as i32,
                left: vec![left],
                right,
                confidence: if l.proposed {
                    confidence.min(0.6)
                } else {
                    confidence
                },
                amount: l.amount.0,
                currency: "BRL".to_string(),
                rule_id: String::new(),
                explanation: rule_ptbr(&l.rule).to_string(),
                created_at: None,
            })
        })
        .collect()
}

/// Summary for the `completed` event.
pub fn summary(run: &Run, findings: &[pb::Finding], matches_total: usize) -> pb::RunSummary {
    let mut by_severity: BTreeMap<String, u32> = BTreeMap::new();
    for f in &run.result.findings {
        *by_severity.entry(f.severity.clone()).or_default() += 1;
    }
    let lines_total = run.result.bank_lines.len();
    let lines_matched = run
        .result
        .bank_lines
        .iter()
        .filter(|l| l.kind != "unapplied")
        .count();
    #[allow(clippy::cast_precision_loss)] // line counts are small
    let ratio = if lines_total == 0 {
        0.0
    } else {
        lines_matched as f64 / lines_total as f64
    };
    pb::RunSummary {
        findings_total: u32::try_from(findings.len()).unwrap_or(u32::MAX),
        findings_by_severity: by_severity.into_iter().collect(),
        amount_at_risk: findings
            .iter()
            .map(|f| f.amount_at_risk)
            .fold(0i64, i64::saturating_add),
        currency: "BRL".to_string(),
        matches_total: u32::try_from(matches_total).unwrap_or(u32::MAX),
        lines_total: u32::try_from(lines_total).unwrap_or(u32::MAX),
        lines_matched: u32::try_from(lines_matched).unwrap_or(u32::MAX),
        matched_ratio: ratio,
        new_tool_ids: Vec::new(),
        proposed_rule_ids: Vec::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn brl_formatting() {
        assert_eq!(brl(4_900_000), "R$ 49.000,00");
        assert_eq!(brl(147_515), "R$ 1.475,15");
        assert_eq!(brl(-6_200), "-R$ 62,00");
        assert_eq!(brl(5), "R$ 0,05");
    }

    #[test]
    fn kinds_map_to_the_contract_taxonomy() {
        assert_eq!(finding_kind("partial_payment"), pb::FindingKind::Partial);
        assert_eq!(finding_kind("ap_boleto_invalid"), pb::FindingKind::Anomaly);
        assert_eq!(
            finding_kind("one_payment_many_titles"),
            pb::FindingKind::Note
        );
        assert_eq!(match_kind("subset_sum_same_customer").0, pb::MatchKind::Sum);
        assert_eq!(match_kind("nosso_numero_exact").0, pb::MatchKind::Exact);
    }
}
