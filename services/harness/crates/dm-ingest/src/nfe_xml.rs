//! Minimal NF-e / NFC-e XML: key, number, series, issue date, issuer, recipient, total and
//! the payment installments (`cobr/dup`). One table row per installment, or one row for the
//! invoice when it has none.

use chrono::NaiveDate;
use dm_core::{Column, ColumnRole, Money, Table, Value};
use dm_normalize::{date_ptbr, DateContext};
use dm_validators::{nfe_key_dv, Verdict};
use quick_xml::events::Event;
use quick_xml::Reader;

use crate::IngestError;

#[derive(Default, Debug)]
struct Invoice {
    key: String,
    number: String,
    series: String,
    issued: Option<NaiveDate>,
    issuer_cnpj: String,
    issuer_name: String,
    recipient_id: String,
    recipient_name: String,
    total: Option<Money>,
    installments: Vec<Installment>,
}

#[derive(Default, Debug, Clone)]
struct Installment {
    number: String,
    due: Option<NaiveDate>,
    amount: Option<Money>,
}

/// Parses an NF-e XML (`nfeProc` or bare `NFe`) into a table.
pub fn parse_nfe_xml(bytes: &[u8], name: &str, source_id: &str) -> Result<Table, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let text = String::from_utf8_lossy(bytes);
    let mut reader = Reader::from_str(&text);
    reader.config_mut().trim_text(true);
    let mut path: Vec<String> = Vec::new();
    let mut inv = Invoice::default();
    let mut current_dup: Option<Installment> = None;
    let mut saw_nfe = false;
    let ctx = DateContext::default();
    // Text arrives in pieces around character references (`Emp&#243;rio`), so it is
    // accumulated per element and consumed at the closing tag.
    let mut buf = String::new();
    loop {
        let event = reader.read_event().map_err(|e| IngestError::Parse {
            format: "nfe-xml",
            detail: e.to_string(),
        })?;
        match event {
            Event::Start(e) => {
                let local = e.local_name().as_ref().to_string();
                if local == "infNFe" {
                    saw_nfe = true;
                    if let Ok(Some(id)) = e.try_get_attribute("Id") {
                        let value = id.value.to_string();
                        inv.key = value.trim_start_matches("NFe").to_string();
                    }
                }
                if local == "dup" {
                    current_dup = Some(Installment::default());
                }
                path.push(local);
                buf.clear();
            }
            Event::Empty(e) => {
                let local = e.local_name().as_ref().to_string();
                if local == "infNFe" {
                    saw_nfe = true;
                }
            }
            Event::Text(t) => buf.push_str(&t.xml10_content()),
            Event::GeneralRef(r) => {
                if let Ok(Some(c)) = r.resolve_char_ref() {
                    buf.push(c);
                } else {
                    let name = r.as_ref().to_string();
                    if let Some(text) = quick_xml::escape::resolve_predefined_entity(&name) {
                        buf.push_str(text);
                    } else {
                        buf.push('&');
                        buf.push_str(&name);
                        buf.push(';');
                    }
                }
            }
            Event::End(_) => {
                let value = std::mem::take(&mut buf).trim().to_string();
                let parent = path.iter().rev().nth(1).map_or("", String::as_str);
                let leaf = path.last().map_or("", String::as_str);
                if leaf == "dup" {
                    if let Some(d) = current_dup.take() {
                        inv.installments.push(d);
                    }
                }
                match (parent, leaf) {
                    ("ide", "nNF") => inv.number = value,
                    ("ide", "serie") => inv.series = value,
                    ("ide", "dhEmi" | "dEmi") => {
                        inv.issued = date_ptbr(&value, &ctx).ok().map(|p| p.date)
                    }
                    ("emit", "CNPJ") => inv.issuer_cnpj = value,
                    ("emit", "xNome") => inv.issuer_name = value,
                    ("dest", "CNPJ" | "CPF") => inv.recipient_id = value,
                    ("dest", "xNome") => inv.recipient_name = value,
                    ("ICMSTot", "vNF") => {
                        inv.total =
                            Money::parse_with_style(&value, dm_core::money::DecimalStyle::Point)
                                .ok()
                    }
                    ("dup", "nDup") => {
                        if let Some(d) = current_dup.as_mut() {
                            d.number = value;
                        }
                    }
                    ("dup", "dVenc") => {
                        if let Some(d) = current_dup.as_mut() {
                            d.due = date_ptbr(&value, &ctx).ok().map(|p| p.date);
                        }
                    }
                    ("dup", "vDup") => {
                        if let Some(d) = current_dup.as_mut() {
                            d.amount = Money::parse_with_style(
                                &value,
                                dm_core::money::DecimalStyle::Point,
                            )
                            .ok();
                        }
                    }
                    _ => {}
                }
                path.pop();
            }
            Event::Eof => break,
            _ => {}
        }
    }
    if !saw_nfe {
        return Err(IngestError::Unrecognized("XML sem infNFe".to_string()));
    }
    let key_status = if inv.key.is_empty() {
        "ausente".to_string()
    } else {
        match nfe_key_dv(&inv.key) {
            Some(k) if k.verdict == Verdict::Valid => "valid".to_string(),
            Some(_) => "invalid".to_string(),
            None => "malformed".to_string(),
        }
    };
    let columns = vec![
        Column::new("Chave", ColumnRole::DocumentId).with_subrole("key"),
        Column::new("Número", ColumnRole::DocumentId).with_subrole("nfe"),
        Column::new("Série", ColumnRole::Kind).with_subrole("series"),
        Column::new("Emissão", ColumnRole::Date).with_subrole("issue"),
        Column::new("CNPJ Emitente", ColumnRole::TaxId).with_subrole("cnpj"),
        Column::new("Emitente", ColumnRole::Name).with_subrole("supplier"),
        Column::new("CNPJ/CPF Destinatário", ColumnRole::TaxId),
        Column::new("Destinatário", ColumnRole::Name).with_subrole("customer"),
        Column::new("Valor NF", ColumnRole::Amount).with_subrole("title"),
        Column::new("Parcela", ColumnRole::DocumentId).with_subrole("title"),
        Column::new("Vencimento", ColumnRole::Date).with_subrole("due"),
        Column::new("Valor Parcela", ColumnRole::Amount),
        Column::new("Chave válida", ColumnRole::Status).with_subrole("key_status"),
    ];
    let mut table = Table::new(name, source_id, columns);
    let base = |inv: &Invoice| {
        vec![
            Value::from(inv.key.as_str()),
            Value::from(inv.number.as_str()),
            Value::from(inv.series.as_str()),
            inv.issued.map_or(Value::Null, Value::Date),
            Value::from(inv.issuer_cnpj.as_str()),
            Value::from(inv.issuer_name.as_str()),
            Value::from(inv.recipient_id.as_str()),
            Value::from(inv.recipient_name.as_str()),
            inv.total.map_or(Value::Null, Value::Money),
        ]
    };
    if inv.installments.is_empty() {
        let mut row = base(&inv);
        row.extend([
            Value::Null,
            Value::Null,
            inv.total.map_or(Value::Null, Value::Money),
            Value::Text(key_status.clone()),
        ]);
        table.push_row(row).map_err(|e| IngestError::Parse {
            format: "nfe-xml",
            detail: e.to_string(),
        })?;
    } else {
        for d in &inv.installments {
            let mut row = base(&inv);
            row.extend([
                Value::from(d.number.as_str()),
                d.due.map_or(Value::Null, Value::Date),
                d.amount.map_or(Value::Null, Value::Money),
                Value::Text(key_status.clone()),
            ]);
            table.push_row(row).map_err(|e| IngestError::Parse {
                format: "nfe-xml",
                detail: e.to_string(),
            })?;
        }
    }
    Ok(table)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> String {
        let key = dm_validators::nfe::compose_key(
            35,
            25,
            3,
            "11222333000181",
            55,
            1,
            88231,
            1,
            12_345_678,
        )
        .unwrap();
        format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\"?><nfeProc versao=\"4.00\"><NFe><infNFe Id=\"NFe{key}\" versao=\"4.00\"><ide><nNF>88231</nNF><serie>1</serie><dhEmi>2025-03-05T10:00:00-03:00</dhEmi></ide><emit><CNPJ>11222333000181</CNPJ><xNome>Emp&#243;rio Bela Vista Ltda</xNome></emit><dest><CNPJ>45723174000110</CNPJ><xNome>Cliente X</xNome></dest><total><ICMSTot><vNF>18450.00</vNF></ICMSTot></total><cobr><dup><nDup>001</nDup><dVenc>2025-03-20</dVenc><vDup>9225.00</vDup></dup><dup><nDup>002</nDup><dVenc>2025-04-20</dVenc><vDup>9225.00</vDup></dup></cobr></infNFe></NFe></nfeProc>"
        )
    }

    #[test]
    fn parses_installments() {
        let t = parse_nfe_xml(fixture().as_bytes(), "nfe", "src_nfe").unwrap();
        assert_eq!(t.len(), 2);
        assert_eq!(t.value(0, 1).unwrap().as_text(), Some("88231"));
        assert_eq!(
            t.value(0, 3),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2025, 3, 5).unwrap()))
        );
        assert_eq!(
            t.value(0, 5).unwrap().as_text(),
            Some("Empório Bela Vista Ltda")
        );
        assert_eq!(
            t.value(0, 8),
            Some(&Value::Money(Money::from_centavos(1_845_000)))
        );
        assert_eq!(t.value(0, 9).unwrap().as_text(), Some("001"));
        assert_eq!(
            t.value(1, 10),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2025, 4, 20).unwrap()))
        );
        assert_eq!(
            t.value(1, 11),
            Some(&Value::Money(Money::from_centavos(922_500)))
        );
        assert_eq!(t.value(0, 12).unwrap().as_text(), Some("valid"));
        assert_eq!(t.value(0, 0).unwrap().as_text().map(str::len), Some(44));
    }

    #[test]
    fn invoice_without_installments_and_errors() {
        let xml = "<NFe><infNFe Id=\"NFe123\"><ide><nNF>7</nNF></ide><total><ICMSTot><vNF>10.00</vNF></ICMSTot></total></infNFe></NFe>";
        let t = parse_nfe_xml(xml.as_bytes(), "x", "s").unwrap();
        assert_eq!(t.len(), 1);
        assert_eq!(
            t.value(0, 11),
            Some(&Value::Money(Money::from_centavos(1_000)))
        );
        assert_eq!(t.value(0, 12).unwrap().as_text(), Some("malformed"));
        assert!(matches!(
            parse_nfe_xml(b"<root/>", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        assert!(matches!(
            parse_nfe_xml(b"<a><b></a>", "x", "s"),
            Err(IngestError::Parse { .. })
        ));
        assert_eq!(parse_nfe_xml(b"", "x", "s"), Err(IngestError::Empty));
    }
}
