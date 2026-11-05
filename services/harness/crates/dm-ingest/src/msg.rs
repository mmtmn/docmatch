//! Outlook messages (`.msg`): a Compound File Binary (the `cfb` crate) holding MAPI
//! properties ([MS-OXMSG]).
//!
//! The message becomes the same items as an `.eml`: one `event` with sender, recipients,
//! date, subject and body, plus a `text_span` per body paragraph. The body is the plain text
//! property, else the HTML body (its text), else the compressed RTF body ([MS-OXRTFCP],
//! decompressed and stripped to text). Attachments are returned as files for the dispatcher,
//! which parses each as a source of its own, like the entries of a ZIP and under the same
//! limits; an attached message (`.msg` inside `.msg`) is read as a message of its own, but its
//! attachments are not opened (one level only). Inline images of the body are not
//! attachments. Every stream is read with a size cap.

use std::io::{Cursor, Read};

use chrono::{DateTime, NaiveDateTime};
use dm_core::{stable_id, EntityMention, Item, ItemKind, Locator, TimeRef, Value};
use dm_normalize::DateContext;
use encoding_rs::Encoding;
use indexmap::IndexMap;

use crate::archive::ZipLimits;
use crate::inbox::InboxContext;
use crate::mentions::extract_mentions;
use crate::IngestError;

type Cfb<'a> = cfb::CompoundFile<Cursor<&'a [u8]>>;

/// Short text properties (subject, names, addresses, ids, file names), bytes.
const MAX_SHORT_PROP: u64 = 8 << 10;
/// Characters kept of a header field (sender, recipients, subject, message id).
const MAX_FIELD_CHARS: usize = 4 << 10;
/// The transport headers, bytes.
const MAX_HEADERS_PROP: u64 = 64 << 10;
/// A body property (text, HTML or compressed RTF), bytes.
const MAX_BODY_PROP: u64 = 16 << 20;
/// The fixed-size property stream of one storage, bytes (a real one is a few kilobytes).
const MAX_PROPS: u64 = 64 << 10;
/// Characters of body text kept.
const MAX_BODY: usize = 8 << 20;
/// Bytes read from one `.msg` in all: twice the file (directory entries may share a chain)
/// and never more than this.
const MAX_READ: u64 = 64 << 20;
/// Recipients read per message.
const MAX_RECIPIENTS: usize = 200;
/// Attached messages read per message.
const MAX_EMBEDDED: usize = 50;

const PT_STRING8: u16 = 0x001E;
const PT_UNICODE: u16 = 0x001F;
const PT_BINARY: u16 = 0x0102;

const PR_SUBJECT: u16 = 0x0037;
const PR_CLIENT_SUBMIT_TIME: u16 = 0x0039;
const PR_SENT_REPRESENTING_NAME: u16 = 0x0042;
const PR_SENT_REPRESENTING_EMAIL: u16 = 0x0065;
const PR_TRANSPORT_HEADERS: u16 = 0x007D;
const PR_RECIPIENT_TYPE: u16 = 0x0C15;
const PR_SENDER_NAME: u16 = 0x0C1A;
const PR_SENDER_EMAIL: u16 = 0x0C1F;
const PR_NORMALIZED_SUBJECT: u16 = 0x0E1D;
const PR_DISPLAY_CC: u16 = 0x0E03;
const PR_DISPLAY_TO: u16 = 0x0E04;
const PR_DELIVERY_TIME: u16 = 0x0E06;
const PR_BODY: u16 = 0x1000;
const PR_RTF_COMPRESSED: u16 = 0x1009;
const PR_HTML: u16 = 0x1013;
const PR_INTERNET_MESSAGE_ID: u16 = 0x1035;
const PR_DISPLAY_NAME: u16 = 0x3001;
const PR_EMAIL_ADDRESS: u16 = 0x3003;
const PR_CREATION_TIME: u16 = 0x3007;
const PR_ATTACH_DATA: u16 = 0x3701;
const PR_ATTACH_FILENAME: u16 = 0x3704;
const PR_ATTACH_METHOD: u16 = 0x3705;
const PR_ATTACH_LONG_FILENAME: u16 = 0x3707;
const PR_ATTACH_MIME_TAG: u16 = 0x370E;
const PR_ATTACH_CONTENT_ID: u16 = 0x3712;
const PR_SMTP_ADDRESS: u16 = 0x39FE;
const PR_INTERNET_CPID: u16 = 0x3FDE;
const PR_MESSAGE_CODEPAGE: u16 = 0x3FFD;
const PR_SENDER_SMTP: u16 = 0x5D01;
const PR_SENT_REPRESENTING_SMTP: u16 = 0x5D02;
const PR_ATTACHMENT_HIDDEN: u16 = 0x7FFE;

/// The properties of one message that become an item.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct MessageProps {
    /// `Nome <endereço>` or the address.
    pub from: String,
    /// To recipients, `; `-separated.
    pub to: String,
    /// Cc recipients.
    pub cc: String,
    /// Subject.
    pub subject: String,
    /// When it was sent, local time: the sender's from the transport headers when present,
    /// else the submit time converted from UTC.
    pub date: Option<NaiveDateTime>,
    /// Internet message id.
    pub message_id: String,
    /// Body text.
    pub body: String,
    /// File names of the attachments.
    pub attachments: Vec<String>,
}

/// A file attached to a message.
#[derive(Debug)]
pub struct MsgAttachment {
    /// File name (no directories).
    pub name: String,
    /// Content.
    pub bytes: Vec<u8>,
}

/// A message attached to the message.
#[derive(Debug)]
pub struct EmbeddedMessage {
    /// File name given to it (`<subject>.msg`).
    pub name: String,
    /// Its properties.
    pub message: MessageProps,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
}

/// What an Outlook message yielded.
#[derive(Debug, Default)]
pub struct MsgParsed {
    /// The message.
    pub message: MessageProps,
    /// Files attached (not opened yet).
    pub attachments: Vec<MsgAttachment>,
    /// Messages attached.
    pub embedded: Vec<EmbeddedMessage>,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
}

/// True when the bytes are a Compound File whose root holds MAPI property streams.
pub fn is_msg(bytes: &[u8]) -> bool {
    let Ok(file) = cfb::CompoundFile::open(Cursor::new(bytes)) else {
        return false;
    };
    file.read_root_storage().any(|e| {
        let name = e.name();
        name.starts_with("__substg1.0_") || name == "__properties_version1.0"
    })
}

/// The Compound File of a message with a budget for every byte read from it: directory
/// entries may point at the same chain any number of times, so the caps per property alone
/// do not bound a file.
struct MsgFile<'a> {
    cfb: Cfb<'a>,
    left: u64,
    exhausted: bool,
}

/// The bytes one `.msg` may have read: twice the file, at most [`MAX_READ`].
fn read_budget(len: usize) -> u64 {
    (len as u64).saturating_mul(2).min(MAX_READ)
}

impl<'a> MsgFile<'a> {
    fn open(bytes: &'a [u8], budget: u64) -> std::io::Result<MsgFile<'a>> {
        let cfb = cfb::CompoundFile::open(Cursor::new(bytes))?;
        Ok(MsgFile {
            cfb,
            left: budget,
            exhausted: false,
        })
    }

    /// A stream, at most `cap` bytes and what is left of the budget.
    fn read(&mut self, path: &str, cap: u64) -> Option<Vec<u8>> {
        let mut stream = self.cfb.open_stream(path).ok()?;
        let want = cap.min(self.left);
        if stream.len() > want && want < cap {
            self.exhausted = true;
        }
        let mut buf = Vec::new();
        (&mut stream).take(want).read_to_end(&mut buf).ok()?;
        self.left = self.left.saturating_sub(buf.len() as u64);
        Some(buf)
    }

    /// Child storages of `base` whose names start with `prefix`, sorted.
    fn storages(&self, base: &str, prefix: &str) -> Vec<String> {
        let path = if base.is_empty() { "/" } else { base };
        let Ok(entries) = self.cfb.read_storage(path) else {
            return Vec::new();
        };
        let mut out: Vec<String> = entries
            .filter(|e| e.is_storage() && e.name().starts_with(prefix))
            .map(|e| format!("{base}/{}", e.name()))
            .collect();
        out.sort();
        out
    }
}

fn utf16le(bytes: &[u8]) -> String {
    let units: Vec<u16> = bytes
        .chunks_exact(2)
        .map(|c| u16::from_le_bytes([c[0], c[1]]))
        .collect();
    String::from_utf16_lossy(&units)
        .trim_end_matches('\0')
        .to_string()
}

/// Encoding of a Windows code page number.
fn codepage(cp: u32) -> &'static Encoding {
    let label = match cp {
        65001 => "utf-8".to_string(),
        1250..=1258 => format!("windows-{cp}"),
        28591..=28599 => format!("iso-8859-{}", cp - 28590),
        28605 => "iso-8859-15".to_string(),
        20127 => "us-ascii".to_string(),
        932 => "shift_jis".to_string(),
        936 => "gbk".to_string(),
        949 => "euc-kr".to_string(),
        950 => "big5".to_string(),
        _ => "windows-1252".to_string(),
    };
    Encoding::for_label(label.as_bytes()).unwrap_or(encoding_rs::WINDOWS_1252)
}

/// The encodings of 8-bit strings: the message code page for every property, the internet
/// code page for the body ([MS-OXCMSG]: PidTagMessageCodepage, PidTagInternetCodepage).
#[derive(Clone, Copy)]
struct Encodings {
    strings: &'static Encoding,
    body: &'static Encoding,
}

impl Default for Encodings {
    fn default() -> Self {
        Encodings {
            strings: encoding_rs::WINDOWS_1252,
            body: encoding_rs::WINDOWS_1252,
        }
    }
}

/// The properties of one storage (the message, a recipient, an attachment, an embedded
/// message), read on demand.
struct Props {
    base: String,
    fixed: IndexMap<u16, (u16, [u8; 8])>,
    enc: Encodings,
}

impl Props {
    /// `header` is the size of the property stream header: 32 for the top-level message,
    /// 24 for an embedded message, 8 for a recipient or an attachment, which inherit the
    /// code pages of their message (`inherited`).
    fn open(file: &mut MsgFile<'_>, base: &str, header: usize, inherited: Encodings) -> Props {
        let mut fixed = IndexMap::new();
        if let Some(data) = file.read(&format!("{base}/__properties_version1.0"), MAX_PROPS) {
            for entry in data.get(header..).unwrap_or_default().chunks_exact(16) {
                let kind = u16::from_le_bytes([entry[0], entry[1]]);
                let id = u16::from_le_bytes([entry[2], entry[3]]);
                let mut value = [0u8; 8];
                value.copy_from_slice(&entry[8..16]);
                fixed.insert(id, (kind, value));
            }
        }
        let mut p = Props {
            base: base.to_string(),
            fixed,
            enc: inherited,
        };
        if let Some(cp) = p.long(PR_MESSAGE_CODEPAGE) {
            p.enc.strings = codepage(cp);
            p.enc.body = p.enc.strings;
        }
        if let Some(cp) = p.long(PR_INTERNET_CPID) {
            p.enc.body = codepage(cp);
        }
        p
    }

    fn long(&self, id: u16) -> Option<u32> {
        self.fixed
            .get(&id)
            .map(|(_, v)| u32::from_le_bytes([v[0], v[1], v[2], v[3]]))
    }

    /// A time property (UTC on disk) in local time, as the `.eml` and text dumps have it.
    fn time(&self, id: u16) -> Option<NaiveDateTime> {
        let (_, v) = self.fixed.get(&id)?;
        let ticks = u64::from_le_bytes(*v);
        let secs = i64::try_from(ticks / 10_000_000).ok()? - 11_644_473_600;
        let nanos = u32::try_from((ticks % 10_000_000) * 100).ok()?;
        DateTime::from_timestamp(secs, nanos).and_then(crate::mail::local_time)
    }

    fn stream_name(&self, id: u16, kind: u16) -> String {
        format!("{}/__substg1.0_{id:04X}{kind:04X}", self.base)
    }

    fn string_in(
        &self,
        file: &mut MsgFile<'_>,
        id: u16,
        cap: u64,
        encoding: &'static Encoding,
    ) -> Option<String> {
        if let Some(b) = file.read(&self.stream_name(id, PT_UNICODE), cap) {
            return Some(utf16le(&b));
        }
        let b = file.read(&self.stream_name(id, PT_STRING8), cap)?;
        Some(
            encoding
                .decode_without_bom_handling(&b)
                .0
                .trim_end_matches('\0')
                .to_string(),
        )
    }

    /// A short text property, trimmed and cut at [`MAX_FIELD_CHARS`] (empty when absent).
    fn text(&self, file: &mut MsgFile<'_>, id: u16) -> String {
        field(&self.text_capped(file, id, MAX_SHORT_PROP))
    }

    fn text_capped(&self, file: &mut MsgFile<'_>, id: u16, cap: u64) -> String {
        self.string_in(file, id, cap, self.enc.strings)
            .map(|s| s.trim().to_string())
            .unwrap_or_default()
    }

    fn binary(&self, file: &mut MsgFile<'_>, id: u16, cap: u64) -> Option<Vec<u8>> {
        file.read(&self.stream_name(id, PT_BINARY), cap)
    }

    /// HTML bodies are bytes in the internet code page, unless they are valid UTF-8.
    fn html_body(&self, bytes: &[u8]) -> String {
        if let Ok(s) = std::str::from_utf8(bytes) {
            return s.to_string();
        }
        self.enc
            .body
            .decode_without_bom_handling(bytes)
            .0
            .into_owned()
    }
}

fn with_name(name: &str, address: &str) -> String {
    match (name.trim(), address.trim()) {
        ("", a) => a.to_string(),
        (n, "") => n.to_string(),
        (n, a) if n.eq_ignore_ascii_case(a) => a.to_string(),
        (n, a) => format!("{n} <{a}>"),
    }
}

/// An SMTP address, or nothing for an Exchange (`/O=...`) one.
fn smtp(s: String) -> String {
    if s.contains('@') {
        s
    } else {
        String::new()
    }
}

/// A header field: trimmed, at most [`MAX_FIELD_CHARS`] characters.
fn field(value: &str) -> String {
    let v = value.trim();
    match v.char_indices().nth(MAX_FIELD_CHARS) {
        Some((cut, _)) => v[..cut].trim_end().to_string(),
        None => v.to_string(),
    }
}

fn header_value(headers: &[mailparse::MailHeader<'_>], name: &str) -> Option<String> {
    headers
        .iter()
        .find(|h| h.get_key_ref().eq_ignore_ascii_case(name))
        .map(mailparse::MailHeader::get_value)
        .map(|v| field(&v))
        .filter(|v| !v.is_empty())
}

/// A message as read: its properties, its attachments listed once (and how many storages
/// there were) and the code pages its attachments inherit.
struct ReadMessage {
    props: MessageProps,
    listed: Vec<Listed>,
    attachment_count: usize,
    enc: Encodings,
}

/// Reads the message stored under `base` (empty for the top-level one) and lists its
/// attachments.
fn read_message(
    file: &mut MsgFile<'_>,
    base: &str,
    header: usize,
    inherited: Encodings,
    warnings: &mut Vec<String>,
) -> ReadMessage {
    let p = Props::open(file, base, header, inherited);
    let mut m = MessageProps {
        subject: p.text(file, PR_SUBJECT),
        message_id: p.text(file, PR_INTERNET_MESSAGE_ID),
        ..MessageProps::default()
    };
    if m.subject.is_empty() {
        m.subject = p.text(file, PR_NORMALIZED_SUBJECT);
    }
    let transport = p.text_capped(file, PR_TRANSPORT_HEADERS, MAX_HEADERS_PROP);
    let headers = mailparse::parse_headers(transport.as_bytes())
        .map(|(h, _)| h)
        .unwrap_or_default();
    // Sender: SMTP addresses first, Exchange-only senders fall back to the headers.
    let address = [
        PR_SENDER_SMTP,
        PR_SENT_REPRESENTING_SMTP,
        PR_SENDER_EMAIL,
        PR_SENT_REPRESENTING_EMAIL,
    ]
    .into_iter()
    .map(|id| smtp(p.text(file, id)))
    .find(|a| !a.is_empty())
    .unwrap_or_default();
    let name = [PR_SENDER_NAME, PR_SENT_REPRESENTING_NAME]
        .into_iter()
        .map(|id| p.text(file, id))
        .find(|n| !n.is_empty())
        .unwrap_or_default();
    m.from = if address.is_empty() {
        header_value(&headers, "From").unwrap_or_else(|| name.clone())
    } else {
        with_name(&name, &address)
    };
    if m.message_id.is_empty() {
        m.message_id = header_value(&headers, "Message-ID").unwrap_or_default();
    }
    // Recipients: their own storages, else the display lists.
    let mut to = Vec::new();
    let mut cc = Vec::new();
    let recipients = file.storages(base, "__recip_version1.0_#");
    if recipients.len() > MAX_RECIPIENTS {
        warnings.push(format!(
            "{} destinatários; só os primeiros {MAX_RECIPIENTS} foram lidos",
            recipients.len()
        ));
    }
    for storage in recipients.into_iter().take(MAX_RECIPIENTS) {
        let r = Props::open(file, &storage, 8, p.enc);
        let address = [PR_SMTP_ADDRESS, PR_EMAIL_ADDRESS]
            .into_iter()
            .map(|id| smtp(r.text(file, id)))
            .find(|a| !a.is_empty())
            .unwrap_or_default();
        let entry = with_name(&r.text(file, PR_DISPLAY_NAME), &address);
        if entry.is_empty() {
            continue;
        }
        match r.long(PR_RECIPIENT_TYPE).unwrap_or(1) & 0x0F {
            2 => cc.push(entry),
            3 => {}
            _ => to.push(entry),
        }
    }
    m.to = if to.is_empty() {
        p.text(file, PR_DISPLAY_TO)
    } else {
        field(&to.join("; "))
    };
    m.cc = if cc.is_empty() {
        p.text(file, PR_DISPLAY_CC)
    } else {
        field(&cc.join("; "))
    };
    m.date = header_value(&headers, "Date")
        .and_then(|d| DateTime::parse_from_rfc2822(&d).ok())
        .map(|d| d.naive_local())
        .or_else(|| p.time(PR_CLIENT_SUBMIT_TIME))
        .or_else(|| p.time(PR_DELIVERY_TIME))
        .or_else(|| p.time(PR_CREATION_TIME));
    m.body = body(file, &p, warnings);
    let (listed, attachment_count) = attachments_of(file, base, p.enc);
    m.attachments = listed.iter().map(|a| a.name.clone()).collect();
    ReadMessage {
        props: m,
        listed,
        attachment_count,
        enc: p.enc,
    }
}

fn body(file: &mut MsgFile<'_>, p: &Props, warnings: &mut Vec<String>) -> String {
    let mut text = p
        .string_in(file, PR_BODY, MAX_BODY_PROP, p.enc.body)
        .unwrap_or_default();
    if text.trim().is_empty() {
        let html = p
            .binary(file, PR_HTML, MAX_BODY_PROP)
            .map(|b| p.html_body(&b))
            .or_else(|| p.string_in(file, PR_HTML, MAX_BODY_PROP, p.enc.body));
        if let Some(h) = html.filter(|h| !h.trim().is_empty()) {
            text = crate::html::html_text(&h);
        }
    }
    if text.trim().is_empty() {
        if let Some(rtf) = p.binary(file, PR_RTF_COMPRESSED, MAX_BODY_PROP) {
            match decompress_rtf(&rtf) {
                Some(raw) => text = rtf_text(&raw),
                None => warnings.push("corpo em RTF compactado ilegível".to_string()),
            }
        }
    }
    let mut text = text.replace("\r\n", "\n").replace('\r', "\n");
    if text.len() > MAX_BODY {
        let mut end = MAX_BODY;
        while !text.is_char_boundary(end) {
            end -= 1;
        }
        text.truncate(end);
        warnings.push(format!(
            "corpo da mensagem cortado em {} MB",
            MAX_BODY >> 20
        ));
    }
    text.trim().to_string()
}

/// [MS-OXRTFCP] decompression (`LZFu`, or `MELA` stored uncompressed).
fn decompress_rtf(data: &[u8]) -> Option<Vec<u8>> {
    const PREBUF: &[u8] = b"{\\rtf1\\ansi\\mac\\deff0\\deftab720{\\fonttbl;}{\\f0\\fnil \\froman \\fswiss \\fmodern \\fscript \\fdecor MS Sans SerifSymbolArialTimes New RomanCourier{\\colortbl\\red0\\green0\\blue0\r\n\\par \\pard\\plain\\f0\\fs20\\b\\i\\u\\tab\\tx";
    let word = |at: usize| -> Option<u32> {
        Some(u32::from_le_bytes(data.get(at..at + 4)?.try_into().ok()?))
    };
    let comp_size = word(0)? as usize;
    let raw_size = (word(4)? as usize).min(MAX_BODY);
    let kind = word(8)?;
    let end = comp_size.saturating_add(4).min(data.len());
    let body = data.get(16..end)?;
    if kind == 0x414C_454D {
        return Some(body[..body.len().min(raw_size)].to_vec());
    }
    if kind != 0x7546_5A4C {
        return None;
    }
    let mut dict = [0u8; 4096];
    dict[..PREBUF.len()].copy_from_slice(PREBUF);
    let mut write = PREBUF.len();
    let mut out = Vec::with_capacity(raw_size.min(1 << 20));
    let mut i = 0usize;
    'outer: while i < body.len() && out.len() < raw_size {
        let control = body[i];
        i += 1;
        for bit in 0..8 {
            if out.len() >= raw_size {
                break 'outer;
            }
            if control & (1 << bit) == 0 {
                let Some(&b) = body.get(i) else {
                    break 'outer;
                };
                i += 1;
                out.push(b);
                dict[write] = b;
                write = (write + 1) % 4096;
            } else {
                let (Some(&hi), Some(&lo)) = (body.get(i), body.get(i + 1)) else {
                    break 'outer;
                };
                i += 2;
                let reference = u16::from_be_bytes([hi, lo]);
                let offset = usize::from(reference >> 4);
                let length = usize::from(reference & 0x0F) + 2;
                if offset == write {
                    break 'outer;
                }
                for k in 0..length {
                    let b = dict[(offset + k) % 4096];
                    out.push(b);
                    dict[write] = b;
                    write = (write + 1) % 4096;
                }
            }
        }
    }
    out.truncate(raw_size);
    Some(out)
}

/// Text of an RTF document: destinations, font and color tables and pictures skipped,
/// `\par`/`\line` as line breaks, `\'hh` in windows-1252, `\uN` as Unicode.
fn rtf_text(rtf: &[u8]) -> String {
    const SKIP: [&str; 20] = [
        "fonttbl",
        "colortbl",
        "stylesheet",
        "info",
        "pict",
        "object",
        "header",
        "footer",
        "themedata",
        "colorschememapping",
        "latentstyles",
        "datastore",
        "xmlnstbl",
        "listtable",
        "listoverridetable",
        "rsidtbl",
        "generator",
        "mmathPr",
        "fldinst",
        "pgdsctbl",
    ];
    let mut out = String::new();
    let mut bytes: Vec<u8> = Vec::new();
    let flush = |bytes: &mut Vec<u8>, out: &mut String| {
        if !bytes.is_empty() {
            out.push_str(
                &encoding_rs::WINDOWS_1252
                    .decode_without_bom_handling(bytes)
                    .0,
            );
            bytes.clear();
        }
    };
    // Per group: skipping, and the number of fallback characters after \u.
    let mut stack: Vec<(bool, usize)> = Vec::new();
    let mut skip = false;
    let mut uc = 1usize;
    let mut pending_skip = 0usize;
    let mut i = 0usize;
    while i < rtf.len() {
        let c = rtf[i];
        match c {
            b'{' => {
                if stack.len() < 512 {
                    stack.push((skip, uc));
                }
                i += 1;
            }
            b'}' => {
                if let Some((s, u)) = stack.pop() {
                    skip = s;
                    uc = u;
                }
                i += 1;
            }
            b'\\' => {
                let Some(&next) = rtf.get(i + 1) else {
                    break;
                };
                if next == b'\'' {
                    let hex = rtf
                        .get(i + 2..i + 4)
                        .and_then(|h| std::str::from_utf8(h).ok());
                    i += 4;
                    if pending_skip > 0 {
                        pending_skip -= 1;
                    } else if let Some(b) = hex.and_then(|h| u8::from_str_radix(h, 16).ok()) {
                        if !skip {
                            bytes.push(b);
                        }
                    }
                    continue;
                }
                if !next.is_ascii_alphabetic() {
                    i += 2;
                    if next == b'*' {
                        skip = true;
                    } else if !skip && matches!(next, b'\\' | b'{' | b'}') {
                        bytes.push(next);
                    } else if !skip && next == b'~' {
                        bytes.push(b' ');
                    }
                    continue;
                }
                let start = i + 1;
                let mut j = start;
                while j < rtf.len() && rtf[j].is_ascii_alphabetic() {
                    j += 1;
                }
                let word = std::str::from_utf8(&rtf[start..j]).unwrap_or("");
                let num_start = j;
                if j < rtf.len() && rtf[j] == b'-' {
                    j += 1;
                }
                while j < rtf.len() && rtf[j].is_ascii_digit() && j - num_start < 10 {
                    j += 1;
                }
                let param: Option<i64> = std::str::from_utf8(&rtf[num_start..j])
                    .ok()
                    .and_then(|s| s.parse().ok());
                if j < rtf.len() && rtf[j] == b' ' {
                    j += 1;
                }
                i = j;
                if SKIP.contains(&word) {
                    skip = true;
                    continue;
                }
                if skip {
                    continue;
                }
                match word {
                    "par" | "line" | "row" => {
                        flush(&mut bytes, &mut out);
                        out.push('\n');
                    }
                    "tab" | "cell" => {
                        flush(&mut bytes, &mut out);
                        out.push('\t');
                    }
                    "uc" => uc = usize::try_from(param.unwrap_or(1)).unwrap_or(1).min(8),
                    "u" => {
                        flush(&mut bytes, &mut out);
                        let n = param.unwrap_or(0);
                        let code = if n < 0 { n + 65_536 } else { n };
                        if let Some(ch) = u32::try_from(code).ok().and_then(char::from_u32) {
                            out.push(ch);
                        }
                        pending_skip = uc;
                    }
                    _ => {}
                }
            }
            b'\r' | b'\n' => i += 1,
            _ => {
                if pending_skip > 0 {
                    pending_skip -= 1;
                } else if !skip {
                    bytes.push(c);
                }
                i += 1;
            }
        }
        if out.len() + bytes.len() > MAX_BODY {
            break;
        }
    }
    flush(&mut bytes, &mut out);
    out.lines()
        .map(str::trim_end)
        .collect::<Vec<_>>()
        .join("\n")
}

/// A file name without directories or control characters.
fn safe_name(raw: &str, n: usize) -> String {
    let base = raw.rsplit(['/', '\\']).next().unwrap_or("").trim();
    let clean = crate::archive::clean_name(base);
    if clean.is_empty() || clean == "." || clean == ".." {
        format!("anexo_{n}")
    } else {
        clean
    }
}

/// An attachment of a message, as listed: inline images of the body are not attachments.
/// Only what opening it needs is kept.
struct Listed {
    storage: String,
    name: String,
    method: u32,
}

/// The attachments of the message stored under `base`, in order, at most the ZIP entry limit
/// (a message with more is refused by [`parse_msg`]), and how many storages there are. Each
/// attachment's properties are read once and dropped: only what opening it needs is kept.
fn attachments_of(file: &mut MsgFile<'_>, base: &str, enc: Encodings) -> (Vec<Listed>, usize) {
    let mut out = Vec::new();
    let max = ZipLimits::default().max_entries;
    let storages = file.storages(base, "__attach_version1.0_#");
    let count = storages.len();
    for (n, storage) in storages.into_iter().take(max).enumerate() {
        let a = Props::open(file, &storage, 8, enc);
        let hidden = a.long(PR_ATTACHMENT_HIDDEN).is_some_and(|v| v & 0xFF != 0);
        let mime = a.text(file, PR_ATTACH_MIME_TAG).to_ascii_lowercase();
        let inline_image =
            !a.text(file, PR_ATTACH_CONTENT_ID).is_empty() && mime.starts_with("image/");
        if hidden || inline_image {
            continue;
        }
        let raw_name = [PR_ATTACH_LONG_FILENAME, PR_ATTACH_FILENAME, PR_DISPLAY_NAME]
            .into_iter()
            .map(|id| a.text(file, id))
            .find(|s| !s.is_empty())
            .unwrap_or_default();
        let mut name = safe_name(&raw_name, n + 1);
        let method = a.long(PR_ATTACH_METHOD).unwrap_or(1);
        if method == 5 && !name.to_ascii_lowercase().ends_with(".msg") {
            name.push_str(".msg");
        }
        out.push(Listed {
            storage,
            name,
            method,
        });
    }
    (out, count)
}

/// Parses a message. With `open_attachments` false (a message inside a container), the
/// attachments are listed but not read.
pub fn parse_msg(
    bytes: &[u8],
    open_attachments: bool,
    lim: &ZipLimits,
) -> Result<MsgParsed, IngestError> {
    parse_msg_within(bytes, open_attachments, lim, read_budget(bytes.len()))
}

fn parse_msg_within(
    bytes: &[u8],
    open_attachments: bool,
    lim: &ZipLimits,
    budget: u64,
) -> Result<MsgParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let mut file = MsgFile::open(bytes, budget).map_err(|e| IngestError::Parse {
        format: "msg",
        detail: format!("arquivo .msg ilegível ({e})"),
    })?;
    let mut out = MsgParsed::default();
    let read = read_message(&mut file, "", 32, Encodings::default(), &mut out.warnings);
    let count = read.attachment_count;
    if count > lim.max_entries {
        out.warnings.push(format!(
            "{count} anexos (máximo {}); nenhum foi aberto",
            lim.max_entries
        ));
    }
    out.message = read.props;
    let (listed, enc) = (read.listed, read.enc);
    let max_mb = lim.max_uncompressed >> 20;
    let mut total: u64 = 0;
    let open = open_attachments && count <= lim.max_entries;
    for Listed {
        storage,
        name,
        method,
    } in listed.into_iter().filter(|_| open)
    {
        if crate::cancel::cancelled() {
            out.warnings.push(crate::cancel::STOPPED.to_string());
            break;
        }
        match method {
            0 | 1 => {}
            5 => {
                if out.embedded.len() >= MAX_EMBEDDED {
                    out.warnings.push(format!(
                        "{name}: mais de {MAX_EMBEDDED} mensagens anexadas; esta e as seguintes não foram lidas"
                    ));
                    continue;
                }
                let sub = format!("{storage}/__substg1.0_3701000D");
                let mut warnings = Vec::new();
                let message = read_message(&mut file, &sub, 24, enc, &mut warnings).props;
                if !message.attachments.is_empty() {
                    warnings.push(
                        "anexos da mensagem anexada não são abertos (profundidade máxima 1)"
                            .to_string(),
                    );
                }
                out.embedded.push(EmbeddedMessage {
                    name,
                    message,
                    warnings,
                });
                continue;
            }
            6 => {
                out.warnings
                    .push(format!("{name}: objeto OLE incorporado não é lido"));
                continue;
            }
            _ => {
                out.warnings.push(format!(
                    "{name}: anexo por referência (link), o arquivo não está na mensagem"
                ));
                continue;
            }
        }
        let budget = lim.max_uncompressed.saturating_sub(total);
        let path = format!("{storage}/__substg1.0_{PR_ATTACH_DATA:04X}{PT_BINARY:04X}");
        let Some(data) = file.read(&path, budget.saturating_add(1)) else {
            out.warnings.push(format!("{name}: anexo sem conteúdo"));
            continue;
        };
        if data.len() as u64 > budget {
            out.warnings.push(format!(
                "anexos acima de {max_mb} MB; {name} e os seguintes não foram abertos"
            ));
            break;
        }
        total += data.len() as u64;
        // Two attachments with the same name stay distinct sources.
        let mut unique = name.clone();
        let mut k = 2;
        while out.attachments.iter().any(|x| x.name == unique) {
            unique = match name.rsplit_once('.') {
                Some((stem, ext)) => format!("{stem} ({k}).{ext}"),
                None => format!("{name} ({k})"),
            };
            k += 1;
        }
        out.attachments.push(MsgAttachment {
            name: unique,
            bytes: data,
        });
    }
    if file.exhausted {
        out.warnings.push(format!(
            "leitura do .msg parou no limite de bytes lidos (o dobro do arquivo, até {} MB); o restante da mensagem e dos anexos não foi lido",
            MAX_READ >> 20
        ));
    }
    Ok(out)
}

/// The items of a message: an `event` and a `text_span` per body paragraph, like an `.eml`.
pub fn message_items(
    m: &MessageProps,
    source_id: &str,
    len: usize,
    ctx: &InboxContext,
) -> Vec<Item> {
    let date_ctx: DateContext = ctx.date_context();
    let event_id = stable_id("it", &[source_id, "msg"]);
    let locator = Locator::ByteRange { start: 0, end: len };
    let mut fields: IndexMap<String, Value> = IndexMap::new();
    for (key, value) in [
        ("from", &m.from),
        ("to", &m.to),
        ("cc", &m.cc),
        ("subject", &m.subject),
        ("message_id", &m.message_id),
    ] {
        if !value.trim().is_empty() {
            fields.insert(key.to_string(), Value::Text(value.trim().to_string()));
        }
    }
    if let Some(at) = m.date {
        fields.insert(
            "date".to_string(),
            Value::Text(at.format("%d/%m/%Y %H:%M").to_string()),
        );
    }
    if !m.attachments.is_empty() {
        fields.insert(
            "attachments".to_string(),
            Value::Text(m.attachments.join("; ")),
        );
    }
    fields.insert("text".to_string(), Value::from(m.body.as_str()));
    if let Some((_, domain)) = m.from.rsplit_once('@') {
        let domain: String = domain
            .chars()
            .take_while(|c| c.is_ascii_alphanumeric() || *c == '.' || *c == '-')
            .collect();
        if !domain.is_empty() {
            fields.insert(
                "sender_domain".to_string(),
                Value::Text(domain.to_lowercase()),
            );
        }
    }
    let time = m.date.map(|at| TimeRef::Instant { at });
    let mut mentions: Vec<EntityMention> = Vec::new();
    for text in [&m.subject, &m.from] {
        for mention in extract_mentions(text, &date_ctx) {
            let mut em = mention.to_entity_mention();
            em.span = None;
            mentions.push(em);
        }
    }
    mentions.extend(
        extract_mentions(&m.body, &date_ctx)
            .into_iter()
            .map(|x| x.to_entity_mention()),
    );
    let mut items = vec![Item {
        id: event_id.clone(),
        kind: ItemKind::Event,
        source_id: source_id.to_string(),
        locator: locator.clone(),
        fields,
        time: time.clone(),
        entity_mentions: mentions,
    }];
    for (i, para) in m
        .body
        .split("\n\n")
        .map(str::trim)
        .filter(|p| !p.is_empty())
        .enumerate()
    {
        let mut fields = IndexMap::new();
        fields.insert("text".to_string(), Value::Text(para.to_string()));
        fields.insert("event_id".to_string(), Value::Text(event_id.clone()));
        fields.insert("paragraph".to_string(), Value::Integer(i as i64));
        items.push(Item {
            id: stable_id("it", &[&event_id, "para", &i.to_string()]),
            kind: ItemKind::TextSpan,
            source_id: source_id.to_string(),
            locator: locator.clone(),
            fields,
            time: time.clone(),
            entity_mentions: extract_mentions(para, &date_ctx)
                .into_iter()
                .map(|x| x.to_entity_mention())
                .collect(),
        });
    }
    items
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::io::Write;

    fn put(file: &mut cfb::CompoundFile<Cursor<Vec<u8>>>, path: &str, data: &[u8]) {
        let mut s = file
            .create_stream(path)
            .unwrap_or_else(|e| panic!("{path}: {e}"));
        s.write_all(data).unwrap_or_else(|e| panic!("{e}"));
    }

    fn unicode(s: &str) -> Vec<u8> {
        s.encode_utf16().flat_map(u16::to_le_bytes).collect()
    }

    fn props(header: usize, entries: &[(u16, u16, [u8; 8])]) -> Vec<u8> {
        let mut out = vec![0u8; header];
        for (kind, id, value) in entries {
            out.extend_from_slice(&kind.to_le_bytes());
            out.extend_from_slice(&id.to_le_bytes());
            out.extend_from_slice(&6u32.to_le_bytes());
            out.extend_from_slice(value);
        }
        out
    }

    fn long(v: u32) -> [u8; 8] {
        let mut b = [0u8; 8];
        b[..4].copy_from_slice(&v.to_le_bytes());
        b
    }

    /// A message with a body, a To and a Cc recipient, a CSV attachment, a hidden inline
    /// image and an attached message (which has an attachment of its own).
    pub(crate) fn sample_msg() -> Vec<u8> {
        let mut f =
            cfb::CompoundFile::create(Cursor::new(Vec::new())).unwrap_or_else(|e| panic!("{e}"));
        let s = |id: u16| format!("/__substg1.0_{id:04X}001F");
        put(
            &mut f,
            &s(PR_SUBJECT),
            &unicode("NF 55120 - boleto atualizado"),
        );
        put(&mut f, &s(PR_SENDER_NAME), &unicode("Ana Lima"));
        put(
            &mut f,
            &s(PR_SENDER_EMAIL),
            &unicode("ana.lima@example.com"),
        );
        put(
            &mut f,
            &s(PR_BODY),
            &unicode("Bom dia,\r\n\r\nsegue o boleto da NF 55120 de R$ 14.994,00.\r\n"),
        );
        put(
            &mut f,
            &s(PR_TRANSPORT_HEADERS),
            &unicode("Date: Sat, 18 Jul 2026 16:52:00 -0300\r\nMessage-ID: <x1@example.com>\r\n"),
        );
        put(&mut f, "/__properties_version1.0", &props(32, &[]));
        for (i, (name, addr, kind)) in [
            ("Pagar", "pagar@example.com", 1u32),
            ("Compras", "compras@example.com", 2),
        ]
        .iter()
        .enumerate()
        {
            let r = format!("/__recip_version1.0_#{i:08X}");
            f.create_storage(&r).unwrap_or_else(|e| panic!("{e}"));
            put(&mut f, &format!("{r}/__substg1.0_3001001F"), &unicode(name));
            put(&mut f, &format!("{r}/__substg1.0_39FE001F"), &unicode(addr));
            put(
                &mut f,
                &format!("{r}/__properties_version1.0"),
                &props(8, &[(0x0003, PR_RECIPIENT_TYPE, long(*kind))]),
            );
        }
        let a0 = "/__attach_version1.0_#00000000";
        f.create_storage(a0).unwrap_or_else(|e| panic!("{e}"));
        put(
            &mut f,
            &format!("{a0}/__substg1.0_3707001F"),
            &unicode("C:\\temp\\extrato.csv"),
        );
        put(
            &mut f,
            &format!("{a0}/__substg1.0_37010102"),
            b"data;historico;valor;tipo\n10/07/2026;PIX;1,00;C\n",
        );
        put(
            &mut f,
            &format!("{a0}/__properties_version1.0"),
            &props(8, &[(0x0003, PR_ATTACH_METHOD, long(1))]),
        );
        let a1 = "/__attach_version1.0_#00000001";
        f.create_storage(a1).unwrap_or_else(|e| panic!("{e}"));
        put(
            &mut f,
            &format!("{a1}/__substg1.0_3707001F"),
            &unicode("image001.png"),
        );
        put(&mut f, &format!("{a1}/__substg1.0_37010102"), b"\x89PNG");
        put(
            &mut f,
            &format!("{a1}/__properties_version1.0"),
            &props(8, &[(0x000B, PR_ATTACHMENT_HIDDEN, long(1))]),
        );
        let a2 = "/__attach_version1.0_#00000002";
        f.create_storage(a2).unwrap_or_else(|e| panic!("{e}"));
        put(
            &mut f,
            &format!("{a2}/__substg1.0_3001001F"),
            &unicode("Re: cobrança"),
        );
        put(
            &mut f,
            &format!("{a2}/__properties_version1.0"),
            &props(8, &[(0x0003, PR_ATTACH_METHOD, long(5))]),
        );
        let inner = format!("{a2}/__substg1.0_3701000D");
        f.create_storage(&inner).unwrap_or_else(|e| panic!("{e}"));
        put(
            &mut f,
            &format!("{inner}/__substg1.0_0037001F"),
            &unicode("Re: cobrança"),
        );
        put(
            &mut f,
            &format!("{inner}/__substg1.0_0C1F001F"),
            &unicode("compras@example.com"),
        );
        put(
            &mut f,
            &format!("{inner}/__substg1.0_1000001F"),
            &unicode("Pode pagar."),
        );
        // 2026-07-18 19:00:00 UTC as a FILETIME.
        let ft = (1_784_401_200u64 + 11_644_473_600) * 10_000_000;
        put(
            &mut f,
            &format!("{inner}/__properties_version1.0"),
            &props(24, &[(0x0040, PR_CLIENT_SUBMIT_TIME, ft.to_le_bytes())]),
        );
        let inner_attach = format!("{inner}/__attach_version1.0_#00000000");
        f.create_storage(&inner_attach)
            .unwrap_or_else(|e| panic!("{e}"));
        put(
            &mut f,
            &format!("{inner_attach}/__properties_version1.0"),
            &props(8, &[]),
        );
        f.flush().unwrap_or_else(|e| panic!("{e}"));
        f.into_inner().into_inner()
    }

    #[test]
    fn message_recipients_attachments_and_embedded_message() {
        let bytes = sample_msg();
        assert!(is_msg(&bytes));
        let p = parse_msg(&bytes, true, &ZipLimits::default()).unwrap_or_else(|e| panic!("{e}"));
        assert!(p.warnings.is_empty(), "{:?}", p.warnings);
        let m = &p.message;
        assert_eq!(m.from, "Ana Lima <ana.lima@example.com>");
        assert_eq!(m.to, "Pagar <pagar@example.com>");
        assert_eq!(m.cc, "Compras <compras@example.com>");
        assert_eq!(m.message_id, "<x1@example.com>");
        assert_eq!(
            m.date
                .map(|d| d.format("%d/%m/%Y %H:%M").to_string())
                .as_deref(),
            Some("18/07/2026 16:52")
        );
        assert_eq!(
            m.body,
            "Bom dia,\n\nsegue o boleto da NF 55120 de R$ 14.994,00."
        );
        assert_eq!(
            m.attachments,
            vec!["extrato.csv".to_string(), "Re: cobrança.msg".to_string()]
        );
        assert_eq!(p.attachments.len(), 1);
        assert_eq!(p.attachments[0].name, "extrato.csv");
        assert_eq!(p.embedded.len(), 1);
        let e = &p.embedded[0];
        assert_eq!(e.message.from, "compras@example.com");
        assert_eq!(e.message.body, "Pode pagar.");
        assert_eq!(
            e.message
                .date
                .map(|d| d.format("%d/%m/%Y %H:%M").to_string())
                .as_deref(),
            // 19:00 UTC is 16:00 in Brazil, as an .eml of the same message would say.
            Some("18/07/2026 16:00")
        );
        assert_eq!(e.warnings.len(), 1);
        let items = message_items(m, "src_msg", bytes.len(), &InboxContext::default());
        assert_eq!(items.len(), 3);
        assert_eq!(
            items[0].field("sender_domain").and_then(Value::as_text),
            Some("example.com")
        );
        assert_eq!(
            items[0].field("date").and_then(Value::as_text),
            Some("18/07/2026 16:52")
        );
        assert!(items[0]
            .entity_mentions
            .iter()
            .any(|x| x.normalized.as_deref() == Some("1499400")));
        let closed =
            parse_msg(&bytes, false, &ZipLimits::default()).unwrap_or_else(|e| panic!("{e}"));
        assert!(closed.attachments.is_empty() && closed.embedded.is_empty());
        assert_eq!(closed.message.attachments.len(), 2);
    }

    #[test]
    fn rtf_bodies_are_decompressed_and_stripped() {
        // MELA: stored uncompressed.
        let rtf = b"{\\rtf1\\ansi{\\fonttbl{\\f0 Arial;}}{\\*\\generator x;}Ol\\'e1, NF 12\\par Valor R$ 10,00\\u8364?}";
        let mut data = Vec::new();
        data.extend_from_slice(&((rtf.len() + 12) as u32).to_le_bytes());
        data.extend_from_slice(&(rtf.len() as u32).to_le_bytes());
        data.extend_from_slice(&0x414C_454Du32.to_le_bytes());
        data.extend_from_slice(&0u32.to_le_bytes());
        data.extend_from_slice(rtf);
        let raw = decompress_rtf(&data).unwrap_or_default();
        assert_eq!(rtf_text(&raw), "Olá, NF 12\nValor R$ 10,00€");
        // The example of [MS-OXRTFCP] 3.1.
        let lzfu: [u8; 49] = [
            0x2d, 0x00, 0x00, 0x00, 0x2b, 0x00, 0x00, 0x00, 0x4c, 0x5a, 0x46, 0x75, 0xf1, 0xc5,
            0xc7, 0xa7, 0x03, 0x00, 0x0a, 0x00, 0x72, 0x63, 0x70, 0x67, 0x31, 0x32, 0x35, 0x42,
            0x32, 0x0a, 0xf3, 0x20, 0x68, 0x65, 0x6c, 0x09, 0x00, 0x20, 0x62, 0x77, 0x05, 0xb0,
            0x6c, 0x64, 0x7d, 0x0a, 0x80, 0x0f, 0xa0,
        ];
        let out = decompress_rtf(&lzfu).unwrap_or_default();
        assert_eq!(
            String::from_utf8_lossy(&out),
            "{\\rtf1\\ansi\\ansicpg1252\\pard hello world}\r\n"
        );
        assert!(decompress_rtf(b"short").is_none());
    }

    #[test]
    fn garbage_is_not_a_message() {
        assert!(!is_msg(b"\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1lixo"));
        assert!(parse_msg(
            b"\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1lixo",
            true,
            &ZipLimits::default()
        )
        .is_err());
        assert!(parse_msg(b"", true, &ZipLimits::default()).is_err());
        assert_eq!(safe_name("..", 3), "anexo_3");
        assert_eq!(safe_name("a/b\\c.pdf", 1), "c.pdf");
    }

    /// A message with `recipients` recipients, `embedded` attached messages, a subject of
    /// `subject` and the given transport headers.
    fn crowded_msg(recipients: usize, embedded: usize, subject: &str, headers: &str) -> Vec<u8> {
        let mut f =
            cfb::CompoundFile::create(Cursor::new(Vec::new())).unwrap_or_else(|e| panic!("{e}"));
        let s = |id: u16| format!("/__substg1.0_{id:04X}001F");
        put(&mut f, &s(PR_SUBJECT), &unicode(subject));
        put(&mut f, &s(PR_TRANSPORT_HEADERS), &unicode(headers));
        put(&mut f, &s(PR_BODY), &unicode("Corpo."));
        put(&mut f, "/__properties_version1.0", &props(32, &[]));
        for i in 0..recipients {
            let r = format!("/__recip_version1.0_#{i:08X}");
            f.create_storage(&r).unwrap_or_else(|e| panic!("{e}"));
            put(
                &mut f,
                &format!("{r}/__substg1.0_39FE001F"),
                &unicode(&format!("pessoa{i}@example.com")),
            );
            put(
                &mut f,
                &format!("{r}/__properties_version1.0"),
                &props(8, &[]),
            );
        }
        for i in 0..embedded {
            let a = format!("/__attach_version1.0_#{i:08X}");
            f.create_storage(&a).unwrap_or_else(|e| panic!("{e}"));
            put(
                &mut f,
                &format!("{a}/__properties_version1.0"),
                &props(8, &[(0x0003, PR_ATTACH_METHOD, long(5))]),
            );
            put(
                &mut f,
                &format!("{a}/__substg1.0_3001001F"),
                &unicode(&format!("resposta {i}")),
            );
            let inner = format!("{a}/__substg1.0_3701000D");
            f.create_storage(&inner).unwrap_or_else(|e| panic!("{e}"));
            put(
                &mut f,
                &format!("{inner}/__substg1.0_1000001F"),
                &unicode("ok"),
            );
            put(
                &mut f,
                &format!("{inner}/__properties_version1.0"),
                &props(24, &[]),
            );
        }
        f.flush().unwrap_or_else(|e| panic!("{e}"));
        f.into_inner().into_inner()
    }

    #[test]
    fn recipients_attached_messages_and_fields_are_capped() {
        let long_from = format!("From: {}@example.com\r\n", "a".repeat(50_000));
        let bytes = crowded_msg(250, 60, &"S".repeat(100_000), &long_from);
        let p = parse_msg(&bytes, true, &ZipLimits::default()).unwrap_or_else(|e| panic!("{e}"));
        let m = &p.message;
        assert!(
            m.subject.chars().count() <= MAX_FIELD_CHARS,
            "{}",
            m.subject.len()
        );
        assert!(
            m.from.chars().count() <= MAX_FIELD_CHARS,
            "{}",
            m.from.len()
        );
        assert!(m.to.chars().count() <= MAX_FIELD_CHARS, "{}", m.to.len());
        assert!(m.to.starts_with("pessoa0@example.com; pessoa1@example.com"));
        assert!(!m.to.contains("pessoa220@"), "only the first 200 are read");
        assert_eq!(p.embedded.len(), MAX_EMBEDDED);
        assert!(
            p.warnings.iter().any(|w| w.contains("250 destinatários")),
            "{:?}",
            p.warnings
        );
        assert!(
            p.warnings.iter().any(|w| w.contains("mensagens anexadas")),
            "{:?}",
            p.warnings
        );
    }

    #[test]
    fn reads_stop_at_the_byte_budget_of_the_file() {
        assert_eq!(read_budget(10), 20);
        assert_eq!(read_budget(100 << 20), MAX_READ);
        let bytes = sample_msg();
        let whole =
            parse_msg(&bytes, true, &ZipLimits::default()).unwrap_or_else(|e| panic!("{e}"));
        assert!(whole.warnings.is_empty(), "{:?}", whole.warnings);
        // Directory entries may share one chain: reads are counted, not the file size.
        let cut = parse_msg_within(&bytes, true, &ZipLimits::default(), 300)
            .unwrap_or_else(|e| panic!("{e}"));
        assert!(
            cut.warnings
                .iter()
                .any(|w| w.contains("limite de bytes lidos")),
            "{:?}",
            cut.warnings
        );
        assert!(cut.attachments.iter().map(|a| a.bytes.len()).sum::<usize>() <= 300);
        let mut file = MsgFile::open(&bytes, 10).unwrap_or_else(|e| panic!("{e}"));
        let first = file.read(
            &format!("/__substg1.0_{PR_SUBJECT:04X}001F"),
            MAX_SHORT_PROP,
        );
        assert_eq!(first.map(|b| b.len()), Some(10));
        assert!(file.exhausted);
        let next = file.read(&format!("/__substg1.0_{PR_BODY:04X}001F"), MAX_SHORT_PROP);
        assert_eq!(next.map(|b| b.len()), Some(0));
    }

    #[test]
    fn string8_properties_use_the_message_code_page() {
        let mut f =
            cfb::CompoundFile::create(Cursor::new(Vec::new())).unwrap_or_else(|e| panic!("{e}"));
        let (cyrillic, _, _) = encoding_rs::WINDOWS_1251.encode("Счёт 55120");
        put(
            &mut f,
            &format!("/__substg1.0_{PR_SUBJECT:04X}001E"),
            &cyrillic,
        );
        put(
            &mut f,
            &format!("/__substg1.0_{PR_BODY:04X}001E"),
            "Olá, segue a NF.".as_bytes(),
        );
        put(
            &mut f,
            "/__properties_version1.0",
            &props(
                32,
                &[
                    (0x0003, PR_MESSAGE_CODEPAGE, long(1251)),
                    (0x0003, PR_INTERNET_CPID, long(65001)),
                ],
            ),
        );
        f.flush().unwrap_or_else(|e| panic!("{e}"));
        let bytes = f.into_inner().into_inner();
        let p = parse_msg(&bytes, true, &ZipLimits::default()).unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(p.message.subject, "Счёт 55120");
        assert_eq!(p.message.body, "Olá, segue a NF.");
    }
}
