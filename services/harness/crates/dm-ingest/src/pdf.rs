//! PDF text layer, page by page.
//!
//! `lopdf` parses the file; this module interprets each page's content stream (text state,
//! text and graphics matrices, `Tj`/`TJ`/`'`/`"`, form XObjects) into positioned glyphs, using
//! the font's `/Widths` (or `/W` for CID fonts) for the advance and lopdf's encodings
//! (WinAnsi, Differences, ToUnicode CMaps) for the characters. Glyphs are rebuilt into lines
//! and the lines into cells (columns) by their gaps.
//!
//! Bank statements are read as a table: a line that opens with a date and ends with
//! amounts in pt-BR format (`1.234,56`, negatives as `-`, `( )` or a `D` suffix) becomes a row
//! with date, description, document number, amount in centavos (negative for debits) and
//! balance when present. Column headers (`Data`, `Histórico`, `Documento`, `Crédito`,
//! `Débito`, `Saldo`) place amounts in the right column; without a sign the balance
//! progression decides. Every other line becomes a `page_region` item with a box locator.
//!
//! A PDF without a text layer (scanned) yields a warning: OCR is not available yet. Memory is
//! bounded per page: content, glyphs and lines of one page are dropped before the next.

use std::collections::BTreeMap;

use chrono::NaiveDate;
use dm_core::{stable_id, Column, ColumnRole, Item, ItemKind, Locator, Money, Table, Value};
use dm_normalize::{fold_ascii, DateContext};
use indexmap::IndexMap;
use lopdf::content::Content;
use lopdf::Encoding;
use lopdf::{Dictionary, Document, LoadOptions, Object, ObjectId};

use crate::mentions::extract_mentions;
use crate::IngestError;

/// Pages read at most; the rest are reported.
const MAX_PAGES: usize = 2_000;
/// Decompressed content of one page (and of one font map), bytes.
const MAX_PAGE_CONTENT: usize = 32 * 1024 * 1024;
/// Any single stream while loading (object and xref streams), bytes.
const MAX_STREAM: usize = 64 * 1024 * 1024;
/// Glyphs kept per page.
const MAX_GLYPHS: usize = 250_000;
/// Nesting of form XObjects.
const MAX_FORM_DEPTH: usize = 4;

/// True when the bytes carry the PDF magic header.
pub fn is_pdf(bytes: &[u8]) -> bool {
    bytes.starts_with(b"%PDF")
}

/// What a PDF yielded.
#[derive(Debug, Default)]
pub struct PdfParsed {
    /// Bank statement rows, when the text has them.
    pub table: Option<Table>,
    /// Every other line, grouped in blocks, with page locators.
    pub items: Vec<Item>,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
    /// Pages in the document.
    pub pages: usize,
}

// ------------------------------------------------------------------ geometry and glyphs

#[derive(Clone, Copy, Debug, PartialEq)]
struct Matrix([f32; 6]);

impl Matrix {
    const IDENTITY: Matrix = Matrix([1.0, 0.0, 0.0, 1.0, 0.0, 0.0]);

    fn translate(tx: f32, ty: f32) -> Matrix {
        Matrix([1.0, 0.0, 0.0, 1.0, tx, ty])
    }

    /// `self × other` in PDF row-vector convention.
    #[allow(clippy::many_single_char_names)]
    fn mul(self, o: Matrix) -> Matrix {
        let [a, b, c, d, e, f] = self.0;
        let [oa, ob, oc, od, oe, of] = o.0;
        Matrix([
            a * oa + b * oc,
            a * ob + b * od,
            c * oa + d * oc,
            c * ob + d * od,
            e * oa + f * oc + oe,
            e * ob + f * od + of,
        ])
    }

    #[allow(clippy::many_single_char_names)]
    fn apply(self, x: f32, y: f32) -> (f32, f32) {
        let [a, b, c, d, e, f] = self.0;
        (x * a + y * c + e, x * b + y * d + f)
    }

    fn x_scale(self) -> f32 {
        self.0[0].hypot(self.0[1])
    }

    fn y_scale(self) -> f32 {
        self.0[2].hypot(self.0[3])
    }
}

#[derive(Clone, Copy, Debug)]
struct Glyph {
    x: f32,
    y: f32,
    w: f32,
    size: f32,
    ch: char,
}

/// Helvetica advance widths (Adobe AFM) for ASCII 32..=126, used when a standard font comes
/// without `/Widths`.
const HELVETICA: [u16; 95] = [
    278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, 556, 556, 556,
    556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, 1015, 667, 667, 722, 722, 667,
    611, 778, 722, 278, 500, 667, 556, 833, 722, 778, 667, 778, 722, 667, 611, 722, 667, 944, 667,
    667, 611, 278, 278, 278, 469, 556, 333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500,
    222, 833, 556, 556, 556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
];

fn approx_width(ch: char, mono: bool) -> f32 {
    if mono {
        return 600.0;
    }
    let base = dm_core::table::fold_latin(ch).unwrap_or(ch);
    let code = base as u32;
    if (32..=126).contains(&code) {
        f32::from(HELVETICA[(code - 32) as usize])
    } else {
        556.0
    }
}

// ------------------------------------------------------------------ fonts

struct FontInfo<'a> {
    encoding: Option<Encoding<'a>>,
    two_byte: bool,
    first_char: i64,
    widths: Vec<f32>,
    cid_widths: Vec<(u32, u32, f32)>,
    default_width: f32,
    mono: bool,
}

fn num(obj: &Object) -> Option<f32> {
    obj.as_float().ok().filter(|v| v.is_finite())
}

fn deref<'a>(doc: &'a Document, obj: &'a Object) -> &'a Object {
    doc.dereference(obj).map_or(obj, |(_, o)| o)
}

fn dict_get<'a>(doc: &'a Document, dict: &'a Dictionary, key: &[u8]) -> Option<&'a Object> {
    dict.get(key).ok().map(|o| deref(doc, o))
}

fn array_of<'a>(doc: &'a Document, dict: &'a Dictionary, key: &[u8]) -> Vec<&'a Object> {
    match dict_get(doc, dict, key) {
        Some(Object::Array(items)) => items.iter().map(|o| deref(doc, o)).collect(),
        _ => Vec::new(),
    }
}

impl<'a> FontInfo<'a> {
    fn load(doc: &'a Document, font: &'a Dictionary, undecodable: &mut usize) -> FontInfo<'a> {
        let subtype = dict_get(doc, font, b"Subtype")
            .and_then(|o| o.as_name().ok())
            .unwrap_or_default();
        let base = dict_get(doc, font, b"BaseFont")
            .and_then(|o| o.as_name().ok())
            .map(|n| String::from_utf8_lossy(n).to_ascii_lowercase())
            .unwrap_or_default();
        let two_byte = subtype == b"Type0";
        let encoding = font
            .get_font_encoding_with_limit(doc, MAX_PAGE_CONTENT)
            .ok();
        if encoding.is_none() {
            *undecodable += 1;
        }
        let encoding = match encoding {
            // A CID font without a ToUnicode map falls back to a one-byte encoding in lopdf,
            // which would read every two-byte code as two wrong characters.
            Some(Encoding::UnicodeMapEncoding(m)) => Some(Encoding::UnicodeMapEncoding(m)),
            Some(_) if two_byte => None,
            other => other,
        };
        let mut info = FontInfo {
            encoding,
            two_byte,
            first_char: dict_get(doc, font, b"FirstChar")
                .and_then(|o| o.as_i64().ok())
                .unwrap_or(0),
            widths: array_of(doc, font, b"Widths")
                .into_iter()
                .map(|o| num(o).unwrap_or(0.0))
                .collect(),
            cid_widths: Vec::new(),
            default_width: if two_byte { 1000.0 } else { 0.0 },
            mono: base.contains("courier") || base.contains("mono"),
        };
        if two_byte {
            if let Some(descendant) = array_of(doc, font, b"DescendantFonts")
                .first()
                .and_then(|o| o.as_dict().ok())
            {
                if let Some(dw) = dict_get(doc, descendant, b"DW").and_then(num) {
                    info.default_width = dw;
                }
                info.cid_widths = cid_widths(&array_of(doc, descendant, b"W"));
            }
        }
        info
    }

    fn width(&self, code: u32, ch: char) -> f32 {
        if self.two_byte {
            return self
                .cid_widths
                .iter()
                .find(|(lo, hi, _)| (*lo..=*hi).contains(&code))
                .map_or(self.default_width, |(_, _, w)| *w);
        }
        let idx = i64::from(code) - self.first_char;
        usize::try_from(idx)
            .ok()
            .and_then(|i| self.widths.get(i))
            .copied()
            .filter(|w| *w > 0.0)
            .unwrap_or_else(|| approx_width(ch, self.mono))
    }

    fn decode(&self, code: &[u8]) -> Option<String> {
        match &self.encoding {
            Some(e) => e.bytes_to_string(code).ok(),
            None if !self.two_byte => Some(code.iter().map(|b| char::from(*b)).collect()),
            None => None,
        }
    }
}

/// `/W` array of a CID font: `c [w1 w2 ...]` or `c_first c_last w`.
fn cid_widths(w: &[&Object]) -> Vec<(u32, u32, f32)> {
    let mut out = Vec::new();
    let mut i = 0;
    while i < w.len() && out.len() < 65_536 {
        let Some(first) = w[i].as_i64().ok().and_then(|v| u32::try_from(v).ok()) else {
            break;
        };
        match w.get(i + 1) {
            Some(Object::Array(list)) => {
                for (k, v) in list.iter().enumerate() {
                    let code = first.saturating_add(u32::try_from(k).unwrap_or(u32::MAX));
                    out.push((code, code, num(v).unwrap_or(0.0)));
                }
                i += 2;
            }
            Some(last) => {
                let (Some(last), Some(width)) = (
                    last.as_i64().ok().and_then(|v| u32::try_from(v).ok()),
                    w.get(i + 2).and_then(|o| num(o)),
                ) else {
                    break;
                };
                out.push((first, last, width));
                i += 3;
            }
            None => break,
        }
    }
    out
}

// ------------------------------------------------------------------ content interpretation

#[derive(Clone, Copy)]
struct GState {
    ctm: Matrix,
    char_spacing: f32,
    word_spacing: f32,
    h_scale: f32,
    leading: f32,
    rise: f32,
    font: Option<usize>,
    font_size: f32,
}

impl Default for GState {
    fn default() -> Self {
        GState {
            ctm: Matrix::IDENTITY,
            char_spacing: 0.0,
            word_spacing: 0.0,
            h_scale: 1.0,
            leading: 0.0,
            rise: 0.0,
            font: None,
            font_size: 0.0,
        }
    }
}

/// Fonts and XObjects visible from a content stream.
struct Scope<'a> {
    fonts: BTreeMap<Vec<u8>, &'a Dictionary>,
    xobjects: BTreeMap<Vec<u8>, &'a Object>,
}

fn resource_dicts(doc: &Document, page: ObjectId) -> Vec<&Dictionary> {
    let Ok((direct, ids)) = doc.get_page_resources(page) else {
        return Vec::new();
    };
    direct
        .into_iter()
        .chain(ids.into_iter().filter_map(|id| doc.get_dictionary(id).ok()))
        .collect()
}

fn xobjects_of<'a>(
    doc: &'a Document,
    resources: &[&'a Dictionary],
) -> BTreeMap<Vec<u8>, &'a Object> {
    let mut out = BTreeMap::new();
    for res in resources {
        if let Some(Object::Dictionary(xo)) = dict_get(doc, res, b"XObject") {
            for (k, v) in xo {
                out.entry(k.clone()).or_insert(v);
            }
        }
    }
    out
}

fn fonts_of<'a>(
    doc: &'a Document,
    resources: &[&'a Dictionary],
) -> BTreeMap<Vec<u8>, &'a Dictionary> {
    let mut out = BTreeMap::new();
    for res in resources {
        if let Some(Object::Dictionary(fonts)) = dict_get(doc, res, b"Font") {
            for (k, v) in fonts {
                if let Ok(d) = deref(doc, v).as_dict() {
                    out.entry(k.clone()).or_insert(d);
                }
            }
        }
    }
    out
}

struct Interpreter<'a> {
    doc: &'a Document,
    fonts: Vec<FontInfo<'a>>,
    font_index: BTreeMap<usize, usize>,
    glyphs: Vec<Glyph>,
    truncated: bool,
    images: usize,
    undecodable_fonts: usize,
    undecodable_chars: usize,
}

impl<'a> Interpreter<'a> {
    fn font(&mut self, dict: &'a Dictionary) -> usize {
        let key = std::ptr::from_ref(dict) as usize;
        if let Some(i) = self.font_index.get(&key) {
            return *i;
        }
        let info = FontInfo::load(self.doc, dict, &mut self.undecodable_fonts);
        self.fonts.push(info);
        let i = self.fonts.len() - 1;
        self.font_index.insert(key, i);
        i
    }

    fn show(&mut self, gs: &GState, tm: &mut Matrix, bytes: &[u8]) {
        let Some(fi) = gs.font else {
            return;
        };
        let step = if self.fonts[fi].two_byte { 2 } else { 1 };
        for code_bytes in bytes.chunks(step) {
            let font = &self.fonts[fi];
            let code = code_bytes
                .iter()
                .fold(0u32, |acc, b| (acc << 8) | u32::from(*b));
            let text = font.decode(code_bytes);
            let first = text.as_deref().and_then(|t| t.chars().next());
            let w0 = font.width(code, first.unwrap_or(' ')) / 1000.0;
            let trm = Matrix([
                gs.font_size * gs.h_scale,
                0.0,
                0.0,
                gs.font_size,
                0.0,
                gs.rise,
            ])
            .mul(*tm)
            .mul(gs.ctm);
            let (x, y) = trm.apply(0.0, 0.0);
            let size = trm.y_scale();
            let width = w0 * trm.x_scale();
            match text {
                Some(t) => {
                    let n = t.chars().count().max(1);
                    for (k, ch) in t.chars().enumerate() {
                        if ch.is_control() || self.glyphs.len() >= MAX_GLYPHS {
                            self.truncated |= self.glyphs.len() >= MAX_GLYPHS;
                            continue;
                        }
                        let part = width / n as f32;
                        let gx = x + part * k as f32;
                        if gx.is_finite() && y.is_finite() && size.is_finite() {
                            self.glyphs.push(Glyph {
                                x: gx,
                                y,
                                w: part,
                                size,
                                ch,
                            });
                        }
                    }
                }
                None => self.undecodable_chars += 1,
            }
            let space = step == 1 && code == 32;
            let tx =
                (w0 * gs.font_size + gs.char_spacing + if space { gs.word_spacing } else { 0.0 })
                    * gs.h_scale;
            *tm = Matrix::translate(tx, 0.0).mul(*tm);
        }
    }

    fn run(&mut self, content: &[u8], scope: &Scope<'a>, base: GState, depth: usize) {
        let Ok(content) = Content::decode(content) else {
            return;
        };
        let mut gs = base;
        let mut stack: Vec<GState> = Vec::new();
        let mut tm = Matrix::IDENTITY;
        let mut tlm = Matrix::IDENTITY;
        for op in &content.operations {
            let o = &op.operands;
            let f = |i: usize| o.get(i).and_then(num);
            match op.operator.as_str() {
                "q" => {
                    if stack.len() < 256 {
                        stack.push(gs);
                    }
                }
                "Q" => {
                    if let Some(prev) = stack.pop() {
                        gs = prev;
                    }
                }
                "cm" => {
                    if let (Some(a), Some(b), Some(c), Some(d), Some(e), Some(ff)) =
                        (f(0), f(1), f(2), f(3), f(4), f(5))
                    {
                        gs.ctm = Matrix([a, b, c, d, e, ff]).mul(gs.ctm);
                    }
                }
                "BT" => {
                    tm = Matrix::IDENTITY;
                    tlm = Matrix::IDENTITY;
                }
                "Tf" => {
                    let font = o
                        .first()
                        .and_then(|n| n.as_name().ok())
                        .and_then(|n| scope.fonts.get(n).copied());
                    gs.font = font.map(|d| self.font(d));
                    gs.font_size = f(1).unwrap_or(0.0);
                }
                "Tc" => gs.char_spacing = f(0).unwrap_or(0.0),
                "Tw" => gs.word_spacing = f(0).unwrap_or(0.0),
                "Tz" => gs.h_scale = f(0).unwrap_or(100.0) / 100.0,
                "TL" => gs.leading = f(0).unwrap_or(0.0),
                "Ts" => gs.rise = f(0).unwrap_or(0.0),
                "Td" | "TD" => {
                    let (tx, ty) = (f(0).unwrap_or(0.0), f(1).unwrap_or(0.0));
                    if op.operator == "TD" {
                        gs.leading = -ty;
                    }
                    tlm = Matrix::translate(tx, ty).mul(tlm);
                    tm = tlm;
                }
                "Tm" => {
                    if let (Some(a), Some(b), Some(c), Some(d), Some(e), Some(ff)) =
                        (f(0), f(1), f(2), f(3), f(4), f(5))
                    {
                        tlm = Matrix([a, b, c, d, e, ff]);
                        tm = tlm;
                    }
                }
                "T*" => {
                    tlm = Matrix::translate(0.0, -gs.leading).mul(tlm);
                    tm = tlm;
                }
                "Tj" => {
                    if let Some(Ok(s)) = o.first().map(Object::as_str) {
                        self.show(&gs, &mut tm, s);
                    }
                }
                "'" | "\"" => {
                    let s = if op.operator == "\"" {
                        gs.word_spacing = f(0).unwrap_or(gs.word_spacing);
                        gs.char_spacing = f(1).unwrap_or(gs.char_spacing);
                        o.get(2)
                    } else {
                        o.first()
                    };
                    tlm = Matrix::translate(0.0, -gs.leading).mul(tlm);
                    tm = tlm;
                    if let Some(Ok(s)) = s.map(Object::as_str) {
                        self.show(&gs, &mut tm, s);
                    }
                }
                "TJ" => {
                    if let Some(Ok(items)) = o.first().map(Object::as_array) {
                        for item in items {
                            match item {
                                Object::String(s, _) => self.show(&gs, &mut tm, s),
                                other => {
                                    if let Some(n) = num(other) {
                                        let tx = -n / 1000.0 * gs.font_size * gs.h_scale;
                                        tm = Matrix::translate(tx, 0.0).mul(tm);
                                    }
                                }
                            }
                        }
                    }
                }
                "BI" | "ID" => self.images += 1,
                "Do" => {
                    if let Some(name) = o.first().and_then(|n| n.as_name().ok()) {
                        self.xobject(name, scope, &gs, depth);
                    }
                }
                _ => {}
            }
        }
    }

    fn xobject(&mut self, name: &[u8], scope: &Scope<'a>, gs: &GState, depth: usize) {
        let Some(obj) = scope.xobjects.get(name) else {
            return;
        };
        let Ok(stream) = deref(self.doc, obj).as_stream() else {
            return;
        };
        let subtype = dict_get(self.doc, &stream.dict, b"Subtype")
            .and_then(|o| o.as_name().ok())
            .unwrap_or_default();
        if subtype == b"Image" {
            self.images += 1;
            return;
        }
        if subtype != b"Form" || depth >= MAX_FORM_DEPTH {
            return;
        }
        let Ok(content) = stream.get_plain_content_with_limit(MAX_PAGE_CONTENT) else {
            return;
        };
        let matrix: Vec<f32> = array_of(self.doc, &stream.dict, b"Matrix")
            .into_iter()
            .filter_map(num)
            .collect();
        let mut inner = *gs;
        if let [a, b, c, d, e, f] = matrix[..] {
            inner.ctm = Matrix([a, b, c, d, e, f]).mul(gs.ctm);
        }
        let own: Vec<&Dictionary> = dict_get(self.doc, &stream.dict, b"Resources")
            .and_then(|o| o.as_dict().ok())
            .into_iter()
            .collect();
        let mut fonts = fonts_of(self.doc, &own);
        for (k, v) in &scope.fonts {
            fonts.entry(k.clone()).or_insert(*v);
        }
        let mut xobjects = xobjects_of(self.doc, &own);
        for (k, v) in &scope.xobjects {
            xobjects.entry(k.clone()).or_insert(*v);
        }
        let child = Scope { fonts, xobjects };
        self.run(&content, &child, inner, depth + 1);
    }
}

fn media_box(doc: &Document, page: ObjectId) -> [f32; 4] {
    let mut id = Some(page);
    for _ in 0..32 {
        let Some(dict) = id.and_then(|i| doc.get_dictionary(i).ok()) else {
            break;
        };
        let values: Vec<f32> = array_of(doc, dict, b"MediaBox")
            .into_iter()
            .filter_map(num)
            .collect();
        if let [x0, y0, x1, y1] = values[..] {
            if (x1 - x0).abs() > 1.0 && (y1 - y0).abs() > 1.0 {
                return [x0.min(x1), y0.min(y1), x0.max(x1), y0.max(y1)];
            }
        }
        id = dict.get(b"Parent").ok().and_then(|p| p.as_reference().ok());
    }
    [0.0, 0.0, 612.0, 792.0]
}

// ------------------------------------------------------------------ lines and cells

#[derive(Clone, Debug)]
struct Cell {
    x0: f32,
    x1: f32,
    text: String,
}

#[derive(Clone, Debug)]
struct Line {
    page: u32,
    /// Baseline, page units from the top of the page.
    top: f32,
    size: f32,
    x0: f32,
    x1: f32,
    cells: Vec<Cell>,
}

impl Line {
    fn text(&self) -> String {
        self.cells
            .iter()
            .map(|c| c.text.as_str())
            .collect::<Vec<_>>()
            .join(" ")
    }
}

/// Splits a cell on runs of two or more spaces (monospaced layouts pad columns with blanks).
fn split_padded(cell: Cell, size: f32) -> Vec<Cell> {
    if !cell.text.contains("  ") {
        return vec![cell];
    }
    let total = cell.text.chars().count().max(1);
    let per_char = ((cell.x1 - cell.x0) / total as f32).max(size * 0.3);
    let mut out = Vec::new();
    let mut start: Option<usize> = None;
    let chars: Vec<char> = cell.text.chars().collect();
    let mut i = 0;
    let push = |out: &mut Vec<Cell>, s: usize, e: usize| {
        let text: String = chars[s..e].iter().collect::<String>().trim().to_string();
        if !text.is_empty() {
            out.push(Cell {
                x0: cell.x0 + per_char * s as f32,
                x1: cell.x0 + per_char * e as f32,
                text,
            });
        }
    };
    while i < chars.len() {
        if chars[i] == ' ' && chars.get(i + 1) == Some(&' ') {
            if let Some(s) = start.take() {
                push(&mut out, s, i);
            }
            while i < chars.len() && chars[i] == ' ' {
                i += 1;
            }
            continue;
        }
        if start.is_none() && chars[i] != ' ' {
            start = Some(i);
        }
        i += 1;
    }
    if let Some(s) = start {
        push(&mut out, s, chars.len());
    }
    out
}

fn build_lines(mut glyphs: Vec<Glyph>, page: u32, mbox: [f32; 4]) -> Vec<Line> {
    let height = mbox[3] - mbox[1];
    glyphs.retain(|g| g.size >= 1.0 && g.size < 500.0);
    for g in &mut glyphs {
        g.x -= mbox[0];
        g.y -= mbox[1];
    }
    glyphs.sort_by(|a, b| b.y.total_cmp(&a.y).then(a.x.total_cmp(&b.x)));
    let mut rows: Vec<Vec<Glyph>> = Vec::new();
    for g in glyphs {
        match rows.last_mut() {
            Some(row) if (row[0].y - g.y).abs() <= 0.5 * row[0].size.max(g.size) => row.push(g),
            _ => rows.push(vec![g]),
        }
    }
    let mut lines = Vec::new();
    for mut row in rows {
        row.sort_by(|a, b| a.x.total_cmp(&b.x));
        // Fake bold: the same glyph drawn twice at almost the same place.
        row.dedup_by(|b, a| a.ch == b.ch && (a.x - b.x).abs() < 0.15 * a.size);
        let size = row.iter().map(|g| g.size).fold(0.0f32, f32::max);
        let mut cells: Vec<Cell> = Vec::new();
        let mut prev_end = f32::MIN;
        let mut prev_space = true;
        for g in &row {
            let gap = g.x - prev_end;
            let new_cell = cells.is_empty() || gap > 1.0 * size;
            if new_cell {
                cells.push(Cell {
                    x0: g.x,
                    x1: g.x + g.w,
                    text: String::new(),
                });
            } else if gap > 0.18 * g.size && !prev_space && g.ch != ' ' {
                if let Some(c) = cells.last_mut() {
                    c.text.push(' ');
                }
            }
            if let Some(c) = cells.last_mut() {
                if !(g.ch == ' ' && c.text.is_empty()) {
                    c.text.push(g.ch);
                }
                c.x1 = c.x1.max(g.x + g.w);
            }
            prev_space = g.ch == ' ';
            prev_end = prev_end.max(g.x + g.w);
        }
        let cells: Vec<Cell> = cells
            .into_iter()
            .flat_map(|c| split_padded(c, size))
            .map(|mut c| {
                c.text = c.text.trim().to_string();
                c
            })
            .filter(|c| !c.text.is_empty())
            .collect();
        if cells.is_empty() {
            continue;
        }
        let x0 = cells.iter().map(|c| c.x0).fold(f32::MAX, f32::min);
        let x1 = cells.iter().map(|c| c.x1).fold(f32::MIN, f32::max);
        lines.push(Line {
            page,
            top: height - row[0].y,
            size,
            x0,
            x1,
            cells,
        });
    }
    lines
}

// ------------------------------------------------------------------ statement rows

/// Day, month and (when written) year of a statement date.
type PartialDate = (u32, u32, Option<i32>);

/// `dd/mm/aaaa`, `dd/mm/aa` or `dd/mm` at the start of a text; the rest of the text.
fn leading_date(text: &str) -> Option<(PartialDate, &str)> {
    let token_end = text.find(char::is_whitespace).unwrap_or(text.len());
    let token = &text[..token_end];
    let parts: Vec<&str> = token.split(['/', '.']).collect();
    let digits = |s: &str, n: usize| s.len() == n && s.chars().all(|c| c.is_ascii_digit());
    let (d, m, y) = match parts[..] {
        [d, m] if digits(d, 2) && digits(m, 2) => (d, m, None),
        [d, m, y] if digits(d, 2) && digits(m, 2) && (digits(y, 4) || digits(y, 2)) => {
            (d, m, Some(y))
        }
        _ => return None,
    };
    let day: u32 = d.parse().ok()?;
    let month: u32 = m.parse().ok()?;
    if !(1..=31).contains(&day) || !(1..=12).contains(&month) {
        return None;
    }
    let year = match y {
        Some(y) if y.len() == 2 => Some(2000 + y.parse::<i32>().ok()?),
        Some(y) => Some(y.parse().ok()?),
        None => None,
    };
    Some(((day, month, year), text[token_end..].trim_start()))
}

/// Sign written next to an amount.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Sign {
    Credit,
    Debit,
    None,
}

/// A pt-BR amount cell: `1.234,56`, `R$ 1.234,56`, `-1.234,56`, `1.234,56-`, `(1.234,56)`,
/// `1.234,56 D`, `1.234,56C`. Returns centavos (absolute) and the written sign.
fn parse_amount(cell: &str) -> Option<(i64, Sign)> {
    let mut s = cell.trim().replace("R$", "");
    s = s.trim().to_string();
    let mut sign = Sign::None;
    if let Some(rest) = s.strip_prefix('(').and_then(|r| r.strip_suffix(')')) {
        s = rest.trim().to_string();
        sign = Sign::Debit;
    }
    if let Some(last) = s.chars().last() {
        let tail = match last {
            'D' | 'd' | '-' => Some(Sign::Debit),
            'C' | 'c' | '+' => Some(Sign::Credit),
            '*' => Some(Sign::None),
            _ => None,
        };
        if let Some(t) = tail {
            s.pop();
            s = s.trim_end().to_string();
            if t != Sign::None {
                sign = t;
            }
        }
    }
    if let Some(rest) = s.strip_prefix('-') {
        s = rest.trim().to_string();
        sign = Sign::Debit;
    } else if let Some(rest) = s.strip_prefix('+') {
        s = rest.trim().to_string();
        sign = Sign::Credit;
    }
    let (int, frac) = s.split_once(',')?;
    if frac.len() != 2 || !frac.chars().all(|c| c.is_ascii_digit()) || int.is_empty() {
        return None;
    }
    let groups: Vec<&str> = int.split('.').collect();
    let grouped_ok = groups.len() > 1
        && (1..=3).contains(&groups[0].len())
        && groups[1..].iter().all(|g| g.len() == 3);
    if !(grouped_ok || groups.len() == 1)
        || !groups
            .iter()
            .all(|g| !g.is_empty() && g.chars().all(|c| c.is_ascii_digit()))
    {
        return None;
    }
    let digits: String = groups.concat();
    if digits.len() > 15 {
        return None;
    }
    let reais: i64 = digits.parse().ok()?;
    let cents: i64 = frac.parse().ok()?;
    Some((reais.checked_mul(100)?.checked_add(cents)?, sign))
}

fn sign_cell(text: &str) -> Option<Sign> {
    match text.trim() {
        "C" | "c" | "+" | "CR" => Some(Sign::Credit),
        "D" | "d" | "-" | "DB" => Some(Sign::Debit),
        _ => None,
    }
}

fn is_document_like(text: &str) -> bool {
    let digits = text.chars().filter(char::is_ascii_digit).count();
    digits >= 3
        && text
            .chars()
            .all(|c| c.is_ascii_digit() || matches!(c, '.' | '-' | '/'))
}

fn is_balance_or_total(folded: &str) -> bool {
    let squashed: String = folded.chars().filter(|c| *c != ' ').collect();
    squashed.starts_with("SALDO")
        || squashed.starts_with("TOTAL")
        || squashed.starts_with("SUBTOTAL")
        || squashed.starts_with("LIMITE")
        || folded.contains("SALDO ANTERIOR")
        || folded.contains("SALDO DO DIA")
        || folded.contains("SALDO FINAL")
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum HeaderCol {
    Date,
    Description,
    Document,
    Value,
    Credit,
    Debit,
    Balance,
    Side,
}

fn header_word(folded: &str) -> Option<HeaderCol> {
    let w = folded.trim().trim_end_matches("(R$)").trim();
    Some(match w {
        "DATA" | "DT" | "DATA MOV" | "DATA LANCAMENTO" | "DATA DO LANCAMENTO" => HeaderCol::Date,
        "HISTORICO" | "DESCRICAO" | "LANCAMENTO" | "LANCAMENTOS" | "HISTORICO DO LANCAMENTO" => {
            HeaderCol::Description
        }
        "DOCUMENTO" | "DOC" | "DOCTO" | "N DOC" | "NR DOC" | "NO DOC" | "N DOCUMENTO" => {
            HeaderCol::Document
        }
        "VALOR" | "VALOR R$" | "VALORES" => HeaderCol::Value,
        "CREDITO" | "CREDITOS" | "ENTRADAS" | "ENTRADA" => HeaderCol::Credit,
        "DEBITO" | "DEBITOS" | "SAIDAS" | "SAIDA" => HeaderCol::Debit,
        "SALDO" | "SALDO R$" => HeaderCol::Balance,
        "D/C" | "C/D" => HeaderCol::Side,
        _ => return None,
    })
}

fn header_of(line: &Line) -> Option<Vec<(HeaderCol, f32, f32)>> {
    let cols: Vec<(HeaderCol, f32, f32)> = line
        .cells
        .iter()
        .filter_map(|c| header_word(&fold_ascii(&c.text)).map(|h| (h, c.x0, c.x1)))
        .collect();
    let has = |h: HeaderCol| cols.iter().any(|(c, _, _)| *c == h);
    let money = has(HeaderCol::Value) || has(HeaderCol::Credit) || has(HeaderCol::Debit);
    (cols.len() >= 3 && has(HeaderCol::Date) && money).then_some(cols)
}

/// The header column a cell falls under: most horizontal overlap, then nearest centre.
fn column_of(
    header: &[(HeaderCol, f32, f32)],
    cell: &Cell,
    allowed: &[HeaderCol],
) -> Option<HeaderCol> {
    let centre = f32::midpoint(cell.x0, cell.x1);
    header
        .iter()
        .filter(|(h, _, _)| allowed.contains(h))
        .map(|(h, x0, x1)| {
            let overlap = (cell.x1.min(*x1) - cell.x0.max(*x0)).max(0.0);
            let distance = (centre - f32::midpoint(*x0, *x1)).abs();
            (*h, overlap, distance)
        })
        .max_by(|a, b| a.1.total_cmp(&b.1).then(b.2.total_cmp(&a.2)))
        .map(|(h, _, _)| h)
}

#[derive(Clone, Debug)]
struct Row {
    page: u32,
    date: (u32, u32, Option<i32>),
    description: String,
    document: String,
    amount: i64,
    sign: Sign,
    balance: Option<i64>,
    /// Balance before this row, from a "saldo anterior" line.
    opening: Option<i64>,
    line: Line,
    desc_x0: f32,
}

#[derive(Default)]
struct Statement {
    header: Option<Vec<(HeaderCol, f32, f32)>>,
    rows: Vec<Row>,
    current_year: Option<i32>,
    first_year: Option<i32>,
    last_balance: Option<i64>,
    last_date: Option<(u32, u32, Option<i32>)>,
}

/// Lines a page did not turn into rows, kept for items.
type Leftover = Vec<Line>;

fn signed(amount: i64, sign: Sign) -> i64 {
    if sign == Sign::Debit {
        -amount
    } else {
        amount
    }
}

impl Statement {
    fn see_years(&mut self, line: &Line) {
        for cell in &line.cells {
            for token in cell.text.split_whitespace() {
                if let Some(((_, _, Some(y)), _)) = leading_date(token) {
                    self.current_year = Some(y);
                    self.first_year.get_or_insert(y);
                }
            }
        }
    }

    /// Amount cells from the right, with their signs and columns.
    fn amounts(&self, cells: &[Cell]) -> (Vec<(i64, Sign, Option<HeaderCol>)>, usize) {
        let mut out = Vec::new();
        let mut end = cells.len();
        let mut pending_sign: Option<Sign> = None;
        while end > 0 {
            let cell = &cells[end - 1];
            if pending_sign.is_none() {
                if let Some(s) = sign_cell(&cell.text) {
                    if end >= 2 && parse_amount(&cells[end - 2].text).is_some() {
                        pending_sign = Some(s);
                        end -= 1;
                        continue;
                    }
                }
            }
            let Some((value, mut sign)) = parse_amount(&cell.text) else {
                break;
            };
            if let Some(s) = pending_sign.take() {
                sign = s;
            }
            let col = self.header.as_ref().and_then(|h| {
                column_of(
                    h,
                    cell,
                    &[
                        HeaderCol::Value,
                        HeaderCol::Credit,
                        HeaderCol::Debit,
                        HeaderCol::Balance,
                    ],
                )
            });
            out.push((value, sign, col));
            end -= 1;
            if out.len() == 3 {
                break;
            }
        }
        out.reverse();
        (out, end)
    }

    /// Tries to read the line as a statement row.
    fn row(&mut self, line: &Line) -> Option<Row> {
        let first = line.cells.first()?;
        let (date, rest_of_first) = match leading_date(&first.text) {
            Some((d, rest)) => (Some(d), rest.to_string()),
            None => (None, first.text.clone()),
        };
        let (amounts, end) = self.amounts(&line.cells);
        if amounts.is_empty() {
            return None;
        }
        let mut middle: Vec<Cell> = Vec::new();
        let date_cell = date.is_some();
        if !rest_of_first.is_empty() && end > 0 {
            middle.push(Cell {
                x0: first.x0,
                x1: first.x1,
                text: rest_of_first,
            });
        }
        if end > 1 {
            middle.extend(line.cells[1..end].iter().cloned());
        } else if end == 0 {
            // The first cell itself was an amount: no date, no description.
            return None;
        }
        let description_all = middle
            .iter()
            .map(|c| c.text.as_str())
            .collect::<Vec<_>>()
            .join(" ");
        let folded = fold_ascii(&description_all);
        let mut balance_line = is_balance_or_total(&folded);
        if !date_cell && description_all.is_empty() {
            balance_line = true;
        }
        // Amount roles: by header column when there is one, else value then balance.
        let mut value: Option<(i64, Sign)> = None;
        let mut balance: Option<i64> = None;
        if self.header.is_some() {
            for (v, s, col) in &amounts {
                match col {
                    Some(HeaderCol::Balance) => balance = Some(signed(*v, *s)),
                    // A debit column holds debits unless the amount says otherwise.
                    Some(HeaderCol::Debit) => {
                        let sign = if *s == Sign::Credit {
                            Sign::Credit
                        } else {
                            Sign::Debit
                        };
                        value = Some((*v, sign));
                    }
                    Some(HeaderCol::Credit) => {
                        let sign = if *s == Sign::Debit {
                            Sign::Debit
                        } else {
                            Sign::Credit
                        };
                        value = Some((*v, sign));
                    }
                    _ => {
                        if value.is_none() {
                            value = Some((*v, *s));
                        } else {
                            balance = Some(signed(*v, *s));
                        }
                    }
                }
            }
        } else {
            let (v, s, _) = amounts[0];
            value = Some((v, s));
            if amounts.len() >= 2 {
                let (b, bs, _) = amounts[amounts.len() - 1];
                balance = Some(signed(b, bs));
            }
        }
        if balance_line {
            if let Some(b) = balance.or(value.map(|(v, s)| signed(v, s))) {
                self.last_balance = Some(b);
            }
            return None;
        }
        let (amount, sign) = value?;
        let date = if let Some(d) = date {
            d
        } else {
            // A dated row before, on a statement: the bank prints the date once per day.
            let last = self.last_date?;
            if self.header.is_none() {
                // Without a header, the line must start right of the date column.
                let date_x1 = self.rows.last()?.line.cells[0].x1;
                if line.x0 < date_x1 {
                    return None;
                }
            }
            last
        };
        // Document: by header column, else a numeric cell set apart from the description.
        let mut document = String::new();
        let mut desc_cells: Vec<&Cell> = Vec::new();
        for (i, c) in middle.iter().enumerate() {
            let by_header = self.header.as_ref().map(|h| {
                column_of(
                    h,
                    c,
                    &[HeaderCol::Description, HeaderCol::Document, HeaderCol::Date],
                ) == Some(HeaderCol::Document)
            });
            let apart =
                i > 0 && i == middle.len() - 1 && c.x0 - middle[i - 1].x1 >= 2.0 * line.size;
            let is_doc =
                document.is_empty() && is_document_like(&c.text) && by_header.unwrap_or(apart);
            if is_doc {
                document.clone_from(&c.text);
            } else {
                desc_cells.push(c);
            }
        }
        let description = desc_cells
            .iter()
            .map(|c| c.text.as_str())
            .collect::<Vec<_>>()
            .join(" ");
        let desc_x0 = desc_cells.first().map_or(line.x0, |c| c.x0);
        let date = (date.0, date.1, date.2.or(self.current_year));
        self.last_date = Some(date);
        let opening = self.last_balance.take();
        Some(Row {
            page: line.page,
            date,
            description,
            document,
            amount,
            sign,
            balance,
            opening,
            line: line.clone(),
            desc_x0,
        })
    }

    /// Reads the lines of one page; returns the lines that are not rows.
    fn page(&mut self, lines: Vec<Line>) -> Leftover {
        let mut left = Vec::new();
        let mut last_row_on_page: Option<usize> = None;
        for line in lines {
            if let Some(h) = header_of(&line) {
                self.header = Some(h);
                left.push(line);
                last_row_on_page = None;
                continue;
            }
            self.see_years(&line);
            if let Some(row) = self.row(&line) {
                self.rows.push(row);
                last_row_on_page = Some(self.rows.len() - 1);
                continue;
            }
            // Continuation of the previous row's description.
            if let Some(i) = last_row_on_page {
                let prev = &self.rows[i];
                let close = line.top - prev.line.top <= 1.6 * line.size.max(prev.line.size);
                let indented = line.x0 >= prev.desc_x0 - 0.5 * line.size
                    && line.x0 > prev.line.cells[0].x0 + 0.5 * line.size;
                let plain = !line.cells.iter().any(|c| parse_amount(&c.text).is_some())
                    && leading_date(&line.cells[0].text).is_none();
                if close && indented && plain {
                    let text = line.text();
                    let row = &mut self.rows[i];
                    row.description.push(' ');
                    row.description.push_str(&text);
                    row.line.top = line.top;
                    continue;
                }
            }
            last_row_on_page = None;
            left.push(line);
        }
        left
    }

    /// Rows with years and signs resolved, the statement year, and whether a column header
    /// was seen.
    fn finish(mut self) -> (Vec<Row>, Option<i32>, bool) {
        // Missing years: the statement's year.
        let year = self.first_year;
        for r in &mut self.rows {
            if r.date.2.is_none() {
                r.date.2 = year;
            }
        }
        // Unsigned amounts: the balance progression decides.
        let mut prev_balance: Option<i64> = None;
        for r in &mut self.rows {
            let before = r.opening.or(prev_balance);
            if r.sign == Sign::None {
                if let (Some(b0), Some(b1)) = (before, r.balance) {
                    if b1 - b0 == -r.amount {
                        r.sign = Sign::Debit;
                    } else if b1 - b0 == r.amount {
                        r.sign = Sign::Credit;
                    }
                }
            }
            if r.balance.is_some() {
                prev_balance = r.balance;
            } else if let Some(b0) = before {
                prev_balance = Some(b0 + signed(r.amount, r.sign));
            }
        }
        let header = self.header.is_some();
        (self.rows, year, header)
    }
}

fn statement_table(name: &str, source_id: &str, rows: &[Row]) -> (Table, usize) {
    let columns = vec![
        Column::new("Data", ColumnRole::Date).with_subrole("posting"),
        Column::new("Histórico", ColumnRole::FreeText).with_subrole("descriptor"),
        Column::new("Documento", ColumnRole::DocumentId),
        Column::new("Valor", ColumnRole::Amount),
        Column::new("Saldo", ColumnRole::Amount).with_subrole("balance"),
        Column::new("Tipo", ColumnRole::Kind).with_subrole("side"),
        Column::new("Página", ColumnRole::Unknown).with_subrole("page"),
    ];
    let mut table = Table::new(name, source_id, columns);
    let mut bad_dates = 0;
    for r in rows {
        let date = r
            .date
            .2
            .and_then(|y| NaiveDate::from_ymd_opt(y, r.date.1, r.date.0));
        let Some(date) = date else {
            bad_dates += 1;
            continue;
        };
        let amount = signed(r.amount, r.sign);
        let _ = table.push_row(vec![
            Value::Date(date),
            Value::from(r.description.as_str()),
            Value::from(r.document.as_str()),
            Value::Money(Money::from_centavos(amount)),
            r.balance
                .map_or(Value::Null, |b| Value::Money(Money::from_centavos(b))),
            Value::Text(if amount < 0 { "D" } else { "C" }.to_string()),
            Value::Integer(i64::from(r.page)),
        ]);
    }
    (table, bad_dates)
}

/// Groups leftover lines into blocks (paragraphs) and turns them into items.
fn items_from(
    lines: &[Line],
    page: u32,
    size: (f32, f32),
    source_id: &str,
    ctx: &DateContext,
    out: &mut Vec<Item>,
) {
    let (width, height) = size;
    let mut blocks: Vec<Vec<&Line>> = Vec::new();
    for line in lines {
        match blocks.last_mut() {
            Some(b)
                if line.top - b[b.len() - 1].top <= 2.0 * line.size.max(b[b.len() - 1].size) =>
            {
                b.push(line);
            }
            _ => blocks.push(vec![line]),
        }
    }
    for (i, block) in blocks.iter().enumerate() {
        let text = block
            .iter()
            .map(|l| l.text())
            .collect::<Vec<_>>()
            .join("\n");
        let x0 = block.iter().map(|l| l.x0).fold(f32::MAX, f32::min);
        let x1 = block.iter().map(|l| l.x1).fold(f32::MIN, f32::max);
        let top = block.first().map_or(0.0, |l| l.top - 0.8 * l.size);
        let bottom = block.last().map_or(0.0, |l| l.top + 0.25 * l.size);
        let norm = |v: f32, d: f32| {
            if d > 0.0 {
                (v / d).clamp(0.0, 1.0)
            } else {
                0.0
            }
        };
        let mut fields = IndexMap::new();
        fields.insert("text".to_string(), Value::Text(text.clone()));
        fields.insert("page".to_string(), Value::Integer(i64::from(page)));
        let mentions = extract_mentions(&text, ctx)
            .into_iter()
            .map(|m| m.to_entity_mention())
            .collect();
        out.push(Item {
            id: stable_id(
                "it",
                &[source_id, "page", &page.to_string(), &i.to_string()],
            ),
            kind: ItemKind::PageRegion,
            source_id: source_id.to_string(),
            locator: Locator::Page {
                page,
                bbox: [
                    norm(x0, width),
                    norm(top, height),
                    norm(x1, width),
                    norm(bottom, height),
                ],
            },
            fields,
            time: None,
            entity_mentions: mentions,
        });
    }
}

fn pages_list(pages: &[u32]) -> String {
    let shown: Vec<String> = pages.iter().take(10).map(ToString::to_string).collect();
    let mut s = shown.join(", ");
    if pages.len() > 10 {
        s.push_str(&format!(" e mais {}", pages.len() - 10));
    }
    s
}

/// Reads the text layer of a PDF: a bank statement table when the lines have dates and
/// amounts, items for the rest.
pub fn parse_pdf(bytes: &[u8], name: &str, source_id: &str) -> Result<PdfParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    if !is_pdf(bytes) {
        return Err(IngestError::Unrecognized("sem cabeçalho %PDF".to_string()));
    }
    let options = LoadOptions {
        max_decompressed_size: Some(MAX_STREAM),
        ..LoadOptions::default()
    };
    let mut doc =
        Document::load_mem_with_options(bytes, options).map_err(|e| IngestError::Parse {
            format: "pdf",
            detail: e.to_string(),
        })?;
    if doc.is_encrypted() && doc.decrypt("").is_err() {
        return Err(IngestError::Parse {
            format: "pdf",
            detail: "PDF protegido por senha".to_string(),
        });
    }
    let doc = doc;
    let pages = doc.get_pages();
    let mut out = PdfParsed {
        pages: pages.len(),
        ..PdfParsed::default()
    };
    let mut statement = Statement::default();
    let mut leftovers: Vec<(u32, (f32, f32), Leftover)> = Vec::new();
    let mut without_text: Vec<u32> = Vec::new();
    let mut undecodable_fonts = 0usize;
    let mut undecodable_chars = 0usize;
    let mut truncated_pages: Vec<u32> = Vec::new();
    let mut unreadable_pages: Vec<u32> = Vec::new();
    for (&number, &page_id) in pages.iter().take(MAX_PAGES) {
        let mbox = media_box(&doc, page_id);
        let resources = resource_dicts(&doc, page_id);
        let scope = Scope {
            fonts: fonts_of(&doc, &resources),
            xobjects: xobjects_of(&doc, &resources),
        };
        let Ok(content) = doc.get_page_content_with_limit(page_id, MAX_PAGE_CONTENT) else {
            unreadable_pages.push(number);
            continue;
        };
        let mut interp = Interpreter {
            doc: &doc,
            fonts: Vec::new(),
            font_index: BTreeMap::new(),
            glyphs: Vec::new(),
            truncated: false,
            images: 0,
            undecodable_fonts: 0,
            undecodable_chars: 0,
        };
        interp.run(&content, &scope, GState::default(), 0);
        drop(content);
        undecodable_fonts += interp.undecodable_fonts;
        undecodable_chars += interp.undecodable_chars;
        if interp.truncated {
            truncated_pages.push(number);
        }
        let images = interp.images;
        let glyphs = std::mem::take(&mut interp.glyphs);
        if glyphs.iter().all(|g| g.ch.is_whitespace()) {
            without_text.push(number);
            let _ = images;
            continue;
        }
        let lines = build_lines(glyphs, number, mbox);
        let left = statement.page(lines);
        leftovers.push((number, (mbox[2] - mbox[0], mbox[3] - mbox[1]), left));
    }
    let (rows, year, header) = statement.finish();
    // One or two dated amounts in an invoice or a letter are not a statement.
    let is_statement =
        !rows.is_empty() && (header || rows.len() >= 3 || rows.iter().any(|r| r.balance.is_some()));
    let ctx = DateContext {
        reference_year: year,
        reference_date: year.and_then(|y| NaiveDate::from_ymd_opt(y, 12, 31)),
        max_days_ahead: None,
    };
    if is_statement {
        let (table, bad) = statement_table(name, source_id, &rows);
        if bad > 0 {
            out.warnings.push(format!(
                "{bad} linha(s) do extrato sem ano na data foram ignoradas"
            ));
        }
        out.table = Some(table);
    } else {
        // Too few dated amounts to be a statement: they are plain text.
        for r in rows {
            if let Some(entry) = leftovers.iter_mut().find(|(p, _, _)| *p == r.page) {
                entry.2.push(r.line);
                entry.2.sort_by(|a, b| a.top.total_cmp(&b.top));
            }
        }
    }
    for (page, size, lines) in &leftovers {
        items_from(lines, *page, *size, source_id, &ctx, &mut out.items);
    }
    let read_pages = pages.len().min(MAX_PAGES);
    if pages.is_empty() {
        out.warnings.push("PDF sem páginas".to_string());
    } else if without_text.len() == read_pages {
        out.warnings.push(
            "PDF sem camada de texto (digitalizado ou só imagens): o OCR ainda não está disponível, nenhum conteúdo foi lido".to_string(),
        );
    } else if !without_text.is_empty() {
        out.warnings.push(format!(
            "página(s) sem texto, provavelmente digitalizada(s): {}; o OCR ainda não está disponível",
            pages_list(&without_text)
        ));
    }
    if pages.len() > MAX_PAGES {
        out.warnings.push(format!(
            "só as primeiras {MAX_PAGES} de {} páginas foram lidas",
            pages.len()
        ));
    }
    if !unreadable_pages.is_empty() {
        out.warnings.push(format!(
            "conteúdo ilegível na(s) página(s) {}",
            pages_list(&unreadable_pages)
        ));
    }
    if !truncated_pages.is_empty() {
        out.warnings.push(format!(
            "texto cortado em {MAX_GLYPHS} caracteres na(s) página(s) {}",
            pages_list(&truncated_pages)
        ));
    }
    if undecodable_chars > 0 || undecodable_fonts > 0 {
        out.warnings.push(format!(
            "{undecodable_chars} caractere(s) em fonte sem mapa Unicode (ToUnicode) não lido(s)"
        ));
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn signature_behaviour() {
        assert!(is_pdf(b"%PDF-1.4"));
        assert!(!is_pdf(b"PDF"));
        assert_eq!(parse_pdf(b"", "x", "s").unwrap_err(), IngestError::Empty);
        assert!(matches!(
            parse_pdf(b"nope", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        assert!(matches!(
            parse_pdf(b"%PDF-1.4\n", "x", "s"),
            Err(IngestError::Parse { .. })
        ));
    }

    #[test]
    fn amounts_in_every_brazilian_style() {
        assert_eq!(parse_amount("1.234,56"), Some((123_456, Sign::None)));
        assert_eq!(parse_amount("R$ 1.234,56"), Some((123_456, Sign::None)));
        assert_eq!(parse_amount("-980,00"), Some((98_000, Sign::Debit)));
        assert_eq!(parse_amount("980,00-"), Some((98_000, Sign::Debit)));
        assert_eq!(parse_amount("(980,00)"), Some((98_000, Sign::Debit)));
        assert_eq!(parse_amount("980,00 D"), Some((98_000, Sign::Debit)));
        assert_eq!(parse_amount("24.300,00 C"), Some((2_430_000, Sign::Credit)));
        assert_eq!(parse_amount("1234567,89"), Some((123_456_789, Sign::None)));
        assert_eq!(
            parse_amount("1.234.567,89 C"),
            Some((123_456_789, Sign::Credit))
        );
        assert_eq!(parse_amount("1.23,45"), None);
        assert_eq!(parse_amount("12,3"), None);
        assert_eq!(parse_amount("000098765001"), None);
        assert_eq!(parse_amount("05/07/2026"), None);
        assert_eq!(parse_amount("ABC,DE"), None);
        assert_eq!(parse_amount(",00"), None);
    }

    #[test]
    fn dates_and_documents() {
        assert_eq!(
            leading_date("05/07/2026 PIX RECEBIDO"),
            Some(((5, 7, Some(2026)), "PIX RECEBIDO"))
        );
        assert_eq!(leading_date("05/07 TARIFA"), Some(((5, 7, None), "TARIFA")));
        assert_eq!(leading_date("05/07/26"), Some(((5, 7, Some(2026)), "")));
        assert_eq!(leading_date("32/07/2026"), None);
        assert_eq!(leading_date("TARIFA"), None);
        assert!(is_document_like("000098765001"));
        assert!(is_document_like("123.456-7"));
        assert!(!is_document_like("NF 55120"));
        assert!(!is_document_like("12"));
        assert!(is_balance_or_total("SALDO ANTERIOR"));
        assert!(is_balance_or_total("S A L D O"));
        assert!(!is_balance_or_total("PIX RECEBIDO"));
    }

    fn line(page: u32, top: f32, cells: &[(f32, &str)]) -> Line {
        let cells: Vec<Cell> = cells
            .iter()
            .map(|(x, t)| Cell {
                x0: *x,
                x1: x + 5.0 * t.chars().count() as f32,
                text: (*t).to_string(),
            })
            .collect();
        Line {
            page,
            top,
            size: 9.0,
            x0: cells[0].x0,
            x1: cells.last().map_or(0.0, |c| c.x1),
            cells,
        }
    }

    #[test]
    fn statement_rows_with_header_columns_and_balance_signs() {
        let mut st = Statement::default();
        let lines = vec![
            line(1, 40.0, &[(40.0, "Período: 01/07/2026 a 31/07/2026")]),
            line(
                1,
                60.0,
                &[
                    (40.0, "Data"),
                    (100.0, "Histórico"),
                    (300.0, "Documento"),
                    (400.0, "Crédito"),
                    (460.0, "Débito"),
                    (520.0, "Saldo"),
                ],
            ),
            line(
                1,
                72.0,
                &[
                    (40.0, "01/07"),
                    (100.0, "SALDO ANTERIOR"),
                    (520.0, "1.000,00"),
                ],
            ),
            line(
                1,
                84.0,
                &[
                    (40.0, "10/07"),
                    (100.0, "LIQUIDACAO COBRANCA"),
                    (300.0, "000098765001"),
                    (400.0, "500,00"),
                    (520.0, "1.500,00"),
                ],
            ),
            line(1, 94.0, &[(100.0, "PAGADOR FULANO")]),
            line(
                1,
                106.0,
                &[
                    (40.0, "11/07"),
                    (100.0, "TARIFA PACOTE"),
                    (460.0, "20,00"),
                    (520.0, "1.480,00"),
                ],
            ),
            line(
                1,
                118.0,
                &[
                    (100.0, "PIX ENVIADO"),
                    (460.0, "80,00"),
                    (520.0, "1.400,00"),
                ],
            ),
            line(1, 200.0, &[(40.0, "Central de atendimento 0800 000 0000")]),
        ];
        let left = st.page(lines);
        let (rows, year, header) = st.finish();
        assert_eq!(year, Some(2026));
        assert!(header);
        assert_eq!(rows.len(), 3, "{rows:#?}");
        assert_eq!(rows[0].description, "LIQUIDACAO COBRANCA PAGADOR FULANO");
        assert_eq!(rows[0].document, "000098765001");
        assert_eq!((rows[0].amount, rows[0].sign), (50_000, Sign::Credit));
        assert_eq!(rows[0].balance, Some(150_000));
        assert_eq!(rows[1].sign, Sign::Debit);
        assert_eq!(
            rows[2].date,
            (11, 7, Some(2026)),
            "date carried from the row above"
        );
        assert_eq!(rows[2].sign, Sign::Debit);
        assert_eq!(
            left.len(),
            4,
            "period, header, saldo anterior and footer stay as text"
        );
    }

    #[test]
    fn statement_without_header_uses_suffixes_and_balance_delta() {
        let mut st = Statement::default();
        let lines = vec![
            line(
                1,
                50.0,
                &[
                    (40.0, "05/07/2026"),
                    (100.0, "SALDO ANTERIOR"),
                    (500.0, "100,00"),
                ],
            ),
            line(
                1,
                60.0,
                &[
                    (40.0, "05/07/2026"),
                    (100.0, "TED RECEBIDA"),
                    (400.0, "50,00"),
                    (500.0, "150,00"),
                ],
            ),
            line(
                1,
                70.0,
                &[
                    (40.0, "06/07/2026"),
                    (100.0, "TARIFA"),
                    (400.0, "10,00"),
                    (500.0, "140,00"),
                ],
            ),
            line(
                1,
                80.0,
                &[(40.0, "07/07/2026"), (100.0, "PIX"), (400.0, "5,00 D")],
            ),
        ];
        let _ = st.page(lines);
        let (rows, _, header) = st.finish();
        assert!(!header);
        let signs: Vec<Sign> = rows.iter().map(|r| r.sign).collect();
        assert_eq!(signs, vec![Sign::Credit, Sign::Debit, Sign::Debit]);
        let (table, bad) = statement_table("extrato", "src", &rows);
        assert_eq!(bad, 0);
        assert_eq!(
            table.value(1, 3),
            Some(&Value::Money(Money::from_centavos(-1_000)))
        );
        assert_eq!(table.value(1, 5).unwrap().as_text(), Some("D"));
        assert_eq!(table.value(2, 4), Some(&Value::Null));
    }

    #[test]
    fn padded_cells_split_on_double_spaces() {
        let cell = Cell {
            x0: 0.0,
            x1: 300.0,
            text: "05/07/2026  TARIFA    10,00".to_string(),
        };
        let parts: Vec<String> = split_padded(cell, 10.0)
            .into_iter()
            .map(|c| c.text)
            .collect();
        assert_eq!(parts, vec!["05/07/2026", "TARIFA", "10,00"]);
    }

    #[test]
    fn matrices_compose_like_pdf() {
        let m = Matrix::translate(10.0, 20.0).mul(Matrix([2.0, 0.0, 0.0, 2.0, 0.0, 0.0]));
        assert_eq!(m.apply(1.0, 1.0), (22.0, 42.0));
        assert!((Matrix([0.0, 3.0, -3.0, 0.0, 0.0, 0.0]).x_scale() - 3.0).abs() < 1e-6);
        assert_eq!(
            cid_widths(&[
                &Object::Integer(1),
                &Object::Integer(3),
                &Object::Integer(500)
            ]),
            vec![(1, 3, 500.0)]
        );
    }
}
