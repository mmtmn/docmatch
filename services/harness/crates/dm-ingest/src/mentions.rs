//! Mentions inside free text: document numbers, amounts, dates, bank details, boleto lines,
//! CNPJ/CPF, e-mail addresses and sender domains.

use std::sync::LazyLock;

use chrono::NaiveDate;
use dm_core::{EntityKind, EntityMention};
use dm_normalize::{date_ptbr, doc_number, money_ptbr, tax_id, DateContext, TaxIdKind};
use dm_validators::boleto::parse_boleto_with_reference;
use dm_validators::Verdict;
use regex::Regex;
use serde::{Deserialize, Serialize};

/// Kind of mention.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MentionKind {
    /// Invoice, order, bill or other document number.
    DocNumber,
    /// Monetary amount.
    Amount,
    /// Date (with or without year).
    Date,
    /// Bank name or code.
    Bank,
    /// Agency number.
    Agency,
    /// Account number.
    Account,
    /// Pix key.
    Pix,
    /// Boleto digit line (47 or 48 digits).
    Boleto,
    /// CNPJ.
    Cnpj,
    /// CPF.
    Cpf,
    /// E-mail address.
    Email,
    /// Domain of an e-mail address.
    Domain,
}

/// A mention found in text.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Mention {
    /// Kind.
    pub kind: MentionKind,
    /// Text as written.
    pub text: String,
    /// Normalized value (digits, centavos, ISO date, lowercase address...).
    pub normalized: String,
    /// Byte span in the input.
    pub span: (usize, usize),
    /// Extra detail (document prefix, boleto validity, bank code).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub extra: Option<String>,
}

impl Mention {
    /// Converts to the evidence-model mention.
    pub fn to_entity_mention(&self) -> EntityMention {
        let kind = match self.kind {
            MentionKind::DocNumber => EntityKind::Document,
            MentionKind::Amount => EntityKind::Amount,
            MentionKind::Date => EntityKind::Date,
            MentionKind::Bank | MentionKind::Agency | MentionKind::Account | MentionKind::Pix => {
                EntityKind::BankAccount
            }
            MentionKind::Boleto => EntityKind::Boleto,
            MentionKind::Cnpj => EntityKind::Company,
            MentionKind::Cpf => EntityKind::Person,
            MentionKind::Email | MentionKind::Domain => EntityKind::Email,
        };
        EntityMention {
            entity_id: None,
            kind,
            text: self.text.clone(),
            normalized: Some(self.normalized.clone()),
            span: Some(self.span),
        }
    }
}

static RE_DOC: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)\b(nfs-?e|nf-?e|nf|nota\s+fiscal|ct-?e|pedido\s+de\s+compra|ordem\s+de\s+compra|pedido|pc|oc|boleto|t[ií]tulo|duplicata|fatura|dup|fat|invoice|po)\b\.?\s*(?:n[º°o]?\.?\s*|#\s*|:\s*|-\s*)?(\d[\d./-]*\d|\d)").unwrap()
});
// The regex crate has no lookaround: the "reais" suffix is part of the match and the money
// parser strips it.
static RE_AMOUNT: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)(?:R\$\s?-?\d[\d.]*(?:,\d{1,2})?|\b\d{1,3}(?:\.\d{3})+,\d{2}\b|\b\d+,\d{2}\s?(?:reais|BRL)\b|\b\d+,\d{2}\b)").unwrap()
});
static RE_DATE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"\b(?:\d{4}-\d{2}-\d{2}|\d{1,2}/\d{1,2}(?:/\d{2,4})?)\b").unwrap()
});
static RE_BANK: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)\bbanco\s*:?\s*(\d{3}\b|do\s+brasil|ita[uú]|bradesco|santander|caixa|inter|nubank|sicoob|sicredi|safra|btg|c6|original|banrisul|mercado\s+pago|pagseguro|stone)").unwrap()
});
static RE_AGENCY: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)\bag(?:[êe]ncia)?\.?\s*:?\s*(\d{3,5}(?:-?\d)?)\b").unwrap());
static RE_ACCOUNT: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)\b(?:c/c|cc|conta(?:\s+corrente)?|conta)\.?\s*:?\s*(\d{3,13}(?:-?[\dxX])?)\b")
        .unwrap()
});
static RE_PIX: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)\b(?:chave\s+)?pix\s*:?\s*([A-Za-z0-9.@+_-]{6,})").unwrap());
static RE_BOLETO: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\b\d[\d. -]{44,70}\d\b").unwrap());
static RE_CNPJ: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\b\d{2}\.?\d{3}\.?\d{3}/?\d{4}-?\d{2}\b").unwrap());
static RE_CPF: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\b\d{3}\.?\d{3}\.?\d{3}-?\d{2}\b").unwrap());
static RE_EMAIL: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}").unwrap());

const BANK_CODES: [(&str, &str); 17] = [
    ("do brasil", "001"),
    ("itau", "341"),
    ("bradesco", "237"),
    ("santander", "033"),
    ("caixa", "104"),
    ("inter", "077"),
    ("nubank", "260"),
    ("sicoob", "756"),
    ("sicredi", "748"),
    ("safra", "422"),
    ("btg", "208"),
    ("c6", "336"),
    ("original", "212"),
    ("banrisul", "041"),
    ("mercado pago", "323"),
    ("pagseguro", "290"),
    ("stone", "197"),
];

fn overlaps(spans: &[(usize, usize)], span: (usize, usize)) -> bool {
    spans.iter().any(|(s, e)| span.0 < *e && *s < span.1)
}

/// Extracts every mention from a text. `ctx` completes dates without a year and dates the
/// boleto factor; `reference` is the date used for the boleto cycle (today when `None`).
pub fn extract_mentions(text: &str, ctx: &DateContext) -> Vec<Mention> {
    let mut out: Vec<Mention> = Vec::new();
    let mut taken: Vec<(usize, usize)> = Vec::new();
    let reference = ctx
        .reference_date
        .unwrap_or_else(|| chrono::Local::now().date_naive());

    // Longest and most specific first: boleto lines, CNPJ, CPF, e-mails.
    for m in RE_BOLETO.find_iter(text) {
        if let Some((end, digits)) = boleto_window(m.as_str()) {
            let extra = parse_boleto_with_reference(&digits, reference)
                .map(|b| if b.valid { "valid" } else { "invalid" }.to_string())
                .ok();
            push(
                &mut out,
                &mut taken,
                MentionKind::Boleto,
                &m.as_str()[..end],
                digits,
                (m.start(), m.start() + end),
                extra,
            );
        }
    }
    for m in RE_CNPJ.find_iter(text) {
        let t = tax_id(m.as_str());
        if t.kind == TaxIdKind::Cnpj {
            let extra = Some(verdict_label(t.verdict).to_string());
            push(
                &mut out,
                &mut taken,
                MentionKind::Cnpj,
                m.as_str(),
                t.digits,
                (m.start(), m.end()),
                extra,
            );
        }
    }
    for m in RE_CPF.find_iter(text) {
        let t = tax_id(m.as_str());
        if t.kind == TaxIdKind::Cpf && t.verdict == Verdict::Valid {
            push(
                &mut out,
                &mut taken,
                MentionKind::Cpf,
                m.as_str(),
                t.digits,
                (m.start(), m.end()),
                Some("valid".to_string()),
            );
        }
    }
    for m in RE_EMAIL.find_iter(text) {
        let normalized = m.as_str().to_lowercase();
        let domain = normalized.rsplit('@').next().unwrap_or("").to_string();
        push(
            &mut out,
            &mut taken,
            MentionKind::Email,
            m.as_str(),
            normalized,
            (m.start(), m.end()),
            Some(domain),
        );
    }
    for c in RE_DOC.captures_iter(text) {
        let whole = c.get(0).unwrap();
        let prefix = c
            .get(1)
            .map(|p| dm_normalize::fold_ascii(p.as_str()).replace(' ', "_"))
            .unwrap_or_default();
        let number = c.get(2).map_or("", |n| n.as_str());
        let canonical = doc_number(number).canonical;
        if canonical.is_empty() {
            continue;
        }
        push(
            &mut out,
            &mut taken,
            MentionKind::DocNumber,
            whole.as_str(),
            canonical,
            (whole.start(), whole.end()),
            Some(prefix),
        );
    }
    for m in RE_AMOUNT.find_iter(text) {
        if let Ok(money) = money_ptbr(m.as_str()) {
            push(
                &mut out,
                &mut taken,
                MentionKind::Amount,
                m.as_str(),
                money.centavos().to_string(),
                (m.start(), m.end()),
                None,
            );
        }
    }
    for m in RE_DATE.find_iter(text) {
        if let Ok(p) = date_ptbr(m.as_str(), ctx) {
            let extra = p.year_inferred.then(|| "year_inferred".to_string());
            push(
                &mut out,
                &mut taken,
                MentionKind::Date,
                m.as_str(),
                p.date.format("%Y-%m-%d").to_string(),
                (m.start(), m.end()),
                extra,
            );
        }
    }
    for c in RE_BANK.captures_iter(text) {
        let whole = c.get(0).unwrap();
        let raw = c.get(1).map_or("", |x| x.as_str());
        let folded = dm_normalize::fold_ascii(raw).to_lowercase();
        let code = if folded.chars().all(|c| c.is_ascii_digit()) {
            folded.clone()
        } else {
            BANK_CODES
                .iter()
                .find(|(name, _)| folded.contains(name))
                .map_or(folded.clone(), |(_, code)| (*code).to_string())
        };
        push(
            &mut out,
            &mut taken,
            MentionKind::Bank,
            whole.as_str(),
            code,
            (whole.start(), whole.end()),
            Some(raw.to_string()),
        );
    }
    for c in RE_AGENCY.captures_iter(text) {
        let whole = c.get(0).unwrap();
        let digits: String = c
            .get(1)
            .map_or("", |x| x.as_str())
            .chars()
            .filter(char::is_ascii_digit)
            .collect();
        push(
            &mut out,
            &mut taken,
            MentionKind::Agency,
            whole.as_str(),
            digits,
            (whole.start(), whole.end()),
            None,
        );
    }
    for c in RE_ACCOUNT.captures_iter(text) {
        let whole = c.get(0).unwrap();
        let value = c.get(1).map_or("", |x| x.as_str());
        let normalized: String = value
            .chars()
            .filter(char::is_ascii_alphanumeric)
            .collect::<String>()
            .to_uppercase();
        push(
            &mut out,
            &mut taken,
            MentionKind::Account,
            whole.as_str(),
            normalized,
            (whole.start(), whole.end()),
            None,
        );
    }
    // A Pix key is often an e-mail or a CNPJ already reported above, so it may overlap.
    for c in RE_PIX.captures_iter(text) {
        let whole = c.get(0).unwrap();
        let key = c
            .get(1)
            .map_or("", |x| x.as_str())
            .trim_end_matches(['.', ',']);
        out.push(Mention {
            kind: MentionKind::Pix,
            text: whole.as_str().to_string(),
            normalized: key.to_lowercase(),
            span: (whole.start(), whole.end()),
            extra: None,
        });
    }
    // Domains derived from e-mails are separate mentions so entity resolution can key on them.
    let domains: Vec<Mention> = out
        .iter()
        .filter(|m| m.kind == MentionKind::Email)
        .filter_map(|m| {
            m.extra.clone().map(|d| Mention {
                kind: MentionKind::Domain,
                text: m.text.clone(),
                normalized: d,
                span: m.span,
                extra: None,
            })
        })
        .collect();
    out.extend(domains);
    out.sort_by_key(|m| (m.span.0, m.span.1));
    out
}

/// The greedy boleto regex may absorb a trailing date or number; keep the shortest prefix
/// that holds exactly 47, 48 or 44 digits and is followed by a separator or the end.
fn boleto_window(matched: &str) -> Option<(usize, String)> {
    let mut digits = String::with_capacity(48);
    let mut candidate: Option<(usize, String)> = None;
    for (i, c) in matched.char_indices() {
        if c.is_ascii_digit() {
            digits.push(c);
            if matches!(digits.len(), 44 | 47 | 48) {
                let end = i + c.len_utf8();
                let boundary = matched[end..]
                    .chars()
                    .next()
                    .is_none_or(|n| !n.is_ascii_digit());
                if boundary {
                    candidate = Some((end, digits.clone()));
                }
            }
            if digits.len() > 48 {
                break;
            }
        }
    }
    candidate
}

fn verdict_label(v: Verdict) -> &'static str {
    match v {
        Verdict::Valid => "valid",
        Verdict::Invalid => "invalid",
        Verdict::Malformed => "malformed",
    }
}

fn push(
    out: &mut Vec<Mention>,
    taken: &mut Vec<(usize, usize)>,
    kind: MentionKind,
    text: &str,
    normalized: String,
    span: (usize, usize),
    extra: Option<String>,
) {
    if overlaps(taken, span) {
        return;
    }
    taken.push(span);
    out.push(Mention {
        kind,
        text: text.to_string(),
        normalized,
        span,
        extra,
    });
}

/// Reference date helper for callers that only have a year.
pub fn reference_for_year(year: i32) -> NaiveDate {
    NaiveDate::from_ymd_opt(year, 12, 31).unwrap_or(NaiveDate::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn kinds(text: &str) -> Vec<(MentionKind, String)> {
        let ctx = DateContext::with_reference_date(NaiveDate::from_ymd_opt(2025, 3, 31).unwrap());
        extract_mentions(text, &ctx)
            .into_iter()
            .map(|m| (m.kind, m.normalized))
            .collect()
    }

    #[test]
    fn document_numbers_amounts_and_dates() {
        let got = kinds("Oi, a NF 088231 (pedido PC-4521) de R$ 18.450,00 vence em 05/03, ok? Boleto nº 77 no valor de 1.234,56.");
        assert!(got.contains(&(MentionKind::DocNumber, "88231".into())));
        assert!(got.contains(&(MentionKind::DocNumber, "4521".into())));
        assert!(got.contains(&(MentionKind::DocNumber, "77".into())));
        assert!(got.contains(&(MentionKind::Amount, "1845000".into())));
        assert!(got.contains(&(MentionKind::Amount, "123456".into())));
        assert!(got.contains(&(MentionKind::Date, "2025-03-05".into())));
    }

    #[test]
    fn tax_ids_emails_and_bank_details() {
        let got = kinds("Fornecedor 11.222.333/0001-81, contato compras@example.com. Pagar no Banco Itaú ag 1234-5 c/c 12345-6, pix: financeiro@example.com");
        assert!(got.contains(&(MentionKind::Cnpj, "11222333000181".into())));
        assert!(got.contains(&(MentionKind::Email, "compras@example.com".into())));
        assert!(got.contains(&(MentionKind::Domain, "example.com".into())));
        assert!(got.contains(&(MentionKind::Bank, "341".into())));
        assert!(got.contains(&(MentionKind::Agency, "12345".into())));
        assert!(got.contains(&(MentionKind::Account, "123456".into())));
        assert!(got.contains(&(MentionKind::Pix, "financeiro@example.com".into())));
        let cpf = kinds("CPF 111.444.777-35 e CPF inválido 111.444.777-36");
        assert_eq!(
            cpf.iter().filter(|(k, _)| *k == MentionKind::Cpf).count(),
            1
        );
    }

    #[test]
    fn boleto_lines_in_text() {
        let line =
            dm_validators::compose_bank_line("237", 9, 1000, 184_500, "3381260007827139500006330")
                .unwrap();
        let pretty = format!(
            "{}.{} {}.{} {}.{} {} {}",
            &line[0..5],
            &line[5..10],
            &line[10..15],
            &line[15..21],
            &line[21..26],
            &line[26..32],
            &line[32..33],
            &line[33..47]
        );
        let text = format!("segue a linha digitável: {pretty} para pagamento");
        let ctx = DateContext::with_reference_date(NaiveDate::from_ymd_opt(2026, 1, 1).unwrap());
        let mentions = extract_mentions(&text, &ctx);
        let boleto = mentions
            .iter()
            .find(|m| m.kind == MentionKind::Boleto)
            .expect("boleto mention");
        assert_eq!(boleto.normalized, line);
        assert_eq!(boleto.extra.as_deref(), Some("valid"));
        // the digits inside the boleto line are not reported again as amounts or dates
        assert!(!mentions.iter().any(|m| m.kind != MentionKind::Boleto
            && m.span.0 >= boleto.span.0
            && m.span.1 <= boleto.span.1));
    }

    #[test]
    fn whatsapp_time_is_not_an_amount() {
        let got = kinds("[05/03 14:30] Ana: mandei 14,30 reais");
        assert_eq!(
            got.iter()
                .filter(|(k, _)| *k == MentionKind::Amount)
                .count(),
            1
        );
        assert!(got.contains(&(MentionKind::Date, "2025-03-05".into())));
    }

    #[test]
    fn entity_mention_conversion() {
        let m = Mention {
            kind: MentionKind::Cnpj,
            text: "x".into(),
            normalized: "1".into(),
            span: (0, 1),
            extra: None,
        };
        assert_eq!(m.to_entity_mention().kind, EntityKind::Company);
        assert_eq!(
            reference_for_year(2025),
            NaiveDate::from_ymd_opt(2025, 12, 31).unwrap()
        );
    }
}
