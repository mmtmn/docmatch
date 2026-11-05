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
use crate::grid::{clean_cell, table_from_rows};
use crate::IngestError;

/// Decompressed XML member, bytes.
pub(crate) const MAX_XML: u64 = 64 << 20;
/// Text kept, bytes.
pub(crate) const MAX_TEXT: usize = 8 << 20;
/// Table cells kept per document.
const MAX_CELLS: usize = 500_000;
/// Element nesting followed.
const MAX_DEPTH: usize = 512;
/// Repetitions honoured for one ODT cell or row (`number-columns-repeated`).
const MAX_REPEAT: usize = 256;

/// What a text document yielded.
#[derive(Debug, Default)]
pub struct DocParsed {
    /// Tables with a recognizable header.
    pub tables: Vec<Table>,
    /// Paragraphs (and layout tables), one line each.
    pub text: String,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
}

#[derive(Default)]
struct OpenTable {
    rows: Vec<Vec<Vec<String>>>,
    row: Option<Vec<Vec<String>>>,
    cell: Option<Vec<String>>,
}

enum Block {
    Para(String),
    Table(usize),
}

/// Builds blocks, paragraphs and tables from the structural events of either format.
#[derive(Default)]
struct Builder {
    blocks: Vec<Block>,
    tables: Vec<Vec<Vec<Vec<String>>>>,
    open: Vec<OpenTable>,
    paras: Vec<String>,
    text_bytes: usize,
    cells: usize,
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

    /// Closes a cell: `repeat` copies of it, then `extra` empty cells (a horizontal merge).
    fn end_cell(&mut self, repeat: usize, extra: usize) {
        let Some(t) = self.open.last_mut() else {
            return;
        };
        let Some(cell) = t.cell.take() else {
            return;
        };
        let row = t.row.get_or_insert_with(Vec::new);
        let total = repeat.clamp(1, MAX_REPEAT) + extra.min(MAX_REPEAT);
        if self.cells + total > MAX_CELLS {
            self.cells_cut = true;
            return;
        }
        self.cells += total;
        for _ in 0..repeat.clamp(1, MAX_REPEAT) {
            row.push(cell.clone());
        }
        for _ in 0..extra.min(MAX_REPEAT) {
            row.push(Vec::new());
        }
    }

    fn end_row(&mut self, repeat: usize) {
        let Some(t) = self.open.last_mut() else {
            return;
        };
        if t.cell.is_some() {
            self.end_cell(1, 0);
        }
        let Some(t) = self.open.last_mut() else {
            return;
        };
        let Some(row) = t.row.take() else {
            return;
        };
        let empty = row.iter().all(|c| c.iter().all(|p| p.trim().is_empty()));
        // Trailing empty rows repeated thousands of times are formatting, not data.
        let times = if empty {
            1
        } else {
            repeat.clamp(1, MAX_REPEAT)
        };
        for _ in 0..times {
            t.rows.push(row.clone());
        }
    }

    fn end_table(&mut self) {
        if self.open.last().is_some_and(|t| t.row.is_some()) {
            self.end_row(1);
        }
        let Some(done) = self.open.pop() else {
            return;
        };
        if let Some(parent) = self.open.last_mut() {
            // A table inside a cell: its text joins the enclosing cell.
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
        let mut data_tables = vec![false; self.tables.len()];
        for (i, rows) in self.tables.iter().enumerate() {
            let grid: Vec<Vec<String>> = rows
                .iter()
                .map(|r| r.iter().map(|c| clean_cell(&c.join(" "))).collect())
                .collect();
            let name = format!("{stem}:tabela {}", i + 1);
            if let Some(t) = table_from_rows(&name, source_id, grid) {
                out.tables.push(t);
                data_tables[i] = true;
            }
        }
        let mut text = String::new();
        for block in &self.blocks {
            match block {
                Block::Para(p) => {
                    text.push_str(p);
                    text.push('\n');
                }
                Block::Table(i) if data_tables[*i] => text.push('\n'),
                Block::Table(i) => {
                    for row in &self.tables[*i] {
                        for cell in row {
                            for p in cell.iter().filter(|p| !p.trim().is_empty()) {
                                text.push_str(p.trim());
                                text.push('\n');
                            }
                        }
                    }
                    text.push('\n');
                }
            }
        }
        out.text = text;
        if self.text_cut {
            out.warnings.push(format!(
                "texto do {format} cortado em {} MB",
                MAX_TEXT >> 20
            ));
        }
        if self.cells_cut {
            out.warnings.push(format!(
                "tabelas do {format} cortadas em {MAX_CELLS} células"
            ));
        }
        out
    }
}

fn attr(e: &BytesStart<'_>, local: &str) -> Option<String> {
    e.attributes()
        .flatten()
        .find(|a| a.key.local_name().as_ref() == local)
        .map(|a| {
            a.normalized_value(quick_xml::XmlVersion::Implicit1_0)
                .map_or_else(|_| a.value.to_string(), std::borrow::Cow::into_owned)
        })
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
    // Horizontal merge of the open cells (`w:gridSpan` inside `w:tcPr`).
    let mut spans: Vec<usize> = Vec::new();
    let mut warnings = Vec::new();
    loop {
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
                        b.end_cell(1, span.saturating_sub(1));
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
    // Repetition of the open cells and rows.
    let mut cell_repeat: Vec<usize> = Vec::new();
    let mut row_repeat: Vec<usize> = Vec::new();
    let mut in_body = false;
    // Local names of the open elements: the body is the `text` child of `body`.
    let mut names: Vec<String> = Vec::new();
    let mut warnings = Vec::new();
    loop {
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
                    "table-cell" | "covered-table-cell" => {
                        b.start_cell();
                        b.end_cell(count_attr(&e, "number-columns-repeated"), 0);
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
                        b.end_cell(repeat, 0);
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
}
