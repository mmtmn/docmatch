//! Column role inference from header words (pt-BR and English) and value shapes.

use dm_core::table::normalize_column_name;
use dm_core::{Column, ColumnRole};
use dm_normalize::{
    date_ptbr, money_ptbr, status_ptbr, tax_id, DateContext, PaymentStatus, TaxIdKind,
};
use dm_validators::Verdict;
use serde::{Deserialize, Serialize};

/// A column with the evidence behind its role.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ColumnGuess {
    /// The column.
    pub column: Column,
    /// Short explanation (`header:vencimento`, `values:date 0.95`).
    pub evidence: String,
}

/// Votes for the header language.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HeaderVotes {
    /// Portuguese words recognized.
    pub pt: usize,
    /// English words recognized.
    pub en: usize,
}

struct Vocab {
    word: &'static str,
    role: ColumnRole,
    subrole: Option<&'static str>,
    pt: bool,
}

const fn v(word: &'static str, role: ColumnRole, subrole: Option<&'static str>, pt: bool) -> Vocab {
    Vocab {
        word,
        role,
        subrole,
        pt,
    }
}

/// Header vocabulary, normalized form. Longer entries are tried first by the matcher.
const VOCAB: &[Vocab] = &[
    // amounts
    v("valor_pago", ColumnRole::Amount, Some("paid"), true),
    v("vlr_pago", ColumnRole::Amount, Some("paid"), true),
    v("valor_titulo", ColumnRole::Amount, Some("title"), true),
    v("valor_original", ColumnRole::Amount, Some("title"), true),
    v("valor_liquido", ColumnRole::Amount, Some("net"), true),
    v("valor_bruto", ColumnRole::Amount, Some("gross"), true),
    v("valor_total", ColumnRole::Amount, Some("total"), true),
    v("valor_nf", ColumnRole::Amount, Some("title"), true),
    v("valor_documento", ColumnRole::Amount, Some("title"), true),
    v("valor_juros", ColumnRole::Amount, Some("interest"), true),
    v("valor_multa", ColumnRole::Amount, Some("fine"), true),
    v("valor_desconto", ColumnRole::Amount, Some("discount"), true),
    v("valor", ColumnRole::Amount, None, true),
    v("vlr", ColumnRole::Amount, None, true),
    v("vl", ColumnRole::Amount, None, true),
    v("total", ColumnRole::Amount, Some("total"), true),
    v("montante", ColumnRole::Amount, None, true),
    v("credito", ColumnRole::Amount, Some("credit"), true),
    v("debito", ColumnRole::Amount, Some("debit"), true),
    v("saldo", ColumnRole::Amount, Some("balance"), true),
    v("juros", ColumnRole::Amount, Some("interest"), true),
    v("multa", ColumnRole::Amount, Some("fine"), true),
    v("desconto", ColumnRole::Amount, Some("discount"), true),
    v("paid_amount", ColumnRole::Amount, Some("paid"), false),
    v("amount_paid", ColumnRole::Amount, Some("paid"), false),
    v("total_amount", ColumnRole::Amount, Some("total"), false),
    v("net_amount", ColumnRole::Amount, Some("net"), false),
    v("gross_amount", ColumnRole::Amount, Some("gross"), false),
    v("amount", ColumnRole::Amount, None, false),
    v("amt", ColumnRole::Amount, None, false),
    v("value", ColumnRole::Amount, None, false),
    v("credit", ColumnRole::Amount, Some("credit"), false),
    v("debit", ColumnRole::Amount, Some("debit"), false),
    v("balance", ColumnRole::Amount, Some("balance"), false),
    v("price", ColumnRole::Amount, None, false),
    // dates
    v("data_de_vencimento", ColumnRole::Date, Some("due"), true),
    v("data_vencimento", ColumnRole::Date, Some("due"), true),
    v("dt_vencimento", ColumnRole::Date, Some("due"), true),
    v("vencimento", ColumnRole::Date, Some("due"), true),
    v("venc", ColumnRole::Date, Some("due"), true),
    v("vcto", ColumnRole::Date, Some("due"), true),
    v("data_de_emissao", ColumnRole::Date, Some("issue"), true),
    v("data_emissao", ColumnRole::Date, Some("issue"), true),
    v("dt_emissao", ColumnRole::Date, Some("issue"), true),
    v("emissao", ColumnRole::Date, Some("issue"), true),
    v("data_de_pagamento", ColumnRole::Date, Some("paid"), true),
    v("data_pagamento", ColumnRole::Date, Some("paid"), true),
    v("dt_pagamento", ColumnRole::Date, Some("paid"), true),
    v("pagamento", ColumnRole::Date, Some("paid"), true),
    v("pgto", ColumnRole::Date, Some("paid"), true),
    v("data_baixa", ColumnRole::Date, Some("paid"), true),
    v("baixa", ColumnRole::Date, Some("paid"), true),
    v("liquidacao", ColumnRole::Date, Some("paid"), true),
    v("data_lancamento", ColumnRole::Date, Some("posting"), true),
    v("dt_lancamento", ColumnRole::Date, Some("posting"), true),
    v("lancamento", ColumnRole::Date, Some("posting"), true),
    v("data_movimento", ColumnRole::Date, Some("posting"), true),
    v("movimento", ColumnRole::Date, Some("posting"), true),
    v("competencia", ColumnRole::Date, Some("competence"), true),
    v("data", ColumnRole::Date, None, true),
    v("dt", ColumnRole::Date, None, true),
    v("due_date", ColumnRole::Date, Some("due"), false),
    v("due", ColumnRole::Date, Some("due"), false),
    v("issue_date", ColumnRole::Date, Some("issue"), false),
    v("invoice_date", ColumnRole::Date, Some("issue"), false),
    v("payment_date", ColumnRole::Date, Some("paid"), false),
    v("paid_at", ColumnRole::Date, Some("paid"), false),
    v("paid_date", ColumnRole::Date, Some("paid"), false),
    v("posting_date", ColumnRole::Date, Some("posting"), false),
    v("posted", ColumnRole::Date, Some("posting"), false),
    v("transaction_date", ColumnRole::Date, Some("posting"), false),
    v("date", ColumnRole::Date, None, false),
    v("timestamp", ColumnRole::Date, None, false),
    // document ids
    v("chave_de_acesso", ColumnRole::DocumentId, Some("key"), true),
    v("chave_acesso", ColumnRole::DocumentId, Some("key"), true),
    v("chave", ColumnRole::DocumentId, Some("key"), true),
    v("numero_nf", ColumnRole::DocumentId, Some("nfe"), true),
    v("num_nf", ColumnRole::DocumentId, Some("nfe"), true),
    v("nro_nf", ColumnRole::DocumentId, Some("nfe"), true),
    v("nota_fiscal", ColumnRole::DocumentId, Some("nfe"), true),
    v("nfe", ColumnRole::DocumentId, Some("nfe"), true),
    v("nf", ColumnRole::DocumentId, Some("nfe"), true),
    v("nota", ColumnRole::DocumentId, Some("nfe"), true),
    v("numero_do_documento", ColumnRole::DocumentId, None, true),
    v("numero_documento", ColumnRole::DocumentId, None, true),
    v("num_documento", ColumnRole::DocumentId, None, true),
    v("nro_documento", ColumnRole::DocumentId, None, true),
    v("no_documento", ColumnRole::DocumentId, None, true),
    v("n_documento", ColumnRole::DocumentId, None, true),
    v("documento", ColumnRole::DocumentId, None, true),
    v("doc", ColumnRole::DocumentId, None, true),
    v("titulo", ColumnRole::DocumentId, Some("title"), true),
    v("duplicata", ColumnRole::DocumentId, Some("title"), true),
    v("fatura", ColumnRole::DocumentId, Some("title"), true),
    v("pedido", ColumnRole::DocumentId, Some("order"), true),
    v("pc", ColumnRole::DocumentId, Some("order"), true),
    v("oc", ColumnRole::DocumentId, Some("order"), true),
    v(
        "ordem_de_compra",
        ColumnRole::DocumentId,
        Some("order"),
        true,
    ),
    v(
        "seu_numero",
        ColumnRole::DocumentId,
        Some("seu_numero"),
        true,
    ),
    v("numero", ColumnRole::DocumentId, None, true),
    v("num", ColumnRole::DocumentId, None, true),
    v("nro", ColumnRole::DocumentId, None, true),
    v("invoice_number", ColumnRole::DocumentId, Some("nfe"), false),
    v("invoice_no", ColumnRole::DocumentId, Some("nfe"), false),
    v("invoice", ColumnRole::DocumentId, Some("nfe"), false),
    v("doc_number", ColumnRole::DocumentId, None, false),
    v("document_number", ColumnRole::DocumentId, None, false),
    v("document", ColumnRole::DocumentId, None, false),
    v("order_number", ColumnRole::DocumentId, Some("order"), false),
    v("order", ColumnRole::DocumentId, Some("order"), false),
    v("po_number", ColumnRole::DocumentId, Some("order"), false),
    v("po", ColumnRole::DocumentId, Some("order"), false),
    v("access_key", ColumnRole::DocumentId, Some("key"), false),
    v("reference", ColumnRole::DocumentId, None, false),
    v("ref", ColumnRole::DocumentId, None, false),
    v("number", ColumnRole::DocumentId, None, false),
    v("id", ColumnRole::DocumentId, None, false),
    // nosso número
    v("nosso_numero", ColumnRole::NossoNumero, None, true),
    v("nossonumero", ColumnRole::NossoNumero, None, true),
    v("nosso_num", ColumnRole::NossoNumero, None, true),
    v("nn", ColumnRole::NossoNumero, None, true),
    v("boleto", ColumnRole::NossoNumero, None, true),
    v("our_number", ColumnRole::NossoNumero, None, false),
    v("bank_slip_number", ColumnRole::NossoNumero, None, false),
    // tax ids
    v("cnpj_cpf", ColumnRole::TaxId, None, true),
    v("cpf_cnpj", ColumnRole::TaxId, None, true),
    v("cnpj", ColumnRole::TaxId, Some("cnpj"), true),
    v("cpf", ColumnRole::TaxId, Some("cpf"), true),
    v("inscricao", ColumnRole::TaxId, None, true),
    v("tax_id", ColumnRole::TaxId, None, false),
    v("taxid", ColumnRole::TaxId, None, false),
    v("vat", ColumnRole::TaxId, None, false),
    v("ein", ColumnRole::TaxId, None, false),
    // names
    v("razao_social", ColumnRole::Name, None, true),
    v("nome_fantasia", ColumnRole::Name, None, true),
    v("fornecedor", ColumnRole::Name, Some("supplier"), true),
    v("cliente", ColumnRole::Name, Some("customer"), true),
    v("favorecido", ColumnRole::Name, Some("payee"), true),
    v("beneficiario", ColumnRole::Name, Some("payee"), true),
    v("sacado", ColumnRole::Name, Some("payer"), true),
    v("pagador", ColumnRole::Name, Some("payer"), true),
    v("cedente", ColumnRole::Name, Some("payee"), true),
    v("empresa", ColumnRole::Name, None, true),
    v("nome", ColumnRole::Name, None, true),
    v("vendor", ColumnRole::Name, Some("supplier"), false),
    v("supplier", ColumnRole::Name, Some("supplier"), false),
    v("customer", ColumnRole::Name, Some("customer"), false),
    v("client", ColumnRole::Name, Some("customer"), false),
    v("payee", ColumnRole::Name, Some("payee"), false),
    v("payer", ColumnRole::Name, Some("payer"), false),
    v("beneficiary", ColumnRole::Name, Some("payee"), false),
    v("counterparty", ColumnRole::Name, None, false),
    v("company", ColumnRole::Name, None, false),
    v("name", ColumnRole::Name, None, false),
    // status
    v("paid_flag", ColumnRole::Status, Some("flag"), false),
    v("flag_pago", ColumnRole::Status, Some("flag"), true),
    v("pago", ColumnRole::Status, Some("flag"), true),
    v("quitado", ColumnRole::Status, Some("flag"), true),
    v("baixado", ColumnRole::Status, Some("flag"), true),
    v("liquidado", ColumnRole::Status, Some("flag"), true),
    v("situacao", ColumnRole::Status, None, true),
    v("sit", ColumnRole::Status, None, true),
    v("status", ColumnRole::Status, None, true),
    v("paid", ColumnRole::Status, Some("flag"), false),
    v("settled", ColumnRole::Status, Some("flag"), false),
    v("state", ColumnRole::Status, None, false),
    // kind
    v("tipo_lancamento", ColumnRole::Kind, None, true),
    v("tipo_de_lancamento", ColumnRole::Kind, None, true),
    v("tipo", ColumnRole::Kind, None, true),
    v("natureza", ColumnRole::Kind, None, true),
    v("especie", ColumnRole::Kind, None, true),
    v("operacao", ColumnRole::Kind, None, true),
    v("categoria", ColumnRole::Kind, None, true),
    v("transaction_type", ColumnRole::Kind, None, false),
    v("type", ColumnRole::Kind, None, false),
    v("kind", ColumnRole::Kind, None, false),
    v("category", ColumnRole::Kind, None, false),
    v("nature", ColumnRole::Kind, None, false),
    // free text
    v("historico", ColumnRole::FreeText, Some("descriptor"), true),
    v("descricao", ColumnRole::FreeText, Some("descriptor"), true),
    v("observacao", ColumnRole::FreeText, None, true),
    v("obs", ColumnRole::FreeText, None, true),
    v("complemento", ColumnRole::FreeText, None, true),
    v("detalhes", ColumnRole::FreeText, None, true),
    v("memo", ColumnRole::FreeText, Some("descriptor"), false),
    v("narrative", ColumnRole::FreeText, Some("descriptor"), false),
    v(
        "description",
        ColumnRole::FreeText,
        Some("descriptor"),
        false,
    ),
    v("notes", ColumnRole::FreeText, None, false),
    v("remarks", ColumnRole::FreeText, None, false),
    v("details", ColumnRole::FreeText, None, false),
];

/// Looks a normalized header up in the vocabulary: exact match first, then the longest
/// vocabulary word that is a whole-token prefix or suffix of the header.
fn header_match(normalized: &str) -> Option<&'static Vocab> {
    if let Some(exact) = VOCAB.iter().find(|e| e.word == normalized) {
        return Some(exact);
    }
    let tokens: Vec<&str> = normalized.split('_').collect();
    let mut best: Option<&'static Vocab> = None;
    for entry in VOCAB {
        let entry_tokens: Vec<&str> = entry.word.split('_').collect();
        let n = entry_tokens.len();
        if n > tokens.len() {
            continue;
        }
        let prefix = tokens[..n] == entry_tokens[..];
        let suffix = tokens[tokens.len() - n..] == entry_tokens[..];
        if (prefix || suffix) && best.is_none_or(|b| entry.word.len() > b.word.len()) {
            best = Some(entry);
        }
    }
    best
}

/// Counts header words recognized in each language.
pub fn header_language_votes(headers: &[&str]) -> HeaderVotes {
    let mut votes = HeaderVotes::default();
    for h in headers {
        let normalized = normalize_column_name(h);
        if let Some(entry) = header_match(&normalized) {
            if entry.pt {
                votes.pt += 1;
            } else {
                votes.en += 1;
            }
        }
    }
    votes
}

/// Value-shape evidence of a column.
#[derive(Clone, Copy, Debug, Default)]
struct Shape {
    money: f64,
    money_with_separator: f64,
    date: f64,
    tax_id: f64,
    doc_id: f64,
    nosso_numero: f64,
    status: f64,
    name: f64,
    free_text: f64,
    count: usize,
}

fn shape_of(values: &[&str]) -> Shape {
    let sample: Vec<&str> = values
        .iter()
        .map(|s| s.trim())
        .filter(|s| !s.is_empty())
        .take(100)
        .collect();
    let mut shape = Shape {
        count: sample.len(),
        ..Shape::default()
    };
    if sample.is_empty() {
        return shape;
    }
    let ctx = DateContext::default();
    let mut money = 0usize;
    let mut money_sep = 0usize;
    let mut date = 0usize;
    let mut tax = 0usize;
    let mut doc = 0usize;
    let mut nosso = 0usize;
    let mut status = 0usize;
    let mut name = 0usize;
    let mut free = 0usize;
    for s in &sample {
        let digits = s.chars().filter(char::is_ascii_digit).count();
        let letters = s.chars().filter(|c| c.is_alphabetic()).count();
        let words = s.split_whitespace().count();
        if money_ptbr(s).is_ok() {
            money += 1;
            if s.contains(',') || s.contains('.') || s.to_ascii_uppercase().contains("R$") {
                money_sep += 1;
            }
        }
        if date_ptbr(s, &ctx).is_ok() && digits >= 4 {
            date += 1;
        }
        let t = tax_id(s);
        if t.kind != TaxIdKind::Unknown && t.verdict == Verdict::Valid {
            tax += 1;
        }
        if letters <= 3 && (3..=12).contains(&digits) && digits * 2 >= s.len() && !s.contains(',') {
            doc += 1;
        }
        if letters == 0
            && (8..=25).contains(&digits)
            && s.chars()
                .all(|c| c.is_ascii_digit() || c == '-' || c == '/' || c == '.')
        {
            nosso += 1;
        }
        if status_ptbr(s) != PaymentStatus::Unknown {
            status += 1;
        }
        if letters >= 3 && letters > digits && words >= 1 && s.len() <= 80 {
            name += 1;
        }
        if letters >= 3 && (s.len() > 40 || words >= 6) {
            free += 1;
        }
    }
    let n = sample.len() as f64;
    shape.money = money as f64 / n;
    shape.money_with_separator = money_sep as f64 / n;
    shape.date = date as f64 / n;
    shape.tax_id = tax as f64 / n;
    shape.doc_id = doc as f64 / n;
    shape.nosso_numero = nosso as f64 / n;
    shape.status = status as f64 / n;
    shape.name = name as f64 / n;
    shape.free_text = free as f64 / n;
    shape
}

fn role_from_shape(shape: &Shape) -> Option<(ColumnRole, Option<&'static str>, f64, &'static str)> {
    if shape.count == 0 {
        return None;
    }
    if shape.tax_id >= 0.6 {
        return Some((ColumnRole::TaxId, None, shape.tax_id, "values:tax_id"));
    }
    if shape.date >= 0.7 {
        return Some((ColumnRole::Date, None, shape.date, "values:date"));
    }
    if shape.money_with_separator >= 0.6 {
        return Some((
            ColumnRole::Amount,
            None,
            shape.money_with_separator,
            "values:money",
        ));
    }
    if shape.nosso_numero >= 0.7 && shape.doc_id < 0.5 {
        return Some((
            ColumnRole::NossoNumero,
            None,
            shape.nosso_numero,
            "values:nosso_numero",
        ));
    }
    if shape.doc_id >= 0.7 {
        return Some((ColumnRole::DocumentId, None, shape.doc_id, "values:doc_id"));
    }
    if shape.status >= 0.8 {
        return Some((ColumnRole::Status, None, shape.status, "values:status"));
    }
    if shape.free_text >= 0.5 {
        return Some((
            ColumnRole::FreeText,
            None,
            shape.free_text,
            "values:free_text",
        ));
    }
    if shape.name >= 0.7 {
        return Some((ColumnRole::Name, None, shape.name, "values:name"));
    }
    None
}

/// Infers the role of every column from its header and a sample of its values.
///
/// Header words decide when they match the vocabulary; value shapes decide otherwise, and
/// override the header only for the unambiguous shapes (valid tax ids, dates).
pub fn infer_columns(headers: &[&str], sample_rows: &[Vec<&str>]) -> Vec<ColumnGuess> {
    let mut seen = std::collections::HashSet::new();
    headers
        .iter()
        .enumerate()
        .map(|(i, header)| {
            let header = header.trim();
            let mut name = if header.is_empty() {
                format!("col_{}", i + 1)
            } else {
                normalize_column_name(header)
            };
            // keep names unique
            let base = name.clone();
            let mut n = 2;
            while !seen.insert(name.clone()) {
                name = format!("{base}_{n}");
                n += 1;
            }
            let values: Vec<&str> = sample_rows
                .iter()
                .filter_map(|r| r.get(i).copied())
                .collect();
            let shape = shape_of(&values);
            let header_hit = if header.is_empty() {
                None
            } else {
                header_match(&base)
            };
            let by_values = role_from_shape(&shape);
            let (role, subrole, confidence, evidence) = match (header_hit, by_values) {
                (Some(h), Some((vr, _, vc, ve))) if vr == h.role => (
                    h.role,
                    h.subrole,
                    0.98,
                    format!("header:{};{ve} {vc:.2}", h.word),
                ),
                (Some(h), Some((vr, vs, vc, ve)))
                    if matches!(vr, ColumnRole::TaxId | ColumnRole::Date)
                        && vc >= 0.9
                        && h.role != ColumnRole::FreeText =>
                {
                    (vr, vs, 0.8, format!("{ve} {vc:.2} sobre header:{}", h.word))
                }
                (Some(h), _) => (h.role, h.subrole, 0.9, format!("header:{}", h.word)),
                (None, Some((vr, vs, vc, ve))) => {
                    (vr, vs, (0.5 + vc / 2.0).min(0.85), format!("{ve} {vc:.2}"))
                }
                (None, None) => (ColumnRole::Unknown, None, 0.0, "sem evidência".to_string()),
            };
            let mut column = Column {
                name,
                header: header.to_string(),
                role,
                subrole: subrole.map(ToString::to_string),
                confidence,
            };
            column.confidence = confidence.clamp(0.0, 1.0);
            ColumnGuess { column, evidence }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn guess(headers: &[&str], rows: &[&[&str]]) -> Vec<(ColumnRole, Option<String>)> {
        let rows: Vec<Vec<&str>> = rows.iter().map(|r| r.to_vec()).collect();
        infer_columns(headers, &rows)
            .into_iter()
            .map(|g| (g.column.role, g.column.subrole))
            .collect()
    }

    #[test]
    fn portuguese_headers() {
        let g = guess(
            &[
                "Data de Vencimento",
                "Fornecedor",
                "Nº Documento",
                "Valor",
                "Situação",
                "CNPJ",
                "Nosso Número",
                "Histórico",
            ],
            &[&[
                "05/03/2025",
                "Padaria Central",
                "088231",
                "1.234,56",
                "Aberto",
                "11.222.333/0001-81",
                "12345678901234",
                "Pagamento ref. NF 88231 do mês",
            ]],
        );
        assert_eq!(g[0], (ColumnRole::Date, Some("due".into())));
        assert_eq!(g[1], (ColumnRole::Name, Some("supplier".into())));
        assert_eq!(g[2], (ColumnRole::DocumentId, None));
        assert_eq!(g[3], (ColumnRole::Amount, None));
        assert_eq!(g[4], (ColumnRole::Status, None));
        assert_eq!(g[5], (ColumnRole::TaxId, Some("cnpj".into())));
        assert_eq!(g[6], (ColumnRole::NossoNumero, None));
        assert_eq!(g[7], (ColumnRole::FreeText, Some("descriptor".into())));
    }

    #[test]
    fn english_headers() {
        let g = guess(
            &[
                "due_date",
                "vendor",
                "invoice_number",
                "amount",
                "paid_flag",
            ],
            &[&["2025-03-05", "Padaria Central", "88231", "1234.56", "N"]],
        );
        assert_eq!(g[0], (ColumnRole::Date, Some("due".into())));
        assert_eq!(g[1], (ColumnRole::Name, Some("supplier".into())));
        assert_eq!(g[2], (ColumnRole::DocumentId, Some("nfe".into())));
        assert_eq!(g[3], (ColumnRole::Amount, None));
        assert_eq!(g[4], (ColumnRole::Status, Some("flag".into())));
        let votes = header_language_votes(&["due_date", "vendor", "amount"]);
        assert_eq!((votes.pt, votes.en), (0, 3));
        let votes = header_language_votes(&["Vencimento", "Fornecedor", "Valor"]);
        assert_eq!((votes.pt, votes.en), (3, 0));
    }

    #[test]
    fn values_decide_without_headers() {
        let rows: Vec<Vec<&str>> = vec![
            vec![
                "05/03/2025",
                "1.234,56",
                "11.222.333/0001-81",
                "088231",
                "Pago",
                "Empório Bela Vista",
            ],
            vec![
                "06/03/2025",
                "99,90",
                "45.723.174/0001-10",
                "088232",
                "Aberto",
                "Padaria Central",
            ],
            vec![
                "07/03/2025",
                "R$ 10,00",
                "11.222.333/0001-81",
                "088233",
                "Pago",
                "Distribuidora Nordeste",
            ],
        ];
        let g = infer_columns(&["", "", "", "", "", ""], &rows);
        let roles: Vec<ColumnRole> = g.iter().map(|c| c.column.role).collect();
        assert_eq!(
            roles,
            vec![
                ColumnRole::Date,
                ColumnRole::Amount,
                ColumnRole::TaxId,
                ColumnRole::DocumentId,
                ColumnRole::Status,
                ColumnRole::Name
            ]
        );
        assert_eq!(g[0].column.name, "col_1");
        assert!(g[0].column.confidence < 0.9);
    }

    #[test]
    fn values_override_misleading_header_for_tax_ids_and_dates() {
        let rows: Vec<Vec<&str>> = vec![
            vec!["11.222.333/0001-81", "05/03/2025"],
            vec!["45.723.174/0001-10", "06/03/2025"],
        ];
        let g = guess(
            &["Documento", "Referência"],
            &rows.iter().map(Vec::as_slice).collect::<Vec<_>>(),
        );
        assert_eq!(g[0].0, ColumnRole::TaxId);
        assert_eq!(g[1].0, ColumnRole::Date);
    }

    #[test]
    fn composite_headers_and_uniqueness() {
        let g = infer_columns(&["Valor", "Valor", "Data Venc.", "Vlr Pago (R$)"], &[]);
        assert_eq!(g[0].column.name, "valor");
        assert_eq!(g[1].column.name, "valor_2");
        assert_eq!(g[2].column.role, ColumnRole::Date);
        assert_eq!(g[2].column.subrole.as_deref(), Some("due"));
        assert_eq!(g[3].column.role, ColumnRole::Amount);
        assert_eq!(g[3].column.subrole.as_deref(), Some("paid"));
    }
}
