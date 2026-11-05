//! Receivables controls. Each probe is a pure function of the dataset, the cascade and the
//! title outcomes, and returns findings with evidence references and pt-BR text.
//!
//! Consolidation keeps one story per bank line: the "condition" findings
//! (`intercompany_receipt`, `paid_without_nosso_numero`) are only raised for lines no more
//! specific finding already covers, and a late title paid without fees gets its own finding
//! only when the uncharged fee is material and no finding about the same title already puts
//! it under review; every late title still counts in the `uncharged_late_fees` aggregate.

use std::collections::{BTreeMap, BTreeSet};

use dm_core::Money;
use dm_normalize::name_key;

use crate::cascade::{Cascade, LineKind};
use crate::config::EngineConfig;
use crate::ptbr;
use crate::sources::{Dataset, Side};
use crate::titles::{Status, TitleOutcome};

/// Ordering sections of the finding list.
pub mod section {
    /// Line-level findings, by line.
    pub const LINES: u8 = 0;
    /// Bank fees aggregate.
    pub const FEES: u8 = 1;
    /// Open titles.
    pub const OPEN: u8 = 2;
    /// Boleto hygiene.
    pub const HYGIENE: u8 = 3;
    /// Accounts payable.
    pub const PAYABLES: u8 = 5;
    /// Uncharged late fees aggregate.
    pub const LATE_FEES: u8 = 6;
    /// Master-data quality.
    pub const MASTER_DATA: u8 = 7;
}

/// A finding before codes are assigned.
#[derive(Clone, Debug, PartialEq)]
pub struct Finding {
    /// Ordering section.
    pub section: u8,
    /// Anchor within the section (line number, title index, e-mail number).
    pub anchor: usize,
    /// Order among findings of the same anchor.
    pub order: u8,
    /// Kind.
    pub kind: &'static str,
    /// Severity.
    pub severity: &'static str,
    /// Money at stake.
    pub amount: Money,
    /// Evidence references.
    pub evidence: Vec<String>,
    /// Title (pt-BR).
    pub title: String,
    /// Explanation (pt-BR).
    pub explanation: String,
}

impl Finding {
    /// True when the finding cites bank line `n`.
    pub fn cites_line(&self, n: usize) -> bool {
        self.evidence.contains(&bank(n))
    }

    /// True when the finding cites title `id`.
    pub fn cites_title(&self, id: &str) -> bool {
        self.evidence.contains(&format!("title:{id}"))
    }
}

/// `bank:<n>`.
pub fn bank(n: usize) -> String {
    format!("bank:{n}")
}

fn title_ref(ds: &Dataset, t: usize) -> String {
    format!("title:{}", ds.titles[t].id)
}

#[allow(clippy::too_many_arguments)]
fn finding(
    section: u8,
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
        section,
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

/// Kinds that already put a title under review (its late fee is then not reported apart).
const REVIEW_KINDS: [&str; 3] = [
    "ambiguous_name_match",
    "intercompany_receipt",
    "amount_only_proposal",
];

/// Every receivables finding.
pub fn ar_findings(
    ds: &Dataset,
    cascade: &Cascade,
    out: &[TitleOutcome],
    cfg: &EngineConfig,
) -> Vec<Finding> {
    let mut f = line_findings(ds, cascade, out);
    f.extend(master_duplicates(ds, cascade));
    f.extend(intercompany(ds, cascade, &f));
    f.extend(material_late_fees(ds, out, cfg, &f));
    f.extend(bank_fees(ds, cascade));
    f.extend(overdue_open(ds, out));
    f.extend(boleto_hygiene(ds, cascade, &f));
    f
}

fn line_findings(ds: &Dataset, cascade: &Cascade, out: &[TitleOutcome]) -> Vec<Finding> {
    let mut f = Vec::new();
    for (li, lo) in cascade.lines.iter().enumerate() {
        let line = &ds.lines[li];
        let n = line.n;
        if line.side != Side::Credit {
            continue;
        }
        let (article, channel) = ptbr::channel(&line.folded);
        if lo.applied.len() >= 2 {
            let mut ev = vec![bank(n)];
            ev.extend(lo.applied.iter().map(|a| title_ref(ds, a.title)));
            let docs: Vec<&str> = lo
                .applied
                .iter()
                .map(|a| ds.titles[a.title].reference.as_str())
                .collect();
            f.push(finding(
                section::LINES,
                n,
                0,
                "one_payment_many_titles",
                "info",
                Money::ZERO,
                ev,
                format!(
                    "{} {channel} paga {} notas",
                    ptbr::capitalize(article),
                    ptbr::count_fem(lo.applied.len())
                ),
                format!(
                    "O crédito de {} em {} quita de uma vez {}.",
                    line.amount.format_brl(),
                    ptbr::date(line.date),
                    docs.join(" e ")
                ),
            ));
        }
        for a in &lo.applied {
            let o = &out[a.title];
            let t = &ds.titles[a.title];
            if o.has("early") && o.lines.last() == Some(&n) {
                let early = -o.days_late.unwrap_or(0);
                f.push(finding(
                    section::LINES,
                    n,
                    1,
                    "early_payment",
                    "info",
                    Money::ZERO,
                    vec![bank(n), title_ref(ds, a.title)],
                    format!("Nota paga {} antes do vencimento", ptbr::days(early)),
                    format!(
                        "{} vencia em {} e foi paga em {}.",
                        t.reference,
                        ptbr::date(t.due),
                        ptbr::date(line.date)
                    ),
                ));
            }
            if o.status == Status::Partial {
                let ratio = ptbr::ratio_pct(a.cash, t.amount);
                let title = if ratio.fract().is_zero() {
                    format!("Pagamento parcial de exatamente {}%", ptbr::pct(ratio))
                } else {
                    format!("Pagamento parcial de {}%", ptbr::pct(ratio))
                };
                f.push(finding(
                    section::LINES,
                    n,
                    3,
                    "partial_payment",
                    "high",
                    o.open_amount,
                    vec![bank(n), title_ref(ds, a.title)],
                    title,
                    format!(
                        "{} de {} recebidos para {}; faltam {}.",
                        a.cash.format_brl(),
                        t.amount.format_brl(),
                        t.reference,
                        o.open_amount.format_brl()
                    ),
                ));
            }
            if a.withholding.is_positive() {
                let rate = lo.withholding_rate.unwrap_or_default();
                f.push(finding(
                    section::LINES,
                    n,
                    4,
                    "withholding_suspected",
                    "medium",
                    a.withholding,
                    vec![bank(n), title_ref(ds, a.title)],
                    format!(
                        "Diferença de {}% em nota de serviço: provável {} retido",
                        ptbr::pct(rate),
                        withheld_tax(rate)
                    ),
                    format!(
                        "{} é de serviço e entrou com {} a menos; peça o comprovante de retenção antes de baixar a diferença.",
                        t.reference,
                        a.withholding.format_brl()
                    ),
                ));
            }
            for (d, of) in cascade.duplicate_of.iter().enumerate() {
                if *of == Some(a.title) {
                    f.push(finding(
                        section::LINES,
                        n,
                        5,
                        "duplicate_title_across_erps",
                        "high",
                        ds.titles[d].amount,
                        vec![title_ref(ds, a.title), title_ref(ds, d), bank(n)],
                        "Mesmo título nos dois ERPs; contas a receber superestimado".to_string(),
                        format!(
                            "{} ({}) e {} ({}) têm o mesmo CNPJ, valor e vencimento; só um pagamento entrou.",
                            t.reference,
                            ds.ledgers[t.ledger].key,
                            ds.titles[d].reference,
                            ds.ledgers[ds.titles[d].ledger].key
                        ),
                    ));
                }
            }
            if a.fine.is_positive() {
                let rate = ptbr::ratio_pct(a.fine, a.principal);
                f.push(finding(
                    section::LINES,
                    n,
                    6,
                    "overpayment_fine_received",
                    "low",
                    a.fine,
                    vec![bank(n), title_ref(ds, a.title)],
                    format!(
                        "Recebido {}% a mais: multa por atraso a contabilizar como receita financeira",
                        ptbr::pct(rate.round_dp(1))
                    ),
                    format!(
                        "{} entrou com {} acima do valor de face.",
                        t.reference,
                        a.fine.format_brl()
                    ),
                ));
            }
        }
        if lo.rule == "fuzzy_name_unique_amount" {
            if let Some(a) = lo.applied.first() {
                let o = &out[a.title];
                let mut ev = vec![bank(n), title_ref(ds, a.title)];
                for &c in &lo.competitors {
                    if let Some(t) = closest_title(ds, c, line.amount) {
                        ev.push(title_ref(ds, t));
                    }
                }
                let late = o.days_late.filter(|_| o.has("late"));
                let title = match late {
                    Some(d) => format!(
                        "Nome ambíguo resolvido pelo valor; {} de atraso sem encargos",
                        ptbr::days(d)
                    ),
                    None => "Nome ambíguo resolvido pelo valor".to_string(),
                };
                f.push(finding(
                    section::LINES,
                    n,
                    7,
                    "ambiguous_name_match",
                    "low",
                    o.fee_estimate.unwrap_or(Money::ZERO),
                    ev,
                    title,
                    format!(
                        "\"{}\" serve para mais de um cliente; só {} tinha título aberto de {}.",
                        line.description,
                        ds.titles[a.title].customer,
                        line.amount.format_brl()
                    ),
                ));
            }
        }
        if lo.rule == "amount_only_unique" {
            if let Some(a) = lo.applied.first() {
                f.push(finding(
                    section::LINES,
                    n,
                    9,
                    "amount_only_proposal",
                    "medium",
                    line.amount,
                    vec![bank(n), title_ref(ds, a.title)],
                    "Depósito não identificado: proposta por valor único, precisa de confirmação"
                        .to_string(),
                    format!(
                        "Só {} tem saldo aberto de {}; confirme com o cliente antes de baixar.",
                        ds.titles[a.title].reference,
                        line.amount.format_brl()
                    ),
                ));
            }
        }
        if lo.kind == LineKind::Unapplied {
            let title = if lo.payer.is_some() {
                "Recebimento sem título em aberto: adiantamento de cliente"
            } else {
                "Crédito sem identificação nem título correspondente"
            };
            f.push(finding(
                section::LINES,
                n,
                11,
                "unapplied_cash",
                "medium",
                line.amount,
                vec![bank(n)],
                title.to_string(),
                format!(
                    "{} em {} (\"{}\") não corresponde a nenhum título aberto.",
                    line.amount.format_brl(),
                    ptbr::date(line.date),
                    line.description
                ),
            ));
        }
    }
    f
}

fn withheld_tax(rate: rust_decimal::Decimal) -> &'static str {
    let r = rate.normalize().to_string();
    match r.as_str() {
        "1.5" => "IRRF",
        "4.65" => "PIS/COFINS/CSLL",
        "6.15" => "IRRF e PIS/COFINS/CSLL",
        _ => "ISS",
    }
}

/// The title of `entity` whose amount is closest to `amount` (ties: earliest due).
fn closest_title(ds: &Dataset, entity: usize, amount: Money) -> Option<usize> {
    ds.entities[entity]
        .titles
        .iter()
        .copied()
        .min_by_key(|t| (ds.titles[*t].amount.abs_diff(amount), ds.titles[*t].due, *t))
}

/// Same tax id written as different names in different ledgers.
fn master_duplicates(ds: &Dataset, cascade: &Cascade) -> Vec<Finding> {
    let mut f = Vec::new();
    for e in &ds.entities {
        let Some(tax) = &e.tax_id else { continue };
        let mut by_ledger: BTreeMap<usize, BTreeSet<Vec<String>>> = BTreeMap::new();
        for &t in &e.titles {
            by_ledger
                .entry(ds.titles[t].ledger)
                .or_default()
                .insert(name_key(&ds.titles[t].customer).tokens);
        }
        if by_ledger.len() < 2 {
            continue;
        }
        let all: BTreeSet<&Vec<String>> = by_ledger.values().flatten().collect();
        if all.len() < 2 {
            continue;
        }
        let mut ev: Vec<String> = e.titles.iter().map(|t| title_ref(ds, *t)).collect();
        let mut lines: Vec<usize> = Vec::new();
        for (li, lo) in cascade.lines.iter().enumerate() {
            if lo.name_based() && lo.applied.iter().any(|a| e.titles.contains(&a.title)) {
                lines.push(ds.lines[li].n);
            }
        }
        ev.extend(lines.iter().map(|n| bank(*n)));
        let names: BTreeSet<&str> = e
            .titles
            .iter()
            .map(|t| ds.titles[*t].customer.as_str())
            .collect();
        let (sec, anchor) = lines
            .first()
            .map_or((section::MASTER_DATA, 0), |n| (section::LINES, *n));
        f.push(finding(
            sec,
            anchor,
            10,
            "customer_master_duplicate",
            "low",
            Money::ZERO,
            ev,
            "Mesmo CNPJ com nomes diferentes nos dois cadastros".to_string(),
            format!(
                "O CNPJ {tax} aparece como {}; unifique o cadastro para o casamento por nome não depender do CNPJ.",
                names.into_iter().map(|n| format!("\"{n}\"")).collect::<Vec<_>>().join(" e ")
            ),
        ));
    }
    f
}

fn intercompany(ds: &Dataset, cascade: &Cascade, existing: &[Finding]) -> Vec<Finding> {
    let mut f = Vec::new();
    for (li, lo) in cascade.lines.iter().enumerate() {
        let n = ds.lines[li].n;
        if lo.kind != LineKind::Receipt || lo.applied.is_empty() {
            continue;
        }
        let foreign = lo
            .applied
            .iter()
            .all(|a| !ds.ledgers[ds.titles[a.title].ledger].main);
        if !foreign || existing.iter().any(|x| x.cites_line(n)) {
            continue;
        }
        let first = lo.applied[0].title;
        let label = &ds.ledgers[ds.titles[first].ledger].label;
        let mut ev = vec![bank(n)];
        ev.extend(lo.applied.iter().map(|a| title_ref(ds, a.title)));
        let total = lo.applied.iter().fold(Money::ZERO, |s, a| {
            s.saturating_add(ds.titles[a.title].amount)
        });
        f.push(finding(
            section::LINES,
            n,
            8,
            "intercompany_receipt",
            "medium",
            total,
            ev,
            format!("Recebível da {label} caiu na conta principal"),
            format!(
                "{} é um título da {label} e foi pago na conta da operação principal: registre o repasse entre as empresas.",
                ds.titles[first].reference
            ),
        ));
    }
    f
}

fn material_late_fees(
    ds: &Dataset,
    out: &[TitleOutcome],
    cfg: &EngineConfig,
    existing: &[Finding],
) -> Vec<Finding> {
    let threshold = Money::from_decimal(cfg.late_fee_materiality_brl);
    let mut f = Vec::new();
    for (ti, o) in out.iter().enumerate() {
        let Some(fee) = o.fee_estimate else { continue };
        let t = &ds.titles[ti];
        let reviewed = existing
            .iter()
            .any(|x| REVIEW_KINDS.contains(&x.kind) && x.cites_title(&t.id));
        if fee < threshold || reviewed {
            continue;
        }
        let Some(&n) = o.lines.last() else { continue };
        let title = match (o.effective_due, o.business_days_late) {
            (Some(_), Some(b)) => format!(
                "Vencimento em {}; paga {} atrasada sem encargos",
                ptbr::weekday(t.due),
                ptbr::business_days(b)
            ),
            _ => format!(
                "Paga {} atrasada sem encargos",
                ptbr::days(o.days_late.unwrap_or(0))
            ),
        };
        f.push(finding(
            section::LINES,
            n,
            2,
            "paid_late_no_fee",
            "low",
            fee,
            vec![bank(n), title_ref(ds, ti)],
            title,
            format!(
                "Multa de {}% e juros de {}% ao mês não cobrados em {}: cerca de {}.",
                ptbr::pct(cfg.fine_rate_pct),
                ptbr::pct(cfg.interest_month_pct),
                t.reference,
                fee.format_brl()
            ),
        ));
    }
    f
}

fn bank_fees(ds: &Dataset, cascade: &Cascade) -> Vec<Finding> {
    let lines: Vec<usize> = cascade
        .lines
        .iter()
        .enumerate()
        .filter(|(_, lo)| lo.kind == LineKind::Fee)
        .map(|(li, _)| li)
        .collect();
    if lines.is_empty() {
        return Vec::new();
    }
    let total = lines
        .iter()
        .fold(Money::ZERO, |s, li| s.saturating_add(ds.lines[*li].amount));
    vec![finding(
        section::FEES,
        0,
        0,
        "unbooked_bank_fees",
        "low",
        total,
        lines.iter().map(|li| bank(ds.lines[*li].n)).collect(),
        "Tarifas bancárias sem lançamento".to_string(),
        format!(
            "{} em tarifas debitadas no extrato precisam de lançamento como despesa bancária.",
            total.format_brl()
        ),
    )]
}

fn overdue_open(ds: &Dataset, out: &[TitleOutcome]) -> Vec<Finding> {
    out.iter()
        .enumerate()
        .filter(|(_, o)| o.status == Status::Open && o.has("overdue"))
        .map(|(ti, o)| {
            let t = &ds.titles[ti];
            finding(
                section::OPEN,
                ti,
                0,
                "overdue_open",
                "medium",
                o.open_amount,
                vec![title_ref(ds, ti)],
                format!(
                    "Título vencido há {} sem cobrança",
                    ptbr::days(o.days_late.unwrap_or(0))
                ),
                format!(
                    "{} de {} venceu em {} e não tem pagamento no extrato.",
                    t.reference,
                    t.customer,
                    ptbr::date(t.due)
                ),
            )
        })
        .collect()
}

fn boleto_hygiene(ds: &Dataset, cascade: &Cascade, existing: &[Finding]) -> Vec<Finding> {
    let mut f = Vec::new();
    for (li, lo) in cascade.lines.iter().enumerate() {
        let n = ds.lines[li].n;
        if !lo.name_based() || existing.iter().any(|x| x.cites_line(n)) {
            continue;
        }
        let (_, channel) = ptbr::channel(&ds.lines[li].folded);
        for a in lo
            .applied
            .iter()
            .filter(|a| ds.titles[a.title].nosso_numero.is_some())
        {
            f.push(finding(
                section::HYGIENE,
                n,
                0,
                "paid_without_nosso_numero",
                "info",
                Money::ZERO,
                vec![bank(n), title_ref(ds, a.title)],
                format!("Pago por {channel}, sem nosso número no extrato"),
                format!(
                    "{} tinha boleto ({}), mas o cliente pagou por {channel} identificado só pelo nome.",
                    ds.titles[a.title].reference,
                    ds.titles[a.title].nosso_numero_raw.clone().unwrap_or_default()
                ),
            ));
        }
    }
    f
}

/// Aggregates computed after everything else: uncharged late fees and tax-id quality.
pub fn aggregate_findings(ds: &Dataset, out: &[TitleOutcome]) -> Vec<Finding> {
    let mut f = Vec::new();
    let late: Vec<usize> = (0..out.len())
        .filter(|t| out[*t].fee_estimate.is_some())
        .collect();
    if !late.is_empty() {
        let total = late.iter().fold(Money::ZERO, |s, t| {
            s.saturating_add(out[*t].fee_estimate.unwrap_or(Money::ZERO))
        });
        f.push(finding(
            section::LATE_FEES,
            0,
            0,
            "uncharged_late_fees",
            "low",
            total,
            late.iter().map(|t| title_ref(ds, *t)).collect(),
            format!(
                "{} títulos pagos com atraso sem multa nem juros",
                ptbr::capitalize(&ptbr::count_masc(late.len()))
            ),
            format!(
                "Somando multa e juros contratuais, cerca de {} deixaram de ser cobrados.",
                total.format_brl()
            ),
        ));
    }
    f.extend(invalid_tax_ids(ds));
    f
}

fn invalid_tax_ids(ds: &Dataset) -> Vec<Finding> {
    let mut first_by_id: BTreeMap<&str, usize> = BTreeMap::new();
    let mut order: Vec<&str> = Vec::new();
    for (ti, t) in ds.titles.iter().enumerate() {
        if let Some(id) = t.tax_id.as_deref() {
            if !first_by_id.contains_key(id) {
                first_by_id.insert(id, ti);
                order.push(id);
            }
        }
    }
    let invalid: Vec<&str> = order
        .iter()
        .copied()
        .filter(|id| ds.titles[first_by_id[id]].tax_id_invalid)
        .collect();
    if invalid.is_empty() {
        return Vec::new();
    }
    let kind_word = if invalid.iter().all(|id| id.len() == 14) {
        "CNPJs"
    } else {
        "documentos"
    };
    if invalid.len() == order.len() {
        // One finding. Evidence, independent of row order: per ledger (file order), among the
        // titles whose id no earlier ledger has, the one with the smallest reference.
        let mut ev = Vec::new();
        let mut seen: BTreeSet<&str> = BTreeSet::new();
        for li in 0..ds.ledgers.len() {
            let pick = ds
                .titles
                .iter()
                .enumerate()
                .filter(|(_, t)| t.ledger == li)
                .filter(|(_, t)| t.tax_id.as_deref().is_some_and(|id| !seen.contains(id)))
                .min_by_key(|(ti, t)| (dm_normalize::fold_ascii(&t.reference), *ti))
                .map(|(ti, _)| ti);
            if let Some(ti) = pick {
                ev.push(title_ref(ds, ti));
            }
            seen.extend(
                ds.titles
                    .iter()
                    .filter(|t| t.ledger == li)
                    .filter_map(|t| t.tax_id.as_deref()),
            );
        }
        return vec![finding(
            section::MASTER_DATA,
            1,
            0,
            "invalid_tax_ids",
            "info",
            Money::ZERO,
            ev,
            format!("Todos os {} {kind_word} falham no dígito verificador", order.len()),
            "Nenhum documento dos cadastros passa na conferência do dígito verificador: os cadastros parecem fictícios ou corrompidos.".to_string(),
        )];
    }
    invalid
        .iter()
        .map(|id| {
            let ev = ds
                .titles
                .iter()
                .enumerate()
                .filter(|(_, t)| t.tax_id.as_deref() == Some(*id))
                .map(|(ti, _)| title_ref(ds, ti))
                .collect();
            finding(
                section::MASTER_DATA,
                first_by_id[id] + 1,
                0,
                "invalid_tax_ids",
                "low",
                Money::ZERO,
                ev,
                format!("Documento {id} falha no dígito verificador"),
                "Confira o cadastro do cliente: o dígito verificador não confere.".to_string(),
            )
        })
        .collect()
}
