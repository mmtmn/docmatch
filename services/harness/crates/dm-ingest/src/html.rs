//! HTML pages and fragments (a statement saved from internet banking, an e-mail body): the
//! DOM is built by html5ever (through `scraper`), a browser-grade parser that never fails on
//! broken markup.
//!
//! Every `<table>` is a candidate grid (its own rows and cells; a nested table is a grid of its
//! own; a cell spanning rows repeats its value down its columns) and goes through the
//! spreadsheet header detection and role inference; a table with no header row is layout and
//! stays in the text. The text is the page as a browser would copy it: one line per block
//! element, a blank line after paragraphs, no scripts, styles, head or hidden elements; data
//! tables are parts of their own ([`HtmlParsed::parts`]). The DOM is walked iteratively (a
//! deeply nested page cannot exhaust the stack), the input is cut at [`MAX_HTML`] bytes and
//! the parse stops where html5ever's tree gets too deep or too large ([`crate::html_limit`]).

use std::collections::BTreeMap;

use dm_core::Table;
use ego_tree::iter::Edge;
use ego_tree::NodeId;
use encoding_rs::{Encoding, UTF_16BE, UTF_16LE, UTF_8, WINDOWS_1252};
use scraper::node::Element;
use scraper::{Html, Node};

use crate::cancel::{Every, STOPPED};
use crate::grid::{clean_cell, grid_records, table_from_records, MAX_CELL_CHARS};
use crate::html_limit::{parse_bounded, Limit, MAX_DEPTH, MAX_NODES};
use crate::source::{document_text, TextPart};
use crate::IngestError;

/// Bytes of markup read; the rest of a larger page is cut (with a warning).
pub(crate) const MAX_HTML: usize = 8 << 20;
/// Columns one cell may span.
const MAX_SPAN: usize = 64;
/// Rows one cell may span (`rowspan="0"`, to the end of its row group, is this many).
const MAX_ROWSPAN: usize = 1_000;
/// Cells kept per page, the copies of spanning cells included.
const MAX_CELLS: usize = 300_000;
/// Bytes of cell text kept per page, the copies of cells spanning rows included.
const MAX_CELL_BYTES: usize = 8 << 20;
/// Bytes of text kept from one cell before it is cleaned.
const MAX_RAW_CELL: usize = MAX_CELL_CHARS * 4;

/// What an HTML page yielded.
#[derive(Debug, Default)]
pub struct HtmlParsed {
    /// Tables with a recognizable header.
    pub tables: Vec<Table>,
    /// The text of the page, one line per block; a data table leaves a blank line.
    pub text: String,
    /// The text in parts, data tables apart (empty when the page has none).
    pub parts: Vec<TextPart>,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
}

/// The encoding announced by a byte-order mark or a `<meta charset>` in the first bytes,
/// else UTF-8 when the bytes are valid UTF-8, else windows-1252.
fn encoding_of(bytes: &[u8]) -> &'static Encoding {
    if bytes.starts_with(&[0xEF, 0xBB, 0xBF]) {
        return UTF_8;
    }
    if bytes.starts_with(&[0xFF, 0xFE]) {
        return UTF_16LE;
    }
    if bytes.starts_with(&[0xFE, 0xFF]) {
        return UTF_16BE;
    }
    let head = String::from_utf8_lossy(&bytes[..bytes.len().min(4096)]).to_ascii_lowercase();
    if let Some(at) = head.find("charset") {
        let rest = head[at + "charset".len()..].trim_start_matches([' ', '=', '"', '\'']);
        let label: String = rest
            .chars()
            .take_while(|c| c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | ':' | '.'))
            .collect();
        if let Some(enc) = Encoding::for_label(label.as_bytes()) {
            // A page saved as UTF-16 announces it with a BOM; a meta saying so is wrong.
            if enc != UTF_16LE && enc != UTF_16BE {
                return enc;
            }
        }
    }
    if std::str::from_utf8(bytes).is_ok() {
        UTF_8
    } else {
        WINDOWS_1252
    }
}

/// Elements whose content is never text of the page.
fn skipped(name: &str) -> bool {
    matches!(
        name,
        "script"
            | "style"
            | "noscript"
            | "template"
            | "head"
            | "svg"
            | "object"
            | "iframe"
            | "select"
            | "button"
    )
}

fn hidden(e: &Element) -> bool {
    e.attr("hidden").is_some()
        || e.attr("style").is_some_and(|s| {
            let s: String = s
                .chars()
                .filter(|c| !c.is_whitespace())
                .collect::<String>()
                .to_ascii_lowercase();
            s.contains("display:none")
        })
}

/// Block elements: a line break before and after.
fn block(name: &str) -> bool {
    matches!(
        name,
        "div"
            | "tr"
            | "td"
            | "th"
            | "table"
            | "li"
            | "ul"
            | "ol"
            | "dl"
            | "dt"
            | "dd"
            | "section"
            | "article"
            | "header"
            | "footer"
            | "nav"
            | "aside"
            | "main"
            | "address"
            | "center"
            | "caption"
            | "form"
            | "fieldset"
            | "figure"
            | "hr"
    )
}

/// Paragraph-like elements: a blank line after.
fn paragraph(name: &str) -> bool {
    matches!(
        name,
        "p" | "h1" | "h2" | "h3" | "h4" | "h5" | "h6" | "blockquote" | "pre"
    )
}

/// Appends at most what keeps `s` within `cap` bytes.
pub(crate) fn push_capped(s: &mut String, add: &str, cap: usize) {
    let room = cap.saturating_sub(s.len());
    if add.len() <= room {
        s.push_str(add);
    } else {
        let mut end = room;
        while !add.is_char_boundary(end) {
            end -= 1;
        }
        s.push_str(&add[..end]);
    }
}

/// The cells and bytes a page's grids may still take.
struct CellBudget {
    cells: usize,
    bytes: usize,
    cut: bool,
}

impl CellBudget {
    fn take(&mut self, cells: usize, bytes: usize) -> bool {
        if self.cells + cells > MAX_CELLS || self.bytes + bytes > MAX_CELL_BYTES {
            self.cut = true;
            return false;
        }
        self.cells += cells;
        self.bytes += bytes;
        true
    }
}

/// A cell being read: its text and how many columns and rows it spans.
struct OpenCell {
    text: String,
    cols: usize,
    rows: usize,
}

#[derive(Default)]
struct GridAcc {
    order: usize,
    node: Option<NodeId>,
    rows: Vec<Vec<String>>,
    row: Option<Vec<String>>,
    cell: Option<OpenCell>,
    /// Cells of earlier rows still spanning down, by column: rows left and the value shown.
    down: Vec<Option<(usize, String)>>,
}

impl GridAcc {
    /// Fills the columns at the end of `row` that a cell above still covers.
    fn fill_down(
        row: &mut Vec<String>,
        down: &mut [Option<(usize, String)>],
        budget: &mut CellBudget,
    ) {
        while let Some(Some((left, value))) = down.get_mut(row.len()) {
            if !budget.take(1, value.len()) {
                return;
            }
            row.push(value.clone());
            *left -= 1;
            if *left == 0 {
                down[row.len() - 1] = None;
            }
        }
    }

    fn close_cell(&mut self, budget: &mut CellBudget) {
        let Some(cell) = self.cell.take() else {
            return;
        };
        let row = self.row.get_or_insert_with(Vec::new);
        Self::fill_down(row, &mut self.down, budget);
        if !budget.take(cell.cols, cell.text.len() * cell.rows) {
            return;
        }
        let col = row.len();
        if cell.rows > 1 {
            if self.down.len() < col + cell.cols {
                self.down.resize(col + cell.cols, None);
            }
            for (k, slot) in self.down[col..col + cell.cols].iter_mut().enumerate() {
                let shown = if k == 0 {
                    cell.text.clone()
                } else {
                    String::new()
                };
                *slot = Some((cell.rows - 1, shown));
            }
        }
        row.push(cell.text);
        for _ in 1..cell.cols {
            row.push(String::new());
        }
    }

    fn close_row(&mut self, budget: &mut CellBudget) {
        self.close_cell(budget);
        let Some(mut row) = self.row.take() else {
            return;
        };
        // Columns past the last cell that cells above still cover.
        loop {
            Self::fill_down(&mut row, &mut self.down, budget);
            let more = self.down[row.len().min(self.down.len())..]
                .iter()
                .any(Option::is_some);
            if !more || budget.cut {
                break;
            }
            row.push(String::new());
        }
        self.rows.push(row);
    }

    /// A row group (`thead`, `tbody`, `tfoot`) ends: spans stop at it.
    fn close_group(&mut self, budget: &mut CellBudget) {
        if self.row.is_some() || self.cell.is_some() {
            self.close_row(budget);
        }
        self.down.clear();
    }
}

fn span_attr(e: &Element, name: &str, max: usize, zero: usize) -> usize {
    match e.attr(name).and_then(|s| s.trim().parse::<usize>().ok()) {
        Some(0) => zero,
        Some(n) => n.min(max),
        None => 1,
    }
}

/// A table of the page: (document order, node, rows of cell texts).
type Grid = (usize, NodeId, Vec<Vec<String>>);

/// Every table of the document, in document order, and whether the cell budget cut them.
fn grids(doc: &Html) -> (Vec<Grid>, bool) {
    let mut stack: Vec<GridAcc> = Vec::new();
    let mut done = Vec::new();
    let mut opened = 0usize;
    let mut budget = CellBudget {
        cells: 0,
        bytes: 0,
        cut: false,
    };
    let mut skip: Option<NodeId> = None;
    let mut every = Every::default();
    for edge in doc.tree.root().traverse() {
        if every.cancelled() {
            break;
        }
        match edge {
            Edge::Open(node) => {
                if skip.is_some() {
                    continue;
                }
                match node.value() {
                    Node::Element(e) => {
                        let name = e.name();
                        if skipped(name) || hidden(e) {
                            skip = Some(node.id());
                            continue;
                        }
                        match name {
                            "table" => {
                                stack.push(GridAcc {
                                    order: opened,
                                    node: Some(node.id()),
                                    ..GridAcc::default()
                                });
                                opened += 1;
                            }
                            "thead" | "tbody" | "tfoot" => {
                                if let Some(t) = stack.last_mut() {
                                    t.close_group(&mut budget);
                                }
                            }
                            "tr" => {
                                if let Some(t) = stack.last_mut() {
                                    t.close_row(&mut budget);
                                    t.row = Some(Vec::new());
                                }
                            }
                            "td" | "th" => {
                                if let Some(t) = stack.last_mut() {
                                    t.close_cell(&mut budget);
                                    t.cell = Some(OpenCell {
                                        text: String::new(),
                                        cols: span_attr(e, "colspan", MAX_SPAN, 1),
                                        rows: span_attr(e, "rowspan", MAX_ROWSPAN, MAX_ROWSPAN),
                                    });
                                }
                            }
                            "br" | "p" | "div" | "li" => {
                                if let Some(c) = stack.last_mut().and_then(|t| t.cell.as_mut()) {
                                    push_capped(&mut c.text, " ", MAX_RAW_CELL);
                                }
                            }
                            _ => {}
                        }
                    }
                    Node::Text(t) => {
                        if let Some(c) = stack.last_mut().and_then(|g| g.cell.as_mut()) {
                            push_capped(&mut c.text, t, MAX_RAW_CELL);
                        }
                    }
                    _ => {}
                }
            }
            Edge::Close(node) => {
                if let Some(id) = skip {
                    if id == node.id() {
                        skip = None;
                    }
                    continue;
                }
                if let Node::Element(e) = node.value() {
                    match e.name() {
                        "td" | "th" => {
                            if let Some(t) = stack.last_mut() {
                                t.close_cell(&mut budget);
                            }
                        }
                        "tr" => {
                            if let Some(t) = stack.last_mut() {
                                t.close_row(&mut budget);
                            }
                        }
                        "thead" | "tbody" | "tfoot" => {
                            if let Some(t) = stack.last_mut() {
                                t.close_group(&mut budget);
                            }
                        }
                        "table" => {
                            if let Some(mut t) = stack.pop() {
                                t.close_row(&mut budget);
                                if let Some(id) = t.node {
                                    done.push((
                                        t.order,
                                        id,
                                        t.rows
                                            .into_iter()
                                            .map(|r| r.iter().map(|c| clean_cell(c)).collect())
                                            .collect(),
                                    ));
                                }
                            }
                        }
                        _ => {}
                    }
                }
            }
        }
    }
    done.sort_by_key(|(order, _, _)| *order);
    (done, budget.cut)
}

fn ensure_newline(out: &mut String) {
    if !out.is_empty() && !out.ends_with('\n') {
        out.push('\n');
    }
}

/// Lines trimmed, at most one blank line in a row, none at the start or the end.
fn tidy(out: &str) -> String {
    let mut text = String::with_capacity(out.len());
    let mut blank = true;
    for line in out.lines() {
        let line = line.trim();
        if line.is_empty() {
            if !blank {
                text.push('\n');
            }
            blank = true;
        } else {
            text.push_str(line);
            text.push('\n');
            blank = false;
        }
    }
    while text.ends_with("\n\n") {
        text.pop();
    }
    text
}

/// The text of the page in parts: prose, and each data table (`data`: node → table name) on
/// its own.
fn page_parts(doc: &Html, data: &BTreeMap<NodeId, String>) -> Vec<TextPart> {
    let mut parts: Vec<TextPart> = Vec::new();
    let mut out = String::new();
    let mut used = 0usize;
    let mut table: Option<(NodeId, String)> = None;
    let mut skip: Option<NodeId> = None;
    let mut pre = 0usize;
    let mut every = Every::default();
    for edge in doc.tree.root().traverse() {
        if every.cancelled() {
            break;
        }
        match edge {
            Edge::Open(node) => {
                if skip.is_some() {
                    continue;
                }
                match node.value() {
                    Node::Element(e) => {
                        let name = e.name();
                        if skipped(name) || hidden(e) {
                            skip = Some(node.id());
                            continue;
                        }
                        if table.is_none() {
                            if let Some(t) = data.get(&node.id()) {
                                used += out.len();
                                parts.push(TextPart {
                                    table: None,
                                    text: tidy(&std::mem::take(&mut out)),
                                });
                                table = Some((node.id(), t.clone()));
                            }
                        }
                        if table.is_some() {
                            // A data table reads one row per line, cells tab-separated.
                            match name {
                                "tr" | "table" | "thead" | "tbody" | "tfoot" | "caption" => {
                                    ensure_newline(&mut out);
                                }
                                "td" | "th" => {
                                    if !out.is_empty() && !out.ends_with('\n') {
                                        out.push('\t');
                                    }
                                }
                                n if n == "br" || block(n) || paragraph(n) => {
                                    if !out.ends_with([' ', '\t', '\n']) {
                                        out.push(' ');
                                    }
                                }
                                _ => {}
                            }
                        } else if name == "br" {
                            out.push('\n');
                        } else if block(name) || paragraph(name) {
                            ensure_newline(&mut out);
                        }
                        if name == "pre" {
                            pre += 1;
                        }
                    }
                    Node::Text(t) => {
                        if pre > 0 {
                            out.push_str(t);
                        } else {
                            let mut last_space = out.ends_with([' ', '\t', '\n']) || out.is_empty();
                            for c in t.chars() {
                                if c.is_whitespace() {
                                    if !last_space {
                                        out.push(' ');
                                        last_space = true;
                                    }
                                } else {
                                    out.push(c);
                                    last_space = false;
                                }
                            }
                        }
                    }
                    _ => {}
                }
            }
            Edge::Close(node) => {
                if let Some(id) = skip {
                    if id == node.id() {
                        skip = None;
                        ensure_newline(&mut out);
                    }
                    continue;
                }
                if let Node::Element(e) = node.value() {
                    let name = e.name();
                    if name == "pre" {
                        pre = pre.saturating_sub(1);
                    }
                    if table.is_some() {
                        if matches!(
                            name,
                            "tr" | "table" | "thead" | "tbody" | "tfoot" | "caption"
                        ) {
                            ensure_newline(&mut out);
                        }
                    } else if paragraph(name) {
                        ensure_newline(&mut out);
                        out.push('\n');
                    } else if block(name) {
                        ensure_newline(&mut out);
                    }
                }
                if table.as_ref().is_some_and(|(id, _)| *id == node.id()) {
                    if let Some((_, name)) = table.take() {
                        used += out.len();
                        let mut text = tidy(&std::mem::take(&mut out));
                        if !text.is_empty() {
                            text.push('\n');
                        }
                        parts.push(TextPart {
                            table: Some(name),
                            text,
                        });
                    }
                }
            }
        }
        if used + out.len() > crate::office::MAX_TEXT {
            break;
        }
    }
    let text = tidy(&out);
    match table {
        Some((_, name)) => parts.push(TextPart {
            table: Some(name),
            text,
        }),
        None => parts.push(TextPart { table: None, text }),
    }
    parts.retain(|p| p.table.is_some() || !p.text.is_empty());
    parts
}

/// The tree of `markup`, read up to where it got too deep or too large, and the warning when
/// it was cut.
fn parse_within_limits(markup: &str) -> (Html, Option<String>) {
    let (html, limit) = parse_bounded(markup);
    let warning = limit.map(|why| match why {
        Limit::Depth => format!(
            "página HTML aninhada demais (mais de {MAX_DEPTH} níveis): o restante não foi lido"
        ),
        Limit::Size => format!(
            "página HTML com elementos demais (mais de {MAX_NODES}): o restante não foi lido"
        ),
        Limit::Cancelled => STOPPED.to_string(),
    });
    (html, warning)
}

/// The text of an HTML fragment (an e-mail body), without tables detection, under the same
/// limits as a page.
pub(crate) fn html_text(markup: &str) -> String {
    let markup = &markup[..floor_char_boundary(markup, MAX_HTML)];
    let (doc, _) = parse_within_limits(markup);
    let parts = page_parts(&doc, &BTreeMap::new());
    tidy(&document_text(&parts, |_| true))
}

fn floor_char_boundary(s: &str, max: usize) -> usize {
    let mut end = s.len().min(max);
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    end
}

/// Parses an HTML page: data tables and the text.
pub fn parse_html(bytes: &[u8], stem: &str, source_id: &str) -> Result<HtmlParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let mut out = HtmlParsed::default();
    let encoding = encoding_of(bytes);
    let (decoded, _, _) = encoding.decode(bytes);
    let mut markup: &str = &decoded;
    if markup.len() > MAX_HTML {
        markup = &markup[..floor_char_boundary(markup, MAX_HTML)];
        out.warnings.push(format!(
            "página HTML acima de {} MB: só o começo foi lido",
            MAX_HTML >> 20
        ));
    }
    let (doc, cut) = parse_within_limits(markup);
    out.warnings.extend(cut);
    let (grids, cut) = grids(&doc);
    if cut {
        out.warnings.push(format!(
            "tabelas da página cortadas no limite de {MAX_CELLS} células ou {} MB de texto",
            MAX_CELL_BYTES >> 20
        ));
    }
    // Which grids are tables is known before they are typed: the text is read and the tree
    // dropped first, so the tree and the typed tables are never in memory together.
    let mut data = BTreeMap::new();
    let mut records = Vec::new();
    for (order, id, rows) in grids {
        if let Some(r) = grid_records(rows) {
            let name = format!("{stem}:tabela {}", order + 1);
            data.insert(id, name.clone());
            records.push((name, r));
        }
    }
    let parts = page_parts(&doc, &data);
    drop(doc);
    for (name, (headers, rows)) in records {
        if let Some(t) = table_from_records(&name, source_id, &headers, &headers, rows) {
            out.tables.push(t);
        }
    }
    out.text = tidy(&document_text(&parts, |_| false));
    if parts.iter().any(|p| p.table.is_some()) {
        out.parts = parts;
    }
    if crate::cancel::cancelled() && !out.warnings.iter().any(|w| w == STOPPED) {
        out.warnings.push(STOPPED.to_string());
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use dm_core::{ColumnRole, Money, Value};

    const PAGE: &str = "<!DOCTYPE html><html><head><title>Extrato</title><style>td{color:red}</style><script>var x = '<table><tr><td>Data</td><td>Valor</td></tr></table>';</script></head><body><div class=\"topo\"><p>Banco Sintético S.A.<br>Agência 0001 &middot; Conta 12345-6</p></div><table width=\"100%\"><tr><td><table class=\"extrato\"><thead><tr><th>Data</th><th>Histórico</th><th>Documento</th><th>Valor (R$)</th></tr></thead><tbody><tr><td>10/07/2026</td><td>LIQUIDACAO   COBRANCA<br>SIMPLES</td><td>000098765001</td><td>24.300,00</td></tr><tr><td>15/07/2026</td><td>TARIFA PACOTE</td><td></td><td>-980,00</td></tr><tr><td colspan=\"3\">Saldo</td><td>23.320,00</td></tr></tbody></table></td></tr></table><p style=\"display: none\">pré-cabeçalho escondido</p><p>Dúvidas? Fale com o gerente.</p></body></html>";

    #[test]
    fn statement_page_gives_the_nested_data_table_and_the_text() {
        let h = parse_html(PAGE.as_bytes(), "extrato", "src").unwrap_or_else(|e| panic!("{e}"));
        assert!(h.warnings.is_empty(), "{:?}", h.warnings);
        assert_eq!(h.tables.len(), 1, "the layout table is not a table");
        let t = &h.tables[0];
        assert_eq!(t.name, "extrato:tabela 2");
        assert_eq!(t.len(), 3);
        assert_eq!(t.columns[0].role, ColumnRole::Date);
        assert_eq!(t.columns[1].role, ColumnRole::FreeText);
        assert_eq!(t.columns[3].role, ColumnRole::Amount);
        assert_eq!(
            t.value(0, 1),
            Some(&Value::Text("LIQUIDACAO COBRANCA SIMPLES".into()))
        );
        assert_eq!(
            t.value(1, 3).and_then(Value::as_money),
            Some(Money::from_centavos(-98_000))
        );
        assert_eq!(
            h.text,
            "Banco Sintético S.A.\nAgência 0001 · Conta 12345-6\n\nDúvidas? Fale com o gerente.\n"
        );
    }

    #[test]
    fn data_tables_are_text_parts_of_their_own() {
        let h = parse_html(PAGE.as_bytes(), "extrato", "src").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(h.parts.len(), 3, "{:?}", h.parts);
        assert_eq!(h.parts[1].table.as_deref(), Some("extrato:tabela 2"));
        assert_eq!(
            h.parts[1].text,
            "Data\tHistórico\tDocumento\tValor (R$)\n10/07/2026\tLIQUIDACAO COBRANCA SIMPLES\t000098765001\t24.300,00\n15/07/2026\tTARIFA PACOTE\t\t-980,00\nSaldo\t23.320,00\n\n"
        );
        let all = document_text(&h.parts, |_| true);
        assert!(
            all.contains("TARIFA PACOTE") && all.contains("Dúvidas?"),
            "{all}"
        );
        assert_eq!(tidy(&document_text(&h.parts, |_| false)), h.text);
        let plain = parse_html(b"<p>Sem tabela</p>", "p", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(plain.parts.is_empty());
    }

    #[test]
    fn cells_spanning_rows_repeat_down_their_columns() {
        let page = "<table><tr><th>Data</th><th>Histórico</th><th>Valor</th></tr>\
            <tr><td rowspan=\"2\">10/07/2026</td><td>PIX A</td><td>1,00</td></tr>\
            <tr><td>PIX B</td><td>2,00</td></tr>\
            <tr><td>11/07/2026</td><td rowspan=\"0\" colspan=\"2\">TED</td></tr>\
            <tr><td>12/07/2026</td></tr></table>";
        let h = parse_html(page.as_bytes(), "x", "s").unwrap_or_else(|e| panic!("{e}"));
        let t = &h.tables[0];
        assert_eq!(t.len(), 4);
        assert_eq!(t.value(1, 0), t.value(0, 0), "the date spans two rows");
        assert_eq!(t.value(1, 1), Some(&Value::Text("PIX B".into())));
        assert_eq!(t.value(3, 1), Some(&Value::Text("TED".into())));
        assert_eq!(t.value(3, 2), Some(&Value::Null), "a colspan copy is empty");
        // A row group ends the spans.
        let groups = "<table><thead><tr><th>Data</th><th>Valor</th><th>Tipo</th></tr></thead>\
            <tbody><tr><td rowspan=5>10/07/2026</td><td>1,00</td><td>C</td></tr></tbody>\
            <tbody><tr><td>11/07/2026</td><td>2,00</td><td>D</td></tr></tbody></table>";
        let h = parse_html(groups.as_bytes(), "x", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(h.tables[0].len(), 2);
        assert_eq!(
            h.tables[0].value(1, 1).and_then(Value::as_money),
            Some(Money::from_centavos(200))
        );
    }

    #[test]
    fn copies_of_spanning_cells_are_bounded() {
        // Every cell spans a thousand rows: copies cost cells and bytes from one budget.
        let text = "x".repeat(4000);
        let mut page = String::from("<table><tr><th>A</th><th>B</th><th>C</th></tr>");
        for _ in 0..50 {
            page.push_str(&format!(
                "<tr><td rowspan=1000>{text}</td><td rowspan=1000>{text}</td><td rowspan=1000>{text}</td></tr>"
            ));
        }
        page.push_str(&"<tr></tr>".repeat(2000));
        page.push_str("</table>");
        let h = parse_html(page.as_bytes(), "x", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(
            h.warnings.iter().any(|w| w.contains("cortadas")),
            "{:?}",
            h.warnings
        );
        let bytes: usize = h
            .tables
            .iter()
            .flat_map(|t| t.rows.iter())
            .flat_map(|r| r.values.iter())
            .map(|v| v.render().len())
            .sum();
        assert!(bytes <= MAX_CELL_BYTES, "{bytes}");
    }

    #[test]
    fn pages_past_the_tree_limits_are_cut_with_a_warning() {
        let mut page = String::from("<p>Antes do corte</p>");
        page.push_str(&"<span></i>".repeat(2_000));
        page.push_str("depois");
        let h = parse_html(page.as_bytes(), "p", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(h.text, "Antes do corte\n");
        assert!(
            h.warnings[0].contains("aninhada demais"),
            "{:?}",
            h.warnings
        );
        // A message body goes through the same limits.
        let body = format!("<div>NF 12</div>{}", "<b><div></b>".repeat(2_000));
        assert_eq!(html_text(&body), "NF 12\n");
    }

    #[test]
    fn charset_and_broken_markup() {
        let latin: Vec<u8> = b"<html><head><meta charset=\"iso-8859-1\"></head><body><p>Emiss\xE3o</p><div>sem fechar"
            .to_vec();
        let h = parse_html(&latin, "p", "s").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(h.text, "Emissão\n\nsem fechar\n");
        let mut deep = String::from("<p>topo</p>");
        for _ in 0..200_000 {
            deep.push_str("<div>");
        }
        deep.push_str("fundo");
        let started = std::time::Instant::now();
        let h = parse_html(deep.as_bytes(), "p", "s").unwrap_or_else(|e| panic!("{e}"));
        assert!(started.elapsed().as_secs() < 30, "{:?}", started.elapsed());
        assert_eq!(h.text, "topo\n");
        assert_eq!(h.warnings.len(), 1, "{:?}", h.warnings);
        assert!(parse_html(b"", "p", "s").is_err());
        assert_eq!(
            html_text("<p>Ol&aacute; <b>NF 12</b></p><p>R$ 10,00</p>"),
            "Olá NF 12\n\nR$ 10,00\n"
        );
    }
}
