//! XLSX / XLS / XLSB / ODS workbooks to one [`Table`] per sheet (calamine).

use std::io::Cursor;

use calamine::{open_workbook_auto_from_rs, Data, Range, Reader};
use dm_core::{ColumnRole, Decimal, Money, Table, Value};
use dm_normalize::{date_ptbr, DateContext, DecimalStyle};
use rust_decimal::prelude::FromPrimitive;

use crate::delimited::typed_value;
use crate::roles::infer_columns;
use crate::IngestError;

/// Rows sampled for role inference.
const SAMPLE_ROWS: usize = 200;

/// Parses every sheet of a workbook. Sheets without a header row are skipped with a warning
/// returned in the second element.
pub fn parse_xlsx(
    bytes: &[u8],
    name: &str,
    source_id: &str,
) -> Result<(Vec<Table>, Vec<String>), IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let cursor = Cursor::new(bytes.to_vec());
    let mut workbook = open_workbook_auto_from_rs(cursor).map_err(|e| IngestError::Parse {
        format: "xlsx",
        detail: e.to_string(),
    })?;
    let sheet_names = workbook.sheet_names();
    let mut tables = Vec::new();
    let mut warnings = Vec::new();
    for sheet in sheet_names {
        let range = match workbook.worksheet_range(&sheet) {
            Ok(r) => r,
            Err(e) => {
                warnings.push(format!("planilha {sheet}: {e}"));
                continue;
            }
        };
        match sheet_to_table(&range, &sheet, name, source_id) {
            Some(t) => tables.push(t),
            None => warnings.push(format!(
                "planilha {sheet}: sem linha de cabeçalho reconhecível"
            )),
        }
    }
    if tables.is_empty() && warnings.is_empty() {
        return Err(IngestError::Empty);
    }
    Ok((tables, warnings))
}

fn cell_text(cell: &Data) -> String {
    match cell {
        Data::Empty => String::new(),
        Data::String(s) | Data::DateTimeIso(s) | Data::DurationIso(s) => s.trim().to_string(),
        Data::Float(f) => {
            if f.fract() == 0.0 && f.abs() < 1e15 {
                format!("{}", *f as i64)
            } else {
                format!("{f}")
            }
        }
        Data::Int(i) => i.to_string(),
        Data::Bool(b) => b.to_string(),
        Data::DateTime(dt) => dt
            .as_datetime()
            .map(|d| d.format("%d/%m/%Y").to_string())
            .unwrap_or_default(),
        Data::Error(e) => format!("#{e:?}"),
    }
}

fn is_textual(cell: &Data) -> bool {
    matches!(cell, Data::String(s) if !s.trim().is_empty() && s.trim().parse::<f64>().is_err())
}

/// First row where most non-empty cells are text and at least two cells are filled.
fn find_header_row(range: &Range<Data>) -> Option<usize> {
    let max_width = range.width();
    for (i, row) in range.rows().enumerate().take(50) {
        let filled = row.iter().filter(|c| !matches!(c, Data::Empty)).count();
        let textual = row.iter().filter(|c| is_textual(c)).count();
        if filled >= 2 && textual * 2 >= filled && filled * 2 >= max_width.min(filled.max(2)) {
            return Some(i);
        }
    }
    None
}

fn sheet_to_table(range: &Range<Data>, sheet: &str, name: &str, source_id: &str) -> Option<Table> {
    let header_row = find_header_row(range)?;
    let rows: Vec<&[Data]> = range.rows().collect();
    let headers: Vec<String> = rows[header_row].iter().map(cell_text).collect();
    let data = &rows[header_row + 1..];
    let sample: Vec<Vec<String>> = data
        .iter()
        .take(SAMPLE_ROWS)
        .map(|r| r.iter().map(cell_text).collect())
        .collect();
    let sample_refs: Vec<Vec<&str>> = sample
        .iter()
        .map(|r| r.iter().map(String::as_str).collect())
        .collect();
    let header_refs: Vec<&str> = headers.iter().map(String::as_str).collect();
    let columns = infer_columns(&header_refs, &sample_refs)
        .into_iter()
        .map(|g| g.column)
        .collect();
    let table_name = if name.is_empty() {
        sheet.to_string()
    } else {
        format!("{name}:{sheet}")
    };
    let mut table = Table::new(table_name, source_id, columns);
    let ctx = DateContext::default();
    for row in data {
        if row.iter().all(|c| matches!(c, Data::Empty)) {
            continue;
        }
        let mut values = Vec::with_capacity(table.columns.len());
        for (i, col) in table.columns.iter().enumerate() {
            let cell = row.get(i).unwrap_or(&Data::Empty);
            values.push(cell_value(cell, col.role, &ctx));
        }
        table.push_row(values).ok()?;
    }
    Some(table)
}

fn cell_value(cell: &Data, role: ColumnRole, ctx: &DateContext) -> Value {
    match cell {
        Data::Empty => Value::Null,
        Data::Error(_) => Value::Null,
        Data::Bool(b) => Value::Bool(*b),
        Data::DateTime(dt) => match dt.as_datetime() {
            Some(d) if d.time() == chrono::NaiveTime::default() => Value::Date(d.date()),
            Some(d) => Value::DateTime(d),
            None => Value::Null,
        },
        Data::DateTimeIso(s) => match date_ptbr(s, ctx) {
            Ok(p) if p.time.is_some() => Value::DateTime(p.datetime()),
            Ok(p) => Value::Date(p.date),
            Err(_) => Value::Text(s.clone()),
        },
        Data::DurationIso(s) => Value::Text(s.clone()),
        Data::Int(i) => match role {
            ColumnRole::Amount => Value::Money(Money::from_decimal(Decimal::from(*i))),
            ColumnRole::DocumentId
            | ColumnRole::NossoNumero
            | ColumnRole::TaxId
            | ColumnRole::Status
            | ColumnRole::Kind => Value::Text(i.to_string()),
            _ => Value::Integer(*i),
        },
        Data::Float(f) => {
            match role {
                ColumnRole::Amount => Decimal::from_f64(*f)
                    .map_or(Value::Null, |d| Value::Money(Money::from_decimal(d))),
                ColumnRole::Date => excel_serial_to_date(*f)
                    .map_or_else(|| Value::Text(cell_text(cell)), Value::Date),
                ColumnRole::DocumentId
                | ColumnRole::NossoNumero
                | ColumnRole::TaxId
                | ColumnRole::Status
                | ColumnRole::Kind => Value::Text(cell_text(cell)),
                _ => Decimal::from_f64(*f).map_or(Value::Null, |d| {
                    if d.fract().is_zero() {
                        Value::Integer(*f as i64)
                    } else {
                        Value::Number(d.normalize())
                    }
                }),
            }
        }
        Data::String(s) => typed_value(s, role, DecimalStyle::Auto, ctx),
    }
}

/// Excel serial day numbers (1900 system) for date columns stored as plain numbers.
fn excel_serial_to_date(serial: f64) -> Option<chrono::NaiveDate> {
    if !(1.0..=2_958_465.0).contains(&serial) {
        return None;
    }
    let days = serial.floor() as i64;
    // Excel's day 1 is 1900-01-01 and it believes 1900 was a leap year.
    let base = chrono::NaiveDate::from_ymd_opt(1899, 12, 30)?;
    base.checked_add_signed(chrono::Duration::days(days))
}

#[cfg(test)]
mod tests {
    use super::*;
    use rust_xlsxwriter::{ExcelDateTime, Workbook};

    fn fixture() -> Vec<u8> {
        let mut wb = Workbook::new();
        let ws = wb.add_worksheet();
        ws.set_name("Títulos").unwrap();
        ws.write_string(0, 0, "Relatório de títulos").unwrap();
        for (c, h) in ["Vencimento", "Fornecedor", "Documento", "Valor", "Situação"]
            .iter()
            .enumerate()
        {
            ws.write_string(2, c as u16, *h).unwrap();
        }
        ws.write_datetime(3, 0, ExcelDateTime::from_ymd(2025, 3, 5).unwrap())
            .unwrap();
        ws.write_string(3, 1, "Empório Bela Vista").unwrap();
        ws.write_string(3, 2, "088231").unwrap();
        ws.write_number(3, 3, 17380.0).unwrap();
        ws.write_string(3, 4, "Aberto").unwrap();
        ws.write_string(4, 0, "10/03/2025").unwrap();
        ws.write_string(4, 1, "Padaria Central").unwrap();
        ws.write_number(4, 2, 88232.0).unwrap();
        ws.write_number(4, 3, 500.5).unwrap();
        ws.write_string(4, 4, "Pago").unwrap();
        let empty = wb.add_worksheet();
        empty.set_name("Vazia").unwrap();
        wb.save_to_buffer().unwrap()
    }

    #[test]
    fn parses_sheet_with_title_row_and_typed_cells() {
        let bytes = fixture();
        let (tables, warnings) = parse_xlsx(&bytes, "titulos", "src_x").unwrap();
        assert_eq!(tables.len(), 1, "{warnings:?}");
        assert_eq!(warnings.len(), 1);
        let t = &tables[0];
        assert_eq!(t.name, "titulos:Títulos");
        assert_eq!(t.len(), 2);
        assert_eq!(t.columns[0].role, ColumnRole::Date);
        assert_eq!(t.columns[3].role, ColumnRole::Amount);
        assert_eq!(
            t.value(0, 0),
            Some(&Value::Date(
                chrono::NaiveDate::from_ymd_opt(2025, 3, 5).unwrap()
            ))
        );
        assert_eq!(
            t.value(1, 0),
            Some(&Value::Date(
                chrono::NaiveDate::from_ymd_opt(2025, 3, 10).unwrap()
            ))
        );
        assert_eq!(
            t.value(0, 3),
            Some(&Value::Money(Money::from_centavos(1_738_000)))
        );
        assert_eq!(
            t.value(1, 3),
            Some(&Value::Money(Money::from_centavos(50_050)))
        );
        assert_eq!(t.value(0, 2).unwrap().as_text(), Some("088231"));
        assert_eq!(t.value(1, 2).unwrap().as_text(), Some("88232"));
    }

    #[test]
    fn garbage_is_an_error() {
        assert!(matches!(
            parse_xlsx(b"not a workbook", "x", "s"),
            Err(IngestError::Parse { .. })
        ));
        assert_eq!(parse_xlsx(b"", "x", "s"), Err(IngestError::Empty));
    }

    #[test]
    fn excel_serials() {
        assert_eq!(
            excel_serial_to_date(45_721.0),
            chrono::NaiveDate::from_ymd_opt(2025, 3, 5)
        );
        assert_eq!(excel_serial_to_date(0.0), None);
    }
}
