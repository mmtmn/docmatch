//! CNAB bank files: FEBRABAN 240 (collection return/remittance and payments) and the 400
//! collection return of the major banks.
//!
//! Both layouts produce the same *collection* table (one row per título movement: nosso
//! número, seu número, payer, due date, face value, amount paid, charges, dates, movement), so
//! the reconciliation reads a 240 and a 400 file the same way. FEBRABAN 240 payment batches
//! (segments A/B/J) produce a separate *payments* table.
//!
//! Positions are 1-based and inclusive, as in the bank manuals. Amounts carry two implied
//! decimals; dates are `DDMMAAAA` (240) or `DDMMAA` (400). Every parser is total: a malformed
//! record becomes a warning, never a panic.

mod c240;
mod c400;

use chrono::NaiveDate;
use dm_core::{Column, ColumnRole, Money, Record as TableRecord, Table, Value};
use serde::{Deserialize, Serialize};

use crate::IngestError;

pub use c240::parse_cnab240;
pub use c400::{layout_banks, parse_cnab400};

/// CNAB layout, by record length.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CnabLayout {
    /// 240-character records (FEBRABAN multi-bank).
    Cnab240,
    /// 400-character records (legacy per-bank layouts).
    Cnab400,
}

/// Detects the layout from the record lengths (every non-empty line must have the same
/// length, 240 or 400, ignoring line endings).
pub fn detect_cnab_layout(bytes: &[u8]) -> Option<CnabLayout> {
    let text = decode(bytes);
    let mut lens = text
        .lines()
        .map(|l| l.trim_end_matches('\r'))
        .filter(|l| !l.trim().is_empty())
        .map(|l| l.chars().count());
    let first = lens.next()?;
    if !lens.all(|l| l == first) {
        return None;
    }
    match first {
        240 => Some(CnabLayout::Cnab240),
        400 => Some(CnabLayout::Cnab400),
        _ => None,
    }
}

/// True for the collection table (títulos de cobrança) of a CNAB file, as opposed to the
/// payments table: it is the one that names the payer (`sacado`) and the nosso número.
pub fn is_collection_table(table: &Table) -> bool {
    table
        .columns
        .iter()
        .any(|c| c.role == ColumnRole::NossoNumero)
        && table
            .columns
            .iter()
            .any(|c| c.role == ColumnRole::Name && c.subrole.as_deref() == Some("customer"))
}

/// The collection table with one row per título instead of one per movement: a return that
/// confirms an entry (02) and then changes its due date (14), or a liquidation followed by its
/// fee record (28), describes one título. Rows are grouped by nosso número (seu número when the
/// layout has none). A título liquidated, written off or rejected in any movement takes that
/// row; otherwise the last movement wins, so a changed due date is the one kept. Fee records
/// never make a título on their own. Each kept row keeps its original row number, so evidence
/// still points to the line of the file.
pub fn titles_view(table: &Table) -> Table {
    let col = |name: &str| table.column_index(name);
    let (Some(nosso), Some(seu), Some(situation)) =
        (col("nosso_numero"), col("seu_numero"), col("situacao"))
    else {
        return table.clone();
    };
    let text = |r: &TableRecord, c: usize| {
        r.values
            .get(c)
            .and_then(Value::as_text)
            .unwrap_or_default()
            .to_string()
    };
    let mut groups: Vec<(String, Vec<&TableRecord>)> = Vec::new();
    for r in &table.rows {
        let key = match (text(r, nosso), text(r, seu)) {
            (n, _) if !n.is_empty() => format!("n:{n}"),
            (_, s) if !s.is_empty() => format!("s:{s}"),
            _ => format!("row:{}", r.row),
        };
        match groups.iter_mut().find(|(k, _)| *k == key) {
            Some((_, rows)) => rows.push(r),
            None => groups.push((key, vec![r])),
        }
    }
    let mut out = Table::new(
        table.name.clone(),
        table.source_id.clone(),
        table.columns.clone(),
    );
    for (_, rows) in groups {
        let movements: Vec<&TableRecord> = rows
            .iter()
            .copied()
            .filter(|r| text(r, situation) != Situation::Tarifa.label())
            .collect();
        let closed = [Situation::Liquidado, Situation::Baixado]
            .map(Situation::label)
            .to_vec();
        let pick = movements
            .iter()
            .rev()
            .find(|r| closed.contains(&text(r, situation).as_str()))
            .or_else(|| {
                movements
                    .iter()
                    .rev()
                    .find(|r| text(r, situation) != Situation::Rejeitado.label())
            })
            .or_else(|| movements.last());
        let Some(pick) = pick else {
            continue;
        };
        let mut values = pick.values.clone();
        // A later movement may leave payer fields blank: fill them from earlier rows.
        for (c, v) in values.iter_mut().enumerate() {
            if v.is_null() {
                if let Some(earlier) = rows
                    .iter()
                    .rev()
                    .find_map(|r| r.values.get(c).filter(|x| !x.is_null()).cloned())
                {
                    *v = earlier;
                }
            }
        }
        out.rows.push(TableRecord {
            row: pick.row,
            values,
        });
    }
    out
}

/// CNAB files are ASCII in practice; anything else is read as windows-1252 (a superset of the
/// ISO-8859-1 some banks declare), so a stray accent never breaks the column positions.
fn decode(bytes: &[u8]) -> String {
    match std::str::from_utf8(bytes) {
        Ok(s) => s.to_string(),
        Err(_) => encoding_rs::WINDOWS_1252.decode(bytes).0.into_owned(),
    }
}

/// One record as characters, padded with blanks to the layout length so that a record whose
/// trailing blanks were stripped by an editor still reads at the right positions.
struct Record {
    /// One-based line number in the file.
    line: usize,
    chars: Vec<char>,
}

impl Record {
    /// Characters `start..=end` (1-based, inclusive), trimmed.
    fn text(&self, start: usize, end: usize) -> String {
        self.raw(start, end).trim().to_string()
    }

    /// Characters `start..=end` (1-based, inclusive), untrimmed.
    fn raw(&self, start: usize, end: usize) -> String {
        if start == 0 || end < start {
            return String::new();
        }
        self.chars
            .iter()
            .skip(start - 1)
            .take(end - start + 1)
            .collect()
    }

    fn at(&self, pos: usize) -> char {
        pos.checked_sub(1)
            .and_then(|i| self.chars.get(i))
            .copied()
            .unwrap_or(' ')
    }
}

/// Splits the text into fixed-length records. Short records are padded; longer ones are kept
/// as they are (the caller reports them).
fn records(text: &str, len: usize) -> (Vec<Record>, usize, usize) {
    let mut out = Vec::new();
    let mut padded = 0;
    let mut long = 0;
    for (i, raw) in text.split('\n').enumerate() {
        let line = raw.trim_end_matches('\r');
        if line.trim().is_empty() {
            continue;
        }
        let mut chars: Vec<char> = line.chars().collect();
        if chars.len() < len {
            padded += 1;
            chars.resize(len, ' ');
        } else if chars.len() > len {
            long += 1;
            continue;
        }
        out.push(Record { line: i + 1, chars });
    }
    (out, padded, long)
}

/// Parses a numeric field with two implied decimals. Blank means "not informed".
fn money_field(raw: &str) -> Option<Money> {
    let s = raw.trim();
    if s.is_empty() || !s.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    // At most 18 digits fit an i64 of centavos with room to spare; CNAB fields are ≤ 15.
    let digits = s.trim_start_matches('0');
    if digits.len() > 17 {
        return None;
    }
    let centavos: i64 = if digits.is_empty() {
        0
    } else {
        digits.parse().ok()?
    };
    Some(Money::from_centavos(centavos))
}

/// `DDMMAAAA` (or `DDMMAA` with `short`); zeros and blanks mean "no date".
fn date_field(raw: &str, short: bool) -> Result<Option<NaiveDate>, ()> {
    let s = raw.trim();
    if s.is_empty() || s.chars().all(|c| c == '0') {
        return Ok(None);
    }
    let expected = if short { 6 } else { 8 };
    if s.len() != expected || !s.chars().all(|c| c.is_ascii_digit()) {
        return Err(());
    }
    let day: u32 = s[0..2].parse().map_err(|_| ())?;
    let month: u32 = s[2..4].parse().map_err(|_| ())?;
    let year: i32 = if short {
        // Two-digit years: CNAB 400 files are all from this century in practice; 70..99
        // stays in the 1900s for the old files some banks still ship as samples.
        let yy: i32 = s[4..6].parse().map_err(|_| ())?;
        if yy < 70 {
            2000 + yy
        } else {
            1900 + yy
        }
    } else {
        s[4..8].parse().map_err(|_| ())?
    };
    NaiveDate::from_ymd_opt(year, month, day)
        .map(Some)
        .ok_or(())
}

/// Tax id from a CNAB inscription: type (`1` CPF, `2` CNPJ) and a zero-padded number.
fn inscription(kind: char, number: &str) -> Option<String> {
    let digits: String = number.chars().filter(char::is_ascii_digit).collect();
    if digits.is_empty() || digits.chars().all(|c| c == '0') {
        return None;
    }
    let keep = match kind {
        '1' => 11,
        '2' => 14,
        // Unknown type: a CNPJ when it does not fit a CPF.
        _ => {
            if digits.trim_start_matches('0').len() > 11 {
                14
            } else {
                11
            }
        }
    };
    let start = digits.len().saturating_sub(keep);
    Some(digits[start..].to_string())
}

/// Counts of problems found while reading, turned into pt-BR warnings at the end.
#[derive(Default)]
struct Problems {
    bad_dates: usize,
    padded: usize,
    long: usize,
    other: Vec<String>,
}

impl Problems {
    fn date(&mut self, raw: &str, short: bool) -> Option<NaiveDate> {
        date_field(raw, short).unwrap_or_else(|()| {
            self.bad_dates += 1;
            None
        })
    }

    fn into_warnings(self, layout: usize) -> Vec<String> {
        let mut out = self.other;
        if self.padded > 0 {
            out.push(format!(
                "{} registro(s) com menos de {layout} posições (completados com brancos)",
                self.padded
            ));
        }
        if self.long > 0 {
            out.push(format!(
                "{} registro(s) com mais de {layout} posições ignorados",
                self.long
            ));
        }
        if self.bad_dates > 0 {
            out.push(format!(
                "{} data(s) inválida(s) deixadas em branco",
                self.bad_dates
            ));
        }
        out
    }
}

/// What a movement means for the título, in the words the reconciliation reads as status.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
enum Situation {
    Aberto,
    Liquidado,
    Baixado,
    Rejeitado,
    Tarifa,
    Outro,
}

impl Situation {
    fn label(self) -> &'static str {
        match self {
            Situation::Aberto => "Aberto",
            Situation::Liquidado => "Liquidado",
            Situation::Baixado => "Baixado",
            Situation::Rejeitado => "Rejeitado",
            Situation::Tarifa => "Tarifa",
            Situation::Outro => "Outro",
        }
    }
}

/// One título movement of a collection file (240 segments T+U or P+Q, or a 400 detail).
#[derive(Default)]
struct CollectionRow {
    line: usize,
    bank: String,
    nosso_numero: String,
    seu_numero: String,
    payer: String,
    payer_tax_id: Option<String>,
    due: Option<NaiveDate>,
    issued: Option<NaiveDate>,
    face_value: Option<Money>,
    paid: Option<Money>,
    interest: Option<Money>,
    discount: Option<Money>,
    rebate: Option<Money>,
    iof: Option<Money>,
    fee: Option<Money>,
    other_expenses: Option<Money>,
    other_credits: Option<Money>,
    net: Option<Money>,
    occurred: Option<NaiveDate>,
    credited: Option<NaiveDate>,
    movement_code: String,
    movement: String,
    situation: Option<Situation>,
    species: String,
    company_ref: String,
    reason: String,
}

fn text_value(s: &str) -> Value {
    Value::from(s.trim())
}

fn money_value(m: Option<Money>) -> Value {
    m.map_or(Value::Null, Value::Money)
}

fn date_value(d: Option<NaiveDate>) -> Value {
    d.map_or(Value::Null, Value::Date)
}

fn collection_table(name: &str, source_id: &str, rows: &[CollectionRow]) -> Table {
    // Column order matters to consumers that take the first column of a role: the face
    // value is the first amount and the seu número the first document id.
    let columns = vec![
        Column::new("Nosso número", ColumnRole::NossoNumero),
        Column::new("Seu número", ColumnRole::DocumentId).with_subrole("seu_numero"),
        Column::new("Sacado", ColumnRole::Name).with_subrole("customer"),
        Column::new("Inscrição do sacado", ColumnRole::TaxId),
        Column::new("Vencimento", ColumnRole::Date).with_subrole("due"),
        Column::new("Valor do título", ColumnRole::Amount).with_subrole("title"),
        Column::new("Valor pago", ColumnRole::Amount).with_subrole("paid"),
        Column::new("Juros e multa", ColumnRole::Amount).with_subrole("interest"),
        Column::new("Desconto", ColumnRole::Amount).with_subrole("discount"),
        Column::new("Abatimento", ColumnRole::Amount).with_subrole("rebate"),
        Column::new("IOF", ColumnRole::Amount).with_subrole("iof"),
        Column::new("Tarifa", ColumnRole::Amount).with_subrole("fee"),
        Column::new("Outras despesas", ColumnRole::Amount).with_subrole("expenses"),
        Column::new("Outros créditos", ColumnRole::Amount).with_subrole("other_credits"),
        Column::new("Valor líquido", ColumnRole::Amount).with_subrole("net"),
        Column::new("Data da ocorrência", ColumnRole::Date).with_subrole("occurrence"),
        Column::new("Data do crédito", ColumnRole::Date).with_subrole("credit"),
        Column::new("Emissão", ColumnRole::Date).with_subrole("issue"),
        Column::new("Situação", ColumnRole::Status),
        Column::new("Código de movimento", ColumnRole::Unknown).with_subrole("movement"),
        Column::new("Movimento", ColumnRole::FreeText).with_subrole("movement"),
        Column::new("Espécie", ColumnRole::Kind),
        Column::new("Uso da empresa", ColumnRole::FreeText).with_subrole("company_ref"),
        Column::new("Motivo", ColumnRole::Unknown).with_subrole("reason"),
        Column::new("Banco", ColumnRole::Unknown).with_subrole("bank"),
        Column::new("Linha", ColumnRole::Unknown).with_subrole("line"),
    ];
    let mut table = Table::new(name, source_id, columns);
    for r in rows {
        let values = vec![
            text_value(&r.nosso_numero),
            text_value(&r.seu_numero),
            text_value(&r.payer),
            r.payer_tax_id.clone().map_or(Value::Null, Value::Text),
            date_value(r.due),
            money_value(r.face_value),
            money_value(r.paid),
            money_value(r.interest),
            money_value(r.discount),
            money_value(r.rebate),
            money_value(r.iof),
            money_value(r.fee),
            money_value(r.other_expenses),
            money_value(r.other_credits),
            money_value(r.net),
            date_value(r.occurred),
            date_value(r.credited),
            date_value(r.issued),
            r.situation
                .map_or(Value::Null, |s| Value::Text(s.label().to_string())),
            text_value(&r.movement_code),
            text_value(&r.movement),
            text_value(&r.species),
            text_value(&r.company_ref),
            text_value(&r.reason),
            text_value(&r.bank),
            Value::Integer(i64::try_from(r.line).unwrap_or(i64::MAX)),
        ];
        // The value count matches the columns by construction.
        let _ = table.push_row(values);
    }
    table
}

/// One payment of a FEBRABAN 240 payment batch (segment A with B, or J with J-52).
#[derive(Default)]
struct PaymentRow {
    line: usize,
    segment: char,
    payee: String,
    payee_tax_id: Option<String>,
    payee_account: String,
    seu_numero: String,
    bank_ref: String,
    barcode: String,
    scheduled: Option<NaiveDate>,
    amount: Option<Money>,
    effective: Option<NaiveDate>,
    effective_amount: Option<Money>,
    due: Option<NaiveDate>,
    face_value: Option<Money>,
    discount: Option<Money>,
    interest: Option<Money>,
    occurrences: String,
    situation: String,
}

fn payments_table(name: &str, source_id: &str, rows: &[PaymentRow]) -> Table {
    let columns = vec![
        Column::new("Favorecido", ColumnRole::Name).with_subrole("payee"),
        Column::new("Inscrição do favorecido", ColumnRole::TaxId),
        Column::new("Seu número", ColumnRole::DocumentId).with_subrole("seu_numero"),
        Column::new("Número no banco", ColumnRole::DocumentId).with_subrole("bank_ref"),
        Column::new("Código de barras", ColumnRole::DocumentId).with_subrole("barcode"),
        Column::new("Data do pagamento", ColumnRole::Date).with_subrole("paid"),
        Column::new("Valor do pagamento", ColumnRole::Amount).with_subrole("paid"),
        Column::new("Data efetiva", ColumnRole::Date).with_subrole("effective"),
        Column::new("Valor efetivo", ColumnRole::Amount).with_subrole("effective"),
        Column::new("Vencimento", ColumnRole::Date).with_subrole("due"),
        Column::new("Valor do documento", ColumnRole::Amount).with_subrole("title"),
        Column::new("Desconto e abatimento", ColumnRole::Amount).with_subrole("discount"),
        Column::new("Mora e multa", ColumnRole::Amount).with_subrole("interest"),
        Column::new("Conta do favorecido", ColumnRole::FreeText).with_subrole("account"),
        Column::new("Situação", ColumnRole::Status),
        Column::new("Ocorrências", ColumnRole::Unknown).with_subrole("occurrences"),
        Column::new("Segmento", ColumnRole::Kind).with_subrole("segment"),
        Column::new("Linha", ColumnRole::Unknown).with_subrole("line"),
    ];
    let mut table = Table::new(name, source_id, columns);
    for r in rows {
        let values = vec![
            text_value(&r.payee),
            r.payee_tax_id.clone().map_or(Value::Null, Value::Text),
            text_value(&r.seu_numero),
            text_value(&r.bank_ref),
            text_value(&r.barcode),
            date_value(r.scheduled),
            money_value(r.amount),
            date_value(r.effective),
            money_value(r.effective_amount),
            date_value(r.due),
            money_value(r.face_value),
            money_value(r.discount),
            money_value(r.interest),
            text_value(&r.payee_account),
            text_value(&r.situation),
            text_value(&r.occurrences),
            Value::Text(r.segment.to_string()),
            Value::Integer(i64::try_from(r.line).unwrap_or(i64::MAX)),
        ];
        let _ = table.push_row(values);
    }
    table
}

/// The parsed tables of a CNAB file and the problems found, in pt-BR.
#[derive(Debug, Default)]
pub struct CnabParsed {
    /// Collection and/or payments tables.
    pub tables: Vec<Table>,
    /// Non-fatal problems.
    pub warnings: Vec<String>,
}

fn unrecognized(msg: &str) -> IngestError {
    IngestError::Unrecognized(msg.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn layout_detection_and_field_helpers() {
        let c240 = format!("{}\r\n{}\r\n", "0".repeat(240), "1".repeat(240));
        assert_eq!(
            detect_cnab_layout(c240.as_bytes()),
            Some(CnabLayout::Cnab240)
        );
        let c400 = format!("{}\n", "0".repeat(400));
        assert_eq!(
            detect_cnab_layout(c400.as_bytes()),
            Some(CnabLayout::Cnab400)
        );
        assert_eq!(detect_cnab_layout(b"abc\n"), None);
        assert_eq!(detect_cnab_layout(b""), None);
        assert_eq!(
            money_field("000000002430000"),
            Some(Money::from_centavos(2_430_000))
        );
        assert_eq!(money_field("000000000000000"), Some(Money::ZERO));
        assert_eq!(money_field("   "), None);
        assert_eq!(money_field("12a"), None);
        assert_eq!(
            date_field("10072026", false),
            Ok(NaiveDate::from_ymd_opt(2026, 7, 10))
        );
        assert_eq!(
            date_field("100726", true),
            Ok(NaiveDate::from_ymd_opt(2026, 7, 10))
        );
        assert_eq!(date_field("00000000", false), Ok(None));
        assert_eq!(date_field("        ", false), Ok(None));
        assert_eq!(date_field("31022026", false), Err(()));
        assert_eq!(date_field("1007202", false), Err(()));
        assert_eq!(
            inscription('2', "021456789000123"),
            Some("21456789000123".to_string())
        );
        assert_eq!(
            inscription('1', "000012345678909"),
            Some("12345678909".to_string())
        );
        assert_eq!(inscription('0', "000000000000000"), None);
    }

    #[test]
    fn titles_view_keeps_one_row_per_titulo() {
        let row = |nosso: &str, situation: Situation, due: u32, paid: Option<i64>| CollectionRow {
            nosso_numero: nosso.to_string(),
            seu_numero: format!("NF {nosso}"),
            payer: if due == 10 {
                "PADARIA".to_string()
            } else {
                String::new()
            },
            due: NaiveDate::from_ymd_opt(2026, 7, due),
            face_value: Some(Money::from_centavos(1_000)),
            paid: paid.map(Money::from_centavos),
            situation: Some(situation),
            ..CollectionRow::default()
        };
        let t = collection_table(
            "x",
            "s",
            &[
                row("1", Situation::Aberto, 10, None),
                row("1", Situation::Aberto, 20, None),
                row("2", Situation::Aberto, 10, None),
                row("2", Situation::Liquidado, 11, Some(1_000)),
                row("2", Situation::Tarifa, 11, None),
                row("3", Situation::Tarifa, 10, None),
                row("4", Situation::Rejeitado, 10, None),
            ],
        );
        let v = titles_view(&t);
        assert_eq!(v.len(), 3);
        let col = |n: &str| v.column_index(n).unwrap();
        // Título 1: the changed due date wins and the payer comes from the first movement.
        assert_eq!(v.rows[0].row, 1);
        assert_eq!(
            v.value(0, col("vencimento")).and_then(Value::as_date),
            NaiveDate::from_ymd_opt(2026, 7, 20)
        );
        assert_eq!(
            v.value(0, col("sacado")).unwrap().as_text(),
            Some("PADARIA")
        );
        // Título 2: liquidated; the fee record does not make another título.
        assert_eq!(
            v.value(1, col("situacao")).unwrap().as_text(),
            Some("Liquidado")
        );
        assert_eq!(v.rows[1].row, 3);
        // Título 3 had only a fee record; título 4 stays, rejected.
        assert_eq!(
            v.value(2, col("situacao")).unwrap().as_text(),
            Some("Rejeitado")
        );
    }

    #[test]
    fn short_records_are_padded_and_long_ones_dropped() {
        let text = format!(
            "{}\n{}\n\n{}\n",
            "0".repeat(238),
            "1".repeat(240),
            "2".repeat(241)
        );
        let (recs, padded, long) = records(&text, 240);
        assert_eq!(recs.len(), 2);
        assert_eq!((padded, long), (1, 1));
        assert_eq!(recs[0].chars.len(), 240);
        assert_eq!(recs[1].line, 2);
        assert_eq!(recs[0].at(240), ' ');
        assert_eq!(recs[1].text(1, 3), "111");
        assert_eq!(recs[1].raw(0, 3), "");
    }
}
