//! Data formats with their own structure: JSON, JSON Lines and XML other than NF-e/CT-e.
//!
//! Arrays of records become tables: a JSON array whose items are mostly objects (anywhere in
//! the document, except inside another record), the JSON Lines objects, or XML elements that
//! repeat under one parent and carry child elements or attributes. Nested keys become dotted
//! columns (`cliente.cnpj`) down to [`MAX_FLATTEN_DEPTH`] levels; deeper values and arrays
//! inside a record become text. A JSON array of arrays is a grid with a header row. A document
//! with no record array but scalar fields is one record. Roles are inferred from the last key
//! of each path, so `cliente.cnpj` is a tax id. Memory and time are bounded: the input is
//! read only up to [`MAX_INPUT`] bytes; a JSON document (or JSON Lines line) up to
//! [`MAX_JSON_DOCUMENT`] bytes and [`MAX_JSON_COST`] bytes of estimated memory, nesting at
//! most 128 levels (serde_json's limit); XML at most [`MAX_XML_DEPTH`] levels,
//! [`MAX_XML_NODES`] elements and [`MAX_XML_ATTRS`] attributes, with the text of an element
//! cut at [`MAX_XML_TEXT`] bytes; records at most [`MAX_RECORD_CELLS`] cells per file, and
//! tables at most [`crate::grid::MAX_ROWS`] rows and [`crate::grid::MAX_COLUMNS`] columns.

use dm_core::Table;
use encoding_rs::Encoding;
use indexmap::IndexMap;
use quick_xml::events::{BytesStart, Event};
use quick_xml::Reader;

/// A JSON value that keeps the order of object keys as written (serde_json's map sorts
/// them, and column order follows the document).
#[derive(Clone, Debug, PartialEq)]
enum Json {
    Null,
    Bool(bool),
    Number(serde_json::Number),
    String(String),
    Array(Vec<Json>),
    Object(Box<IndexMap<String, Json>>),
}

impl Json {
    fn is_object(&self) -> bool {
        matches!(self, Json::Object(_))
    }

    fn as_array(&self) -> Option<&Vec<Json>> {
        match self {
            Json::Array(a) => Some(a),
            _ => None,
        }
    }
}

impl std::fmt::Display for Json {
    /// Compact JSON.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let quoted = |s: &str| serde_json::to_string(s).unwrap_or_default();
        match self {
            Json::Null => f.write_str("null"),
            Json::Bool(b) => write!(f, "{b}"),
            Json::Number(n) => write!(f, "{n}"),
            Json::String(s) => f.write_str(&quoted(s)),
            Json::Array(items) => {
                f.write_str("[")?;
                for (i, item) in items.iter().enumerate() {
                    if i > 0 {
                        f.write_str(",")?;
                    }
                    write!(f, "{item}")?;
                }
                f.write_str("]")
            }
            Json::Object(fields) => {
                f.write_str("{")?;
                for (i, (k, v)) in fields.iter().enumerate() {
                    if i > 0 {
                        f.write_str(",")?;
                    }
                    write!(f, "{}:{v}", quoted(k))?;
                }
                f.write_str("}")
            }
        }
    }
}

/// Memory a JSON value may take once read, estimated: a node per value, the bytes of every
/// string, a map per object and a slot per key. It bounds the tree whatever the document
/// looks like on disk (`[{},{},...]` costs a hundred bytes of memory per three of input).
pub(crate) const MAX_JSON_COST: usize = 96 << 20;
/// A value's node.
const VALUE_COST: usize = std::mem::size_of::<Json>();
/// An object's boxed map and the base of its index.
const OBJECT_COST: usize = 128;
/// A key's slot (string, hash and index entry), besides its bytes.
const ENTRY_COST: usize = 64;

/// Message of the error a [`Budget`] raises when it runs out.
const BUDGET_EXCEEDED: &str = "limite de memória do JSON excedido";

/// The memory left to a value and everything nested in it.
struct Budget {
    left: std::cell::Cell<usize>,
    every: std::cell::Cell<u32>,
    exhausted: std::cell::Cell<bool>,
}

impl Budget {
    fn new(n: usize) -> Budget {
        Budget {
            left: std::cell::Cell::new(n),
            every: std::cell::Cell::new(0),
            exhausted: std::cell::Cell::new(false),
        }
    }

    fn take<E: serde::de::Error>(&self, cost: usize) -> Result<(), E> {
        let n = self.every.get().wrapping_add(1);
        self.every.set(n);
        if n.is_multiple_of(4096) && crate::cancel::cancelled() {
            return Err(E::custom(crate::cancel::STOPPED));
        }
        if let Some(left) = self.left.get().checked_sub(cost) {
            self.left.set(left);
            Ok(())
        } else {
            self.exhausted.set(true);
            Err(E::custom(BUDGET_EXCEEDED))
        }
    }

    fn exhausted(&self) -> bool {
        self.exhausted.get()
    }
}

/// Deserializes one value under a budget.
struct Seed<'a>(&'a Budget);

impl<'de> serde::de::DeserializeSeed<'de> for Seed<'_> {
    type Value = Json;

    fn deserialize<D: serde::Deserializer<'de>>(self, d: D) -> Result<Json, D::Error> {
        d.deserialize_any(JsonVisitor(self.0))
    }
}

struct JsonVisitor<'a>(&'a Budget);

impl<'de> serde::de::Visitor<'de> for JsonVisitor<'_> {
    type Value = Json;

    fn expecting(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("um valor JSON")
    }

    fn visit_unit<E: serde::de::Error>(self) -> Result<Json, E> {
        self.0.take(VALUE_COST)?;
        Ok(Json::Null)
    }

    fn visit_none<E: serde::de::Error>(self) -> Result<Json, E> {
        self.0.take(VALUE_COST)?;
        Ok(Json::Null)
    }

    fn visit_bool<E: serde::de::Error>(self, v: bool) -> Result<Json, E> {
        self.0.take(VALUE_COST)?;
        Ok(Json::Bool(v))
    }

    fn visit_i64<E: serde::de::Error>(self, v: i64) -> Result<Json, E> {
        self.0.take(VALUE_COST)?;
        Ok(Json::Number(v.into()))
    }

    fn visit_u64<E: serde::de::Error>(self, v: u64) -> Result<Json, E> {
        self.0.take(VALUE_COST)?;
        Ok(Json::Number(v.into()))
    }

    fn visit_f64<E: serde::de::Error>(self, v: f64) -> Result<Json, E> {
        self.0.take(VALUE_COST)?;
        Ok(serde_json::Number::from_f64(v).map_or(Json::Null, Json::Number))
    }

    fn visit_str<E: serde::de::Error>(self, v: &str) -> Result<Json, E> {
        self.0.take(VALUE_COST + v.len())?;
        Ok(Json::String(v.to_string()))
    }

    fn visit_string<E: serde::de::Error>(self, v: String) -> Result<Json, E> {
        self.0.take(VALUE_COST + v.len())?;
        Ok(Json::String(v))
    }

    fn visit_seq<A: serde::de::SeqAccess<'de>>(self, mut seq: A) -> Result<Json, A::Error> {
        self.0.take(VALUE_COST)?;
        let mut items = Vec::new();
        while let Some(item) = seq.next_element_seed(Seed(self.0))? {
            items.push(item);
        }
        items.shrink_to_fit();
        Ok(Json::Array(items))
    }

    fn visit_map<A: serde::de::MapAccess<'de>>(self, mut map: A) -> Result<Json, A::Error> {
        self.0.take(VALUE_COST + OBJECT_COST)?;
        let mut fields: IndexMap<String, Json> = IndexMap::new();
        while let Some(k) = map.next_key::<String>()? {
            self.0.take(ENTRY_COST + k.len())?;
            let v = map.next_value_seed(Seed(self.0))?;
            // A repeated key keeps its first position and its last value, like serde_json.
            fields.insert(k, v);
        }
        fields.shrink_to_fit();
        Ok(Json::Object(Box::new(fields)))
    }
}

/// One JSON value from text under `budget`; the error keeps serde_json's classification.
fn parse_value(text: &str, budget: &Budget) -> Result<Json, serde_json::Error> {
    use serde::de::DeserializeSeed;
    let mut de = serde_json::Deserializer::from_str(text);
    let v = Seed(budget).deserialize(&mut de)?;
    de.end()?;
    Ok(v)
}

use crate::grid::{clean_cell, table_from_records, table_from_rows, Cell, MAX_COLUMNS, MAX_ROWS};
use crate::IngestError;

/// Bytes of a JSON or XML document read; JSON Lines are read line by line up to the same size.
pub(crate) const MAX_INPUT: usize = 64 << 20;
/// Bytes of a whole JSON document, and of one JSON Lines line (each is materialized before
/// its records are found).
pub(crate) const MAX_JSON_DOCUMENT: usize = 8 << 20;
/// Cells of records kept per document (every table, JSON Lines and XML included), counted
/// as written and once more as a full grid: a file whose records each bring new keys would
/// otherwise become a grid of rows times every key.
pub(crate) const MAX_RECORD_CELLS: usize = 2_000_000;
/// Bytes of text kept per XML element (text and each attribute value).
const MAX_XML_TEXT: usize = crate::grid::MAX_CELL_CHARS * 4;
/// Attributes kept per XML document.
const MAX_XML_ATTRS: usize = 1_000_000;
/// Levels of nested objects flattened into dotted columns.
const MAX_FLATTEN_DEPTH: usize = 4;
/// Levels searched for record arrays.
const MAX_SEARCH_DEPTH: usize = 16;
/// Characters of a nested value written as text.
const MAX_NESTED_TEXT: usize = 500;
/// Tables per document.
const MAX_TABLES: usize = 64;
/// Element nesting followed in XML.
const MAX_XML_DEPTH: usize = 256;
/// Elements kept in memory from one XML document (each one keeps its name, attributes and
/// text: about 200 bytes plus its strings).
const MAX_XML_NODES: usize = 300_000;

/// What a structured document yielded.
#[derive(Debug, Default)]
pub struct StructuredParsed {
    /// Tables of records.
    pub tables: Vec<Table>,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
}

/// Records with their columns in first-seen order; a row keeps the cells it has, by column.
#[derive(Default)]
struct Records {
    columns: IndexMap<String, ()>,
    rows: Vec<Vec<(u16, Cell)>>,
    cells: usize,
    dropped_columns: bool,
    /// The cell budget stopped the reading.
    cut: bool,
}

impl Records {
    /// Adds a record; false once the rows or the cells of the document are spent.
    fn push(&mut self, record: IndexMap<String, Cell>, budget: &mut usize) -> bool {
        if self.rows.len() >= MAX_ROWS {
            return false;
        }
        if record.len() > *budget {
            self.cut = true;
            return false;
        }
        *budget -= record.len();
        let mut kept = Vec::with_capacity(record.len());
        for (k, v) in record {
            let i = if let Some(i) = self.columns.get_index_of(&k) {
                i
            } else {
                if self.columns.len() >= MAX_COLUMNS {
                    self.dropped_columns = true;
                    continue;
                }
                self.columns.insert(k, ());
                self.columns.len() - 1
            };
            kept.push((i as u16, v));
        }
        self.cells += kept.len();
        self.rows.push(kept);
        true
    }

    /// The table, as many rows as a full grid of them fits in `budget`.
    fn into_table(self, name: &str, source_id: &str, budget: &mut usize) -> Option<Table> {
        let headers: Vec<String> = self.columns.keys().cloned().collect();
        let roles: Vec<String> = headers.iter().map(|h| role_word(h)).collect();
        let width = headers.len().max(1);
        let fit = (*budget / width).min(self.rows.len());
        *budget -= fit * width;
        let rows: Vec<Vec<Cell>> = self
            .rows
            .into_iter()
            .take(fit)
            .map(|r| {
                let mut row = vec![Cell::Empty; headers.len()];
                for (i, c) in r {
                    row[usize::from(i)] = c;
                }
                row
            })
            .collect();
        table_from_records(name, source_id, &headers, &roles, rows)
    }

    /// Rows the grid budget leaves out of the table.
    fn rows_over(&self, budget: usize) -> usize {
        let width = self.columns.len().max(1);
        self.rows.len().saturating_sub(budget / width)
    }
}

/// The word role inference reads for a dotted path: its last key (an XML attribute's `@`
/// dropped), so `cliente.cnpj` reads as `cnpj`.
fn role_word(path: &str) -> String {
    path.rsplit('.')
        .next()
        .unwrap_or(path)
        .trim_start_matches('@')
        .to_string()
}

fn truncated(mut s: String) -> String {
    if s.chars().count() > MAX_NESTED_TEXT {
        s = s.chars().take(MAX_NESTED_TEXT).collect();
        s.push('…');
    }
    s
}

// ------------------------------------------------------------------------------------ JSON

fn scalar(v: &Json) -> Option<Cell> {
    match v {
        Json::Null => Some(Cell::Empty),
        Json::Bool(b) => Some(Cell::Bool(*b)),
        Json::Number(n) => Some(Cell::Number(n.to_string())),
        Json::String(s) => Some(if s.trim().is_empty() {
            Cell::Empty
        } else {
            Cell::Text(clean_cell(s))
        }),
        Json::Array(_) | Json::Object(_) => None,
    }
}

fn flatten_json(prefix: &str, v: &Json, depth: usize, out: &mut IndexMap<String, Cell>) {
    match v {
        Json::Object(map) if depth < MAX_FLATTEN_DEPTH => {
            for (k, child) in map.iter() {
                let key = if prefix.is_empty() {
                    k.clone()
                } else {
                    format!("{prefix}.{k}")
                };
                flatten_json(&key, child, depth + 1, out);
            }
        }
        Json::Array(items) if items.iter().all(|i| scalar(i).is_some()) => {
            let parts: Vec<String> = items
                .iter()
                .filter_map(scalar)
                .map(|c| c.text().to_string())
                .filter(|s| !s.is_empty())
                .collect();
            let joined = parts.join("; ");
            out.insert(
                prefix.to_string(),
                if joined.is_empty() {
                    Cell::Empty
                } else {
                    Cell::Text(clean_cell(&joined))
                },
            );
        }
        Json::Array(_) | Json::Object(_) => {
            out.insert(prefix.to_string(), Cell::Text(truncated(v.to_string())));
        }
        other => {
            if let Some(c) = scalar(other) {
                out.insert(prefix.to_string(), c);
            }
        }
    }
}

fn record_of(v: &Json) -> IndexMap<String, Cell> {
    let mut out = IndexMap::new();
    match v {
        Json::Object(_) => flatten_json("", v, 0, &mut out),
        other => flatten_json("valor", other, MAX_FLATTEN_DEPTH, &mut out),
    }
    out
}

fn is_record_array(items: &[Json]) -> bool {
    !items.is_empty() && items.iter().filter(|i| i.is_object()).count() * 2 >= items.len()
}

fn is_grid(items: &[Json]) -> bool {
    items.len() >= 2
        && items.iter().all(|i| {
            i.as_array()
                .is_some_and(|row| row.iter().all(|c| scalar(c).is_some()))
        })
}

/// Record arrays and grids of a document, by path (`titulos`, `dados.lancamentos`).
fn find_tables<'a>(path: &str, v: &'a Json, depth: usize, out: &mut Vec<(String, &'a Json)>) {
    if depth > MAX_SEARCH_DEPTH || out.len() >= MAX_TABLES {
        return;
    }
    match v {
        Json::Array(items) if is_record_array(items) || is_grid(items) => {
            out.push((path.to_string(), v));
        }
        Json::Array(items) => {
            for (i, item) in items.iter().enumerate().take(MAX_TABLES) {
                find_tables(&format!("{path}[{i}]"), item, depth + 1, out);
            }
        }
        Json::Object(map) => {
            for (k, child) in map.iter() {
                let p = if path.is_empty() {
                    k.clone()
                } else {
                    format!("{path}.{k}")
                };
                find_tables(&p, child, depth + 1, out);
            }
        }
        _ => {}
    }
}

fn table_name(stem: &str, path: &str) -> String {
    if path.is_empty() {
        stem.to_string()
    } else {
        format!("{stem}:{path}")
    }
}

fn json_text(bytes: &[u8]) -> String {
    let (encoding, rest) =
        Encoding::for_bom(bytes).map_or((encoding_rs::UTF_8, bytes), |(e, n)| (e, &bytes[n..]));
    encoding.decode_without_bom_handling(rest).0.into_owned()
}

fn cut_input(bytes: &[u8], warnings: &mut Vec<String>, what: &str) -> usize {
    if bytes.len() > MAX_INPUT {
        warnings.push(format!(
            "{what} acima de {} MB: só o começo foi lido",
            MAX_INPUT >> 20
        ));
        MAX_INPUT
    } else {
        bytes.len()
    }
}

fn record_warnings(records: &Records, name: &str, budget: usize, warnings: &mut Vec<String>) {
    if records.dropped_columns {
        warnings.push(format!(
            "{name}: mais de {MAX_COLUMNS} campos; os excedentes ficaram de fora"
        ));
    }
    if records.rows.len() >= MAX_ROWS {
        warnings.push(format!(
            "{name}: só os primeiros {MAX_ROWS} registros foram lidos"
        ));
    }
    let over = records.rows_over(budget);
    if records.cut || over > 0 {
        warnings.push(format!(
            "{name}: registros cortados no limite de {MAX_RECORD_CELLS} células por arquivo"
        ));
    }
}

/// Parses a JSON document into tables of records.
pub fn parse_json(
    bytes: &[u8],
    stem: &str,
    source_id: &str,
) -> Result<StructuredParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let mut out = StructuredParsed::default();
    if bytes.len() > MAX_JSON_DOCUMENT {
        return Err(IngestError::Unrecognized(format!(
            "JSON acima de {} MB não é lido (use JSON Lines para arquivos grandes)",
            MAX_JSON_DOCUMENT >> 20
        )));
    }
    let text = json_text(bytes);
    let budget = Budget::new(MAX_JSON_COST);
    let doc: Json = match parse_value(&text, &budget) {
        Ok(d) => d,
        // One object per line: JSON Lines whose first line was too long to sniff.
        Err(e) if e.is_syntax() && e.to_string().contains("trailing characters") => {
            return parse_jsonl(bytes, stem, source_id);
        }
        Err(_) if budget.exhausted() => {
            return Err(IngestError::Unrecognized(format!(
                "JSON com valores demais para ler inteiro (mais de {} MB na memória); use JSON Lines para arquivos grandes",
                MAX_JSON_COST >> 20
            )));
        }
        Err(e) if e.to_string().contains(crate::cancel::STOPPED) => {
            return Err(IngestError::Unrecognized(
                crate::cancel::STOPPED.to_string(),
            ));
        }
        Err(e) => {
            return Err(IngestError::Parse {
                format: "json",
                detail: format!("JSON inválido ({e})"),
            })
        }
    };
    let mut found = Vec::new();
    let mut cells = MAX_RECORD_CELLS;
    find_tables("", &doc, 0, &mut found);
    for (path, v) in &found {
        let name = table_name(stem, path);
        let Some(items) = v.as_array() else {
            continue;
        };
        if is_grid(items) {
            let rows: Vec<Vec<String>> = items
                .iter()
                .take(MAX_ROWS)
                .map(|r| {
                    r.as_array()
                        .map(|cells| {
                            cells
                                .iter()
                                .filter_map(scalar)
                                .map(|c| c.text().to_string())
                                .collect()
                        })
                        .unwrap_or_default()
                })
                .collect();
            let width = rows
                .iter()
                .map(Vec::len)
                .max()
                .unwrap_or(1)
                .clamp(1, MAX_COLUMNS);
            let rows: Vec<Vec<String>> = rows.into_iter().take(cells / width).collect();
            cells -= rows.len() * width;
            if let Some(t) = table_from_rows(&name, source_id, rows) {
                out.tables.push(t);
            }
            continue;
        }
        let mut records = Records::default();
        let mut skipped = 0usize;
        let mut sparse = cells;
        for item in items {
            if item.is_object() {
                if !records.push(record_of(item), &mut sparse) {
                    break;
                }
            } else {
                skipped += 1;
            }
        }
        record_warnings(&records, &name, cells, &mut out.warnings);
        if skipped > 0 {
            out.warnings.push(format!(
                "{name}: {skipped} item(ns) que não são objetos foram ignorados"
            ));
        }
        if let Some(t) = records.into_table(&name, source_id, &mut cells) {
            out.tables.push(t);
        }
    }
    if found.is_empty() {
        if let Json::Object(_) = doc {
            let record = record_of(&doc);
            if record.len() >= 2 {
                let mut records = Records::default();
                let mut sparse = cells;
                records.push(record, &mut sparse);
                if let Some(t) = records.into_table(stem, source_id, &mut cells) {
                    out.tables.push(t);
                }
            }
        }
    }
    if found.len() >= MAX_TABLES {
        out.warnings.push(format!(
            "só as primeiras {MAX_TABLES} listas de registros foram lidas"
        ));
    }
    if out.tables.is_empty() {
        out.warnings.push(
            "JSON sem lista de registros (uma lista de objetos ou um objeto com campos)"
                .to_string(),
        );
    }
    Ok(out)
}

/// Parses JSON Lines: one record per line (objects; other values are skipped and counted).
pub fn parse_jsonl(
    bytes: &[u8],
    stem: &str,
    source_id: &str,
) -> Result<StructuredParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let mut out = StructuredParsed::default();
    let end = cut_input(bytes, &mut out.warnings, "JSON Lines");
    let text = json_text(&bytes[..end]);
    let mut records = Records::default();
    let mut invalid: Vec<usize> = Vec::new();
    let mut too_big: Vec<usize> = Vec::new();
    let mut sparse = MAX_RECORD_CELLS;
    let mut stopped = false;
    for (i, line) in text.lines().enumerate() {
        if i % 1024 == 0 && crate::cancel::cancelled() {
            stopped = true;
            break;
        }
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        if records.rows.len() >= MAX_ROWS || records.cut {
            break;
        }
        // Each line is a document of its own, under the same bounds.
        if line.len() > MAX_JSON_DOCUMENT {
            too_big.push(i + 1);
            continue;
        }
        let budget = Budget::new(MAX_JSON_COST);
        match parse_value(line, &budget) {
            Ok(v) if v.is_object() => {
                if !records.push(record_of(&v), &mut sparse) {
                    break;
                }
            }
            Err(_) if budget.exhausted() => too_big.push(i + 1),
            _ => invalid.push(i + 1),
        }
    }
    record_warnings(&records, stem, MAX_RECORD_CELLS, &mut out.warnings);
    if !too_big.is_empty() {
        let shown: Vec<String> = too_big.iter().take(10).map(ToString::to_string).collect();
        out.warnings.push(format!(
            "{} linha(s) grandes demais (mais de {} MB ou valores demais) foram ignoradas: {}{}",
            too_big.len(),
            MAX_JSON_DOCUMENT >> 20,
            shown.join(", "),
            if too_big.len() > 10 { "…" } else { "" }
        ));
    }
    if stopped {
        out.warnings.push(crate::cancel::STOPPED.to_string());
    }
    if !invalid.is_empty() {
        let shown: Vec<String> = invalid.iter().take(10).map(ToString::to_string).collect();
        out.warnings.push(format!(
            "{} linha(s) que não são objetos JSON foram ignoradas: {}{}",
            invalid.len(),
            shown.join(", "),
            if invalid.len() > 10 { "…" } else { "" }
        ));
    }
    let mut cells = MAX_RECORD_CELLS;
    if let Some(t) = records.into_table(stem, source_id, &mut cells) {
        out.tables.push(t);
    } else if invalid.is_empty() && too_big.is_empty() {
        out.warnings
            .push("JSON Lines sem nenhum objeto".to_string());
    }
    Ok(out)
}

// ------------------------------------------------------------------------------------- XML

#[derive(Default)]
struct XNode {
    name: String,
    attrs: Vec<(String, String)>,
    text: String,
    children: Vec<usize>,
}

fn xml_decoded(bytes: &[u8]) -> String {
    if let Some((enc, n)) = Encoding::for_bom(bytes) {
        return enc.decode_without_bom_handling(&bytes[n..]).0.into_owned();
    }
    if let Ok(s) = std::str::from_utf8(bytes) {
        return s.to_string();
    }
    // The declaration names the encoding of a non-UTF-8 document.
    let head = String::from_utf8_lossy(&bytes[..bytes.len().min(256)]).to_ascii_lowercase();
    let enc = head
        .find("encoding=")
        .and_then(|at| {
            let rest = &head[at + "encoding=".len()..];
            let quote = rest.chars().next()?;
            let label: String = rest[1..].chars().take_while(|c| *c != quote).collect();
            Encoding::for_label(label.as_bytes())
        })
        .unwrap_or(encoding_rs::WINDOWS_1252);
    enc.decode_without_bom_handling(bytes).0.into_owned()
}

/// The attributes of an element, at most [`MAX_COLUMNS`]: they become columns, and quick-xml's
/// duplicate check is quadratic in their number, so it is off and the list is cut.
fn attrs_of(e: &BytesStart<'_>) -> Vec<(String, String)> {
    e.attributes()
        .with_checks(false)
        .flatten()
        .filter(|a| {
            let key = a.key.as_ref();
            key != "xmlns" && !key.starts_with("xmlns:")
        })
        .take(MAX_COLUMNS)
        .map(|a| {
            let value = a
                .normalized_value(quick_xml::XmlVersion::Implicit1_0)
                .map_or_else(|_| a.value.to_string(), std::borrow::Cow::into_owned);
            let mut kept = String::new();
            crate::html::push_capped(&mut kept, &value, MAX_XML_TEXT);
            (a.key.local_name().as_ref().to_string(), kept)
        })
        .collect()
}

/// Reads the element tree (local names, attributes, text), bounded in depth and size.
fn xml_tree(text: &str, warnings: &mut Vec<String>) -> Result<Vec<XNode>, IngestError> {
    let mut reader = Reader::from_str(text);
    let mut nodes: Vec<XNode> = vec![XNode::default()];
    let mut stack: Vec<usize> = vec![0];
    let mut attrs = 0usize;
    let mut every = crate::cancel::Every::default();
    loop {
        if every.cancelled() {
            warnings.push(crate::cancel::STOPPED.to_string());
            break;
        }
        if attrs > MAX_XML_ATTRS {
            warnings.push(format!(
                "XML com mais de {MAX_XML_ATTRS} atributos; o restante não foi lido"
            ));
            break;
        }
        let event = match reader.read_event() {
            Ok(e) => e,
            Err(e) => {
                if nodes.len() <= 1 {
                    return Err(IngestError::Parse {
                        format: "xml",
                        detail: format!("XML inválido ({e})"),
                    });
                }
                warnings.push(format!("XML interrompido ({e}); o restante não foi lido"));
                break;
            }
        };
        match event {
            Event::Start(e) | Event::Empty(e) if nodes.len() >= MAX_XML_NODES => {
                let _ = e;
                warnings.push(format!(
                    "XML com mais de {MAX_XML_NODES} elementos; o restante não foi lido"
                ));
                break;
            }
            Event::Start(e) => {
                if stack.len() > MAX_XML_DEPTH {
                    warnings.push(format!(
                        "XML aninhado demais (mais de {MAX_XML_DEPTH} níveis); o restante não foi lido"
                    ));
                    break;
                }
                let id = nodes.len();
                let a = attrs_of(&e);
                attrs += a.len();
                nodes.push(XNode {
                    name: e.local_name().as_ref().to_string(),
                    attrs: a,
                    ..XNode::default()
                });
                if let Some(parent) = stack.last() {
                    nodes[*parent].children.push(id);
                }
                stack.push(id);
            }
            Event::Empty(e) => {
                let id = nodes.len();
                let a = attrs_of(&e);
                attrs += a.len();
                nodes.push(XNode {
                    name: e.local_name().as_ref().to_string(),
                    attrs: a,
                    ..XNode::default()
                });
                if let Some(parent) = stack.last() {
                    nodes[*parent].children.push(id);
                }
            }
            Event::End(_) => {
                if stack.len() > 1 {
                    stack.pop();
                }
            }
            Event::Text(t) => {
                if let Some(top) = stack.last() {
                    let node = &mut nodes[*top];
                    if node.text.len() < MAX_XML_TEXT {
                        crate::html::push_capped(&mut node.text, &t.xml10_content(), MAX_XML_TEXT);
                    }
                }
            }
            Event::CData(t) => {
                if let Some(top) = stack.last() {
                    let node = &mut nodes[*top];
                    if node.text.len() < MAX_XML_TEXT {
                        crate::html::push_capped(&mut node.text, &t.xml10_content(), MAX_XML_TEXT);
                    }
                }
            }
            Event::GeneralRef(r) => {
                if let Some(top) = stack.last() {
                    let s = if let Ok(Some(c)) = r.resolve_char_ref() {
                        c.to_string()
                    } else {
                        let name = r.as_ref().to_string();
                        quick_xml::escape::resolve_predefined_entity(&name)
                            .map_or_else(|| format!("&{name};"), ToString::to_string)
                    };
                    crate::html::push_capped(&mut nodes[*top].text, &s, MAX_XML_TEXT);
                }
            }
            Event::Eof => break,
            _ => {}
        }
    }
    Ok(nodes)
}

fn is_record_element(nodes: &[XNode], id: usize) -> bool {
    !nodes[id].children.is_empty() || !nodes[id].attrs.is_empty()
}

fn flatten_xml(
    nodes: &[XNode],
    id: usize,
    prefix: &str,
    depth: usize,
    out: &mut IndexMap<String, Cell>,
) {
    let node = &nodes[id];
    let key = |name: &str| {
        if prefix.is_empty() {
            name.to_string()
        } else {
            format!("{prefix}.{name}")
        }
    };
    // Repeated leaf children (`<telefone>` twice) join into one cell.
    let mut seen: IndexMap<&str, Vec<usize>> = IndexMap::new();
    for &c in &node.children {
        seen.entry(nodes[c].name.as_str()).or_default().push(c);
    }
    for (name, ids) in seen {
        let path = key(name);
        let leaves = ids
            .iter()
            .all(|&c| nodes[c].children.is_empty() && nodes[c].attrs.is_empty());
        if leaves {
            let parts: Vec<String> = ids
                .iter()
                .map(|&c| clean_cell(&nodes[c].text))
                .filter(|s| !s.is_empty())
                .collect();
            out.insert(
                path,
                if parts.is_empty() {
                    Cell::Empty
                } else {
                    Cell::Text(clean_cell(&parts.join("; ")))
                },
            );
        } else if ids.len() == 1 && depth + 1 < MAX_FLATTEN_DEPTH {
            flatten_xml(nodes, ids[0], &path, depth + 1, out);
        } else {
            let text: Vec<String> = ids.iter().map(|&c| subtree_text(nodes, c)).collect();
            out.insert(path, Cell::Text(truncated(clean_cell(&text.join("; ")))));
        }
    }
    if node.children.is_empty() {
        let t = clean_cell(&node.text);
        if !t.is_empty() {
            let k = if prefix.is_empty() {
                "texto".to_string()
            } else {
                prefix.to_string()
            };
            out.insert(k, Cell::Text(t));
        }
    }
    // Attributes after the child elements: an `id` attribute must not come before the
    // document number the engine looks for first.
    for (k, v) in &node.attrs {
        if !v.trim().is_empty() {
            out.insert(key(&format!("@{k}")), Cell::Text(clean_cell(v)));
        }
    }
}

fn subtree_text(nodes: &[XNode], id: usize) -> String {
    let mut parts = Vec::new();
    let mut stack = vec![id];
    while let Some(n) = stack.pop() {
        if parts.len() > 64 {
            break;
        }
        let t = nodes[n].text.trim();
        if !t.is_empty() {
            parts.push(t.to_string());
        }
        stack.extend(nodes[n].children.iter().rev());
    }
    parts.join(" ")
}

/// Groups of repeated record elements: (path of the group, element ids).
fn xml_groups(nodes: &[XNode]) -> Vec<(String, Vec<usize>)> {
    let mut out = Vec::new();
    // (node, path, depth); iterative so a deep document cannot exhaust the stack.
    let mut stack: Vec<(usize, String, usize)> = vec![(0, String::new(), 0)];
    while let Some((id, path, depth)) = stack.pop() {
        if out.len() >= MAX_TABLES || depth > MAX_SEARCH_DEPTH {
            continue;
        }
        let node = &nodes[id];
        let mut by_name: IndexMap<&str, Vec<usize>> = IndexMap::new();
        for &c in &node.children {
            by_name.entry(nodes[c].name.as_str()).or_default().push(c);
        }
        let homogeneous = by_name.len() == 1;
        let mut descend = Vec::new();
        for (name, ids) in by_name {
            let child_path = if path.is_empty() {
                name.to_string()
            } else {
                format!("{path}.{name}")
            };
            let records = ids.iter().all(|&c| is_record_element(nodes, c));
            // Repeated records, or a container holding only records of one kind (one or more)
            // below the document element.
            if records && (ids.len() >= 2 || (homogeneous && id != 0)) {
                out.push((child_path, ids));
            } else {
                for c in ids {
                    descend.push((c, child_path.clone(), depth + 1));
                }
            }
        }
        stack.extend(descend.into_iter().rev());
    }
    out
}

/// Parses an XML document (not NF-e/CT-e) into tables of repeated records.
pub fn parse_xml(
    bytes: &[u8],
    stem: &str,
    source_id: &str,
) -> Result<StructuredParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let mut out = StructuredParsed::default();
    let end = cut_input(bytes, &mut out.warnings, "XML");
    let text = xml_decoded(&bytes[..end]);
    let nodes = xml_tree(&text, &mut out.warnings)?;
    let groups = xml_groups(&nodes);
    let mut cells = MAX_RECORD_CELLS;
    for (path, ids) in &groups {
        let name = table_name(stem, path);
        let mut records = Records::default();
        let mut sparse = cells;
        for &id in ids {
            let mut r = IndexMap::new();
            flatten_xml(&nodes, id, "", 0, &mut r);
            if !records.push(r, &mut sparse) {
                break;
            }
        }
        record_warnings(&records, &name, cells, &mut out.warnings);
        if let Some(t) = records.into_table(&name, source_id, &mut cells) {
            out.tables.push(t);
        }
    }
    if groups.is_empty() {
        // One record: the document element and its fields.
        if let Some(&root) = nodes[0].children.first() {
            let mut r = IndexMap::new();
            flatten_xml(&nodes, root, "", 0, &mut r);
            if r.len() >= 2 {
                let mut records = Records::default();
                let mut sparse = cells;
                records.push(r, &mut sparse);
                if let Some(t) = records.into_table(stem, source_id, &mut cells) {
                    out.tables.push(t);
                }
            }
        }
    }
    if groups.len() >= MAX_TABLES {
        out.warnings.push(format!(
            "só os primeiros {MAX_TABLES} grupos de registros foram lidos"
        ));
    }
    if out.tables.is_empty() {
        out.warnings.push(
            "XML sem registros repetidos (elementos irmãos com campos) nem campos no elemento raiz"
                .to_string(),
        );
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use dm_core::{ColumnRole, Money, Value};

    #[test]
    fn json_record_arrays_with_nested_keys() {
        let doc = r#"{"empresa": {"nome": "Exemplo Sintético", "cnpj": "11.222.333/0001-81"},
            "titulos": [
              {"cliente": {"nome": "MOINHO AZUL COM LTDA", "cnpj": "90123456000134"}, "documento": "FAT-2026-0411", "vencimento": "2026-07-20", "valor": 27850.00, "pago": false, "tags": ["a", "b"], "parcelas": [{"n": 1}]},
              {"cliente": {"nome": "NORTE PAES EIRELI", "cnpj": "35791357000190"}, "documento": "FAT-2026-0412", "vencimento": "2026-07-14", "valor": 10450, "pago": true, "extra": null}
            ]}"#;
        let p = parse_json(doc.as_bytes(), "omie", "src").unwrap_or_else(|e| panic!("{e}"));
        assert!(p.warnings.is_empty(), "{:?}", p.warnings);
        assert_eq!(p.tables.len(), 1);
        let t = &p.tables[0];
        assert_eq!(t.name, "omie:titulos");
        let names: Vec<&str> = t.columns.iter().map(|c| c.name.as_str()).collect();
        assert_eq!(
            names,
            vec![
                "cliente_nome",
                "cliente_cnpj",
                "documento",
                "vencimento",
                "valor",
                "pago",
                "tags",
                "parcelas",
                "extra"
            ]
        );
        assert_eq!(t.columns[0].role, ColumnRole::Name);
        assert_eq!(t.columns[1].role, ColumnRole::TaxId);
        assert_eq!(t.columns[3].role, ColumnRole::Date);
        assert_eq!(t.columns[4].role, ColumnRole::Amount);
        assert_eq!(t.columns[5].role, ColumnRole::Status);
        assert_eq!(
            t.value(1, 4).and_then(Value::as_money),
            Some(Money::from_centavos(1_045_000))
        );
        assert_eq!(t.value(0, 6), Some(&Value::Text("a; b".into())));
        assert_eq!(t.value(0, 7), Some(&Value::Text("[{\"n\":1}]".into())));
        assert!(t.value(0, 3).and_then(Value::as_date).is_some());
    }

    #[test]
    fn json_shapes() {
        let grid =
            br#"[["data", "historico", "valor"], ["10/07/2026", "PIX RECEBIDO", "2.700,00"]]"#;
        let p = parse_json(grid, "g", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(p.tables[0].name, "g");
        assert_eq!(p.tables[0].len(), 1);
        let one = br#"{"numero": "NF 1", "valor": "10,00", "vencimento": "10/07/2026"}"#;
        assert_eq!(
            parse_json(one, "o", "s").map(|p| p.tables.len()).ok(),
            Some(1)
        );
        let none = parse_json(b"[1, 2, 3]", "n", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(none.tables.is_empty());
        assert_eq!(none.warnings.len(), 1);
        assert!(parse_json(b"{\"a\": ", "x", "s").is_err());
        let deep = format!("{}1{}", "[".repeat(200), "]".repeat(200));
        assert!(parse_json(deep.as_bytes(), "d", "s").is_err());
        let jsonl_like = b"{\"a\": 1, \"b\": \"x\"}\n{\"a\": 2, \"b\": \"y\"}\n";
        let p = parse_json(jsonl_like, "l", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(p.tables[0].len(), 2);
    }

    #[test]
    fn jsonl_counts_bad_lines() {
        let lines = b"{\"data\": \"10/07/2026\", \"historico\": \"PIX\", \"valor\": \"2.700,00\", \"tipo\": \"C\"}\n\nnot json\n[1]\n{\"data\": \"13/07/2026\", \"historico\": \"TED\", \"valor\": \"8.450,00\", \"tipo\": \"C\"}\n";
        let p = parse_jsonl(lines, "l", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(p.tables[0].len(), 2);
        assert_eq!(
            p.warnings,
            vec!["2 linha(s) que não são objetos JSON foram ignoradas: 3, 4".to_string()]
        );
        assert_eq!(p.tables[0].columns[2].role, ColumnRole::Amount);
    }

    #[test]
    fn xml_repeated_records_attributes_and_encoding() {
        let xml = "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><extrato xmlns=\"urn:x\"><conta agencia=\"0001\" numero=\"12345-6\"/><lancamentos><lancamento id=\"1\"><data>10/07/2026</data><historico>LIQUIDAÇÃO COBRANÇA</historico><documento>000098765001</documento><valor>24.300,00</valor><tipo>C</tipo></lancamento><lancamento id=\"2\"><data>15/07/2026</data><historico>TARIFA &amp; PACOTE</historico><valor>980,00</valor><tipo>D</tipo></lancamento></lancamentos></extrato>";
        // Latin-1 bytes, as the declaration says.
        let bytes: Vec<u8> = xml.chars().map(|c| c as u32 as u8).collect();
        let p = parse_xml(&bytes, "x", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(p.warnings.is_empty(), "{:?}", p.warnings);
        assert_eq!(p.tables.len(), 1);
        let t = &p.tables[0];
        assert_eq!(t.name, "x:extrato.lancamentos.lancamento");
        assert_eq!(t.len(), 2);
        let names: Vec<&str> = t.columns.iter().map(|c| c.name.as_str()).collect();
        assert_eq!(
            names,
            vec!["data", "historico", "documento", "valor", "tipo", "id"]
        );
        assert_eq!(
            t.value(0, 1),
            Some(&Value::Text("LIQUIDAÇÃO COBRANÇA".into()))
        );
        assert_eq!(t.value(1, 1), Some(&Value::Text("TARIFA & PACOTE".into())));
        assert_eq!(t.columns[3].role, ColumnRole::Amount);
    }

    #[test]
    fn xml_without_records_and_broken_xml() {
        let p = parse_xml(b"<config><a>1</a></config>", "c", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(p.tables.is_empty());
        assert_eq!(p.warnings.len(), 1);
        let one = parse_xml(
            b"<boleto><numero>1</numero><valor>10,00</valor></boleto>",
            "b",
            "s",
        )
        .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(one.tables.len(), 1);
        assert!(parse_xml(b"<a><b></a>", "c", "s").is_ok());
        let deep = format!("{}x{}", "<a>".repeat(1000), "</a>".repeat(1000));
        let p = parse_xml(deep.as_bytes(), "d", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(
            p.warnings.iter().any(|w| w.contains("aninhado demais")),
            "{:?}",
            p.warnings
        );
    }

    #[test]
    fn json_documents_are_bounded_in_size_and_memory() {
        let big = format!("[{}\"x\"]", "\"x\",".repeat(MAX_JSON_DOCUMENT / 4));
        assert!(matches!(
            parse_json(big.as_bytes(), "b", "s"),
            Err(IngestError::Unrecognized(m)) if m.contains("acima de 8 MB")
        ));
        // Small on disk, large in memory: an empty object is a boxed map.
        let objects = format!("[{}{{}}]", "{},".repeat(MAX_JSON_DOCUMENT / 3 - 10));
        assert!(objects.len() <= MAX_JSON_DOCUMENT);
        assert!(matches!(
            parse_json(objects.as_bytes(), "o", "s"),
            Err(IngestError::Unrecognized(m)) if m.contains("valores demais")
        ));
    }

    #[test]
    fn json_lines_bound_each_line_and_the_records() {
        let mut lines = String::from(
            "{\"data\": \"10/07/2026\", \"valor\": \"1,00\", \"historico\": \"PIX\"}\n",
        );
        lines.push_str(&format!(
            "{{\"x\": \"{}\"}}\n",
            "a".repeat(MAX_JSON_DOCUMENT)
        ));
        lines.push_str("{\"data\": \"11/07/2026\", \"valor\": \"2,00\", \"historico\": \"TED\"}\n");
        let p = parse_jsonl(lines.as_bytes(), "l", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(p.tables[0].len(), 2);
        assert!(
            p.warnings
                .iter()
                .any(|w| w.contains("grandes demais") && w.ends_with(": 2")),
            "{:?}",
            p.warnings
        );
        // Every line brings new keys: the grid of rows times keys is what is bounded.
        let mut wide = String::new();
        for i in 0..40_000 {
            wide.push_str(&format!("{{\"k{}\": 1, \"n\": {i}}}\n", i % 256));
        }
        let p = parse_jsonl(wide.as_bytes(), "w", "s").unwrap_or_else(|e| panic!("{e}"));
        let t = &p.tables[0];
        assert_eq!(t.columns.len(), 256);
        assert!(t.len() * t.columns.len() <= MAX_RECORD_CELLS, "{}", t.len());
        assert!(
            p.warnings.iter().any(|w| w.contains("células por arquivo")),
            "{:?}",
            p.warnings
        );
    }

    #[test]
    fn xml_attributes_and_entity_text_are_capped() {
        let mut attrs = String::new();
        for i in 0..1000 {
            attrs.push_str(&format!(" a{i}=\"{i}\""));
        }
        let xml = format!("<r><item{attrs}/><item{attrs}/></r>");
        let p = parse_xml(xml.as_bytes(), "x", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(p.tables[0].columns.len(), MAX_COLUMNS);
        let refs = format!(
            "<r><item><n>1</n><t>{}</t></item><item><n>2</n><t>{}</t></item></r>",
            "&amp;".repeat(100_000),
            "x".repeat(100_000)
        );
        let mut warnings = Vec::new();
        let nodes = xml_tree(&refs, &mut warnings).unwrap_or_else(|e| panic!("{e}"));
        assert!(nodes.iter().all(|n| n.text.len() <= MAX_XML_TEXT));
        assert!(nodes.iter().all(|n| n.attrs.len() <= MAX_COLUMNS));
        let long_value = format!("<r><i v=\"{}\"/><i v=\"1\"/></r>", "y".repeat(100_000));
        let nodes = xml_tree(&long_value, &mut warnings).unwrap_or_else(|e| panic!("{e}"));
        assert!(nodes
            .iter()
            .all(|n| n.attrs.iter().all(|(_, v)| v.len() <= MAX_XML_TEXT)));
    }
}
