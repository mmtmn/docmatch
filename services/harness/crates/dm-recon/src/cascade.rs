//! The deterministic matching cascade. Each stage only sees what earlier stages left
//! unexplained; lines are taken in statement order and a title's open balance shrinks as it
//! is paid, so no money is used twice.
//!
//! Credits: (1) boleto number in the document column, (2) tax id in the description,
//! (3) payer name — exact amount, service withholding, contractual fine, or a subset of the
//! customer's open titles — with ambiguous names resolved only by a unique amount,
//! (4) an amount that matches exactly one open title when the depositor is unidentified
//! (a proposal, never automatic), (5) unapplied. Debits: bank fees and supplier payments
//! whose description cites an invoice from the payables inbox.

use std::collections::BTreeSet;

use dm_core::Money;
use dm_match::subset_sum;
use regex::Regex;
use rust_decimal::Decimal;

use crate::config::EngineConfig;
use crate::names::{candidates, payer_text};
use crate::sources::{document_numbers, Dataset, Side};

/// What a line turned out to be.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LineKind {
    /// Money from a customer applied to titles.
    Receipt,
    /// A bank fee.
    Fee,
    /// A payment to a supplier.
    SupplierPayment,
    /// Money nobody could explain.
    Unapplied,
}

impl LineKind {
    /// Golden-format code.
    pub fn code(self) -> &'static str {
        match self {
            LineKind::Receipt => "receipt",
            LineKind::Fee => "fee",
            LineKind::SupplierPayment => "supplier_payment",
            LineKind::Unapplied => "unapplied",
        }
    }
}

/// Part of a line applied to one title.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Applied {
    /// Title index.
    pub title: usize,
    /// Cash this title received from the line (fine included).
    pub cash: Money,
    /// Principal settled.
    pub principal: Money,
    /// Fine received on top of the principal.
    pub fine: Money,
    /// Tax the customer withheld (settles principal without cash).
    pub withholding: Money,
}

/// How one bank line was explained.
#[derive(Clone, Debug, PartialEq)]
pub struct LineOutcome {
    /// Kind.
    pub kind: LineKind,
    /// Rule name.
    pub rule: &'static str,
    /// True for proposals that need confirmation.
    pub proposed: bool,
    /// Titles paid.
    pub applied: Vec<Applied>,
    /// Payables references (`AP-55120`).
    pub ap_refs: Vec<String>,
    /// Customer the line was identified with, even when nothing could be applied.
    pub payer: Option<usize>,
    /// Other customers the descriptor could also mean.
    pub competitors: Vec<usize>,
    /// Withholding rate (percent) found on a service title.
    pub withholding_rate: Option<Decimal>,
    /// True when the payer was identified by name only (no document, no tax id).
    pub payer_by_name: bool,
}

impl LineOutcome {
    fn new(kind: LineKind, rule: &'static str) -> Self {
        LineOutcome {
            kind,
            rule,
            proposed: false,
            applied: Vec::new(),
            ap_refs: Vec::new(),
            payer: None,
            competitors: Vec::new(),
            withholding_rate: None,
            payer_by_name: false,
        }
    }

    /// True for rules that identified the payer by name only.
    pub fn name_based(&self) -> bool {
        matches!(
            self.rule,
            "name_amount_due"
                | "name_amount"
                | "name_amount_fine"
                | "name_amount_withholding"
                | "fuzzy_name_unique_amount"
                | "subset_sum_same_customer"
        ) && self.payer_by_name
    }
}

/// Result of the cascade.
#[derive(Clone, Debug, Default)]
pub struct Cascade {
    /// One outcome per bank line, in statement order.
    pub lines: Vec<LineOutcome>,
    /// For each title, the title it duplicates (another ledger's copy).
    pub duplicate_of: Vec<Option<usize>>,
}

struct State<'a> {
    ds: &'a Dataset,
    cfg: &'a EngineConfig,
    tol: Money,
    open: Vec<Money>,
    out: Vec<Option<LineOutcome>>,
    /// Payer found by tax id but nothing applied: no amount-only guess for this line.
    identified: Vec<Option<usize>>,
}

impl State<'_> {
    fn eq(&self, a: Money, b: Money) -> bool {
        a.abs_diff(b) <= self.tol
    }

    fn open_titles_of(&self, entity: usize) -> Vec<usize> {
        let mut v: Vec<usize> = self.ds.entities[entity]
            .titles
            .iter()
            .copied()
            .filter(|t| self.open[*t].is_positive())
            .collect();
        v.sort_by_key(|t| (self.ds.titles[*t].due, *t));
        v
    }

    fn apply(&mut self, line: usize, mut outcome: LineOutcome) {
        for a in &outcome.applied {
            let settled = a.principal.saturating_add(a.withholding);
            let left = self.open[a.title]
                .checked_sub(settled)
                .unwrap_or(Money::ZERO);
            self.open[a.title] = if left.is_negative() {
                Money::ZERO
            } else {
                left
            };
        }
        outcome.applied.sort_by_key(|a| a.title);
        self.out[line] = Some(outcome);
    }

    fn receipt(rule: &'static str, applied: Vec<Applied>) -> LineOutcome {
        let mut o = LineOutcome::new(LineKind::Receipt, rule);
        o.applied = applied;
        o
    }

    fn whole(&self, title: usize, cash: Money) -> Applied {
        let principal = self.open[title];
        let fine = cash.checked_sub(principal).unwrap_or(Money::ZERO);
        Applied {
            title,
            cash,
            principal,
            fine: if fine.is_positive() {
                fine
            } else {
                Money::ZERO
            },
            withholding: Money::ZERO,
        }
    }

    /// Amount = open × (1 + fine [+ pro-rata interest]) for some open title of `titles`.
    fn with_fine(&self, titles: &[usize], amount: Money, date: chrono::NaiveDate) -> Option<usize> {
        let fine = self.cfg.fine_rate();
        let interest = self.cfg.interest_month();
        titles.iter().copied().find(|t| {
            let open = self.open[*t];
            let days = (date - self.ds.titles[*t].due).num_days().max(0);
            let with_interest = fine + interest * Decimal::from(days) / Decimal::from(30);
            self.eq(amount, open.saturating_add(open.mul_rate(fine)))
                || self.eq(amount, open.saturating_add(open.mul_rate(with_interest)))
        })
    }

    fn subset(&self, titles: &[usize], amount: Money) -> Option<Vec<usize>> {
        if titles.len() < 2
            || titles.len()
                > self
                    .cfg
                    .subset_max_titles
                    .min(dm_match::subset_sum::MAX_CANDIDATES)
        {
            return None;
        }
        let cands: Vec<(String, Money)> = titles
            .iter()
            .map(|t| (t.to_string(), self.open[*t]))
            .collect();
        subset_sum(&cands, amount, self.tol, 8)
            .into_iter()
            .find(|s| s.ids.len() >= 2)
            .map(|s| s.ids.iter().filter_map(|id| id.parse().ok()).collect())
    }
}

/// Runs the cascade over every bank line.
pub fn run(ds: &Dataset, ap_invoices: &BTreeSet<String>, cfg: &EngineConfig) -> Cascade {
    let duplicate_of = duplicates(ds);
    let mut st = State {
        ds,
        cfg,
        tol: Money::from_centavos(cfg.amount_tolerance_centavos.max(0)),
        open: ds
            .titles
            .iter()
            .enumerate()
            .map(|(i, t)| {
                if duplicate_of[i].is_some() {
                    Money::ZERO
                } else {
                    t.amount
                }
            })
            .collect(),
        out: vec![None; ds.lines.len()],
        identified: vec![None; ds.lines.len()],
    };
    for i in 0..ds.lines.len() {
        if ds.lines[i].side == Side::Debit {
            debit(&mut st, i, ap_invoices);
        }
    }
    let credits: Vec<usize> = (0..ds.lines.len())
        .filter(|i| ds.lines[*i].side == Side::Credit)
        .collect();
    for &i in &credits {
        by_document(&mut st, i);
    }
    for &i in &credits {
        if st.out[i].is_none() {
            by_tax_id(&mut st, i);
        }
    }
    let mut ambiguous_unresolved = BTreeSet::new();
    for &i in &credits {
        if st.out[i].is_none() && st.identified[i].is_none() && !by_name(&mut st, i) {
            ambiguous_unresolved.insert(i);
        }
    }
    for &i in &credits {
        if st.out[i].is_none() && st.identified[i].is_none() && !ambiguous_unresolved.contains(&i) {
            by_amount_only(&mut st, i);
        }
    }
    let lines = (0..ds.lines.len())
        .map(|i| {
            st.out[i].clone().unwrap_or_else(|| {
                let mut o = LineOutcome::new(LineKind::Unapplied, "none");
                o.payer = st.identified[i];
                o
            })
        })
        .collect();
    Cascade {
        lines,
        duplicate_of,
    }
}

/// Same tax id, amount and due date in two ledgers: the copy outside the main ledger (or in
/// the later file) is a duplicate.
fn duplicates(ds: &Dataset) -> Vec<Option<usize>> {
    let mut dup = vec![None; ds.titles.len()];
    for i in 0..ds.titles.len() {
        for j in 0..ds.titles.len() {
            let (a, b) = (&ds.titles[i], &ds.titles[j]);
            if i == j || a.ledger == b.ledger || dup[i].is_some() || dup[j].is_some() {
                continue;
            }
            let same = a.tax_id.is_some()
                && a.tax_id == b.tax_id
                && a.amount == b.amount
                && a.due == b.due;
            if !same {
                continue;
            }
            let keep_a = match (ds.ledgers[a.ledger].main, ds.ledgers[b.ledger].main) {
                (true, false) => true,
                (false, true) => false,
                _ => a.ledger < b.ledger,
            };
            if keep_a {
                dup[j] = Some(i);
            } else {
                dup[i] = Some(j);
            }
        }
    }
    dup
}

fn debit(st: &mut State<'_>, i: usize, ap_invoices: &BTreeSet<String>) {
    let line = &st.ds.lines[i];
    let words: Vec<&str> = line.folded.split_whitespace().collect();
    let is_fee = st.cfg.fee_patterns.iter().any(|p| {
        words
            .iter()
            .any(|w| *w == p.as_str() || (p.len() >= 5 && w.starts_with(p.as_str())))
    });
    if is_fee {
        st.out[i] = Some(LineOutcome::new(LineKind::Fee, "bank_fee_pattern"));
        return;
    }
    let refs: Vec<String> = document_numbers(&line.folded)
        .into_iter()
        .filter(|n| ap_invoices.contains(n))
        .map(|n| format!("AP-{n}"))
        .collect();
    if !refs.is_empty() {
        let mut o = LineOutcome::new(LineKind::SupplierPayment, "document_in_text");
        o.ap_refs = refs;
        st.out[i] = Some(o);
    }
}

fn by_document(st: &mut State<'_>, i: usize) {
    let line = &st.ds.lines[i];
    let mut docs: Vec<String> = line.document.iter().cloned().collect();
    docs.extend(
        document_numbers(&line.folded)
            .into_iter()
            .filter(|d| d.len() >= 6),
    );
    let hits: Vec<usize> = (0..st.ds.titles.len())
        .filter(|t| st.open[*t].is_positive())
        .filter(|t| {
            st.ds.titles[*t]
                .nosso_numero
                .as_ref()
                .is_some_and(|n| docs.contains(n))
        })
        .collect();
    let [t] = hits[..] else { return };
    let amount = line.amount;
    let open = st.open[t];
    let outcome = if st.eq(amount, open) {
        State::receipt("nosso_numero_exact", vec![st.whole(t, amount)])
    } else if amount < open {
        State::receipt(
            "nosso_numero_partial",
            vec![Applied {
                title: t,
                cash: amount,
                principal: amount,
                fine: Money::ZERO,
                withholding: Money::ZERO,
            }],
        )
    } else {
        State::receipt("nosso_numero_exact", vec![st.whole(t, amount)])
    };
    st.apply(i, outcome);
}

fn tax_ids_in(text: &str) -> Vec<String> {
    // CNPJ (14) or CPF (11) digits, masked or bare.
    let re = Regex::new(r"\d{2}\.?\d{3}\.?\d{3}/?\d{4}-?\d{2}|\d{3}\.?\d{3}\.?\d{3}-?\d{2}");
    let Ok(re) = re else { return Vec::new() };
    re.find_iter(text)
        .map(|m| {
            m.as_str()
                .chars()
                .filter(char::is_ascii_digit)
                .collect::<String>()
        })
        .collect()
}

fn by_tax_id(st: &mut State<'_>, i: usize) {
    let line = &st.ds.lines[i];
    let ids = tax_ids_in(&line.folded);
    let Some(entity) = st
        .ds
        .entities
        .iter()
        .position(|e| e.tax_id.as_ref().is_some_and(|t| ids.contains(t)))
    else {
        return;
    };
    let titles = st.open_titles_of(entity);
    let amount = line.amount;
    let date = line.date;
    if let Some(&t) = titles.iter().find(|t| st.eq(st.open[**t], amount)) {
        let mut o = State::receipt("tax_id_in_text", vec![st.whole(t, amount)]);
        o.payer = Some(entity);
        st.apply(i, o);
    } else if let Some(t) = st.with_fine(&titles, amount, date) {
        let mut o = State::receipt("tax_id_amount_fine", vec![st.whole(t, amount)]);
        o.payer = Some(entity);
        st.apply(i, o);
    } else if let Some(set) = st.subset(&titles, amount) {
        let applied = set.iter().map(|t| st.whole(*t, st.open[*t])).collect();
        let mut o = State::receipt("subset_sum_same_customer", applied);
        o.payer = Some(entity);
        st.apply(i, o);
    } else {
        st.identified[i] = Some(entity);
    }
}

fn is_service(kind: &str, cfg: &EngineConfig) -> bool {
    kind.split(|c: char| !c.is_ascii_alphanumeric())
        .any(|w| cfg.service_kinds.iter().any(|s| s == w))
}

/// Tries every rule for one identified customer.
fn for_entity(st: &State<'_>, i: usize, entity: usize) -> Option<LineOutcome> {
    let line = &st.ds.lines[i];
    let amount = line.amount;
    let titles = st.open_titles_of(entity);
    let exact: Vec<usize> = titles
        .iter()
        .copied()
        .filter(|t| st.eq(st.open[*t], amount))
        .collect();
    if let Some(&t) = exact
        .iter()
        .find(|t| st.ds.titles[**t].due == line.date)
        .or_else(|| exact.first())
    {
        let rule = if st.ds.titles[t].due == line.date {
            "name_amount_due"
        } else {
            "name_amount"
        };
        return Some(State::receipt(rule, vec![st.whole(t, amount)]));
    }
    for &t in &titles {
        if !is_service(&st.ds.titles[t].kind, st.cfg) {
            continue;
        }
        let open = st.open[t];
        for rate in &st.cfg.withholding_rates_pct {
            let net = open.checked_sub(open.percent(*rate)).unwrap_or(open);
            if st.eq(net, amount) {
                let mut o = State::receipt(
                    "name_amount_withholding",
                    vec![Applied {
                        title: t,
                        cash: amount,
                        principal: amount,
                        fine: Money::ZERO,
                        withholding: open.checked_sub(amount).unwrap_or(Money::ZERO),
                    }],
                );
                o.withholding_rate = Some(*rate);
                return Some(o);
            }
        }
    }
    if let Some(t) = st.with_fine(&titles, amount, line.date) {
        return Some(State::receipt(
            "name_amount_fine",
            vec![st.whole(t, amount)],
        ));
    }
    st.subset(&titles, amount).map(|set| {
        let applied = set.iter().map(|t| st.whole(*t, st.open[*t])).collect();
        State::receipt("subset_sum_same_customer", applied)
    })
}

/// Returns false when the descriptor named several customers and the amount could not
/// decide (the line must not then be guessed by amount alone).
fn by_name(st: &mut State<'_>, i: usize) -> bool {
    let Some(payer) = payer_text(&st.ds.lines[i].folded, st.cfg) else {
        return true;
    };
    let cands = candidates(&payer, st.ds, st.cfg);
    match cands.len() {
        0 => true,
        1 => {
            let entity = cands[0].entity;
            match for_entity(st, i, entity) {
                Some(mut o) => {
                    o.payer = Some(entity);
                    o.payer_by_name = true;
                    st.apply(i, o);
                }
                None => st.identified[i] = Some(entity),
            }
            true
        }
        _ => {
            let amount = st.ds.lines[i].amount;
            let hits: Vec<(usize, usize)> = cands
                .iter()
                .flat_map(|c| {
                    st.open_titles_of(c.entity)
                        .into_iter()
                        .map(move |t| (c.entity, t))
                })
                .filter(|(_, t)| st.eq(st.open[*t], amount))
                .collect();
            let [(entity, t)] = hits[..] else {
                return false;
            };
            let mut o = State::receipt("fuzzy_name_unique_amount", vec![st.whole(t, amount)]);
            o.payer = Some(entity);
            o.payer_by_name = true;
            o.competitors = cands
                .iter()
                .map(|c| c.entity)
                .filter(|e| *e != entity)
                .collect();
            st.apply(i, o);
            true
        }
    }
}

fn by_amount_only(st: &mut State<'_>, i: usize) {
    let amount = st.ds.lines[i].amount;
    let hits: Vec<usize> = (0..st.ds.titles.len())
        .filter(|t| st.open[*t].is_positive() && st.eq(st.open[*t], amount))
        .collect();
    if let [t] = hits[..] {
        let mut o = State::receipt("amount_only_unique", vec![st.whole(t, amount)]);
        o.proposed = true;
        st.apply(i, o);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tax_ids_masked_or_bare() {
        assert_eq!(
            tax_ids_in("TED RECEBIDA CNPJ 24680135000178"),
            vec!["24680135000178"]
        );
        assert_eq!(tax_ids_in("PIX 12.345.678/0001-95"), vec!["12345678000195"]);
        assert!(tax_ids_in("PAGAMENTO NF 55120").is_empty());
    }

    #[test]
    fn service_kinds_are_words() {
        let cfg = EngineConfig::default();
        assert!(is_service("SERVICO", &cfg));
        assert!(is_service("PRESTACAO DE SERVICOS", &cfg));
        assert!(!is_service("PRODUTO", &cfg));
        assert!(!is_service("", &cfg));
    }
}
