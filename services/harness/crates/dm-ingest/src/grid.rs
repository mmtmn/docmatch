//! Grids of cells (tables of a text document or an HTML page, records flattened from JSON or
//! XML) to typed [`Table`]s, with the same header detection, role inference and value typing
//! as spreadsheets and CSV, so every format feeds the same classification downstream.

use std::str::FromStr;

use dm_core::table::normalize_column_name;
use dm_core::{ColumnRole, Decimal, Money, Table, Value};
use dm_normalize::{sniff_decimal_style, DateContext, DecimalStyle};
use rust_decimal::prelude::ToPrimitive;

use crate::delimited::typed_value;
use crate::roles::{header_language_votes, infer_columns, HeaderVotes};

/// Rows sampled for role inference and the decimal style.
const SAMPLE_ROWS: usize = 200;
/// Rows searched for a header row.
const HEADER_SEARCH_ROWS: usize = 20;
/// Columns kept per table; wider grids are cut (and the caller warns).
pub(crate) const MAX_COLUMNS: usize = 256;
/// Rows kept per table.
pub(crate) const MAX_ROWS: usize = 200_000;
/// Characters kept per cell.
pub(crate) const MAX_CELL_CHARS: usize = 4_096;
/// Cells of one table once every row has the table's width: a grid with one wide row and
/// many short ones is padded to rows times width, so the rows are cut to fit.
pub(crate) const MAX_GRID_CELLS: usize = 1_000_000;

/// One cell before typing.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Cell {
    /// Nothing.
    Empty,
    /// Text as written (pt-BR or English number and date conventions).
    Text(String),
    /// A number from a typed format (JSON), in its canonical `.` decimal form.
    Number(String),
    /// A boolean from a typed format.
    Bool(bool),
}

impl Cell {
    /// The cell as text, for header detection and role inference.
    pub(crate) fn text(&self) -> &str {
        match self {
            Cell::Empty => "",
            Cell::Text(s) | Cell::Number(s) => s,
            Cell::Bool(true) => "true",
            Cell::Bool(false) => "false",
        }
    }

    fn is_empty(&self) -> bool {
        self.text().trim().is_empty()
    }
}

/// Text cut to [`MAX_CELL_CHARS`] characters, with surrounding whitespace removed and inner
/// whitespace runs collapsed.
pub(crate) fn clean_cell(raw: &str) -> String {
    let mut out = String::new();
    for (i, word) in raw.split_whitespace().enumerate() {
        if out.chars().count() >= MAX_CELL_CHARS {
            break;
        }
        if i > 0 {
            out.push(' ');
        }
        out.push_str(word);
    }
    if out.chars().count() > MAX_CELL_CHARS {
        out = out.chars().take(MAX_CELL_CHARS).collect();
    }
    out
}

fn is_textual(cell: &str) -> bool {
    let c = cell.trim();
    !c.is_empty() && c.parse::<f64>().is_err() && !crate::sniff::is_numeric_like(c)
}

/// The header row of a grid read from a document: the first of the first rows with at least
/// two words of the header vocabulary, else the first row that is mostly text and covers at
/// least half of the width (the spreadsheet rule). `None` when no row looks like a header.
fn header_row(rows: &[Vec<String>], width: usize) -> Option<usize> {
    let window = rows.len().min(HEADER_SEARCH_ROWS);
    for (i, row) in rows.iter().take(window).enumerate() {
        let refs: Vec<&str> = row.iter().map(String::as_str).collect();
        let votes = header_language_votes(&refs);
        let filled = row.iter().filter(|c| !c.trim().is_empty()).count();
        let textual = row.iter().filter(|c| is_textual(c)).count();
        if filled >= 2 && votes.pt + votes.en >= 2 && textual * 2 >= filled {
            return Some(i);
        }
    }
    for (i, row) in rows.iter().take(window).enumerate() {
        let filled = row.iter().filter(|c| !c.trim().is_empty()).count();
        let textual = row.iter().filter(|c| is_textual(c)).count();
        // Shape alone is weak evidence: a two-column grid of words is a key/value block
        // (favorecido, banco, agência, conta), not a table with a header.
        if width >= 3 && filled >= 2 && textual == filled && filled * 2 >= width {
            return Some(i);
        }
    }
    None
}

/// A table drawn in a document (DOCX, ODT, HTML): rows of cell texts, header row found by
/// [`header_row`]. `None` when the grid has no header or no data row under it (a layout
/// table, a signature block): its text then stays in the document's text.
pub(crate) fn table_from_rows(
    name: &str,
    source_id: &str,
    rows: Vec<Vec<String>>,
) -> Option<Table> {
    let (headers, data) = grid_records(rows)?;
    table_from_records(name, source_id, &headers, &headers, data)
}

/// The header and the data rows of a document grid ([`table_from_rows`] without typing), or
/// `None` when it is not a table; at most [`MAX_GRID_CELLS`] cells once padded.
pub(crate) fn grid_records(rows: Vec<Vec<String>>) -> Option<(Vec<String>, Vec<Vec<Cell>>)> {
    // Trailing columns empty in every row are formatting (an ODT row repeats empty cells).
    let width = rows
        .iter()
        .map(|r| {
            r.iter()
                .rposition(|c| !c.trim().is_empty())
                .map_or(0, |i| i + 1)
        })
        .max()
        .unwrap_or(0)
        .min(MAX_COLUMNS);
    if width < 2 || rows.len() < 2 {
        return None;
    }
    let rows: Vec<Vec<String>> = rows
        .into_iter()
        .take((MAX_ROWS + HEADER_SEARCH_ROWS).min(MAX_GRID_CELLS / width))
        .map(|mut r| {
            r.truncate(width);
            r.resize(width, String::new());
            r
        })
        .collect();
    let h = header_row(&rows, width)?;
    let headers = rows[h].clone();
    // The cells move into the records: no copy of the grid.
    let data: Vec<Vec<Cell>> = rows
        .into_iter()
        .skip(h + 1)
        // Tables split across pages repeat their header row.
        .filter(|r| *r != headers)
        .map(|r| {
            r.into_iter()
                .map(|c| {
                    if c.trim().is_empty() {
                        Cell::Empty
                    } else {
                        Cell::Text(c)
                    }
                })
                .collect()
        })
        .collect();
    data.iter()
        .any(|r| r.iter().any(|c| !c.is_empty()))
        .then_some((headers, data))
}

/// Records with known column names (`headers`, written to [`dm_core::Column::header`]) typed
/// into a table. `role_headers` are the words role inference reads (the leaf of a dotted JSON
/// path, for instance); empty rows are skipped. `None` without columns or data rows.
pub(crate) fn table_from_records(
    name: &str,
    source_id: &str,
    headers: &[String],
    role_headers: &[String],
    rows: Vec<Vec<Cell>>,
) -> Option<Table> {
    let width = headers.len();
    if width == 0 {
        return None;
    }
    let rows: Vec<Vec<Cell>> = rows
        .into_iter()
        .filter(|r| r.iter().any(|c| !c.is_empty()))
        .take(MAX_ROWS.min(MAX_GRID_CELLS / width))
        .map(|mut r| {
            r.truncate(width);
            r.resize(width, Cell::Empty);
            r
        })
        .collect();
    if rows.is_empty() {
        return None;
    }
    let sample: Vec<Vec<&str>> = rows
        .iter()
        .take(SAMPLE_ROWS)
        .map(|r| r.iter().map(Cell::text).collect())
        .collect();
    let role_refs: Vec<&str> = role_headers.iter().map(String::as_str).collect();
    let mut columns: Vec<dm_core::Column> = infer_columns(&role_refs, &sample)
        .into_iter()
        .map(|g| g.column)
        .collect();
    // XML attributes (`@moeda`, `valor.@moeda`) are metadata: they take a role from their name
    // only, never from the shape of their values (a currency code reads as a name).
    for ((col, header), word) in columns.iter_mut().zip(headers).zip(role_headers) {
        let attribute = header.starts_with('@') || header.contains(".@");
        if attribute && header_language_votes(&[word.as_str()]) == HeaderVotes::default() {
            col.role = ColumnRole::Unknown;
            col.subrole = None;
            col.confidence = 0.0;
        }
    }
    // Column names follow the full headers (dotted paths stay distinct), unique like CSV's.
    let mut seen = std::collections::HashSet::new();
    for (i, (col, header)) in columns.iter_mut().zip(headers).enumerate() {
        let base = if header.trim().is_empty() {
            format!("col_{}", i + 1)
        } else {
            normalize_column_name(header)
        };
        let mut unique = base.clone();
        let mut n = 2;
        while !seen.insert(unique.clone()) {
            unique = format!("{base}_{n}");
            n += 1;
        }
        col.name = unique;
        col.header = header.trim().to_string();
    }
    let style = sniff_decimal_style(
        &rows
            .iter()
            .take(SAMPLE_ROWS)
            .flat_map(|r| r.iter())
            .filter_map(|c| match c {
                Cell::Text(s) => Some(s.as_str()),
                _ => None,
            })
            .collect::<Vec<_>>(),
    );
    let ctx = DateContext::default();
    let mut table = Table::new(name, source_id, columns);
    for row in rows {
        let values: Vec<Value> = row
            .iter()
            .zip(table.columns.iter())
            .map(|(cell, col)| cell_value(cell, col.role, style, &ctx))
            .collect();
        if table.push_row(values).is_err() {
            return None;
        }
    }
    Some(table)
}

fn cell_value(cell: &Cell, role: ColumnRole, style: DecimalStyle, ctx: &DateContext) -> Value {
    match cell {
        Cell::Empty => Value::Null,
        Cell::Bool(b) => Value::Bool(*b),
        Cell::Text(s) => typed_value(s, role, style, ctx),
        Cell::Number(n) => number_value(n, role, ctx),
    }
}

/// A typed number: money for amount columns, text for identifiers (a CNPJ or a document
/// number written as a JSON number), integer or decimal otherwise.
fn number_value(n: &str, role: ColumnRole, ctx: &DateContext) -> Value {
    let decimal = Decimal::from_str(n)
        .or_else(|_| Decimal::from_scientific(n))
        .ok();
    match role {
        ColumnRole::Amount => decimal.map_or_else(
            || Value::Text(n.to_string()),
            |d| Value::Money(Money::from_decimal(d)),
        ),
        ColumnRole::Date
        | ColumnRole::DocumentId
        | ColumnRole::NossoNumero
        | ColumnRole::TaxId
        | ColumnRole::Status
        | ColumnRole::Kind => typed_value(n, role, DecimalStyle::Point, ctx),
        _ => match decimal {
            Some(d) if d.fract().is_zero() => d
                .to_i64()
                .map_or(Value::Number(d.normalize()), Value::Integer),
            Some(d) => Value::Number(d.normalize()),
            None => Value::Text(n.to_string()),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rows(lines: &[&[&str]]) -> Vec<Vec<String>> {
        lines
            .iter()
            .map(|r| r.iter().map(ToString::to_string).collect())
            .collect()
    }

    #[test]
    fn document_table_with_a_title_row_above_the_header() {
        let t = table_from_rows(
            "doc:tabela 1",
            "src",
            rows(&[
                &["Extrato de julho", ""],
                &["Data", "Histórico", "Documento", "Valor"],
                &[
                    "10/07/2026",
                    "LIQUIDACAO COBRANCA",
                    "000098765001",
                    "24.300,00",
                ],
                &["Data", "Histórico", "Documento", "Valor"],
                &["15/07/2026", "TARIFA", "", "-980,00"],
            ]),
        )
        .unwrap_or_else(|| panic!("sem tabela"));
        assert_eq!(t.len(), 2, "the repeated header is not a row");
        assert_eq!(t.columns[0].role, ColumnRole::Date);
        assert_eq!(t.columns[1].role, ColumnRole::FreeText);
        assert_eq!(t.columns[3].role, ColumnRole::Amount);
        assert_eq!(
            t.value(0, 3).and_then(Value::as_money),
            Some(Money::from_centavos(2_430_000))
        );
        assert_eq!(
            t.value(1, 3).and_then(Value::as_money),
            Some(Money::from_centavos(-98_000))
        );
    }

    #[test]
    fn layout_grids_are_not_tables() {
        assert!(table_from_rows("x", "s", rows(&[&["só uma coluna"], &["outra"]])).is_none());
        assert!(table_from_rows("x", "s", rows(&[&["Data", "Valor"]])).is_none());
        assert!(table_from_rows(
            "x",
            "s",
            rows(&[&["10/07/2026", "1,00"], &["11/07/2026", "2,00"]])
        )
        .is_none());
        assert!(table_from_rows("x", "s", Vec::new()).is_none());
    }

    #[test]
    fn two_columns_of_words_need_header_words() {
        // Bank details in a two-column box: words over words, not a header over data.
        let details = rows(&[
            &["Favorecido", "Caixas Boa Vista Ltda"],
            &["Banco", "Itaú"],
            &["Agência", "0987"],
        ]);
        assert!(table_from_rows("x", "s", details).is_none());
        // The same shape with header words is a table.
        let t = table_from_rows(
            "x",
            "s",
            rows(&[&["Data", "Valor"], &["10/07/2026", "1,00"]]),
        );
        assert!(t.is_some_and(|t| t.columns[1].role == ColumnRole::Amount));
        // Three columns of words over data: the shape is enough.
        let t = table_from_rows(
            "x",
            "s",
            rows(&[
                &["Cliente", "Cidade", "Contato"],
                &["Padaria", "Recife", "Ana"],
            ]),
        );
        assert!(t.is_some_and(|t| t.len() == 1));
    }

    #[test]
    fn records_keep_dotted_names_and_type_numbers() {
        let headers = vec![
            "cliente.nome".to_string(),
            "cliente.cnpj".to_string(),
            "valor".to_string(),
            "parcelas".to_string(),
            "pago".to_string(),
        ];
        let roles = vec![
            "nome".to_string(),
            "cnpj".to_string(),
            "valor".to_string(),
            "parcelas".to_string(),
            "pago".to_string(),
        ];
        let t = table_from_records(
            "t",
            "s",
            &headers,
            &roles,
            vec![vec![
                Cell::Text("Moinho Azul".into()),
                Cell::Number("90123456000134".into()),
                Cell::Number("27850.5".into()),
                Cell::Number("3".into()),
                Cell::Bool(false),
            ]],
        )
        .unwrap_or_else(|| panic!("sem tabela"));
        let names: Vec<&str> = t.columns.iter().map(|c| c.name.as_str()).collect();
        assert_eq!(
            names,
            vec!["cliente_nome", "cliente_cnpj", "valor", "parcelas", "pago"]
        );
        assert_eq!(t.columns[1].role, ColumnRole::TaxId);
        assert_eq!(t.columns[1].header, "cliente.cnpj");
        assert_eq!(t.value(0, 1), Some(&Value::Text("90123456000134".into())));
        assert_eq!(
            t.value(0, 2).and_then(Value::as_money),
            Some(Money::from_centavos(2_785_050))
        );
        assert_eq!(t.value(0, 3), Some(&Value::Integer(3)));
        assert_eq!(t.value(0, 4), Some(&Value::Bool(false)));
        assert!(table_from_records("t", "s", &[], &[], Vec::new()).is_none());
        assert!(table_from_records("t", "s", &headers, &roles, vec![vec![Cell::Empty]]).is_none());
    }

    #[test]
    fn cells_are_cleaned_and_capped() {
        assert_eq!(clean_cell("  a \n  b\t c "), "a b c");
        assert_eq!(
            clean_cell(&"x".repeat(MAX_CELL_CHARS + 10)).len(),
            MAX_CELL_CHARS
        );
        assert_eq!(
            number_value("1e3", ColumnRole::Unknown, &DateContext::default()),
            Value::Integer(1000)
        );
        assert_eq!(
            number_value("abc", ColumnRole::Unknown, &DateContext::default()),
            Value::Text("abc".into())
        );
    }
}
