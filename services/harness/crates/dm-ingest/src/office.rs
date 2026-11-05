//! Text documents: Word (`.docx`, `word/document.xml`) and OpenDocument text (`.odt`,
//! `content.xml`), read from the ZIP package with quick-xml, streaming.
//!
//! Paragraphs become the document's text (one line per paragraph, an empty paragraph is a
//! blank line), which the dispatcher reads like a text dump: an e-mail thread or a chat pasted
//! into a document becomes messages, anything else paragraphs. Tables go through the same
//! header detection and role inference as spreadsheets (see [`crate::grid`]); a table without
//! a header (a layout grid, a signature block) stays in the text. Tracked deletions, comments
//! and the fallback copies of text boxes are not read. Memory is bounded: the XML member is
//! read under the shared ZIP limits and at most [`MAX_XML`] bytes, the text at most
//! [`MAX_TEXT`] bytes and the tables at most [`MAX_CELLS`] cells.

use dm_core::Table;
use quick_xml::events::{BytesStart, Event};
use quick_xml::Reader;

use crate::archive::{read_zip_member, ZipLimits};
use crate::cancel::{Every, STOPPED};
use crate::grid::{clean_cell, table_from_rows};
use crate::source::TextPart;
use crate::IngestError;

/// Decompressed XML member, bytes.
pub(crate) const MAX_XML: u64 = 64 << 20;
/// Text kept, bytes.
pub(crate) const MAX_TEXT: usize = 8 << 20;
/// Table cells kept per document, repetitions included.
const MAX_CELLS: usize = 200_000;
/// Bytes of table text kept per document, every copy of a repeated cell or row included: an
/// ODT can repeat one cell 256 times in a row repeated 256 times.
const MAX_CELL_BYTES: usize = 8 << 20;
/// Element nesting followed.
const MAX_DEPTH: usize = 512;
/// Repetitions honoured for one ODT cell or row (`number-columns-repeated`).
const MAX_REPEAT: usize = 256;

/// What a text document yielded.
#[derive(Debug, Default)]
pub struct DocParsed {
    /// Tables with a recognizable header.
    pub tables: Vec<Table>,
    /// Paragraphs and layout tables, one line each; a data table leaves a blank line (its
    /// text is in [`DocParsed::parts`]).
    pub text: String,
    /// The text in parts, data tables apart (empty when the document has none): see
    /// [`crate::source::document_text`].
    pub parts: Vec<TextPart>,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
}

/// A cell's paragraphs.
type CellText = Vec<String>;

/// How a cell takes part in a vertical merge.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum VMerge {
    /// An ordinary cell: ends any merge running down its column.
    None,
    /// The top of a merge spanning this many more rows (`usize::MAX`: until the column has
    /// an ordinary cell again, as in Word).
    Restart(usize),
    /// A covered cell: it shows the text of the top of the merge.
    Continue,
}

#[derive(Default)]
struct OpenTable {
    rows: Vec<Vec<CellText>>,
    row: Option<Vec<CellText>>,
    cell: Option<CellText>,
    /// Running vertical merges, by column: rows left and the text carried down.
    vspans: Vec<Option<(usize, CellText)>>,
}

enum Block {
    Para(String),
    Table(usize),
}

fn cell_bytes(cell: &CellText) -> usize {
    cell.iter().map(String::len).sum()
}

/// Builds blocks, paragraphs and tables from the structural events of either format.
#[derive(Default)]
struct Builder {
    blocks: Vec<Block>,
    tables: Vec<Vec<Vec<CellText>>>,
    open: Vec<OpenTable>,
    paras: Vec<String>,
    text_bytes: usize,
    cells: usize,
    cell_bytes: usize,
    text_cut: bool,
    cells_cut: bool,
}

impl Builder {
    fn start_para(&mut self) {
        self.paras.push(String::new());
    }

    fn text(&mut self, s: &str) {
        if self.text_bytes >= MAX_TEXT {
            self.text_cut = true;
            return;
        }
        if let Some(p) = self.paras.last_mut() {
            p.push_str(s);
            self.text_bytes += s.len();
        }
    }

    fn end_para(&mut self) {
        let Some(p) = self.paras.pop() else {
            return;
        };
        let p = p.trim_end().to_string();
        if let Some(parent) = self.paras.last_mut() {
            // A text box inside a paragraph: its text continues the line.
            if !p.is_empty() {
                if !parent.is_empty() {
                    parent.push(' ');
                }
                parent.push_str(&p);
            }
            return;
        }
        match self.open.last_mut() {
            Some(t) => {
                if let Some(cell) = t.cell.as_mut() {
                    cell.push(p);
                }
            }
            None => self.blocks.push(Block::Para(p)),
        }
    }

    fn start_table(&mut self) {
        self.open.push(OpenTable::default());
    }

    fn start_row(&mut self) {
        if let Some(t) = self.open.last_mut() {
            t.row = Some(Vec::new());
        }
    }

    fn start_cell(&mut self) {
        if let Some(t) = self.open.last_mut() {
            if t.row.is_none() {
                t.row = Some(Vec::new());
            }
            t.cell = Some(Vec::new());
        }
    }

    /// Takes `cells` more cells and `bytes` more bytes of table text from the document's
    /// budget; false (and the cut is reported) when they do not fit.
    fn spend(&mut self, cells: usize, bytes: usize) -> bool {
        if self.cells.saturating_add(cells) > MAX_CELLS
            || self.cell_bytes.saturating_add(bytes) > MAX_CELL_BYTES
        {
            self.cells_cut = true;
            return false;
        }
        self.cells += cells;
        self.cell_bytes += bytes;
        true
    }

    /// Closes a cell: `repeat` copies of it, then `extra` empty cells (a horizontal merge).
    fn end_cell(&mut self, repeat: usize, extra: usize, vmerge: VMerge) {
        let Some(t) = self.open.last_mut() else {
            return;
        };
        let Some(mut cell) = t.cell.take() else {
            return;
        };
        let col = t.row.as_ref().map_or(0, Vec::len);
        match vmerge {
            VMerge::Continue => {
                if let Some(slot) = t.vspans.get_mut(col) {
                    if let Some((left, carried)) = slot.as_mut() {
                        if cell.iter().all(|p| p.trim().is_empty()) {
                            cell.clone_from(carried);
                        }
                        *left = left.saturating_sub(1);
                        if *left == 0 {
                            *slot = None;
                        }
                    }
                }
            }
            VMerge::Restart(rows) => {
                if t.vspans.len() <= col {
                    t.vspans.resize(col + 1, None);
                }
                t.vspans[col] = Some((rows, cell.clone()));
            }
            VMerge::None => {
                if let Some(slot) = t.vspans.get_mut(col) {
                    *slot = None;
                }
            }
        }
        // Empty cells repeated thousands of times are padding: they cost a cell each, never
        // bytes, and a few hundred are enough to keep the columns aligned.
        let copies = repeat.clamp(1, MAX_REPEAT);
        let bytes = cell_bytes(&cell).saturating_mul(copies);
        let extra = extra.min(MAX_REPEAT);
        if !self.spend(copies + extra, bytes) {
            return;
        }
        let Some(t) = self.open.last_mut() else {
            return;
        };
        let row = t.row.get_or_insert_with(Vec::new);
        for _ in 1..copies {
            row.push(cell.clone());
        }
        row.push(cell);
        for _ in 0..extra {
            row.push(Vec::new());
        }
    }

    fn end_row(&mut self, repeat: usize) {
        let Some(t) = self.open.last_mut() else {
            return;
        };
        if t.cell.is_some() {
            self.end_cell(1, 0, VMerge::None);
        }
        let Some(t) = self.open.last_mut() else {
            return;
        };
        let Some(row) = t.row.take() else {
            return;
        };
        let empty = row.iter().all(|c| c.iter().all(|p| p.trim().is_empty()));
        // Trailing empty rows repeated thousands of times are formatting, not data; a
        // repeated row with text costs every copy against the budget.
        let mut times = if empty {
            1
        } else {
            repeat.clamp(1, MAX_REPEAT)
        };
        let row_cells = row.len();
        let row_bytes: usize = row.iter().map(cell_bytes).sum();
        while times > 1
            && !self.spend(
                row_cells.saturating_mul(times - 1),
                row_bytes.saturating_mul(times - 1),
            )
        {
            times /= 2;
        }
        let Some(t) = self.open.last_mut() else {
            return;
        };
        for _ in 1..times {
            t.rows.push(row.clone());
        }
        t.rows.push(row);
    }

    fn end_table(&mut self) {
        if self.open.last().is_some_and(|t| t.row.is_some()) {
            self.end_row(1);
        }
        let Some(done) = self.open.pop() else {
            return;
        };
        if let Some(parent) = self.open.last_mut() {
            // A table inside a cell: its text joins the enclosing cell (moved, not copied).
            if let Some(cell) = parent.cell.as_mut() {
                for row in done.rows {
                    for c in row {
                        cell.extend(c.into_iter().filter(|p| !p.trim().is_empty()));
                    }
                }
            }
        } else {
            self.tables.push(done.rows);
            self.blocks.push(Block::Table(self.tables.len() - 1));
        }
    }

    /// Closes what is still open (a truncated file) and renders tables and text.
    fn finish(mut self, stem: &str, source_id: &str, format: &str) -> DocParsed {
        while !self.paras.is_empty() {
            self.end_para();
        }
        while !self.open.is_empty() {
            self.end_table();
        }
        let mut out = DocParsed::default();
        let mut data_names: std::collections::BTreeMap<usize, String> =
            std::collections::BTreeMap::new();
        for (i, rows) in self.tables.iter().enumerate() {
            let grid: Vec<Vec<String>> = rows
                .iter()
                .map(|r| r.iter().map(|c| clean_cell(&c.join(" "))).collect())
                .collect();
            let name = format!("{stem}:tabela {}", i + 1);
            if let Some(t) = table_from_rows(&name, source_id, grid) {
                out.tables.push(t);
                data_names.insert(i, name);
            }
        }
        // Data tables are parts of their own: whether one is a ledger, a statement or neither
        // is decided downstream, and the text of a table that is neither (installments, new
        // bank details) belongs to what the document says. Layout tables are prose. A row of
        // several cells is one line, tab-separated; every part shares the text budget.
        let mut parts: Vec<TextPart> = Vec::new();
        let mut prose = String::new();
        let mut used = 0usize;
        let mut push = |text: &mut String, line: &str| -> bool {
            if used + line.len() + 1 > MAX_TEXT {
                return false;
            }
            used += line.len() + 1;
            text.push_str(line);
            text.push('\n');
            true
        };
        'blocks: for block in &self.blocks {
            match block {
                Block::Para(p) => {
                    if !push(&mut prose, p) {
                        self.text_cut = true;
                        break;
                    }
                }
                Block::Table(i) => {
                    let mut table = String::new();
                    let mut cut = false;
                    'rows: for row in &self.tables[*i] {
                        for line in row_lines(row) {
                            if !push(&mut table, &line) {
                                cut = true;
                                break 'rows;
                            }
                        }
                    }
                    cut = cut || !push(&mut table, "");
                    match data_names.get(i) {
                        Some(name) => {
                            parts.push(TextPart {
                                table: None,
                                text: std::mem::take(&mut prose),
                            });
                            parts.push(TextPart {
                                table: Some(name.clone()),
                                text: table,
                            });
                        }
                        None => prose.push_str(&table),
                    }
                    if cut {
                        self.text_cut = true;
                        break 'blocks;
                    }
                }
            }
        }
        parts.push(TextPart {
            table: None,
            text: prose,
        });
        parts.retain(|p| p.table.is_some() || !p.text.is_empty());
        out.text = crate::source::document_text(&parts, |_| false);
        if parts.iter().any(|p| p.table.is_some()) {
            out.parts = parts;
        }
        if self.text_cut {
            out.warnings.push(format!(
                "texto do {format} cortado em {} MB",
                MAX_TEXT >> 20
            ));
        }
        if self.cells_cut {
            out.warnings.push(format!(
                "tabelas do {format} cortadas no limite de {MAX_CELLS} células ou {} MB de texto",
                MAX_CELL_BYTES >> 20
            ));
        }
        out
    }
}

/// The lines of a table row: a one-cell row (a layout box) keeps its paragraphs, a row of
/// several cells is one tab-separated line.
fn row_lines(row: &[CellText]) -> Vec<String> {
    if let [cell] = row {
        return cell
            .iter()
            .filter(|p| !p.trim().is_empty())
            .map(|p| p.trim().to_string())
            .collect();
    }
    let cells: Vec<String> = row.iter().map(|c| clean_cell(&c.join(" "))).collect();
    let line = cells.join("\t").trim_end().to_string();
    if line.trim().is_empty() {
        Vec::new()
    } else {
        vec![line]
    }
}

fn attr(e: &BytesStart<'_>, local: &str) -> Option<String> {
    // Without the duplicate check, which is quadratic in the number of attributes.
    e.attributes()
        .with_checks(false)
        .flatten()
        .find(|a| a.key.local_name().as_ref() == local)
        .map(|a| {
            a.normalized_value(quick_xml::XmlVersion::Implicit1_0)
                .map_or_else(|_| a.value.to_string(), std::borrow::Cow::into_owned)
        })
}

/// `w:vMerge`: `restart` opens a merge, no value (or `continue`) continues it.
fn docx_vmerge(e: &BytesStart<'_>) -> VMerge {
    match attr(e, "val").as_deref() {
        Some("restart") => VMerge::Restart(usize::MAX),
        _ => VMerge::Continue,
    }
}

/// An ODT cell spanning rows opens a merge; a covered cell continues one (or is the right
/// part of a horizontal span, which has no merge running and stays empty).
fn odt_vmerge(local: &str, e: &BytesStart<'_>) -> VMerge {
    if local == "covered-table-cell" {
        return VMerge::Continue;
    }
    match count_attr(e, "number-rows-spanned") {
        n if n > 1 => VMerge::Restart((n - 1).min(MAX_REPEAT)),
        _ => VMerge::None,
    }
}

fn count_attr(e: &BytesStart<'_>, local: &str) -> usize {
    attr(e, local)
        .and_then(|v| v.trim().parse::<usize>().ok())
        .unwrap_or(1)
        .max(1)
}

fn reference_text(r: &quick_xml::events::BytesRef<'_>) -> String {
    if let Ok(Some(c)) = r.resolve_char_ref() {
        return c.to_string();
    }
    let name = r.as_ref().to_string();
    quick_xml::escape::resolve_predefined_entity(&name)
        .map_or_else(|| format!("&{name};"), ToString::to_string)
}

fn xml_error(format: &'static str, e: impl std::fmt::Display) -> IngestError {
    IngestError::Parse {
        format,
        detail: e.to_string(),
    }
}

/// Reads a member of the package, refusing a missing one.
fn member(bytes: &[u8], path: &str, format: &'static str) -> Result<String, IngestError> {
    let xml = read_zip_member(bytes, path, MAX_XML, &ZipLimits::default())?
        .ok_or_else(|| IngestError::Unrecognized(format!("pacote {format} sem {path}")))?;
    Ok(String::from_utf8_lossy(&xml).into_owned())
}

/// Elements whose whole subtree is skipped: deleted text, comments, fallback copies.
fn skipped_docx(local: &str) -> bool {
    matches!(
        local,
        "del" | "delText" | "Fallback" | "instrText" | "commentReference" | "footnoteReference"
    )
}

/// Parses a Word document (`.docx`).
pub fn parse_docx(bytes: &[u8], stem: &str, source_id: &str) -> Result<DocParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let xml = member(bytes, "word/document.xml", "docx")?;
    let mut reader = Reader::from_str(&xml);
    let mut b = Builder::default();
    let mut depth = 0usize;
    let mut skip_from: Option<usize> = None;
    let mut in_t = false;
    // Horizontal and vertical merges of the open cells (`w:gridSpan` and `w:vMerge` inside
    // `w:tcPr`).
    let mut spans: Vec<usize> = Vec::new();
    let mut vmerges: Vec<VMerge> = Vec::new();
    let mut warnings = Vec::new();
    let mut every = Every::default();
    loop {
        if every.cancelled() {
            warnings.push(STOPPED.to_string());
            break;
        }
        let event = match reader.read_event() {
            Ok(e) => e,
            Err(e) => {
                warnings.push(format!(
                    "XML do documento interrompido ({}); o restante não foi lido",
                    xml_error("docx", e)
                ));
                break;
            }
        };
        match event {
            Event::Start(e) => {
                depth += 1;
                if depth > MAX_DEPTH {
                    warnings.push(format!(
                        "documento aninhado demais (mais de {MAX_DEPTH} níveis); o restante não foi lido"
                    ));
                    break;
                }
                if skip_from.is_some() {
                    continue;
                }
                let local = e.local_name().as_ref().to_string();
                if skipped_docx(&local) {
                    skip_from = Some(depth);
                    continue;
                }
                match local.as_str() {
                    "p" => b.start_para(),
                    "t" => in_t = true,
                    "tbl" => b.start_table(),
                    "tr" => b.start_row(),
                    "tc" => {
                        b.start_cell();
                        spans.push(1);
                        vmerges.push(VMerge::None);
                    }
                    "vMerge" => {
                        if let Some(v) = vmerges.last_mut() {
                            *v = docx_vmerge(&e);
                        }
                    }
                    _ => {}
                }
            }
            Event::Empty(e) => {
                if skip_from.is_some() {
                    continue;
                }
                match e.local_name().as_ref() {
                    "tab" | "ptab" => b.text("\t"),
                    "br" | "cr" => b.text("\n"),
                    "noBreakHyphen" => b.text("-"),
                    "gridSpan" => {
                        if let Some(s) = spans.last_mut() {
                            *s = count_attr(&e, "val").min(MAX_REPEAT);
                        }
                    }
                    "vMerge" => {
                        if let Some(v) = vmerges.last_mut() {
                            *v = docx_vmerge(&e);
                        }
                    }
                    "p" => {
                        b.start_para();
                        b.end_para();
                    }
                    _ => {}
                }
            }
            Event::End(e) => {
                if let Some(from) = skip_from {
                    if depth == from {
                        skip_from = None;
                    }
                    depth = depth.saturating_sub(1);
                    continue;
                }
                depth = depth.saturating_sub(1);
                match e.local_name().as_ref() {
                    "p" => b.end_para(),
                    "t" => in_t = false,
                    "tc" => {
                        let span = spans.pop().unwrap_or(1);
                        let vmerge = vmerges.pop().unwrap_or(VMerge::None);
                        b.end_cell(1, span.saturating_sub(1), vmerge);
                    }
                    "tr" => b.end_row(1),
                    "tbl" => b.end_table(),
                    _ => {}
                }
            }
            Event::Text(t) if in_t && skip_from.is_none() => b.text(&t.xml10_content()),
            Event::CData(t) if in_t && skip_from.is_none() => {
                b.text(&t.xml10_content());
            }
            Event::GeneralRef(r) if in_t && skip_from.is_none() => b.text(&reference_text(&r)),
            Event::Eof => break,
            _ => {}
        }
    }
    let mut out = b.finish(stem, source_id, "documento");
    warnings.append(&mut out.warnings);
    out.warnings = warnings;
    Ok(out)
}

/// Parses an OpenDocument text (`.odt`).
pub fn parse_odt(bytes: &[u8], stem: &str, source_id: &str) -> Result<DocParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let xml = member(bytes, "content.xml", "odt")?;
    let mut reader = Reader::from_str(&xml);
    let mut b = Builder::default();
    let mut depth = 0usize;
    let mut skip_from: Option<usize> = None;
    // Repetition and vertical merge of the open cells, repetition of the open rows.
    let mut cell_repeat: Vec<usize> = Vec::new();
    let mut cell_merge: Vec<VMerge> = Vec::new();
    let mut row_repeat: Vec<usize> = Vec::new();
    let mut in_body = false;
    // Local names of the open elements: the body is the `text` child of `body`.
    let mut names: Vec<String> = Vec::new();
    let mut warnings = Vec::new();
    let mut every = Every::default();
    loop {
        if every.cancelled() {
            warnings.push(STOPPED.to_string());
            break;
        }
        let event = match reader.read_event() {
            Ok(e) => e,
            Err(e) => {
                warnings.push(format!(
                    "XML do documento interrompido ({}); o restante não foi lido",
                    xml_error("odt", e)
                ));
                break;
            }
        };
        match event {
            Event::Start(e) => {
                depth += 1;
                if depth > MAX_DEPTH {
                    warnings.push(format!(
                        "documento aninhado demais (mais de {MAX_DEPTH} níveis); o restante não foi lido"
                    ));
                    break;
                }
                let local = e.local_name().as_ref().to_string();
                let parent_is_body = names.last().is_some_and(|n| n == "body");
                names.push(local.clone());
                if skip_from.is_some() {
                    continue;
                }
                match local.as_str() {
                    "text" if parent_is_body => in_body = true,
                    "annotation" | "tracked-changes" | "note-citation" => {
                        skip_from = Some(depth);
                    }
                    "p" | "h" if in_body => b.start_para(),
                    "table" if in_body => b.start_table(),
                    "table-row" if in_body => {
                        b.start_row();
                        row_repeat.push(count_attr(&e, "number-rows-repeated"));
                    }
                    "table-cell" | "covered-table-cell" if in_body => {
                        b.start_cell();
                        cell_repeat.push(count_attr(&e, "number-columns-repeated"));
                        cell_merge.push(odt_vmerge(&local, &e));
                    }
                    _ => {}
                }
            }
            Event::Empty(e) => {
                if skip_from.is_some() || !in_body {
                    continue;
                }
                match e.local_name().as_ref() {
                    "s" => {
                        let n = count_attr(&e, "c").min(MAX_REPEAT);
                        b.text(&" ".repeat(n));
                    }
                    "tab" => b.text("\t"),
                    "line-break" => b.text("\n"),
                    "p" | "h" => {
                        b.start_para();
                        b.end_para();
                    }
                    local @ ("table-cell" | "covered-table-cell") => {
                        let vmerge = odt_vmerge(local, &e);
                        b.start_cell();
                        b.end_cell(count_attr(&e, "number-columns-repeated"), 0, vmerge);
                    }
                    _ => {}
                }
            }
            Event::End(e) => {
                names.pop();
                if let Some(from) = skip_from {
                    if depth == from {
                        skip_from = None;
                    }
                    depth = depth.saturating_sub(1);
                    continue;
                }
                depth = depth.saturating_sub(1);
                if !in_body {
                    continue;
                }
                match e.local_name().as_ref() {
                    "text" if names.last().is_some_and(|n| n == "body") => in_body = false,
                    "p" | "h" => b.end_para(),
                    "table-cell" | "covered-table-cell" => {
                        let repeat = cell_repeat.pop().unwrap_or(1);
                        let vmerge = cell_merge.pop().unwrap_or(VMerge::None);
                        b.end_cell(repeat, 0, vmerge);
                    }
                    "table-row" => {
                        let repeat = row_repeat.pop().unwrap_or(1);
                        b.end_row(repeat);
                    }
                    "table" => b.end_table(),
                    _ => {}
                }
            }
            Event::Text(t) if in_body && skip_from.is_none() => {
                // Whitespace between elements is layout; text belongs to paragraphs.
                let content = t.xml10_content();
                if !b.paras.is_empty() {
                    b.text(&content);
                }
            }
            Event::CData(t) if in_body && skip_from.is_none() && !b.paras.is_empty() => {
                b.text(&t.xml10_content());
            }
            Event::GeneralRef(r) if in_body && skip_from.is_none() && !b.paras.is_empty() => {
                b.text(&reference_text(&r));
            }
            Event::Eof => break,
            _ => {}
        }
    }
    let mut out = b.finish(stem, source_id, "documento");
    warnings.append(&mut out.warnings);
    out.warnings = warnings;
    Ok(out)
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use crate::archive::tests::zip_of;
    use dm_core::{ColumnRole, Money, Value};

    pub(crate) fn docx_of(body: &str) -> Vec<u8> {
        let doc = format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?><w:document xmlns:w=\"http://schemas.openxmlformats.org/wordprocessingml/2006/main\" xmlns:mc=\"http://schemas.openxmlformats.org/markup-compatibility/2006\"><w:body>{body}</w:body></w:document>"
        );
        zip_of(&[
            ("[Content_Types].xml", b"<Types/>"),
            ("word/document.xml", doc.as_bytes()),
        ])
    }

    fn p(text: &str) -> String {
        format!("<w:p><w:r><w:t xml:space=\"preserve\">{text}</w:t></w:r></w:p>")
    }

    fn row(cells: &[&str]) -> String {
        let mut out = String::from("<w:tr>");
        for c in cells {
            out.push_str("<w:tc><w:tcPr><w:tcW w:w=\"1\"/></w:tcPr>");
            out.push_str(&p(c));
            out.push_str("</w:tc>");
        }
        out.push_str("</w:tr>");
        out
    }

    #[test]
    fn docx_paragraphs_and_a_data_table() {
        let body = format!(
            "{}{}<w:p/>{}<w:tbl>{}{}{}</w:tbl>{}<w:p><w:del><w:r><w:delText>apagado</w:delText></w:r></w:del><w:r><w:t>Att,</w:t><w:tab/><w:t>Ana &amp; Cia</w:t></w:r></w:p>",
            p("De: financeiro@example.com"),
            p("Assunto: NF 55120"),
            p("Segue a fatura."),
            row(&["Vencimento", "Documento", "Valor"]),
            row(&["20/07/2026", "NF 55120", "14.700,00"]),
            row(&["20/08/2026", "NF 55121", "1.000,00"]),
            p("Pagar no boleto."),
        );
        let d = parse_docx(&docx_of(&body), "doc", "src").unwrap_or_else(|e| panic!("{e}"));
        assert!(d.warnings.is_empty(), "{:?}", d.warnings);
        assert_eq!(d.tables.len(), 1);
        let t = &d.tables[0];
        assert_eq!(t.name, "doc:tabela 1");
        assert_eq!(t.len(), 2);
        assert_eq!(t.columns[0].role, ColumnRole::Date);
        assert_eq!(
            t.value(0, 2).and_then(Value::as_money),
            Some(Money::from_centavos(1_470_000))
        );
        assert_eq!(
            d.text,
            "De: financeiro@example.com\nAssunto: NF 55120\n\nSegue a fatura.\n\nPagar no boleto.\nAtt,\tAna & Cia\n"
        );
    }

    #[test]
    fn docx_layout_table_stays_in_the_text_and_merges_pad_cells() {
        let layout = format!(
            "<w:tbl><w:tr><w:tc>{}{}</w:tc></w:tr></w:tbl>",
            p("Carta de cobrança"),
            p("Prezado cliente,")
        );
        let merged = "<w:tbl><w:tr><w:tc><w:tcPr><w:gridSpan w:val=\"2\"/></w:tcPr><w:p><w:r><w:t>Data</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Valor</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>10/07/2026</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>x</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>1,00</w:t></w:r></w:p></w:tc></w:tr></w:tbl>";
        let d = parse_docx(&docx_of(&format!("{layout}{merged}")), "d", "s")
            .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(d.text, "Carta de cobrança\nPrezado cliente,\n\n\n");
        assert_eq!(d.tables.len(), 1);
        assert_eq!(d.tables[0].name, "d:tabela 2");
        assert_eq!(d.tables[0].columns.len(), 3);
        assert_eq!(d.tables[0].columns[2].header, "Valor");
    }

    #[test]
    fn broken_packages_are_errors_or_warnings() {
        assert!(matches!(
            parse_docx(&zip_of(&[("outro.xml", b"<x/>")]), "d", "s"),
            Err(IngestError::Unrecognized(m)) if m.contains("word/document.xml")
        ));
        assert!(parse_docx(b"PK\x03\x04lixo", "d", "s").is_err());
        let cut = zip_of(&[(
            "word/document.xml",
            b"<w:document xmlns:w=\"w\"><w:body><w:p><w:r><w:t>Oi</w:t></w:r></w:p><w:p><w:r><w:t>sem fim",
        )]);
        let d = parse_docx(&cut, "d", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(d.text.starts_with("Oi\n"), "{:?}", d.text);
        let bad = zip_of(&[(
            "word/document.xml",
            b"<w:document xmlns:w=\"w\"><w:body><w:p><w:r><w:t>Oi</w:t></w:p></w:body></w:document>",
        )]);
        let d = parse_docx(&bad, "d", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(d.warnings.len(), 1, "{:?}", d.warnings);
        assert!(parse_docx(b"", "d", "s").is_err());
    }

    pub(crate) fn odt_of(body: &str) -> Vec<u8> {
        let content = format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\"?><office:document-content xmlns:office=\"urn:oasis:names:tc:opendocument:xmlns:office:1.0\" xmlns:text=\"urn:oasis:names:tc:opendocument:xmlns:text:1.0\" xmlns:table=\"urn:oasis:names:tc:opendocument:xmlns:table:1.0\"><office:body><office:text>{body}</office:text></office:body></office:document-content>"
        );
        zip_of(&[
            ("mimetype", b"application/vnd.oasis.opendocument.text"),
            ("content.xml", content.as_bytes()),
        ])
    }

    #[test]
    fn odt_paragraphs_spaces_and_repeated_cells() {
        let body = "<text:h>Títulos em aberto</text:h><text:p>Empresa<text:s text:c=\"3\"/>Sintética<text:tab/>Ltda<office:annotation><text:p>comentário</text:p></office:annotation></text:p><table:table><table:table-row><table:table-cell><text:p>Cliente</text:p></table:table-cell><table:table-cell><text:p>Vencimento</text:p></table:table-cell><table:table-cell><text:p>Valor</text:p></table:table-cell></table:table-row><table:table-row><table:table-cell><text:p>Padaria Central</text:p></table:table-cell><table:table-cell><text:p>10/07/2026</text:p></table:table-cell><table:table-cell><text:p>1.234,56</text:p></table:table-cell><table:table-cell table:number-columns-repeated=\"1000\"/></table:table-row><table:table-row table:number-rows-repeated=\"5000\"><table:table-cell table:number-columns-repeated=\"3\"/></table:table-row></table:table><text:p/><text:p>Fim &#233;</text:p>";
        let d = parse_odt(&odt_of(body), "odt", "src").unwrap_or_else(|e| panic!("{e}"));
        assert!(d.warnings.is_empty(), "{:?}", d.warnings);
        assert_eq!(
            d.text,
            "Títulos em aberto\nEmpresa   Sintética\tLtda\n\n\nFim é\n"
        );
        assert_eq!(d.tables.len(), 1);
        let t = &d.tables[0];
        assert_eq!(t.len(), 1);
        assert_eq!(t.columns.len(), 3, "width is capped by the header");
        assert_eq!(t.columns[0].role, ColumnRole::Name);
        assert_eq!(
            t.value(0, 2).and_then(Value::as_money),
            Some(Money::from_centavos(123_456))
        );
        assert!(matches!(
            parse_odt(&zip_of(&[("mimetype", b"x")]), "d", "s"),
            Err(IngestError::Unrecognized(_))
        ));
    }

    #[test]
    fn data_tables_are_text_parts_of_their_own() {
        let body = format!(
            "{}<w:tbl>{}{}</w:tbl>{}<w:tbl>{}{}</w:tbl>",
            p("Segue o cronograma:"),
            row(&["Vencimento", "Descrição", "Valor"]),
            row(&["25/07/2026", "Parcela 1", "7.497,00"]),
            p("Dados para depósito:"),
            row(&["Banco", "341"]),
            row(&["Agência", "0987"]),
        );
        let d = parse_docx(&docx_of(&body), "acordo", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(
            d.tables.len(),
            1,
            "a two-column key/value grid is not a data table"
        );
        assert_eq!(
            d.text,
            "Segue o cronograma:\n\nDados para depósito:\nBanco\t341\nAgência\t0987\n\n"
        );
        assert_eq!(d.parts.len(), 3);
        assert_eq!(d.parts[1].table.as_deref(), Some("acordo:tabela 1"));
        assert_eq!(
            crate::source::document_text(&d.parts, |t| t == "acordo:tabela 1"),
            "Segue o cronograma:\nVencimento\tDescrição\tValor\n25/07/2026\tParcela 1\t7.497,00\n\nDados para depósito:\nBanco\t341\nAgência\t0987\n\n"
        );
        assert_eq!(crate::source::document_text(&d.parts, |_| false), d.text);
    }

    #[test]
    fn vertical_merges_carry_the_value_down() {
        let merged = |top: &str, rest: &str| {
            format!("<w:tc><w:tcPr><w:vMerge{top}/></w:tcPr>{}</w:tc>", p(rest))
        };
        let plain = |t: &str| format!("<w:tc>{}</w:tc>", p(t));
        let body = format!(
            "<w:tbl>{}<w:tr>{}{}{}</w:tr><w:tr>{}{}{}</w:tr><w:tr>{}{}{}</w:tr></w:tbl>",
            row(&["Data", "Histórico", "Valor"]),
            merged(" w:val=\"restart\"", "10/07/2026"),
            plain("PIX A"),
            plain("1,00"),
            merged("", ""),
            plain("PIX B"),
            plain("2,00"),
            plain("11/07/2026"),
            plain("TED"),
            plain("3,00"),
        );
        let d = parse_docx(&docx_of(&body), "d", "s").unwrap_or_else(|e| panic!("{e}"));
        let t = &d.tables[0];
        assert_eq!(t.len(), 3);
        assert_eq!(t.value(1, 0), t.value(0, 0));
        assert_ne!(t.value(2, 0), t.value(0, 0));
        // ODT: a cell spanning rows and the covered cells under it.
        let cell = |t: &str| format!("<table:table-cell><text:p>{t}</text:p></table:table-cell>");
        let odt_body = format!(
            "<table:table><table:table-row>{}{}{}</table:table-row><table:table-row><table:table-cell table:number-rows-spanned=\"2\"><text:p>10/07/2026</text:p></table:table-cell>{}{}</table:table-row><table:table-row><table:covered-table-cell/>{}{}</table:table-row><table:table-row>{}{}{}</table:table-row></table:table>",
            cell("Data"),
            cell("Histórico"),
            cell("Valor"),
            cell("PIX A"),
            cell("1,00"),
            cell("PIX B"),
            cell("2,00"),
            cell("11/07/2026"),
            cell("TED"),
            cell("3,00"),
        );
        let d = parse_odt(&odt_of(&odt_body), "o", "s").unwrap_or_else(|e| panic!("{e}"));
        let t = &d.tables[0];
        assert_eq!(t.len(), 3);
        assert_eq!(t.value(1, 0), t.value(0, 0));
        assert_eq!(t.value(1, 1), Some(&Value::Text("PIX B".into())));
        assert_ne!(t.value(2, 0), t.value(0, 0));
    }

    #[test]
    fn repeated_rows_and_cells_spend_one_budget() {
        // One cell of 1 KB repeated across 256 columns, in a row repeated 256 times: 64 MB
        // of copies if every repetition were kept.
        let text = "x".repeat(1024);
        let body = format!(
            "<table:table><table:table-row><table:table-cell><text:p>Cliente</text:p></table:table-cell><table:table-cell><text:p>Valor</text:p></table:table-cell><table:table-cell><text:p>Obs</text:p></table:table-cell></table:table-row><table:table-row table:number-rows-repeated=\"100000\"><table:table-cell table:number-columns-repeated=\"100000\"><text:p>{text}</text:p></table:table-cell></table:table-row></table:table>"
        );
        let started = std::time::Instant::now();
        let d = parse_odt(&odt_of(&body), "o", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(started.elapsed().as_secs() < 30, "{:?}", started.elapsed());
        assert!(
            d.warnings.iter().any(|w| w.contains("cortadas no limite")),
            "{:?}",
            d.warnings
        );
        let cells: usize = d.tables.iter().map(|t| t.len() * t.columns.len()).sum();
        assert!(cells <= MAX_CELLS, "{cells}");
        let bytes: usize = d
            .tables
            .iter()
            .flat_map(|t| t.rows.iter())
            .flat_map(|r| r.values.iter())
            .map(|v| v.render().len())
            .sum();
        assert!(bytes <= MAX_CELL_BYTES, "{bytes}");
        let all: usize = d.parts.iter().map(|p| p.text.len()).sum::<usize>() + d.text.len();
        assert!(all <= 2 * MAX_TEXT + 16, "{all}");
        // A layout table (no header) of repeated text is cut at the text budget.
        let layout = format!(
            "<table:table><table:table-row table:number-rows-repeated=\"100000\"><table:table-cell table:number-columns-repeated=\"100000\"><text:p>{text}</text:p></table:table-cell></table:table-row></table:table>"
        );
        let d = parse_odt(&odt_of(&layout), "o", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(d.tables.is_empty());
        assert!(d.text.len() <= MAX_TEXT, "{}", d.text.len());
        assert!(
            d.warnings
                .iter()
                .any(|w| w.contains("texto do documento cortado")),
            "{:?}",
            d.warnings
        );
    }
}
