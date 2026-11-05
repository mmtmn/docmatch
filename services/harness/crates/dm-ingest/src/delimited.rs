//! Delimited text (CSV, TSV, `;`-separated exports) to a typed [`Table`].

use dm_core::{ColumnRole, Table, Value};
use dm_normalize::{date_ptbr, money_with_style, tax_id, DateContext, DecimalStyle, TaxIdKind};
use encoding_rs::Encoding;

use crate::roles::infer_columns;
use crate::sniff::{sniff, Sniff};
use crate::IngestError;

/// Rows sampled for role inference.
const SAMPLE_ROWS: usize = 200;

/// Decodes bytes with the sniffed encoding and strips a BOM.
pub(crate) fn decode(bytes: &[u8], sniffed: &Sniff) -> Result<String, IngestError> {
    let encoding = Encoding::for_label(sniffed.encoding.as_bytes()).unwrap_or(encoding_rs::UTF_8);
    let (text, _, _) = encoding.decode(bytes);
    let text = text.trim_start_matches('\u{feff}');
    if text.is_empty() {
        return Err(IngestError::Empty);
    }
    Ok(text.to_string())
}

/// Converts a cell to a typed value according to the column role.
pub(crate) fn typed_value(
    raw: &str,
    role: ColumnRole,
    style: DecimalStyle,
    ctx: &DateContext,
) -> Value {
    let s = raw.trim();
    if s.is_empty() || s == "-" {
        return Value::Null;
    }
    match role {
        ColumnRole::Amount => {
            money_with_style(s, style).map_or_else(|_| Value::Text(s.to_string()), Value::Money)
        }
        ColumnRole::Date => match date_ptbr(s, ctx) {
            Ok(p) if p.time.is_some() => Value::DateTime(p.datetime()),
            Ok(p) => Value::Date(p.date),
            Err(_) => Value::Text(s.to_string()),
        },
        ColumnRole::TaxId => {
            let t = tax_id(s);
            if t.kind == TaxIdKind::Unknown {
                Value::Text(s.to_string())
            } else {
                Value::Text(t.digits)
            }
        }
        _ => Value::Text(s.to_string()),
    }
}

/// Parses delimited text into a table. `sniffed` may come from [`sniff`] or be adjusted by the
/// caller (forcing a delimiter, for instance).
pub fn parse_csv(
    bytes: &[u8],
    sniffed: &Sniff,
    name: &str,
    source_id: &str,
) -> Result<Table, IngestError> {
    let text = decode(bytes, sniffed)?;
    let delimiter = sniffed.delimiter.unwrap_or(b';');
    let mut builder = csv::ReaderBuilder::new();
    builder
        .delimiter(delimiter)
        .has_headers(false)
        .flexible(true)
        .trim(csv::Trim::All);
    match sniffed.quote {
        Some(q) => {
            builder.quote(q);
        }
        None => {
            builder.quoting(false);
        }
    }
    let mut reader = builder.from_reader(text.as_bytes());
    let mut records: Vec<Vec<String>> = Vec::new();
    for record in reader.records() {
        let record = record.map_err(|e| IngestError::Parse {
            format: "csv",
            detail: e.to_string(),
        })?;
        let fields: Vec<String> = record.iter().map(ToString::to_string).collect();
        if fields.iter().all(|f| f.trim().is_empty()) {
            continue;
        }
        records.push(fields);
    }
    if records.is_empty() {
        return Err(IngestError::Empty);
    }
    let width = records.iter().map(Vec::len).max().unwrap_or(0);
    for r in &mut records {
        r.resize(width, String::new());
    }
    let (headers, data): (Vec<String>, &[Vec<String>]) = if sniffed.has_header {
        (records[0].clone(), &records[1..])
    } else {
        (
            (1..=width).map(|i| format!("col_{i}")).collect(),
            &records[..],
        )
    };
    let header_refs: Vec<&str> = headers.iter().map(String::as_str).collect();
    let sample: Vec<Vec<&str>> = data
        .iter()
        .take(SAMPLE_ROWS)
        .map(|r| r.iter().map(String::as_str).collect())
        .collect();
    let guesses = infer_columns(&header_refs, &sample);
    let columns = guesses.into_iter().map(|g| g.column).collect();
    let mut table = Table::new(name, source_id, columns);
    let style: DecimalStyle = sniffed.decimal_style.into();
    let ctx = DateContext::default();
    for row in data {
        let values: Vec<Value> = row
            .iter()
            .zip(table.columns.iter())
            .map(|(cell, col)| typed_value(cell, col.role, style, &ctx))
            .collect();
        table.push_row(values).map_err(|e| IngestError::Parse {
            format: "csv",
            detail: e.to_string(),
        })?;
    }
    Ok(table)
}

/// Sniffs and parses in one call.
pub fn parse_csv_auto(bytes: &[u8], name: &str, source_id: &str) -> Result<Table, IngestError> {
    let sniffed = sniff(bytes);
    parse_csv(bytes, &sniffed, name, source_id)
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;
    use dm_core::Money;

    #[test]
    fn semicolon_pt_csv() {
        let csv = "Vencimento;Fornecedor;Nº Documento;Valor;Status;CNPJ\r\n05/03/2025;Empório Bela Vista;088231;17.380,00;Aberto;11.222.333/0001-81\r\n10/03/2025;Padaria Central;088232;500,00;Pago;45.723.174/0001-10\r\n";
        let t = parse_csv_auto(csv.as_bytes(), "titulos", "src_1").unwrap();
        assert_eq!(t.len(), 2);
        assert_eq!(t.columns.len(), 6);
        assert_eq!(t.columns[0].role, ColumnRole::Date);
        assert_eq!(t.columns[3].role, ColumnRole::Amount);
        assert_eq!(
            t.value(0, 3),
            Some(&Value::Money(Money::from_centavos(1_738_000)))
        );
        assert_eq!(
            t.value(0, 0),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2025, 3, 5).unwrap()))
        );
        assert_eq!(
            t.value(0, 2).unwrap().as_text(),
            Some("088231"),
            "leading zeros are kept verbatim"
        );
        assert_eq!(t.value(0, 5).unwrap().as_text(), Some("11222333000181"));
        assert_eq!(t.columns[2].name, "no_documento");
    }

    #[test]
    fn comma_en_csv_with_quotes_and_point_decimals() {
        let csv = "due_date,vendor,invoice_number,amount,paid_flag\n2025-03-05,\"Emporio, Bela Vista\",88231,1234.56,N\n2025-03-10,Padaria Central,88232,500.00,Y\n";
        let t = parse_csv_auto(csv.as_bytes(), "ap", "src_2").unwrap();
        assert_eq!(t.len(), 2);
        assert_eq!(
            t.value(0, 1).unwrap().as_text(),
            Some("Emporio, Bela Vista")
        );
        assert_eq!(
            t.value(0, 3),
            Some(&Value::Money(Money::from_centavos(123_456)))
        );
        assert_eq!(
            t.value(1, 3),
            Some(&Value::Money(Money::from_centavos(50_000)))
        );
        assert_eq!(t.columns[4].role, ColumnRole::Status);
    }

    #[test]
    fn windows_1252_and_ragged_rows() {
        let mut bytes = b"Hist\xf3rico;Valor\n".to_vec();
        bytes.extend_from_slice(b"PAG FORNECEDOR EMP\xd3RIO BELA VISTA;-1.234,56\n");
        bytes.extend_from_slice(b"TARIFA\n");
        let t = parse_csv_auto(&bytes, "extrato", "src_3").unwrap();
        assert_eq!(t.columns[0].header, "Histórico");
        assert_eq!(t.len(), 2);
        assert_eq!(
            t.value(0, 0).unwrap().as_text(),
            Some("PAG FORNECEDOR EMPÓRIO BELA VISTA")
        );
        assert_eq!(
            t.value(0, 1),
            Some(&Value::Money(Money::from_centavos(-123_456)))
        );
        assert_eq!(t.value(1, 1), Some(&Value::Null));
    }

    #[test]
    fn headerless_csv() {
        let csv = "05/03/2025;1.234,56\n06/03/2025;99,90\n";
        let t = parse_csv_auto(csv.as_bytes(), "x", "src").unwrap();
        assert_eq!(t.columns[0].name, "col_1");
        assert_eq!(t.columns[0].role, ColumnRole::Date);
        assert_eq!(t.columns[1].role, ColumnRole::Amount);
        assert_eq!(t.len(), 2);
    }

    #[test]
    fn empty_input_is_an_error_not_a_panic() {
        assert_eq!(parse_csv_auto(b"", "x", "src"), Err(IngestError::Empty));
        assert_eq!(parse_csv_auto(b"\n\n", "x", "src"), Err(IngestError::Empty));
    }
}
