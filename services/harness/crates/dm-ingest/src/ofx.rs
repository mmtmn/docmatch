//! Minimal OFX (SGML 1.x and XML 2.x) bank statements: one row per `<STMTTRN>`.

use chrono::NaiveDate;
use dm_core::{Column, ColumnRole, Money, Table, Value};
use dm_normalize::money_ptbr;

use crate::IngestError;

/// Parses an OFX statement into a table with posting date, amount, type, ids, name and memo.
pub fn parse_ofx(bytes: &[u8], name: &str, source_id: &str) -> Result<Table, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let text = decode_ofx(bytes);
    let upper = text.to_ascii_uppercase();
    if !upper.contains("<OFX") && !upper.contains("OFXHEADER") {
        return Err(IngestError::Unrecognized("sem cabeçalho OFX".to_string()));
    }
    let columns = vec![
        Column::new("Data", ColumnRole::Date).with_subrole("posting"),
        Column::new("Valor", ColumnRole::Amount),
        Column::new("Tipo", ColumnRole::Kind),
        Column::new("FITID", ColumnRole::DocumentId).with_subrole("fitid"),
        Column::new("Documento", ColumnRole::DocumentId),
        Column::new("Nome", ColumnRole::Name),
        Column::new("Histórico", ColumnRole::FreeText).with_subrole("descriptor"),
        Column::new("Conta", ColumnRole::FreeText).with_subrole("account"),
    ];
    let mut table = Table::new(name, source_id, columns);
    let account = {
        let bank = tag_value(&text, 0, "BANKID")
            .map(|(v, _)| v)
            .unwrap_or_default();
        let acct = tag_value(&text, 0, "ACCTID")
            .map(|(v, _)| v)
            .unwrap_or_default();
        if bank.is_empty() && acct.is_empty() {
            Value::Null
        } else {
            Value::Text(format!("{bank}/{acct}").trim_matches('/').to_string())
        }
    };
    let mut pos = 0;
    while let Some(start) = find_ci(&text, pos, "<STMTTRN>") {
        let end = find_ci(&text, start, "</STMTTRN>").unwrap_or(text.len());
        let block = &text[start..end];
        let date = tag_value(block, 0, "DTPOSTED")
            .and_then(|(v, _)| ofx_date(&v))
            .map_or(Value::Null, Value::Date);
        let amount = tag_value(block, 0, "TRNAMT")
            .and_then(|(v, _)| ofx_amount(&v))
            .map_or(Value::Null, Value::Money);
        let kind = tag_value(block, 0, "TRNTYPE").map_or(Value::Null, |(v, _)| Value::Text(v));
        let fitid = tag_value(block, 0, "FITID").map_or(Value::Null, |(v, _)| Value::Text(v));
        let doc = tag_value(block, 0, "CHECKNUM")
            .or_else(|| tag_value(block, 0, "REFNUM"))
            .map_or(Value::Null, |(v, _)| Value::Text(v));
        let payee = tag_value(block, 0, "NAME").map_or(Value::Null, |(v, _)| Value::from(v));
        let memo = tag_value(block, 0, "MEMO").map_or(Value::Null, |(v, _)| Value::from(v));
        table
            .push_row(vec![
                date,
                amount,
                kind,
                fitid,
                doc,
                payee,
                memo,
                account.clone(),
            ])
            .map_err(|e| IngestError::Parse {
                format: "ofx",
                detail: e.to_string(),
            })?;
        pos = end.max(start + 1);
    }
    if table.is_empty() {
        return Err(IngestError::Unrecognized(
            "OFX sem lançamentos (STMTTRN)".to_string(),
        ));
    }
    Ok(table)
}

/// OFX files declare `CHARSET:1252` or are UTF-8; anything invalid as UTF-8 is windows-1252.
fn decode_ofx(bytes: &[u8]) -> String {
    match std::str::from_utf8(bytes) {
        Ok(s) => s.to_string(),
        Err(_) => encoding_rs::WINDOWS_1252.decode(bytes).0.to_string(),
    }
}

fn find_ci(text: &str, from: usize, needle: &str) -> Option<usize> {
    let hay = text.get(from..)?.as_bytes();
    let n = needle.as_bytes();
    hay.windows(n.len())
        .position(|w| w.eq_ignore_ascii_case(n))
        .map(|p| from + p)
}

/// Value of `<TAG>value` (SGML, runs until the next `<`) or `<TAG>value</TAG>` (XML).
fn tag_value(text: &str, from: usize, tag: &str) -> Option<(String, usize)> {
    let open = format!("<{tag}>");
    let start = find_ci(text, from, &open)? + open.len();
    let rest = &text[start..];
    let end = rest.find('<').unwrap_or(rest.len());
    let value = rest[..end].trim().to_string();
    Some((value, start + end))
}

/// `YYYYMMDD[HHMMSS[.XXX]][[-3:BRT]]` → date.
fn ofx_date(value: &str) -> Option<NaiveDate> {
    let digits: String = value.chars().take_while(char::is_ascii_digit).collect();
    if digits.len() < 8 {
        return None;
    }
    NaiveDate::from_ymd_opt(
        digits[0..4].parse().ok()?,
        digits[4..6].parse().ok()?,
        digits[6..8].parse().ok()?,
    )
}

/// OFX amounts use a point; some Brazilian banks emit a comma.
fn ofx_amount(value: &str) -> Option<Money> {
    let v = value.trim();
    if v.contains(',') && !v.contains('.') {
        return money_ptbr(v).ok();
    }
    Money::parse_with_style(v, dm_core::money::DecimalStyle::Point).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    const OFX: &str = "OFXHEADER:100\nDATA:OFXSGML\nVERSION:102\nCHARSET:1252\n\n<OFX>\n<BANKMSGSRSV1>\n<STMTTRNRS>\n<STMTRS>\n<CURDEF>BRL\n<BANKACCTFROM>\n<BANKID>0237\n<ACCTID>12345-6\n</BANKACCTFROM>\n<BANKTRANLIST>\n<STMTTRN>\n<TRNTYPE>DEBIT\n<DTPOSTED>20250305120000[-3:BRT]\n<TRNAMT>-17380.00\n<FITID>2025030500001\n<CHECKNUM>088231\n<MEMO>PAG FORNECEDOR EMPORIO BELA VISTA COM\n</STMTTRN>\n<STMTTRN>\n<TRNTYPE>CREDIT\n<DTPOSTED>20250306\n<TRNAMT>1234,56\n<FITID>2025030600002\n<NAME>CLIENTE X\n<MEMO>TED RECEBIDA\n</STMTTRN>\n</BANKTRANLIST>\n</STMTRS>\n</STMTTRNRS>\n</BANKMSGSRSV1>\n</OFX>\n";

    #[test]
    fn parses_sgml_statement() {
        let t = parse_ofx(OFX.as_bytes(), "extrato", "src_ofx").unwrap();
        assert_eq!(t.len(), 2);
        assert_eq!(
            t.value(0, 0),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2025, 3, 5).unwrap()))
        );
        assert_eq!(
            t.value(0, 1),
            Some(&Value::Money(Money::from_centavos(-1_738_000)))
        );
        assert_eq!(t.value(0, 2).unwrap().as_text(), Some("DEBIT"));
        assert_eq!(t.value(0, 4).unwrap().as_text(), Some("088231"));
        assert_eq!(
            t.value(0, 6).unwrap().as_text(),
            Some("PAG FORNECEDOR EMPORIO BELA VISTA COM")
        );
        assert_eq!(t.value(0, 7).unwrap().as_text(), Some("0237/12345-6"));
        assert_eq!(
            t.value(1, 1),
            Some(&Value::Money(Money::from_centavos(123_456)))
        );
        assert_eq!(t.value(1, 5).unwrap().as_text(), Some("CLIENTE X"));
    }

    #[test]
    fn xml_flavor_and_errors() {
        let xml = "<?xml version=\"1.0\"?><?OFX OFXHEADER=\"200\"?><OFX><STMTTRN><TRNTYPE>DEBIT</TRNTYPE><DTPOSTED>20250305</DTPOSTED><TRNAMT>-10.50</TRNAMT><FITID>1</FITID></STMTTRN></OFX>";
        let t = parse_ofx(xml.as_bytes(), "x", "s").unwrap();
        assert_eq!(t.len(), 1);
        assert_eq!(
            t.value(0, 1),
            Some(&Value::Money(Money::from_centavos(-1_050)))
        );
        assert!(matches!(
            parse_ofx(b"nada", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        assert!(matches!(
            parse_ofx(b"<OFX></OFX>", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        assert_eq!(parse_ofx(b"", "x", "s"), Err(IngestError::Empty));
        assert_eq!(ofx_date("2025"), None);
    }
}
