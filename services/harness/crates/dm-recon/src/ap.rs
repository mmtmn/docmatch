//! Accounts-payable controls over the inbox, the approval chat and the bank debits.
//!
//! E-mails are grouped into threads by invoice number (leading zeros ignored, so `NF 055120`
//! is `NF 55120`). For each thread the probes compare what the supplier claims (amount, due
//! date, sender, payment method, boleto line, delivered volumes) with what the bank shows
//! and what the chat approved.

use std::collections::{BTreeMap, BTreeSet};

use chrono::{Datelike, NaiveDate};
use dm_core::Money;
use dm_normalize::money_ptbr;
use dm_validators::boleto::parse_boleto_with_reference;
use regex::Regex;

use crate::cascade::{Cascade, LineKind};
use crate::config::EngineConfig;
use crate::probes::{bank, section, Finding};
use crate::ptbr;
use crate::sources::{ChatMessage, Dataset, Email};

/// What one e-mail claims.
#[derive(Clone, Debug, Default)]
struct Claim {
    n: usize,
    invoices: Vec<String>,
    amount: Option<Money>,
    original: Option<Money>,
    updated: Option<Money>,
    total: Option<Money>,
    due: Option<NaiveDate>,
    due_after_days: Option<i64>,
    bank_details: Option<String>,
    digit_lines: Vec<String>,
    delivery: Option<(u32, u32)>,
    cte_count: Option<u32>,
    dunning: bool,
}

impl Claim {
    /// What the supplier asks to be paid now.
    fn claimed(&self) -> Option<Money> {
        self.updated
            .or(self.amount)
            .or(self.total)
            .or(self.original)
    }

    fn method(&self) -> &'static str {
        if !self.digit_lines.is_empty() {
            "boleto"
        } else if self.bank_details.is_some() {
            "transferência"
        } else {
            ""
        }
    }
}

struct Patterns {
    invoice: Regex,
    original: Regex,
    updated: Regex,
    value: Regex,
    total: Regex,
    due: Regex,
    due_after: Regex,
    bank: Regex,
    delivery: Regex,
    cte: Regex,
}

fn patterns() -> Option<Patterns> {
    Some(Patterns {
        invoice: Regex::new(r"\b(?:NF-?E?|NOTA FISCAL|NOTA)\s*(?:N[O°º]?\.?\s*)?(\d{3,})").ok()?,
        original: Regex::new(r"VALOR\s+ORIGINAL\s*:?\s*R\$\s*([\d.]+,\d{2})").ok()?,
        updated: Regex::new(r"VALOR\s+ATUALIZADO\s*:?\s*R\$\s*([\d.]+,\d{2})").ok()?,
        value: Regex::new(r"\bVALOR\s*:?\s*R\$\s*([\d.]+,\d{2})").ok()?,
        total: Regex::new(r"\bTOTAL\s*:?\s*R\$\s*([\d.]+,\d{2})").ok()?,
        due: Regex::new(r"\bVENC(?:IMENTO|\.)?\s*:?\s*(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?").ok()?,
        due_after: Regex::new(r"VENCIMENTO\s+(\d+)\s+DIAS?\s+APOS\s+O?\s*RECEBIMENTO").ok()?,
        bank: Regex::new(r"\bBANCO\s*\d{3}[^\n]*").ok()?,
        delivery: Regex::new(r"(\d+)\s+DE\s+(\d+)\s+VOLUMES").ok()?,
        cte: Regex::new(r"(\d+)\s+CT-?E\b").ok()?,
    })
}

fn money(caps: Option<regex::Captures<'_>>) -> Option<Money> {
    caps.and_then(|c| c.get(1))
        .and_then(|m| money_ptbr(m.as_str()).ok())
}

fn canonical(n: &str) -> String {
    let t = n.trim_start_matches('0');
    if t.is_empty() {
        "0".to_string()
    } else {
        t.to_string()
    }
}

fn claim_of(e: &Email, p: &Patterns, cfg: &EngineConfig, year: i32) -> Claim {
    let text = &e.folded;
    let mut invoices: Vec<String> = p
        .invoice
        .captures_iter(text)
        .filter_map(|c| c.get(1).map(|m| canonical(m.as_str())))
        .collect();
    invoices.sort();
    invoices.dedup();
    let due = p.due.captures(text).and_then(|caps| {
        let day: u32 = caps.get(1)?.as_str().parse().ok()?;
        let month: u32 = caps.get(2)?.as_str().parse().ok()?;
        let year = caps
            .get(3)
            .and_then(|y| y.as_str().parse::<i32>().ok())
            .map_or(e.at.map_or(year, |at| at.year()), |y| {
                if y < 100 {
                    2000 + y
                } else {
                    y
                }
            });
        NaiveDate::from_ymd_opt(year, month, day)
    });
    let digit_lines = e
        .body
        .lines()
        .filter(|l| l.chars().filter(char::is_ascii_digit).count() >= 44)
        .map(|l| {
            let folded = dm_normalize::fold_ascii(l);
            folded
                .split_once(':')
                .map_or(folded.clone(), |(_, rest)| rest.trim().to_string())
        })
        .collect();
    Claim {
        n: e.n,
        invoices,
        amount: money(p.value.captures(text)),
        original: money(p.original.captures(text)),
        updated: money(p.updated.captures(text)),
        total: money(p.total.captures(text)),
        due,
        due_after_days: p
            .due_after
            .captures(text)
            .and_then(|c| c.get(1)?.as_str().parse().ok()),
        bank_details: p.bank.find(text).map(|m| m.as_str().trim().to_string()),
        digit_lines,
        delivery: p.delivery.captures(text).and_then(|c| {
            Some((
                c.get(1)?.as_str().parse().ok()?,
                c.get(2)?.as_str().parse().ok()?,
            ))
        }),
        cte_count: p
            .cte
            .captures(text)
            .and_then(|c| c.get(1)?.as_str().parse().ok()),
        dunning: cfg
            .ap
            .dunning_patterns
            .iter()
            .any(|d| text.contains(d.as_str())),
    }
}

/// Invoice numbers the inbox mentions (canonical), for the supplier-payment rule.
pub fn invoice_numbers(ds: &Dataset) -> BTreeSet<String> {
    let Some(p) = patterns() else {
        return BTreeSet::new();
    };
    let year = ds.period.map_or(2000, |pp| pp.end.year());
    let cfg = EngineConfig::default();
    ds.emails
        .iter()
        .flat_map(|e| claim_of(e, &p, &cfg, year).invoices)
        .collect()
}

fn chat_ref(m: &ChatMessage) -> String {
    format!("chat:{}", m.at.format("%Y-%m-%dT%H:%M"))
}

fn mentions(m: &ChatMessage, invoice: &str) -> bool {
    m.folded
        .split(|c: char| !c.is_ascii_alphanumeric())
        .any(|t| t.chars().all(|c| c.is_ascii_digit()) && !t.is_empty() && canonical(t) == invoice)
}

fn regexes(list: &[String]) -> Vec<Regex> {
    list.iter().filter_map(|r| Regex::new(r).ok()).collect()
}

/// Chat messages that mention the invoice and ask whether it was already paid.
fn already_paid_questions(ds: &Dataset, invoice: &str, cfg: &EngineConfig) -> Vec<String> {
    let q = regexes(&cfg.ap.already_paid_question_patterns);
    ds.chat
        .iter()
        .filter(|m| mentions(m, invoice) && q.iter().any(|r| r.is_match(&m.folded)))
        .map(chat_ref)
        .collect()
}

/// The hedged message right before an approval, in the thread that discussed the invoice.
fn hedged_approvals(ds: &Dataset, invoice: &str, cfg: &EngineConfig) -> Vec<String> {
    let approval = regexes(&cfg.ap.approval_patterns);
    let hedge = regexes(&cfg.ap.hedge_patterns);
    let mut out = Vec::new();
    for (i, m) in ds.chat.iter().enumerate() {
        if !mentions(m, invoice) {
            continue;
        }
        let day = m.at.date();
        let thread: Vec<usize> = (i..ds.chat.len())
            .take_while(|j| ds.chat[*j].at.date() == day)
            .collect();
        for w in thread.windows(2) {
            let (prev, cur) = (&ds.chat[w[0]], &ds.chat[w[1]]);
            if approval.iter().any(|r| r.is_match(&cur.folded))
                && hedge.iter().any(|r| r.is_match(&prev.folded))
            {
                out.push(chat_ref(prev));
            }
        }
    }
    out.sort();
    out.dedup();
    out
}

fn approved_in_chat(ds: &Dataset, claim: &Claim, cfg: &EngineConfig) -> bool {
    let approval = regexes(&cfg.ap.approval_patterns);
    claim.invoices.iter().any(|inv| {
        ds.chat
            .iter()
            .any(|m| mentions(m, inv) && approval.iter().any(|r| r.is_match(&m.folded)))
    })
}

#[allow(clippy::too_many_arguments)]
fn finding(
    anchor: usize,
    order: u8,
    kind: &'static str,
    severity: &'static str,
    amount: Money,
    evidence: Vec<String>,
    title: String,
    explanation: String,
) -> Finding {
    Finding {
        section: section::PAYABLES,
        anchor,
        order,
        kind,
        severity,
        amount,
        evidence,
        title,
        explanation,
    }
}

/// Every payables finding.
pub fn ap_findings(ds: &Dataset, cascade: &Cascade, cfg: &EngineConfig) -> Vec<Finding> {
    let Some(p) = patterns() else {
        return Vec::new();
    };
    let year = ds.period.map_or(2000, |pp| pp.end.year());
    let period_end = ds.period.map(|pp| pp.end);
    let tol = Money::from_centavos(cfg.amount_tolerance_centavos.max(0));
    let claims: Vec<Claim> = ds
        .emails
        .iter()
        .map(|e| claim_of(e, &p, cfg, year))
        .collect();

    // Bank debits that pay an invoice: invoice → line index.
    let mut paid: BTreeMap<String, usize> = BTreeMap::new();
    for (li, lo) in cascade.lines.iter().enumerate() {
        if lo.kind == LineKind::SupplierPayment {
            for r in &lo.ap_refs {
                paid.entry(r.trim_start_matches("AP-").to_string())
                    .or_insert(li);
            }
        }
    }
    // Threads: invoice → claims in inbox order.
    let mut threads: BTreeMap<String, Vec<usize>> = BTreeMap::new();
    for (ci, c) in claims.iter().enumerate() {
        for inv in &c.invoices {
            threads.entry(inv.clone()).or_default().push(ci);
        }
    }

    let mut f = Vec::new();
    for (inv, members) in &threads {
        let first = &claims[members[0]];
        let first_mail = &ds.emails[members[0]];
        let original = first
            .amount
            .or(first.original)
            .or_else(|| members.iter().find_map(|m| claims[*m].original));
        let original_due = first.due;
        let pay_line = paid.get(inv).copied();
        for &m in &members[1..] {
            let later = &claims[m];
            let mail = &ds.emails[m];
            let claimed = later.claimed().unwrap_or(Money::ZERO);
            if let (Some(li), true) = (pay_line, later.dunning) {
                let line = &ds.lines[li];
                let mut ev = vec![
                    bank(line.n),
                    format!("email:{}", first.n),
                    format!("email:{}", later.n),
                ];
                ev.extend(already_paid_questions(ds, inv, cfg));
                f.push(finding(
                    later.n,
                    0,
                    "ap_invoice_already_paid",
                    "high",
                    claimed,
                    ev,
                    format!("NF {} é a NF {inv}, já paga no vencimento", later_invoice_label(mail, inv)),
                    format!(
                        "O fornecedor cobra {} pela NF {inv}, mas o extrato mostra o pagamento de {} em {}.",
                        claimed.format_brl(),
                        line.amount.format_brl(),
                        ptbr::date(line.date)
                    ),
                ));
            }
            if let (Some(updated), Some(orig), Some(due), Some(at)) =
                (later.updated, original, original_due, mail.at)
            {
                if updated > orig && at.date() < due {
                    let mut ev = vec![format!("email:{}", later.n)];
                    if let Some(li) = pay_line {
                        ev.push(bank(ds.lines[li].n));
                    }
                    let increase = updated.checked_sub(orig).unwrap_or(Money::ZERO);
                    f.push(finding(
                        later.n,
                        1,
                        "ap_premature_recharge",
                        "medium",
                        increase,
                        ev,
                        format!(
                            "Recobrança com {}% enviada antes do vencimento",
                            ptbr::pct(ptbr::ratio_pct(increase, orig).round_dp(1))
                        ),
                        format!(
                            "A cobrança atualizada chegou em {}, antes do vencimento original de {}.",
                            ptbr::date(at.date()),
                            ptbr::date(due)
                        ),
                    ));
                }
            }
            let sender_changed = later.n != first.n && mail.from != first_mail.from;
            let method_changed = !later.method().is_empty()
                && !first.method().is_empty()
                && later.method() != first.method();
            if sender_changed || method_changed {
                let title = match (sender_changed, method_changed) {
                    (true, true) => "Remetente e forma de pagamento mudaram entre os e-mails",
                    (true, false) => "Remetente mudou entre os e-mails",
                    _ => "Forma de pagamento mudou entre os e-mails",
                };
                f.push(finding(
                    later.n,
                    3,
                    "ap_sender_or_bank_details_changed",
                    "high",
                    claimed,
                    vec![format!("email:{}", first.n), format!("email:{}", later.n)],
                    title.to_string(),
                    format!(
                        "O primeiro e-mail veio de {} ({}); o seguinte, de {} ({}). Confirme por telefone antes de pagar.",
                        first_mail.from,
                        if first.method().is_empty() { "sem forma de pagamento" } else { first.method() },
                        mail.from,
                        if later.method().is_empty() { "sem forma de pagamento" } else { later.method() }
                    ),
                ));
            }
            if let (Some((got, of)), Some(orig), Some(li)) = (later.delivery, original, pay_line) {
                let line = &ds.lines[li];
                if got < of && line.amount.abs_diff(orig) <= tol {
                    let missing = orig.mul_rate(
                        rust_decimal::Decimal::from(of - got) / rust_decimal::Decimal::from(of),
                    );
                    let mut ev = vec![format!("email:{}", later.n)];
                    ev.extend(hedged_approvals(ds, inv, cfg));
                    ev.push(bank(line.n));
                    f.push(finding(
                        later.n,
                        4,
                        "ap_three_way_match_failed",
                        "high",
                        missing,
                        ev,
                        format!("Pedido entregue {got} de {of} volumes, pago integralmente"),
                        format!(
                            "A NF {inv} foi paga por inteiro ({}) com {} de {of} volumes recebidos; a aprovação não confirmou a entrega.",
                            line.amount.format_brl(),
                            got
                        ),
                    ));
                }
            }
        }
    }

    for c in &claims {
        let mail = &ds.emails[c.n - 1];
        let reference = c.due.or_else(|| mail.at.map(|a| a.date())).or(period_end);
        for raw in &c.digit_lines {
            let Some(reference) = reference else { continue };
            let Ok(b) = parse_boleto_with_reference(raw, reference) else {
                continue;
            };
            let claimed = c.claimed();
            let value_mismatch =
                claimed.is_some_and(|m| b.value.is_some_and(|v| v != m.centavos()));
            let due_gap = c
                .due
                .zip(b.due_date)
                .is_some_and(|(d, bd)| (bd - d).num_days().abs() > cfg.ap.max_boleto_due_gap_days);
            if b.valid && !value_mismatch && !due_gap {
                continue;
            }
            let title = if b.valid {
                "Boleto não confere com a cobrança: possível fraude"
            } else {
                "Linha digitável inválida: possível fraude de boleto"
            };
            let mut problems = Vec::new();
            if !b.valid {
                problems.push("os dígitos verificadores não conferem".to_string());
            }
            if let (true, Some(v)) = (value_mismatch, b.value) {
                problems.push(format!(
                    "o valor codificado é {}",
                    Money::from_centavos(v).format_brl()
                ));
            }
            if let (true, Some(d)) = (due_gap, b.due_date) {
                problems.push(format!("o vencimento codificado é {}", ptbr::date(d)));
            }
            f.push(finding(
                c.n,
                2,
                "ap_boleto_invalid",
                "critical",
                claimed.unwrap_or(Money::ZERO),
                vec![format!("email:{}", c.n)],
                title.to_string(),
                format!(
                    "Na linha digitável enviada, {}. Não pague este boleto.",
                    problems.join("; ")
                ),
            ));
        }
    }

    for c in &claims {
        // Paid invoices and multi-e-mail threads are covered by the thread probes above.
        let is_paid = c.invoices.iter().any(|inv| paid.contains_key(inv));
        let in_thread = c
            .invoices
            .iter()
            .any(|i| threads.get(i).is_some_and(|t| t.len() > 1));
        if is_paid || in_thread {
            continue;
        }
        let mail = &ds.emails[c.n - 1];
        let Some(amount) = c.total.or(c.amount) else {
            continue;
        };
        let due = c.due.or_else(|| {
            let days = c.due_after_days?;
            mail.at.map(|a| a.date() + chrono::Duration::days(days))
        });
        let (Some(due), Some(end)) = (due, period_end) else {
            continue;
        };
        let paid_by_amount = cascade.lines.iter().enumerate().any(|(li, lo)| {
            ds.lines[li].side == crate::sources::Side::Debit
                && lo.kind != LineKind::Fee
                && ds.lines[li].amount.abs_diff(amount) <= tol
        });
        if due > end || paid_by_amount || approved_in_chat(ds, c, cfg) {
            continue;
        }
        let title = match c.cte_count {
            Some(n) => format!("{n} CT-e vencidos sem pagamento nesta conta e sem aprovação"),
            None => "Fatura vencida sem pagamento nesta conta e sem aprovação".to_string(),
        };
        f.push(finding(
            c.n,
            5,
            "ap_invoice_without_payment_or_approval",
            "high",
            amount,
            vec![format!("email:{}", c.n)],
            title,
            format!(
                "{} de {} venceu em {}: não há débito correspondente no extrato nem aprovação no grupo.",
                amount.format_brl(),
                mail.from,
                ptbr::date(due)
            ),
        ));
    }
    f
}

/// Instructions planted in e-mails or chat messages (prompt injection). They become findings
/// for a human; nothing in the pipeline ever acts on document text.
pub fn injection_findings(ds: &Dataset) -> Vec<Finding> {
    let mut f = Vec::new();
    let mut flag = |anchor: usize, evidence: String, text: &str| {
        let verdict = dm_guard::screen_request(text);
        if verdict.suspicious {
            let why: Vec<String> = verdict
                .categories()
                .iter()
                .map(|c| format!("{c:?}"))
                .collect();
            f.push(finding(
                anchor,
                9,
                "injected_instructions",
                "high",
                Money::ZERO,
                vec![evidence],
                "Instruções embutidas no documento: tratadas como dado, nunca executadas"
                    .to_string(),
                format!(
                    "O texto tenta dar ordens ao sistema ({}); revise a origem da mensagem.",
                    why.join(", ")
                ),
            ));
        }
    };
    for e in &ds.emails {
        flag(
            e.n,
            format!("email:{}", e.n),
            &format!("{}\n{}", e.subject, e.body),
        );
    }
    for (i, m) in ds.chat.iter().enumerate() {
        flag(ds.emails.len() + i + 1, chat_ref(m), &m.text);
    }
    f
}

/// How the later e-mail writes the invoice number (`055120`), for the finding title.
fn later_invoice_label(mail: &Email, canonical_number: &str) -> String {
    mail.folded
        .split(|c: char| !c.is_ascii_alphanumeric())
        .find(|t| {
            !t.is_empty()
                && t.chars().all(|c| c.is_ascii_digit())
                && canonical(t) == canonical_number
                && *t != canonical_number
        })
        .map_or_else(|| canonical_number.to_string(), ToString::to_string)
}
