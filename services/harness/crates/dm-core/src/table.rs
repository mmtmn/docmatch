//! Typed tables: columns with inferred roles and rows of [`Value`]s.

use std::fmt;

use chrono::{NaiveDate, NaiveDateTime};
use rust_decimal::Decimal;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};

use crate::error::CoreError;
use crate::evidence::Locator;
use crate::money::Money;

/// The role a column plays in reconciliation, inferred from header words and value shapes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema, Default)]
#[serde(rename_all = "snake_case")]
pub enum ColumnRole {
    /// Monetary amount (valor, total, crédito, débito).
    Amount,
    /// Date or timestamp (emissão, vencimento, pagamento, data).
    Date,
    /// Document number (nota fiscal, pedido, título, duplicata, CT-e, chave).
    DocumentId,
    /// Bank slip identifier ("nosso número") printed on boletos.
    NossoNumero,
    /// CNPJ or CPF.
    TaxId,
    /// Counterparty name (fornecedor, cliente, favorecido, histórico com nome).
    Name,
    /// Status word (aberto, pago, baixado, cancelado, paid flag).
    Status,
    /// Type or category (tipo, natureza, espécie, operação).
    Kind,
    /// Free text (histórico, descrição, observação).
    FreeText,
    /// Nothing recognizable.
    #[default]
    Unknown,
}

impl ColumnRole {
    /// All roles, in display order.
    pub const ALL: [ColumnRole; 10] = [
        ColumnRole::Amount,
        ColumnRole::Date,
        ColumnRole::DocumentId,
        ColumnRole::NossoNumero,
        ColumnRole::TaxId,
        ColumnRole::Name,
        ColumnRole::Status,
        ColumnRole::Kind,
        ColumnRole::FreeText,
        ColumnRole::Unknown,
    ];

    /// Snake-case name used in JSON and SQL column comments.
    pub fn as_str(self) -> &'static str {
        match self {
            ColumnRole::Amount => "amount",
            ColumnRole::Date => "date",
            ColumnRole::DocumentId => "document_id",
            ColumnRole::NossoNumero => "nosso_numero",
            ColumnRole::TaxId => "tax_id",
            ColumnRole::Name => "name",
            ColumnRole::Status => "status",
            ColumnRole::Kind => "kind",
            ColumnRole::FreeText => "free_text",
            ColumnRole::Unknown => "unknown",
        }
    }
}

impl fmt::Display for ColumnRole {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// A cell value. Text is kept verbatim; typed variants come from normalization.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema, Default)]
#[serde(tag = "type", content = "value", rename_all = "snake_case")]
pub enum Value {
    /// Empty cell.
    #[default]
    Null,
    /// Verbatim text.
    Text(String),
    /// Monetary amount in centavos.
    Money(Money),
    /// Non-monetary decimal (rates, quantities).
    Number(Decimal),
    /// Integer (counts, codes without leading zeros).
    Integer(i64),
    /// Boolean flag.
    Bool(bool),
    /// Calendar date.
    Date(NaiveDate),
    /// Date and time (local, no zone: Brazilian documents never carry one).
    DateTime(NaiveDateTime),
}

impl Value {
    /// Text content, if the value is text.
    pub fn as_text(&self) -> Option<&str> {
        match self {
            Value::Text(s) => Some(s),
            _ => None,
        }
    }

    /// Money content, if the value is money.
    pub fn as_money(&self) -> Option<Money> {
        match self {
            Value::Money(m) => Some(*m),
            _ => None,
        }
    }

    /// Date content; a date-time yields its date.
    pub fn as_date(&self) -> Option<NaiveDate> {
        match self {
            Value::Date(d) => Some(*d),
            Value::DateTime(dt) => Some(dt.date()),
            _ => None,
        }
    }

    /// Integer content.
    pub fn as_integer(&self) -> Option<i64> {
        match self {
            Value::Integer(i) => Some(*i),
            _ => None,
        }
    }

    /// True for [`Value::Null`] and for text that is empty after trimming.
    pub fn is_null(&self) -> bool {
        match self {
            Value::Null => true,
            Value::Text(s) => s.trim().is_empty(),
            _ => false,
        }
    }

    /// A plain string rendering used by keys, reports and SQL registration.
    pub fn render(&self) -> String {
        match self {
            Value::Null => String::new(),
            Value::Text(s) => s.clone(),
            Value::Money(m) => m.format_plain(),
            Value::Number(d) => d.normalize().to_string(),
            Value::Integer(i) => i.to_string(),
            Value::Bool(b) => if *b { "true" } else { "false" }.to_string(),
            Value::Date(d) => d.format("%d/%m/%Y").to_string(),
            Value::DateTime(dt) => dt.format("%d/%m/%Y %H:%M").to_string(),
        }
    }
}

impl fmt::Display for Value {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.render())
    }
}

impl From<&str> for Value {
    fn from(s: &str) -> Self {
        if s.trim().is_empty() {
            Value::Null
        } else {
            Value::Text(s.to_string())
        }
    }
}

impl From<String> for Value {
    fn from(s: String) -> Self {
        if s.trim().is_empty() {
            Value::Null
        } else {
            Value::Text(s)
        }
    }
}

impl From<Money> for Value {
    fn from(m: Money) -> Self {
        Value::Money(m)
    }
}

impl From<NaiveDate> for Value {
    fn from(d: NaiveDate) -> Self {
        Value::Date(d)
    }
}

impl From<i64> for Value {
    fn from(i: i64) -> Self {
        Value::Integer(i)
    }
}

/// A column: normalized name, original header, inferred role and an optional sub-role
/// (`due`, `paid`, `issue`, `credit`, `debit`, ...) that refines the role.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Column {
    /// Normalized identifier (lowercase ASCII snake_case), unique within the table.
    pub name: String,
    /// Header text exactly as it appeared in the source.
    pub header: String,
    /// Inferred role.
    pub role: ColumnRole,
    /// Refinement of the role (e.g. `due`, `paid`, `issue` for dates).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub subrole: Option<String>,
    /// Confidence of the inference, `0..=1`.
    #[serde(default = "one")]
    pub confidence: f64,
}

fn one() -> f64 {
    1.0
}

impl Column {
    /// Column with the given role and full confidence; the name is normalized from the header.
    pub fn new(header: impl Into<String>, role: ColumnRole) -> Self {
        let header = header.into();
        Column {
            name: normalize_column_name(&header),
            header,
            role,
            subrole: None,
            confidence: 1.0,
        }
    }

    /// Sets the sub-role.
    pub fn with_subrole(mut self, subrole: impl Into<String>) -> Self {
        self.subrole = Some(subrole.into());
        self
    }

    /// Sets the confidence.
    pub fn with_confidence(mut self, confidence: f64) -> Self {
        self.confidence = confidence.clamp(0.0, 1.0);
        self
    }
}

/// Lowercases, strips accents (Latin-1 range) and keeps `[a-z0-9_]` for use as an identifier.
pub fn normalize_column_name(header: &str) -> String {
    let mut out = String::with_capacity(header.len());
    let mut last_underscore = true;
    for c in header.chars() {
        let m = fold_latin(c).unwrap_or(c).to_ascii_lowercase();
        if m.is_ascii_alphanumeric() {
            out.push(m);
            last_underscore = false;
        } else if !last_underscore {
            out.push('_');
            last_underscore = true;
        }
    }
    let trimmed = out.trim_end_matches('_').to_string();
    if trimmed.is_empty() {
        "col".to_string()
    } else if trimmed.starts_with(|c: char| c.is_ascii_digit()) {
        format!("c_{trimmed}")
    } else {
        trimmed
    }
}

/// Folds the accented Latin letters used in Portuguese to their ASCII base letter.
pub fn fold_latin(c: char) -> Option<char> {
    Some(match c {
        'á' | 'à' | 'â' | 'ã' | 'ä' | 'å' => 'a',
        'Á' | 'À' | 'Â' | 'Ã' | 'Ä' | 'Å' => 'A',
        'é' | 'è' | 'ê' | 'ë' => 'e',
        'É' | 'È' | 'Ê' | 'Ë' => 'E',
        'í' | 'ì' | 'î' | 'ï' => 'i',
        'Í' | 'Ì' | 'Î' | 'Ï' => 'I',
        'ó' | 'ò' | 'ô' | 'õ' | 'ö' => 'o',
        'Ó' | 'Ò' | 'Ô' | 'Õ' | 'Ö' => 'O',
        'ú' | 'ù' | 'û' | 'ü' => 'u',
        'Ú' | 'Ù' | 'Û' | 'Ü' => 'U',
        'ç' => 'c',
        'Ç' => 'C',
        'ñ' => 'n',
        'Ñ' => 'N',
        'º' | 'ª' | '°' => 'o',
        _ => return None,
    })
}

/// A row of values, positionally aligned with the table columns.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Record {
    /// Zero-based data row index (header excluded).
    pub row: usize,
    /// One value per column.
    pub values: Vec<Value>,
}

impl Record {
    /// Value at a column index.
    pub fn get(&self, column: usize) -> Option<&Value> {
        self.values.get(column)
    }
}

/// A typed table parsed from one source (a CSV file, an XLSX sheet, an OFX statement).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Table {
    /// Table name (file stem or sheet name), unique within a run.
    pub name: String,
    /// Identifier of the source document this table came from.
    pub source_id: String,
    /// Columns in source order.
    pub columns: Vec<Column>,
    /// Data rows.
    pub rows: Vec<Record>,
}

impl Table {
    /// Empty table with the given columns.
    pub fn new(
        name: impl Into<String>,
        source_id: impl Into<String>,
        columns: Vec<Column>,
    ) -> Self {
        Table {
            name: name.into(),
            source_id: source_id.into(),
            columns,
            rows: Vec::new(),
        }
    }

    /// Appends a row; the number of values must equal the number of columns.
    pub fn push_row(&mut self, values: Vec<Value>) -> Result<usize, CoreError> {
        if values.len() != self.columns.len() {
            return Err(CoreError::RowArity {
                row: self.rows.len(),
                got: values.len(),
                expected: self.columns.len(),
            });
        }
        let row = self.rows.len();
        self.rows.push(Record { row, values });
        Ok(row)
    }

    /// Number of data rows.
    pub fn len(&self) -> usize {
        self.rows.len()
    }

    /// True when there are no data rows.
    pub fn is_empty(&self) -> bool {
        self.rows.is_empty()
    }

    /// Index of the column with this normalized name.
    pub fn column_index(&self, name: &str) -> Option<usize> {
        self.columns.iter().position(|c| c.name == name)
    }

    /// Indexes of every column with the given role.
    pub fn columns_with_role(&self, role: ColumnRole) -> Vec<usize> {
        self.columns
            .iter()
            .enumerate()
            .filter(|(_, c)| c.role == role)
            .map(|(i, _)| i)
            .collect()
    }

    /// Index of the first column with the given role.
    pub fn first_column_with_role(&self, role: ColumnRole) -> Option<usize> {
        self.columns.iter().position(|c| c.role == role)
    }

    /// Index of the first column with the given role and sub-role.
    pub fn column_with_subrole(&self, role: ColumnRole, subrole: &str) -> Option<usize> {
        self.columns
            .iter()
            .position(|c| c.role == role && c.subrole.as_deref() == Some(subrole))
    }

    /// Value at `(row, column)`.
    pub fn value(&self, row: usize, column: usize) -> Option<&Value> {
        self.rows.get(row).and_then(|r| r.values.get(column))
    }

    /// Locator for a row of this table.
    pub fn locator(&self, row: usize) -> Locator {
        Locator::Row {
            table: self.name.clone(),
            row,
        }
    }

    /// Iterates `(row index, record)`.
    pub fn iter(&self) -> impl Iterator<Item = (usize, &Record)> {
        self.rows.iter().map(|r| (r.row, r))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_headers() {
        assert_eq!(
            normalize_column_name("Data de Vencimento"),
            "data_de_vencimento"
        );
        assert_eq!(normalize_column_name("Valor (R$)"), "valor_r");
        assert_eq!(normalize_column_name("Nº Documento"), "no_documento");
        assert_eq!(normalize_column_name("  CNPJ/CPF "), "cnpj_cpf");
        assert_eq!(normalize_column_name("Emissão"), "emissao");
        assert_eq!(normalize_column_name(""), "col");
        assert_eq!(normalize_column_name("1a coluna"), "c_1a_coluna");
        assert_eq!(normalize_column_name("Histórico"), "historico");
    }

    #[test]
    fn table_rows_are_checked() {
        let mut t = Table::new(
            "titulos",
            "src-1",
            vec![
                Column::new("Valor", ColumnRole::Amount),
                Column::new("Data", ColumnRole::Date),
            ],
        );
        assert!(t.push_row(vec![Value::from("x")]).is_err());
        let row = t
            .push_row(vec![Value::Money(Money::from_centavos(100)), Value::Null])
            .unwrap();
        assert_eq!(row, 0);
        assert_eq!(t.len(), 1);
        assert_eq!(t.column_index("valor"), Some(0));
        assert_eq!(t.first_column_with_role(ColumnRole::Date), Some(1));
        assert_eq!(t.columns_with_role(ColumnRole::Amount), vec![0]);
        assert_eq!(
            t.value(0, 0).unwrap().as_money(),
            Some(Money::from_centavos(100))
        );
        assert_eq!(
            t.locator(0),
            Locator::Row {
                table: "titulos".into(),
                row: 0
            }
        );
    }

    #[test]
    fn value_helpers() {
        assert!(Value::from("   ").is_null());
        assert!(!Value::from("a").is_null());
        assert_eq!(Value::from("a").as_text(), Some("a"));
        let d = NaiveDate::from_ymd_opt(2025, 3, 7).unwrap();
        assert_eq!(Value::Date(d).render(), "07/03/2025");
        assert_eq!(
            Value::Money(Money::from_centavos(123_456)).render(),
            "1.234,56"
        );
        assert_eq!(Value::Number(Decimal::new(1500, 2)).render(), "15");
        assert_eq!(
            Value::DateTime(d.and_hms_opt(9, 5, 0).unwrap()).as_date(),
            Some(d)
        );
    }

    #[test]
    fn value_serde_is_tagged() {
        let v = Value::Money(Money::from_centavos(5));
        let json = serde_json::to_value(&v).unwrap();
        assert_eq!(json, serde_json::json!({"type": "money", "value": 5}));
        let back: Value = serde_json::from_value(json).unwrap();
        assert_eq!(back, v);
        assert_eq!(
            serde_json::to_value(Value::Null).unwrap(),
            serde_json::json!({"type": "null"})
        );
    }
}
