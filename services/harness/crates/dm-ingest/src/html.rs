//! HTML pages and fragments (a statement saved from internet banking, an e-mail body): the
//! DOM is built by html5ever (through `scraper`), a browser-grade parser that never fails on
//! broken markup.
//!
//! Every `<table>` is a candidate grid (its own rows and cells; a nested table is a grid of its
//! own) and goes through the spreadsheet header detection and role inference; a table with no
//! header row is layout and stays in the text. The text is the page as a browser would copy
//! it: one line per block element, a blank line after paragraphs, no scripts, styles, head or
//! hidden elements, data tables left out. The DOM is walked iteratively (a deeply nested page
//! cannot exhaust the stack) and the input is cut at [`MAX_HTML`] bytes.

use std::collections::BTreeSet;

use dm_core::Table;
use ego_tree::iter::Edge;
use ego_tree::NodeId;
use encoding_rs::{Encoding, UTF_16BE, UTF_16LE, UTF_8, WINDOWS_1252};
use scraper::node::Element;
use scraper::{Html, Node};

use crate::grid::{clean_cell, table_from_rows};
use crate::IngestError;

/// Bytes of markup read; the rest of a larger page is cut (with a warning).
pub(crate) const MAX_HTML: usize = 8 << 20;
/// Element nesting read (estimated before parsing; see [`too_deep_at`]).
const MAX_NESTING: usize = 512;
/// Columns one cell may span.
const MAX_SPAN: usize = 64;
/// Cells kept per page.
const MAX_CELLS: usize = 500_000;

/// What an HTML page yielded.
#[derive(Debug, Default)]
pub struct HtmlParsed {
    /// Tables with a recognizable header.
    pub tables: Vec<Table>,
    /// The text of the page, one line per block.
    pub text: String,
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

#[derive(Default)]
struct GridAcc {
    order: usize,
    node: Option<NodeId>,
    rows: Vec<Vec<String>>,
    row: Option<Vec<String>>,
    cell: Option<(String, usize)>,
}

impl GridAcc {
    fn close_cell(&mut self, cells: &mut usize) {
        if let Some((text, span)) = self.cell.take() {
            let row = self.row.get_or_insert_with(Vec::new);
            if *cells + span <= MAX_CELLS {
                *cells += span;
                row.push(text);
                for _ in 1..span {
                    row.push(String::new());
                }
            }
        }
    }

    fn close_row(&mut self, cells: &mut usize) {
        self.close_cell(cells);
        if let Some(row) = self.row.take() {
            self.rows.push(row);
        }
    }
}

/// A table of the page: (document order, node, rows of cell texts).
type Grid = (usize, NodeId, Vec<Vec<String>>);

/// Every table of the document, in document order, and whether the cell limit cut them.
fn grids(doc: &Html) -> (Vec<Grid>, bool) {
    let mut stack: Vec<GridAcc> = Vec::new();
    let mut done = Vec::new();
    let mut opened = 0usize;
    let mut cells = 0usize;
    let mut skip: Option<NodeId> = None;
    for edge in doc.tree.root().traverse() {
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
                            "tr" => {
                                if let Some(t) = stack.last_mut() {
                                    t.close_row(&mut cells);
                                    t.row = Some(Vec::new());
                                }
                            }
                            "td" | "th" => {
                                if let Some(t) = stack.last_mut() {
                                    t.close_cell(&mut cells);
                                    let span = e
                                        .attr("colspan")
                                        .and_then(|s| s.trim().parse::<usize>().ok())
                                        .unwrap_or(1)
                                        .clamp(1, MAX_SPAN);
                                    t.cell = Some((String::new(), span));
                                }
                            }
                            "br" | "p" | "div" | "li" => {
                                if let Some((text, _)) =
                                    stack.last_mut().and_then(|t| t.cell.as_mut())
                                {
                                    text.push(' ');
                                }
                            }
                            _ => {}
                        }
                    }
                    Node::Text(t) => {
                        if let Some((text, _)) = stack.last_mut().and_then(|g| g.cell.as_mut()) {
                            if text.len() < crate::grid::MAX_CELL_CHARS * 4 {
                                text.push_str(t);
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
                    }
                    continue;
                }
                if let Node::Element(e) = node.value() {
                    match e.name() {
                        "td" | "th" => {
                            if let Some(t) = stack.last_mut() {
                                t.close_cell(&mut cells);
                            }
                        }
                        "tr" => {
                            if let Some(t) = stack.last_mut() {
                                t.close_row(&mut cells);
                            }
                        }
                        "table" => {
                            if let Some(mut t) = stack.pop() {
                                t.close_row(&mut cells);
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
    (done, cells >= MAX_CELLS)
}

fn ensure_newline(out: &mut String) {
    if !out.is_empty() && !out.ends_with('\n') {
        out.push('\n');
    }
}

/// The text of the page, skipping the subtrees in `leave_out` (data tables).
fn page_text(doc: &Html, leave_out: &BTreeSet<NodeId>) -> String {
    let mut out = String::new();
    let mut skip: Option<NodeId> = None;
    let mut pre = 0usize;
    for edge in doc.tree.root().traverse() {
        match edge {
            Edge::Open(node) => {
                if skip.is_some() {
                    continue;
                }
                match node.value() {
                    Node::Element(e) => {
                        let name = e.name();
                        if skipped(name) || hidden(e) || leave_out.contains(&node.id()) {
                            skip = Some(node.id());
                            continue;
                        }
                        if name == "br" {
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
                            let mut last_space = out.ends_with([' ', '\n']) || out.is_empty();
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
                    if paragraph(name) {
                        ensure_newline(&mut out);
                        out.push('\n');
                    } else if block(name) {
                        ensure_newline(&mut out);
                    }
                }
            }
        }
        if out.len() > crate::office::MAX_TEXT {
            break;
        }
    }
    // Lines trimmed, at most one blank line in a row, none at the start.
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

/// The text of an HTML fragment (an e-mail body), without tables detection.
pub(crate) fn html_text(markup: &str) -> String {
    let mut markup = &markup[..floor_char_boundary(markup, MAX_HTML)];
    if let Some(cut) = too_deep_at(markup, MAX_NESTING) {
        markup = &markup[..cut];
    }
    page_text(&Html::parse_document(markup), &BTreeSet::new())
}

/// Elements opened and closed implicitly by their neighbours (cells, rows, list items,
/// paragraphs), which never deepen the tree on their own, and void elements.
fn uncounted(name: &str) -> bool {
    matches!(
        name,
        "html"
            | "head"
            | "body"
            | "p"
            | "li"
            | "dt"
            | "dd"
            | "tr"
            | "td"
            | "th"
            | "thead"
            | "tbody"
            | "tfoot"
            | "option"
            | "optgroup"
            | "colgroup"
            | "caption"
            | "rb"
            | "rt"
            | "rp"
            | "rtc"
            | "area"
            | "base"
            | "br"
            | "col"
            | "embed"
            | "hr"
            | "img"
            | "input"
            | "link"
            | "meta"
            | "source"
            | "track"
            | "wbr"
            | "param"
            | "keygen"
            | "basefont"
            | "bgsound"
            | "frame"
    )
}

/// Elements whose content is raw text up to their end tag.
fn raw_text(name: &str) -> bool {
    matches!(
        name,
        "script"
            | "style"
            | "textarea"
            | "title"
            | "xmp"
            | "iframe"
            | "noembed"
            | "noframes"
            | "plaintext"
    )
}

/// The byte offset where the element nesting of `markup`, estimated in one linear pass,
/// first passes `max`; `None` when it never does.
///
/// The tree builder looks up the stack of open elements for most tags, so a page nested a
/// million levels deep costs it time quadratic in the depth: such a page is cut where it
/// gets too deep (browsers flatten the tree at 512 levels anyway). The estimate counts the
/// elements that only close explicitly; cells, rows, list items and tables close what was
/// left open inside them, as the tree builder does, so sloppy markup does not drift.
#[allow(clippy::many_single_char_names)]
fn too_deep_at(markup: &str, max: usize) -> Option<usize> {
    #[derive(PartialEq)]
    enum Mark {
        Table,
        Row,
        Cell,
        Item,
    }
    let b = markup.as_bytes();
    let mut depth = 0usize;
    let mut marks: Vec<(Mark, usize)> = Vec::new();
    let mut i = 0usize;
    while i < b.len() {
        let Some(off) = b[i..].iter().position(|c| *c == b'<') else {
            break;
        };
        let at = i + off;
        let rest = &b[at..];
        if rest.starts_with(b"<!--") {
            i = find(b, at + 4, b"-->").map_or(b.len(), |e| e + 3);
            continue;
        }
        if rest.starts_with(b"<!") || rest.starts_with(b"<?") {
            i = find(b, at + 2, b">").map_or(b.len(), |e| e + 1);
            continue;
        }
        let closing = rest.get(1) == Some(&b'/');
        let name_start = at + 1 + usize::from(closing);
        let name_len = b[name_start..]
            .iter()
            .take_while(|c| c.is_ascii_alphanumeric() || **c == b'-' || **c == b':')
            .count();
        if name_len == 0 || !b[name_start].is_ascii_alphabetic() {
            i = at + 1;
            continue;
        }
        let name = markup[name_start..name_start + name_len].to_ascii_lowercase();
        // The end of the tag, skipping quoted attribute values.
        let mut j = name_start + name_len;
        let mut quote: Option<u8> = None;
        while j < b.len() {
            match (quote, b[j]) {
                (None, b'"' | b'\'') => quote = Some(b[j]),
                (Some(q), c) if c == q => quote = None,
                (None, b'>') => break,
                _ => {}
            }
            j += 1;
        }
        i = (j + 1).min(b.len());
        let pop_to = |marks: &mut Vec<(Mark, usize)>, depth: &mut usize, target: &[Mark]| {
            while let Some((m, d)) = marks.last() {
                if *m == Mark::Table && !target.contains(&Mark::Table) {
                    break;
                }
                let done = target.contains(m);
                *depth = *d;
                marks.pop();
                if done {
                    break;
                }
            }
        };
        if closing {
            match name.as_str() {
                "td" | "th" => pop_to(&mut marks, &mut depth, &[Mark::Cell]),
                "tr" => pop_to(&mut marks, &mut depth, &[Mark::Row]),
                "li" => pop_to(&mut marks, &mut depth, &[Mark::Item]),
                "table" => pop_to(&mut marks, &mut depth, &[Mark::Table]),
                n if uncounted(n) || raw_text(n) => {}
                _ => {
                    while marks.last().is_some_and(|(m, _)| *m == Mark::Item) {
                        if let Some((_, d)) = marks.pop() {
                            depth = d;
                        }
                    }
                    depth = depth.saturating_sub(1);
                }
            }
            continue;
        }
        match name.as_str() {
            "td" | "th" => {
                if marks.last().is_some_and(|(m, _)| *m == Mark::Cell) {
                    pop_to(&mut marks, &mut depth, &[Mark::Cell]);
                }
                marks.push((Mark::Cell, depth));
            }
            "tr" => {
                if marks
                    .last()
                    .is_some_and(|(m, _)| matches!(m, Mark::Cell | Mark::Row))
                {
                    pop_to(&mut marks, &mut depth, &[Mark::Row]);
                }
                marks.push((Mark::Row, depth));
            }
            "li" => {
                if marks.last().is_some_and(|(m, _)| *m == Mark::Item) {
                    pop_to(&mut marks, &mut depth, &[Mark::Item]);
                }
                marks.push((Mark::Item, depth));
            }
            "table" => {
                marks.push((Mark::Table, depth));
                depth += 1;
            }
            n if uncounted(n) => {}
            n => {
                if raw_text(n) {
                    let end = format!("</{n}");
                    i = find_ignore_case(b, i, end.as_bytes()).unwrap_or(b.len());
                    continue;
                }
                depth += 1;
            }
        }
        if depth > max {
            return Some(at);
        }
    }
    None
}

fn find(hay: &[u8], from: usize, needle: &[u8]) -> Option<usize> {
    hay.get(from..)?
        .windows(needle.len())
        .position(|w| w == needle)
        .map(|p| from + p)
}

fn find_ignore_case(hay: &[u8], from: usize, needle: &[u8]) -> Option<usize> {
    hay.get(from..)?
        .windows(needle.len())
        .position(|w| w.eq_ignore_ascii_case(needle))
        .map(|p| from + p)
}

fn floor_char_boundary(s: &str, max: usize) -> usize {
    let mut end = s.len().min(max);
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    end
}

/// Parses an HTML page: data tables and the rest of the text.
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
    if let Some(cut) = too_deep_at(markup, MAX_NESTING) {
        markup = &markup[..cut];
        out.warnings.push(format!(
            "página HTML aninhada demais (mais de {MAX_NESTING} níveis): o restante não foi lido"
        ));
    }
    let doc = Html::parse_document(markup);
    let (grids, cut) = grids(&doc);
    if cut {
        out.warnings
            .push(format!("tabelas da página cortadas em {MAX_CELLS} células"));
    }
    let mut leave_out = BTreeSet::new();
    for (order, id, rows) in grids {
        let name = format!("{stem}:tabela {}", order + 1);
        if let Some(t) = table_from_rows(&name, source_id, rows) {
            out.tables.push(t);
            leave_out.insert(id);
        }
    }
    out.text = page_text(&doc, &leave_out);
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
    fn nesting_estimate_follows_implicit_closes() {
        // A long statement whose cells leave a <font> open: cells and rows close it.
        let mut sloppy = String::from("<html><body><table>");
        for i in 0..5_000 {
            sloppy.push_str(&format!("<tr><td><font face=arial>{i}<td><span>x"));
        }
        sloppy.push_str("</table><div><ul><li><b>a<li>b</ul></div>");
        assert_eq!(too_deep_at(&sloppy, 64), None);
        let script = format!("<script>{}</script><p>ok", "<div>".repeat(1000));
        assert_eq!(too_deep_at(&script, 64), None);
        let comment = format!("<!-- {} --><div title=\"a>b\">", "<div>".repeat(1000));
        assert_eq!(too_deep_at(&comment, 64), None);
        let deep = "<span>".repeat(100);
        assert_eq!(too_deep_at(&deep, 64), Some(64 * 6));
        assert_eq!(too_deep_at("<", 1), None);
        assert_eq!(too_deep_at("</", 1), None);
        assert_eq!(too_deep_at("<a b='", 0), Some(0));
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
