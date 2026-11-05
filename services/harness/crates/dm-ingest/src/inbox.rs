//! Text dumps of inboxes and chat threads.
//!
//! Blocks delimited by lines such as `--- E-MAIL 1 ---` or by `De:`/`From:` headers (Portuguese
//! and English header names), bodies, attachment markers (`[anexo: x.pdf]`) and chat lines
//! `[dd/mm hh:mm] Remetente: mensagem` (WhatsApp exports, with or without year, with role
//! prefixes such as `Compras - Rafael`). Produces `event` items (one per e-mail or message) and
//! `text_span` items (one per paragraph of an e-mail body), each with byte-range locators and
//! extracted mentions.

use std::sync::LazyLock;

use chrono::NaiveDate;
use dm_core::{stable_id, EntityMention, Item, ItemKind, Locator, TimeRef, Value};
use dm_normalize::{date_ptbr, fold_ascii, DateContext};
use indexmap::IndexMap;
use regex::Regex;

use crate::mentions::extract_mentions;

/// Context for dates without a year.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct InboxContext {
    /// Year assumed for `dd/mm` dates.
    pub reference_year: Option<i32>,
    /// Date the export was taken; also disambiguates the boleto factor cycle.
    pub reference_date: Option<NaiveDate>,
}

impl InboxContext {
    /// Date context for the normalizers.
    pub fn date_context(&self) -> DateContext {
        DateContext {
            reference_year: self
                .reference_year
                .or_else(|| self.reference_date.map(|d| chrono::Datelike::year(&d))),
            reference_date: self.reference_date,
            max_days_ahead: None,
        }
    }
}

/// Header fields recognized in both languages.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub(crate) enum HeaderKey {
    From,
    To,
    Cc,
    Date,
    Subject,
    Attachment,
}

impl HeaderKey {
    fn field_name(self) -> &'static str {
        match self {
            HeaderKey::From => "from",
            HeaderKey::To => "to",
            HeaderKey::Cc => "cc",
            HeaderKey::Date => "date",
            HeaderKey::Subject => "subject",
            HeaderKey::Attachment => "attachments",
        }
    }
}

static RE_MARKER: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)^[-=*#_>]{2,}\s*(?:e-?mail|mensagem|message|msg|original message|mensagem original|forwarded message|mensagem encaminhada)\b\s*#?\s*(\d+)?[^\n]*?[-=*#_<]{2,}\s*$").unwrap()
});
static RE_MARKER_BARE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)^(?:e-?mail|mensagem|message|msg)\s*#?\s*(\d+)\s*:?\s*$").unwrap()
});
static RE_CHAT: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^\[?(\d{1,2}/\d{1,2}(?:/\d{2,4})?),?\s+(\d{1,2}:\d{2}(?::\d{2})?)\]?\s*[-\u{2013}]?\s*([^:\n]{1,80}?):\s(.*)$").unwrap()
});
static RE_ATTACH: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)(?:\[(?:anexo|anexos|attachment|attachments|arquivo)\s*:\s*([^\]]+)\]|<<([^>]+)>>|^(?:anexo|anexos|attachment|attachments)\s*:\s*(.+)$)").unwrap()
});

/// True for lines like `--- E-MAIL 1 ---`, `=== Mensagem 2 ===`, `----- Original Message -----`.
pub(crate) fn is_block_marker(line: &str) -> bool {
    let l = line.trim();
    RE_MARKER.is_match(l) || RE_MARKER_BARE.is_match(l)
}

fn marker_index(line: &str) -> Option<u32> {
    let l = line.trim();
    RE_MARKER
        .captures(l)
        .or_else(|| RE_MARKER_BARE.captures(l))
        .and_then(|c| c.get(1))
        .and_then(|m| m.as_str().parse().ok())
}

/// Parsed prefix of a chat line.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ChatLine<'a> {
    pub(crate) date: &'a str,
    pub(crate) time: &'a str,
    pub(crate) sender: &'a str,
    pub(crate) message: &'a str,
}

/// Recognizes `[dd/mm hh:mm] Sender: message` and the Android/iOS WhatsApp variants.
pub(crate) fn chat_line_prefix(line: &str) -> Option<ChatLine<'_>> {
    let c = RE_CHAT.captures(line.trim_end())?;
    Some(ChatLine {
        date: c.get(1)?.as_str(),
        time: c.get(2)?.as_str(),
        sender: c.get(3)?.as_str().trim(),
        message: c.get(4)?.as_str(),
    })
}

/// Recognizes `De:`, `From:`, `Para:`, `To:`, `Cc:`, `Data:`, `Date:`, `Assunto:`, `Subject:`,
/// `Anexo(s):`, `Attachment(s):` (any case, optional surrounding `*` or spaces).
pub(crate) fn header_field(line: &str) -> Option<(HeaderKey, &str)> {
    let l = line.trim_start().trim_start_matches('*');
    let (name, rest) = l.split_once(':')?;
    let key = match fold_ascii(name).as_str() {
        "DE" | "FROM" | "REMETENTE" => HeaderKey::From,
        "PARA" | "TO" | "DESTINATARIO" | "DESTINATARIOS" => HeaderKey::To,
        "CC" | "COPIA" | "COM COPIA" => HeaderKey::Cc,
        "DATA" | "DATE" | "ENVIADO" | "ENVIADA" | "ENVIADO EM" | "ENVIADA EM" | "SENT" => {
            HeaderKey::Date
        }
        "ASSUNTO" | "SUBJECT" => HeaderKey::Subject,
        "ANEXO" | "ANEXOS" | "ATTACHMENT" | "ATTACHMENTS" => HeaderKey::Attachment,
        _ => return None,
    };
    Some((key, rest.trim().trim_end_matches('*').trim()))
}

/// File names referenced by attachment markers inside a body.
pub(crate) fn attachment_markers(body: &str) -> Vec<String> {
    let mut out = Vec::new();
    for line in body.lines() {
        for c in RE_ATTACH.captures_iter(line.trim()) {
            let name = c
                .get(1)
                .or_else(|| c.get(2))
                .or_else(|| c.get(3))
                .map(|m| m.as_str().trim().to_string());
            if let Some(n) = name {
                for part in n.split([',', ';']) {
                    let p = part.trim();
                    if !p.is_empty() {
                        out.push(p.to_string());
                    }
                }
            }
        }
    }
    out
}

#[derive(Default)]
struct EmailBlock {
    start: usize,
    end: usize,
    index: Option<u32>,
    headers: IndexMap<HeaderKey, String>,
    last_header: Option<HeaderKey>,
    body_start: Option<usize>,
}

struct ChatMessage {
    start: usize,
    end: usize,
    date: String,
    time: String,
    sender: String,
    text: String,
}

#[derive(PartialEq, Eq, Clone, Copy)]
enum Mode {
    Start,
    Headers,
    Body,
    Chat,
}

/// Parses an inbox/chat text dump into items.
pub fn parse_txt_inbox(text: &str, source_id: &str, ctx: &InboxContext) -> Vec<Item> {
    let date_ctx = ctx.date_context();
    let mut items = Vec::new();
    let mut emails: Vec<(EmailBlock, String)> = Vec::new();
    let mut chats: Vec<ChatMessage> = Vec::new();
    let mut current: Option<EmailBlock> = None;
    let mut mode = Mode::Start;
    let mut prev_blank = true;
    let mut offset = 0usize;

    let finalize =
        |block: Option<EmailBlock>, end: usize, emails: &mut Vec<(EmailBlock, String)>| {
            if let Some(mut b) = block {
                b.end = end;
                let body = b
                    .body_start
                    .map(|s| text[s.min(end)..end].to_string())
                    .unwrap_or_default();
                if !b.headers.is_empty() || !body.trim().is_empty() {
                    emails.push((b, body));
                }
            }
        };

    for raw_line in text.split_inclusive('\n') {
        let line_start = offset;
        offset += raw_line.len();
        let line = raw_line.trim_end_matches(['\n', '\r']);
        let trimmed = line.trim();

        if is_block_marker(trimmed) {
            finalize(current.take(), line_start, &mut emails);
            current = Some(EmailBlock {
                start: line_start,
                index: marker_index(trimmed),
                ..EmailBlock::default()
            });
            mode = Mode::Headers;
            prev_blank = false;
            continue;
        }
        if let Some(chat) = chat_line_prefix(line) {
            finalize(current.take(), line_start, &mut emails);
            chats.push(ChatMessage {
                start: line_start,
                end: offset,
                date: chat.date.to_string(),
                time: chat.time.to_string(),
                sender: chat.sender.to_string(),
                text: chat.message.to_string(),
            });
            mode = Mode::Chat;
            prev_blank = false;
            continue;
        }
        match mode {
            Mode::Chat => {
                if let Some(last) = chats.last_mut() {
                    if !trimmed.is_empty() {
                        last.text.push('\n');
                        last.text.push_str(line);
                    }
                    last.end = offset;
                }
            }
            Mode::Start | Mode::Headers => {
                if let Some((key, value)) = header_field(line) {
                    let block = current.get_or_insert_with(|| EmailBlock {
                        start: line_start,
                        ..EmailBlock::default()
                    });
                    let entry = block.headers.entry(key).or_default();
                    if !entry.is_empty() {
                        entry.push_str("; ");
                    }
                    entry.push_str(value);
                    block.last_header = Some(key);
                    mode = Mode::Headers;
                } else if trimmed.is_empty() {
                    if let Some(block) = current.as_mut() {
                        if mode == Mode::Headers {
                            block.body_start = Some(offset);
                            mode = Mode::Body;
                        }
                    }
                } else if mode == Mode::Headers && raw_line.starts_with([' ', '\t']) {
                    // folded header continuation
                    if let Some(block) = current.as_mut() {
                        if let Some(key) = block.last_header {
                            let entry = block.headers.entry(key).or_default();
                            entry.push(' ');
                            entry.push_str(trimmed);
                        }
                    }
                } else {
                    // Loose text: an anonymous block whose body starts here.
                    let block = current.get_or_insert_with(|| EmailBlock {
                        start: line_start,
                        ..EmailBlock::default()
                    });
                    if block.body_start.is_none() {
                        block.body_start = Some(line_start);
                    }
                    mode = Mode::Body;
                }
            }
            Mode::Body => {
                // A new header run after a blank line starts an implicit new e-mail.
                if prev_blank && matches!(header_field(line), Some((HeaderKey::From, _))) {
                    finalize(current.take(), line_start, &mut emails);
                    let mut block = EmailBlock {
                        start: line_start,
                        ..EmailBlock::default()
                    };
                    if let Some((key, value)) = header_field(line) {
                        block.headers.insert(key, value.to_string());
                        block.last_header = Some(key);
                    }
                    current = Some(block);
                    mode = Mode::Headers;
                }
            }
        }
        prev_blank = trimmed.is_empty();
    }
    finalize(current.take(), text.len(), &mut emails);

    for (block, body) in emails {
        items.extend(email_items(&block, &body, text, source_id, &date_ctx));
    }
    for chat in chats {
        items.push(chat_item(&chat, source_id, &date_ctx));
    }
    items.sort_by_key(|i| match &i.locator {
        Locator::ByteRange { start, .. } => *start,
        _ => usize::MAX,
    });
    items
}

fn text_value(s: &str) -> Value {
    Value::from(s.trim())
}

fn email_items(
    block: &EmailBlock,
    body: &str,
    text: &str,
    source_id: &str,
    date_ctx: &DateContext,
) -> Vec<Item> {
    let mut items = Vec::new();
    let event_id = stable_id(
        "it",
        &[
            source_id,
            "email",
            &block.start.to_string(),
            &block.end.to_string(),
        ],
    );
    let mut fields: IndexMap<String, Value> = IndexMap::new();
    for (key, value) in &block.headers {
        fields.insert(key.field_name().to_string(), text_value(value));
    }
    let mut attachments = attachment_markers(body);
    if let Some(Value::Text(a)) = fields.get("attachments") {
        attachments.extend(
            a.split([',', ';'])
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty()),
        );
    }
    attachments.dedup();
    if !attachments.is_empty() {
        fields.insert(
            "attachments".to_string(),
            Value::Text(attachments.join("; ")),
        );
    }
    if let Some(index) = block.index {
        fields.insert("block_index".to_string(), Value::Integer(i64::from(index)));
    }
    let body_trimmed = body.trim();
    fields.insert("text".to_string(), text_value(body_trimmed));
    let from = block
        .headers
        .get(&HeaderKey::From)
        .cloned()
        .unwrap_or_default();
    if let Some(domain) = sender_domain(&from) {
        fields.insert("sender_domain".to_string(), Value::Text(domain));
    }
    let time = block
        .headers
        .get(&HeaderKey::Date)
        .and_then(|d| parse_header_date(d, date_ctx));
    let mut mentions: Vec<EntityMention> = Vec::new();
    let subject = block
        .headers
        .get(&HeaderKey::Subject)
        .cloned()
        .unwrap_or_default();
    for m in extract_mentions(&subject, date_ctx) {
        let mut em = m.to_entity_mention();
        em.span = None;
        mentions.push(em);
    }
    for m in extract_mentions(&from, date_ctx) {
        let mut em = m.to_entity_mention();
        em.span = None;
        mentions.push(em);
    }
    for m in extract_mentions(body_trimmed, date_ctx) {
        mentions.push(m.to_entity_mention());
    }
    items.push(Item {
        id: event_id.clone(),
        kind: ItemKind::Event,
        source_id: source_id.to_string(),
        locator: Locator::ByteRange {
            start: block.start,
            end: block.end,
        },
        fields,
        time: time.clone(),
        entity_mentions: mentions,
    });
    // One text_span per paragraph of the body, with absolute byte ranges.
    if let Some(body_start) = block.body_start {
        let body_abs = body_start.min(text.len());
        for (para_start, para) in paragraphs(body) {
            let abs_start = body_abs + para_start;
            let abs_end = abs_start + para.len();
            let mut fields = IndexMap::new();
            fields.insert("text".to_string(), Value::Text(para.to_string()));
            fields.insert("event_id".to_string(), Value::Text(event_id.clone()));
            let mentions = extract_mentions(para, date_ctx)
                .into_iter()
                .map(|m| m.to_entity_mention())
                .collect();
            items.push(Item {
                id: stable_id(
                    "it",
                    &[
                        source_id,
                        "span",
                        &abs_start.to_string(),
                        &abs_end.to_string(),
                    ],
                ),
                kind: ItemKind::TextSpan,
                source_id: source_id.to_string(),
                locator: Locator::ByteRange {
                    start: abs_start,
                    end: abs_end,
                },
                fields,
                time: time.clone(),
                entity_mentions: mentions,
            });
        }
    }
    items
}

/// Paragraphs of a body with their byte offsets inside it (blank-line separated).
fn paragraphs(body: &str) -> Vec<(usize, &str)> {
    let mut out = Vec::new();
    let mut start: Option<usize> = None;
    let mut offset = 0usize;
    for raw in body.split_inclusive('\n') {
        let line = raw.trim_end_matches(['\n', '\r']);
        if line.trim().is_empty() {
            if let Some(s) = start.take() {
                out.push((s, body[s..offset].trim_end()));
            }
        } else if start.is_none() {
            start = Some(offset);
        }
        offset += raw.len();
    }
    if let Some(s) = start {
        out.push((s, body[s..].trim_end()));
    }
    out.into_iter()
        .filter(|(_, p)| !p.trim().is_empty())
        .collect()
}

fn sender_domain(from: &str) -> Option<String> {
    let at = from.rfind('@')?;
    let rest = &from[at + 1..];
    let end = rest
        .find(|c: char| !(c.is_ascii_alphanumeric() || c == '.' || c == '-'))
        .unwrap_or(rest.len());
    let domain = rest[..end].trim_end_matches('.').to_lowercase();
    (!domain.is_empty()).then_some(domain)
}

fn parse_header_date(value: &str, ctx: &DateContext) -> Option<TimeRef> {
    if let Ok(dt) = chrono::DateTime::parse_from_rfc2822(value.trim()) {
        return Some(TimeRef::Instant {
            at: dt.naive_local(),
        });
    }
    let parsed = date_ptbr(value, ctx).ok()?;
    Some(match parsed.time {
        Some(_) => TimeRef::Instant {
            at: parsed.datetime(),
        },
        None => TimeRef::Date { date: parsed.date },
    })
}

fn chat_item(chat: &ChatMessage, source_id: &str, date_ctx: &DateContext) -> Item {
    let mut fields: IndexMap<String, Value> = IndexMap::new();
    let (role, name) = split_sender(&chat.sender);
    fields.insert("sender".to_string(), Value::Text(name.to_string()));
    if let Some(r) = role {
        fields.insert("sender_role".to_string(), Value::Text(r.to_string()));
    }
    fields.insert(
        "date".to_string(),
        Value::Text(format!("{} {}", chat.date, chat.time)),
    );
    fields.insert("text".to_string(), Value::Text(chat.text.clone()));
    let time = date_ptbr(&format!("{} {}", chat.date, chat.time), date_ctx)
        .ok()
        .map(|p| TimeRef::Instant { at: p.datetime() });
    let mentions = extract_mentions(&chat.text, date_ctx)
        .into_iter()
        .map(|m| m.to_entity_mention())
        .collect();
    Item {
        id: stable_id(
            "it",
            &[
                source_id,
                "chat",
                &chat.start.to_string(),
                &chat.end.to_string(),
            ],
        ),
        kind: ItemKind::Event,
        source_id: source_id.to_string(),
        locator: Locator::ByteRange {
            start: chat.start,
            end: chat.end,
        },
        fields,
        time,
        entity_mentions: mentions,
    }
}

/// `Compras - Rafael` → (`Compras`, `Rafael`); `Rafael (Compras)` → (`Compras`, `Rafael`).
fn split_sender(sender: &str) -> (Option<&str>, &str) {
    if let Some((role, name)) = sender
        .split_once(" - ")
        .or_else(|| sender.split_once(" \u{2013} "))
    {
        return (Some(role.trim()), name.trim());
    }
    if let Some((name, role)) = sender.split_once(" (") {
        return (Some(role.trim_end_matches(')').trim()), name.trim());
    }
    (None, sender.trim())
}

#[cfg(test)]
mod tests {
    use super::*;

    const DUMP: &str = "--- E-MAIL 1 ---\nDe: Rafael Compras <compras@example.com>\nPara: financeiro@example.com\nData: 05/03/2025 09:12\nAssunto: NF 088231 - Empório Bela Vista\n\nBom dia,\n\nsegue a NF 088231 no valor de R$ 17.380,00 com vencimento em 20/03/2025.\n[anexo: nf_088231.pdf]\n\nAtt,\nRafael\n\n--- E-MAIL 2 ---\nFrom: Ana <ana@example.com>\nTo: compras@example.com\nDate: Thu, 06 Mar 2025 10:00:00 -0300\nSubject: Re: NF 088231\n\nRecebido, vamos pagar no boleto do Banco 237.\n";

    #[test]
    fn email_blocks_become_events_and_spans() {
        let items = parse_txt_inbox(DUMP, "src_mail", &InboxContext::default());
        let events: Vec<&Item> = items.iter().filter(|i| i.kind == ItemKind::Event).collect();
        assert_eq!(events.len(), 2);
        let first = events[0];
        assert_eq!(
            first.field("from").and_then(Value::as_text),
            Some("Rafael Compras <compras@example.com>")
        );
        assert_eq!(
            first.field("subject").and_then(Value::as_text),
            Some("NF 088231 - Empório Bela Vista")
        );
        assert_eq!(
            first.field("sender_domain").and_then(Value::as_text),
            Some("example.com")
        );
        assert_eq!(
            first.field("attachments").and_then(Value::as_text),
            Some("nf_088231.pdf")
        );
        assert_eq!(
            first.field("block_index").and_then(Value::as_integer),
            Some(1)
        );
        assert!(matches!(first.time, Some(TimeRef::Instant { .. })));
        assert!(first
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("88231")));
        assert!(first
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("1738000")));
        assert!(first
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("2025-03-20")));
        let spans: Vec<&Item> = items
            .iter()
            .filter(|i| i.kind == ItemKind::TextSpan)
            .collect();
        assert!(spans.len() >= 4, "{}", spans.len());
        for s in &spans {
            if let Locator::ByteRange { start, end } = s.locator {
                assert_eq!(&DUMP[start..end], s.text().unwrap());
            } else {
                panic!("span locator");
            }
        }
        let second = events[1];
        assert_eq!(
            second.field("block_index").and_then(Value::as_integer),
            Some(2)
        );
        assert!(
            matches!(second.time, Some(TimeRef::Instant { at }) if at.format("%Y-%m-%d %H:%M").to_string() == "2025-03-06 10:00")
        );
        assert!(second
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("237")));
    }

    #[test]
    fn implicit_blocks_from_headers() {
        let dump = "From: a@example.com\nSubject: um\n\ncorpo um\n\nFrom: b@example.com\nSubject: dois\n\ncorpo dois\n";
        let items = parse_txt_inbox(dump, "s", &InboxContext::default());
        let events: Vec<&Item> = items.iter().filter(|i| i.kind == ItemKind::Event).collect();
        assert_eq!(events.len(), 2);
        assert_eq!(events[0].text(), Some("corpo um"));
        assert_eq!(events[1].text(), Some("corpo dois"));
    }

    #[test]
    fn whatsapp_export_with_and_without_year() {
        let chat = "[05/03 14:30] Compras - Rafael: NF 088231 vence amanhã, R$ 1.234,56\n[05/03 14:31] Financeiro - Ana: ok, pago\ncontinuação da mensagem\n06/03/2025 09:00 - Rafael (Compras): boleto 23793.38128 60007.827136 95000.063305 1 10000000123456\n[28/12/24, 09:15:00] Ana: feliz natal\n";
        let ctx = InboxContext {
            reference_year: Some(2025),
            reference_date: NaiveDate::from_ymd_opt(2025, 3, 31),
        };
        let items = parse_txt_inbox(chat, "src_chat", &ctx);
        assert_eq!(items.len(), 4);
        assert!(items.iter().all(|i| i.kind == ItemKind::Event));
        assert_eq!(
            items[0].field("sender").and_then(Value::as_text),
            Some("Rafael")
        );
        assert_eq!(
            items[0].field("sender_role").and_then(Value::as_text),
            Some("Compras")
        );
        assert!(
            matches!(items[0].time, Some(TimeRef::Instant { at }) if at.format("%Y-%m-%d %H:%M").to_string() == "2025-03-05 14:30")
        );
        assert!(items[0]
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("88231")));
        assert_eq!(items[1].text(), Some("ok, pago\ncontinuação da mensagem"));
        assert_eq!(
            items[2].field("sender_role").and_then(Value::as_text),
            Some("Compras")
        );
        assert!(items[2]
            .entity_mentions
            .iter()
            .any(|m| m.kind == dm_core::EntityKind::Boleto));
        assert!(
            matches!(items[3].time, Some(TimeRef::Instant { at }) if at.format("%Y-%m-%d").to_string() == "2024-12-28")
        );
        for i in &items {
            if let Locator::ByteRange { start, end } = i.locator {
                assert!(start < end && end <= chat.len());
            }
        }
    }

    #[test]
    fn markers_and_headers() {
        assert!(is_block_marker("--- E-MAIL 1 ---"));
        assert!(is_block_marker("=== Mensagem 12 ==="));
        assert!(is_block_marker("----- Original Message -----"));
        assert!(is_block_marker("E-mail 3"));
        assert!(!is_block_marker("--- totals ---"));
        assert!(!is_block_marker("De: alguém"));
        assert_eq!(marker_index("--- E-MAIL 7 ---"), Some(7));
        assert_eq!(
            header_field("Assunto: NF 1"),
            Some((HeaderKey::Subject, "NF 1"))
        );
        assert_eq!(
            header_field("*From:* x").map(|(k, _)| k),
            Some(HeaderKey::From)
        );
        assert_eq!(header_field("Nota: x"), None);
        assert_eq!(
            attachment_markers("[anexo: a.pdf, b.xlsx]\n<<c.pdf>>\nAnexos: d.pdf"),
            vec!["a.pdf", "b.xlsx", "c.pdf", "d.pdf"]
        );
        assert_eq!(
            split_sender("Compras - Rafael"),
            (Some("Compras"), "Rafael")
        );
        assert_eq!(
            split_sender("Rafael (Compras)"),
            (Some("Compras"), "Rafael")
        );
        assert_eq!(split_sender("Rafael"), (None, "Rafael"));
        assert_eq!(
            sender_domain("Rafael <r@example.com>"),
            Some("example.com".into())
        );
        assert_eq!(sender_domain("sem email"), None);
    }

    #[test]
    fn loose_text_is_one_event() {
        let items = parse_txt_inbox(
            "apenas uma anotação sobre a NF 12\n\nsegunda parte\n",
            "s",
            &InboxContext::default(),
        );
        let events: Vec<&Item> = items.iter().filter(|i| i.kind == ItemKind::Event).collect();
        assert_eq!(events.len(), 1);
        assert!(events[0]
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("12")));
        assert!(parse_txt_inbox("", "s", &InboxContext::default()).is_empty());
    }
}
