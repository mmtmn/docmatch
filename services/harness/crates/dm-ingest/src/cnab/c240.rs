//! FEBRABAN CNAB 240.
//!
//! Source: FEBRABAN, "Layout Padrão de Arquivos CNAB 240", versão 10 (10.7 to 10.11); every
//! position read here is unchanged since version 08.4, so files from older layouts read the
//! same. Segments read:
//!
//! * collection return (retorno de cobrança): **T** + **U**, merged per título;
//! * collection remittance (remessa de cobrança): **P** + **Q** (R and S are optional
//!   extensions with discounts and messages, skipped);
//! * payments (pagamentos): **A** (+ **B** for the payee's tax id) and **J** (+ **J-52** for
//!   the beneficiary's tax id). Segment B fields after position 032 changed with the Pix
//!   layout (10.9+), so only the inscription is read from it.
//!
//! Other segments (C, N, O, W, Y, Z, ...) are counted and reported, never guessed.

use std::collections::BTreeMap;

use super::{
    collection_table, inscription, money_field, payments_table, records, unrecognized, CnabParsed,
    CollectionRow, PaymentRow, Problems, Record, Situation,
};
use crate::IngestError;

/// FEBRABAN 240 return movement codes (código de movimento retorno, cobrança).
fn return_movement(code: &str) -> (&'static str, Situation) {
    match code {
        "02" => ("Entrada confirmada", Situation::Aberto),
        "03" => ("Entrada rejeitada", Situation::Rejeitado),
        "04" => ("Transferência de carteira/entrada", Situation::Aberto),
        "05" => ("Transferência de carteira/baixa", Situation::Baixado),
        "06" => ("Liquidação", Situation::Liquidado),
        "07" => (
            "Confirmação do recebimento da instrução de desconto",
            Situation::Aberto,
        ),
        "08" => ("Confirmação do cancelamento do desconto", Situation::Aberto),
        "09" => ("Baixa", Situation::Baixado),
        "11" => ("Títulos em carteira (em ser)", Situation::Aberto),
        "12" => ("Confirmação da instrução de abatimento", Situation::Aberto),
        "13" => (
            "Confirmação do cancelamento de abatimento",
            Situation::Aberto,
        ),
        "14" => ("Confirmação da alteração de vencimento", Situation::Aberto),
        "17" => (
            "Liquidação após baixa ou de título não registrado",
            Situation::Liquidado,
        ),
        "19" => ("Confirmação da instrução de protesto", Situation::Aberto),
        "20" => ("Confirmação da sustação de protesto", Situation::Aberto),
        "23" => ("Remessa a cartório", Situation::Aberto),
        "24" => (
            "Retirada de cartório e manutenção em carteira",
            Situation::Aberto,
        ),
        "25" => ("Protestado e baixado", Situation::Baixado),
        "26" => ("Instrução rejeitada", Situation::Rejeitado),
        "27" => (
            "Confirmação da alteração de outros dados",
            Situation::Aberto,
        ),
        "28" => ("Débito de tarifas/custas", Situation::Tarifa),
        "30" => ("Alteração de dados rejeitada", Situation::Rejeitado),
        _ => ("", Situation::Outro),
    }
}

/// FEBRABAN 240 remittance movement codes (código de movimento remessa, cobrança).
fn remittance_movement(code: &str) -> (&'static str, Situation) {
    match code {
        "01" => ("Entrada de títulos", Situation::Aberto),
        "02" => ("Pedido de baixa", Situation::Baixado),
        "04" => ("Concessão de abatimento", Situation::Aberto),
        "05" => ("Cancelamento de abatimento", Situation::Aberto),
        "06" => ("Alteração de vencimento", Situation::Aberto),
        "07" => ("Concessão de desconto", Situation::Aberto),
        "08" => ("Cancelamento de desconto", Situation::Aberto),
        "09" => ("Protestar", Situation::Aberto),
        "10" => ("Sustar protesto e baixar título", Situation::Baixado),
        "11" => ("Sustar protesto e manter em carteira", Situation::Aberto),
        "31" => ("Alteração de outros dados", Situation::Aberto),
        _ => ("", Situation::Outro),
    }
}

/// FEBRABAN 240 título species (espécie do título, segment P 107-108).
fn species(code: &str) -> &'static str {
    match code {
        "01" => "Cheque",
        "02" => "Duplicata mercantil",
        "03" => "Duplicata mercantil por indicação",
        "04" => "Duplicata de serviço",
        "05" => "Duplicata de serviço por indicação",
        "06" => "Duplicata rural",
        "07" => "Letra de câmbio",
        "08" => "Nota de crédito comercial",
        "09" => "Nota de crédito à exportação",
        "10" => "Nota de crédito industrial",
        "11" => "Nota de crédito rural",
        "12" => "Nota promissória",
        "13" => "Nota promissória rural",
        "14" => "Triplicata mercantil",
        "15" => "Triplicata de serviço",
        "16" => "Nota de seguro",
        "17" => "Recibo",
        "18" => "Fatura",
        "19" => "Nota de débito",
        "20" => "Apólice de seguro",
        "21" => "Mensalidade escolar",
        "22" => "Parcela de consórcio",
        "23" => "Nota fiscal",
        "24" => "Documento de dívida",
        "25" => "Cédula de produto rural",
        "99" => "Outros",
        _ => "",
    }
}

/// First occurrence code of a payment return (up to five 2-character codes in 231-240).
fn payment_situation(occurrences: &str, is_return: bool) -> &'static str {
    if !is_return {
        return "Remessa";
    }
    match occurrences.get(..2).unwrap_or_default() {
        "" => "Sem ocorrência",
        "00" => "Efetivado",
        "BD" => "Agendado",
        _ => "Rejeitado",
    }
}

#[derive(Default)]
struct State {
    bank: String,
    is_return: Option<bool>,
    collection: Vec<CollectionRow>,
    payments: Vec<PaymentRow>,
    pending_t: Option<CollectionRow>,
    pending_p: Option<CollectionRow>,
    pending_payment: Option<PaymentRow>,
    t_without_u: usize,
    p_without_q: usize,
    orphans: BTreeMap<char, usize>,
    skipped: BTreeMap<char, usize>,
    header: bool,
    trailer_count: Option<usize>,
    records: usize,
    problems: Problems,
}

impl State {
    fn flush(&mut self) {
        if let Some(t) = self.pending_t.take() {
            self.t_without_u += 1;
            self.collection.push(t);
        }
        if let Some(p) = self.pending_p.take() {
            self.p_without_q += 1;
            self.collection.push(p);
        }
        if let Some(p) = self.pending_payment.take() {
            self.payments.push(p);
        }
    }

    fn segment_t(&mut self, r: &Record) {
        self.flush();
        let code = r.text(16, 17);
        let (label, situation) = return_movement(&code);
        self.pending_t = Some(CollectionRow {
            line: r.line,
            bank: r.text(1, 3),
            movement: label.to_string(),
            situation: Some(situation),
            movement_code: code,
            nosso_numero: r.text(38, 57),
            seu_numero: r.text(59, 73),
            due: self.problems.date(&r.raw(74, 81), false),
            face_value: money_field(&r.raw(82, 96)),
            company_ref: r.text(106, 130),
            payer_tax_id: inscription(r.at(133), &r.raw(134, 148)),
            payer: r.text(149, 188),
            fee: money_field(&r.raw(199, 213)),
            reason: r.text(214, 223),
            ..CollectionRow::default()
        });
    }

    fn segment_u(&mut self, r: &Record) {
        let Some(mut row) = self.pending_t.take() else {
            *self.orphans.entry('U').or_default() += 1;
            return;
        };
        row.interest = money_field(&r.raw(18, 32));
        row.discount = money_field(&r.raw(33, 47));
        row.rebate = money_field(&r.raw(48, 62));
        row.iof = money_field(&r.raw(63, 77));
        row.paid = money_field(&r.raw(78, 92));
        row.net = money_field(&r.raw(93, 107));
        row.other_expenses = money_field(&r.raw(108, 122));
        row.other_credits = money_field(&r.raw(123, 137));
        row.occurred = self.problems.date(&r.raw(138, 145), false);
        row.credited = self.problems.date(&r.raw(146, 153), false);
        self.collection.push(row);
    }

    fn segment_p(&mut self, r: &Record) {
        self.flush();
        let code = r.text(16, 17);
        let (label, situation) = remittance_movement(&code);
        let species_code = r.text(107, 108);
        self.pending_p = Some(CollectionRow {
            line: r.line,
            bank: r.text(1, 3),
            movement: label.to_string(),
            situation: Some(situation),
            movement_code: code,
            nosso_numero: r.text(38, 57),
            seu_numero: r.text(63, 77),
            due: self.problems.date(&r.raw(78, 85), false),
            face_value: money_field(&r.raw(86, 100)),
            species: species(&species_code).to_string(),
            issued: self.problems.date(&r.raw(110, 117), false),
            company_ref: r.text(196, 220),
            ..CollectionRow::default()
        });
    }

    fn segment_q(&mut self, r: &Record) {
        let Some(mut row) = self.pending_p.take() else {
            *self.orphans.entry('Q').or_default() += 1;
            return;
        };
        row.payer_tax_id = inscription(r.at(18), &r.raw(19, 33));
        row.payer = r.text(34, 73);
        self.collection.push(row);
    }

    fn segment_a(&mut self, r: &Record) {
        self.flush();
        let occurrences = r.text(231, 240);
        let account = format!(
            "{}/{}-{}/{}-{}",
            r.text(21, 23),
            r.text(24, 28),
            r.text(29, 29),
            r.text(30, 41),
            r.text(42, 42)
        );
        self.pending_payment = Some(PaymentRow {
            line: r.line,
            segment: 'A',
            payee: r.text(44, 73),
            payee_account: account.trim_matches(['/', '-']).to_string(),
            seu_numero: r.text(74, 93),
            scheduled: self.problems.date(&r.raw(94, 101), false),
            amount: money_field(&r.raw(120, 134)),
            bank_ref: r.text(135, 154),
            effective: self.problems.date(&r.raw(155, 162), false),
            effective_amount: money_field(&r.raw(163, 177)),
            situation: payment_situation(&occurrences, self.is_return.unwrap_or(false)).to_string(),
            occurrences,
            ..PaymentRow::default()
        });
    }

    fn segment_b(&mut self, r: &Record) {
        match self.pending_payment.as_mut() {
            Some(p) if p.segment == 'A' => {
                p.payee_tax_id = inscription(r.at(18), &r.raw(19, 32));
            }
            _ => *self.orphans.entry('B').or_default() += 1,
        }
    }

    fn segment_j(&mut self, r: &Record) {
        let barcode = r.raw(18, 61);
        if r.raw(18, 19) == "52" && !barcode.chars().all(|c| c.is_ascii_digit()) {
            // J-52: payer and beneficiary of the boleto just paid.
            match self.pending_payment.as_mut() {
                Some(p) if p.segment == 'J' => {
                    p.payee_tax_id = inscription(r.at(76), &r.raw(77, 91));
                    if p.payee.is_empty() {
                        p.payee = r.text(92, 131);
                    }
                }
                _ => *self.orphans.entry('J').or_default() += 1,
            }
            return;
        }
        self.flush();
        let occurrences = r.text(231, 240);
        self.pending_payment = Some(PaymentRow {
            line: r.line,
            segment: 'J',
            barcode: barcode.trim().to_string(),
            payee: r.text(62, 91),
            due: self.problems.date(&r.raw(92, 99), false),
            face_value: money_field(&r.raw(100, 114)),
            discount: money_field(&r.raw(115, 129)),
            interest: money_field(&r.raw(130, 144)),
            scheduled: self.problems.date(&r.raw(145, 152), false),
            amount: money_field(&r.raw(153, 167)),
            seu_numero: r.text(183, 202),
            bank_ref: r.text(203, 222),
            situation: payment_situation(&occurrences, self.is_return.unwrap_or(false)).to_string(),
            occurrences,
            ..PaymentRow::default()
        });
    }

    fn record(&mut self, r: &Record) {
        self.records += 1;
        match r.at(8) {
            '0' => {
                self.header = true;
                self.bank = r.text(1, 3);
                self.is_return = match r.at(143) {
                    '1' => Some(false),
                    '2' => Some(true),
                    _ => None,
                };
            }
            '1' => self.flush(),
            '3' => match r.at(14) {
                'T' => self.segment_t(r),
                'U' => self.segment_u(r),
                'P' => self.segment_p(r),
                'Q' => self.segment_q(r),
                // Optional remittance extensions (discounts, fines, messages): nothing a
                // reconciliation needs, and they never interrupt a P/Q pair.
                'R' | 'S' | 'Y' => {}
                'A' => self.segment_a(r),
                'B' => self.segment_b(r),
                'J' => self.segment_j(r),
                other => *self.skipped.entry(other).or_default() += 1,
            },
            '5' => self.flush(),
            '9' => {
                self.flush();
                self.trailer_count = r.text(24, 29).parse().ok();
            }
            other => *self.skipped.entry(other).or_default() += 1,
        }
    }

    fn warnings(self) -> (Vec<CollectionRow>, Vec<PaymentRow>, Vec<String>) {
        let mut out = Vec::new();
        if !self.header {
            out.push("sem header de arquivo (registro 0)".to_string());
        }
        match self.trailer_count {
            None => out.push(
                "sem trailer de arquivo (registro 9): o arquivo pode estar incompleto".to_string(),
            ),
            Some(n) if n != self.records => out.push(format!(
                "o trailer declara {n} registro(s), o arquivo tem {}: o arquivo pode estar incompleto",
                self.records
            )),
            Some(_) => {}
        }
        if self.t_without_u > 0 {
            out.push(format!(
                "{} segmento(s) T sem o segmento U (valores pagos e datas em branco)",
                self.t_without_u
            ));
        }
        if self.p_without_q > 0 {
            out.push(format!(
                "{} segmento(s) P sem o segmento Q (sacado em branco)",
                self.p_without_q
            ));
        }
        for (seg, n) in &self.orphans {
            out.push(format!(
                "{n} segmento(s) {seg} sem o segmento principal ignorado(s)"
            ));
        }
        for (seg, n) in &self.skipped {
            out.push(format!(
                "{n} registro(s) do segmento ou tipo '{seg}' não lido(s) (fora dos layouts de cobrança e pagamento)"
            ));
        }
        out.extend(self.problems.into_warnings(240));
        (self.collection, self.payments, out)
    }
}

/// Parses a CNAB 240 file: a collection table (segments T/U or P/Q) and/or a payments table
/// (segments A/B and J/J-52).
pub fn parse_cnab240(bytes: &[u8], name: &str, source_id: &str) -> Result<CnabParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let text = super::decode(bytes);
    let (recs, padded, long) = records(&text, 240);
    if recs.is_empty() {
        return Err(unrecognized("registros não têm 240 posições"));
    }
    let mut st = State::default();
    st.problems.padded = padded;
    st.problems.long = long;
    for r in &recs {
        st.record(r);
    }
    st.flush();
    let bank = st.bank.clone();
    let (collection, payments, mut warnings) = st.warnings();
    if collection.is_empty() && payments.is_empty() {
        return Err(unrecognized(
            "CNAB 240 sem títulos de cobrança (segmentos T/U, P/Q) nem pagamentos (A/J)",
        ));
    }
    if bank.is_empty() {
        warnings.push("código do banco ausente no header".to_string());
    }
    let both = !collection.is_empty() && !payments.is_empty();
    let mut tables = Vec::new();
    if !collection.is_empty() {
        let table_name = if both {
            format!("{name}:cobranca")
        } else {
            name.to_string()
        };
        tables.push(collection_table(&table_name, source_id, &collection));
    }
    if !payments.is_empty() {
        let table_name = if both {
            format!("{name}:pagamentos")
        } else {
            name.to_string()
        };
        tables.push(payments_table(&table_name, source_id, &payments));
    }
    Ok(CnabParsed { tables, warnings })
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;
    use dm_core::{Money, Value};

    /// Writes `value` at 1-based `start` of a blank 240-character record.
    fn put(line: &mut [char], start: usize, value: &str) {
        for (i, c) in value.chars().enumerate() {
            line[start - 1 + i] = c;
        }
    }

    fn rec(fields: &[(usize, &str)]) -> String {
        let mut line = vec![' '; 240];
        for (start, value) in fields {
            put(&mut line, *start, value);
        }
        line.into_iter().collect()
    }

    fn sample_return() -> String {
        let lines = [
            rec(&[
                (1, "999"),
                (4, "0000"),
                (8, "0"),
                (73, "EMPRESA SINTETICA"),
                (143, "2"),
                (144, "10072026"),
            ]),
            rec(&[(1, "999"), (4, "0001"), (8, "1"), (9, "T"), (10, "01")]),
            rec(&[
                (1, "999"),
                (4, "0001"),
                (8, "3"),
                (9, "00001"),
                (14, "T"),
                (16, "06"),
                (38, "000098765001"),
                (59, "NF 200311"),
                (74, "10072026"),
                (82, "000000002430000"),
                (106, "PROTHEUS 200311"),
                (133, "2"),
                (134, "021456789000123"),
                (149, "PAO E ARTE PANIFICADORA LTDA"),
                (199, "000000000000350"),
            ]),
            rec(&[
                (1, "999"),
                (4, "0001"),
                (8, "3"),
                (9, "00002"),
                (14, "U"),
                (16, "06"),
                (18, "000000000001000"),
                (78, "000000002431000"),
                (93, "000000002430650"),
                (138, "10072026"),
                (146, "11072026"),
            ]),
            rec(&[
                (1, "999"),
                (4, "0001"),
                (8, "3"),
                (9, "00003"),
                (14, "T"),
                (16, "02"),
                (38, "000098765002"),
                (59, "NF 200312"),
                (74, "10072026"),
                (82, "000000000270000"),
                (133, "1"),
                (134, "000012345678909"),
                (149, "MARIA DA SILVA"),
            ]),
            rec(&[(1, "999"), (4, "0001"), (8, "5")]),
            rec(&[
                (1, "999"),
                (4, "9999"),
                (8, "9"),
                (18, "000001"),
                (24, "000007"),
            ]),
        ];
        lines.join("\r\n") + "\r\n"
    }

    #[test]
    fn return_segments_t_and_u_merge_per_titulo() {
        let parsed = parse_cnab240(sample_return().as_bytes(), "retorno", "src").unwrap();
        assert_eq!(parsed.tables.len(), 1);
        let t = &parsed.tables[0];
        assert!(super::super::is_collection_table(t));
        assert_eq!(t.len(), 2);
        let col = |n: &str| t.column_index(n).unwrap();
        assert_eq!(
            t.value(0, col("nosso_numero")).unwrap().as_text(),
            Some("000098765001")
        );
        assert_eq!(
            t.value(0, col("seu_numero")).unwrap().as_text(),
            Some("NF 200311")
        );
        assert_eq!(
            t.value(0, col("inscricao_do_sacado")).unwrap().as_text(),
            Some("21456789000123")
        );
        assert_eq!(
            t.value(0, col("sacado")).unwrap().as_text(),
            Some("PAO E ARTE PANIFICADORA LTDA")
        );
        assert_eq!(
            t.value(0, col("vencimento")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 10).unwrap()))
        );
        assert_eq!(
            t.value(0, col("valor_do_titulo")),
            Some(&Value::Money(Money::from_centavos(2_430_000)))
        );
        assert_eq!(
            t.value(0, col("valor_pago")),
            Some(&Value::Money(Money::from_centavos(2_431_000)))
        );
        assert_eq!(
            t.value(0, col("juros_e_multa")),
            Some(&Value::Money(Money::from_centavos(1_000)))
        );
        assert_eq!(
            t.value(0, col("tarifa")),
            Some(&Value::Money(Money::from_centavos(350)))
        );
        assert_eq!(
            t.value(0, col("valor_liquido")),
            Some(&Value::Money(Money::from_centavos(2_430_650)))
        );
        assert_eq!(
            t.value(0, col("data_do_credito")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 11).unwrap()))
        );
        assert_eq!(
            t.value(0, col("situacao")).unwrap().as_text(),
            Some("Liquidado")
        );
        assert_eq!(
            t.value(0, col("movimento")).unwrap().as_text(),
            Some("Liquidação")
        );
        assert_eq!(
            t.value(0, col("uso_da_empresa")).unwrap().as_text(),
            Some("PROTHEUS 200311")
        );
        assert_eq!(t.value(0, col("banco")).unwrap().as_text(), Some("999"));
        assert_eq!(t.value(0, col("linha")), Some(&Value::Integer(3)));
        // The second T has no U: kept, with the U fields blank and a warning.
        assert_eq!(
            t.value(1, col("inscricao_do_sacado")).unwrap().as_text(),
            Some("12345678909")
        );
        assert_eq!(
            t.value(1, col("situacao")).unwrap().as_text(),
            Some("Aberto")
        );
        assert_eq!(t.value(1, col("valor_pago")), Some(&Value::Null));
        assert!(
            parsed
                .warnings
                .iter()
                .any(|w| w.contains("sem o segmento U")),
            "{:?}",
            parsed.warnings
        );
        assert!(
            !parsed.warnings.iter().any(|w| w.contains("trailer")),
            "{:?}",
            parsed.warnings
        );
    }

    #[test]
    fn remittance_p_and_q_carry_species_and_issue_date() {
        let lines = [
            rec(&[(1, "999"), (8, "0"), (143, "1")]),
            rec(&[
                (1, "999"),
                (8, "3"),
                (14, "P"),
                (16, "01"),
                (38, "000098765008"),
                (63, "NF 200351"),
                (78, "17072026"),
                (86, "000000001240000"),
                (107, "04"),
                (110, "07072026"),
            ]),
            rec(&[(1, "999"), (8, "3"), (14, "R")]),
            rec(&[
                (1, "999"),
                (8, "3"),
                (14, "Q"),
                (18, "2"),
                (19, "089012345000112"),
                (34, "SABOR DA ROCA FRANQUIAS LTDA"),
            ]),
            rec(&[(1, "999"), (8, "9"), (24, "000005")]),
        ];
        let parsed = parse_cnab240(lines.join("\n").as_bytes(), "remessa", "src").unwrap();
        let t = &parsed.tables[0];
        let col = |n: &str| t.column_index(n).unwrap();
        assert_eq!(t.len(), 1);
        assert_eq!(
            t.value(0, col("especie")).unwrap().as_text(),
            Some("Duplicata de serviço")
        );
        assert_eq!(
            t.value(0, col("emissao")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 7).unwrap()))
        );
        assert_eq!(
            t.value(0, col("sacado")).unwrap().as_text(),
            Some("SABOR DA ROCA FRANQUIAS LTDA")
        );
        assert_eq!(
            t.value(0, col("inscricao_do_sacado")).unwrap().as_text(),
            Some("89012345000112")
        );
        assert_eq!(
            t.value(0, col("situacao")).unwrap().as_text(),
            Some("Aberto")
        );
        assert!(parsed.warnings.is_empty(), "{:?}", parsed.warnings);
    }

    #[test]
    fn payments_a_b_and_j_with_j52() {
        let barcode = "34191790010104351004791020150008291070026000";
        let lines = [
            rec(&[(1, "999"), (8, "0"), (143, "2")]),
            rec(&[(1, "999"), (8, "1"), (9, "C"), (10, "20")]),
            rec(&[
                (1, "999"),
                (8, "3"),
                (14, "A"),
                (21, "341"),
                (24, "00987"),
                (29, "1"),
                (30, "000000012345"),
                (42, "6"),
                (44, "CAIXAS BOA VISTA LTDA"),
                (74, "NF 55120"),
                (94, "20072026"),
                (102, "BRL"),
                (120, "000000001470000"),
                (135, "BANCO123"),
                (155, "20072026"),
                (163, "000000001470000"),
                (231, "00"),
            ]),
            rec(&[
                (1, "999"),
                (8, "3"),
                (14, "B"),
                (18, "2"),
                (19, "11222333000181"),
            ]),
            rec(&[
                (1, "999"),
                (8, "3"),
                (14, "J"),
                (18, barcode),
                (62, "FORNECEDOR DE ENERGIA"),
                (92, "25072026"),
                (100, "000000000500000"),
                (145, "24072026"),
                (153, "000000000500000"),
                (183, "FATURA 7"),
                (231, "BD"),
            ]),
            rec(&[
                (1, "999"),
                (8, "3"),
                (14, "J"),
                (18, "52"),
                (20, "2"),
                (21, "011222333000181"),
                (36, "EMPRESA SINTETICA"),
                (76, "2"),
                (77, "045723174000110"),
                (92, "ENERGIA SINTETICA SA"),
            ]),
            rec(&[(1, "999"), (8, "3"), (14, "O")]),
            rec(&[(1, "999"), (8, "5")]),
            rec(&[(1, "999"), (8, "9"), (24, "000009")]),
        ];
        let parsed = parse_cnab240(lines.join("\n").as_bytes(), "pagamentos", "src").unwrap();
        assert_eq!(parsed.tables.len(), 1);
        let t = &parsed.tables[0];
        assert!(!super::super::is_collection_table(t));
        let col = |n: &str| t.column_index(n).unwrap();
        assert_eq!(t.len(), 2);
        assert_eq!(
            t.value(0, col("favorecido")).unwrap().as_text(),
            Some("CAIXAS BOA VISTA LTDA")
        );
        assert_eq!(
            t.value(0, col("inscricao_do_favorecido"))
                .unwrap()
                .as_text(),
            Some("11222333000181")
        );
        assert_eq!(
            t.value(0, col("conta_do_favorecido")).unwrap().as_text(),
            Some("341/00987-1/000000012345-6")
        );
        assert_eq!(
            t.value(0, col("valor_do_pagamento")),
            Some(&Value::Money(Money::from_centavos(1_470_000)))
        );
        assert_eq!(
            t.value(0, col("situacao")).unwrap().as_text(),
            Some("Efetivado")
        );
        assert_eq!(
            t.value(1, col("codigo_de_barras")).unwrap().as_text(),
            Some(barcode)
        );
        assert_eq!(
            t.value(1, col("inscricao_do_favorecido"))
                .unwrap()
                .as_text(),
            Some("45723174000110")
        );
        assert_eq!(
            t.value(1, col("favorecido")).unwrap().as_text(),
            Some("FORNECEDOR DE ENERGIA")
        );
        assert_eq!(
            t.value(1, col("situacao")).unwrap().as_text(),
            Some("Agendado")
        );
        assert_eq!(
            t.value(1, col("vencimento")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 25).unwrap()))
        );
        assert!(
            parsed.warnings.iter().any(|w| w.contains("'O'")),
            "{:?}",
            parsed.warnings
        );
    }

    #[test]
    fn errors_and_integrity_warnings() {
        assert_eq!(
            parse_cnab240(b"", "x", "s").unwrap_err(),
            IngestError::Empty
        );
        assert!(matches!(
            parse_cnab240(b"abc", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        let only_header = rec(&[(8, "0")]);
        assert!(matches!(
            parse_cnab240(only_header.as_bytes(), "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        let truncated = [
            rec(&[(1, "999"), (8, "3"), (14, "U")]),
            rec(&[(1, "999"), (8, "3"), (14, "T"), (74, "99999999")]),
        ];
        let parsed = parse_cnab240(truncated.join("\n").as_bytes(), "x", "s").unwrap();
        let w = parsed.warnings.join(" | ");
        assert!(w.contains("sem header"), "{w}");
        assert!(w.contains("sem trailer"), "{w}");
        assert!(w.contains("segmento(s) U sem o segmento principal"), "{w}");
        assert!(w.contains("data(s) inválida(s)"), "{w}");
    }
}
