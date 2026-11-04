//! `.eml` messages and `mbox` mailboxes (mailparse), producing the same items as the inbox
//! parser: one `event` per message plus `text_span`s for the body paragraphs.

use dm_core::{stable_id, EntityMention, Item, ItemKind, Locator, TimeRef, Value};
use dm_normalize::DateContext;
use indexmap::IndexMap;
use mailparse::{DispositionType, MailHeaderMap, ParsedMail};

use crate::inbox::InboxContext;
use crate::mentions::extract_mentions;
use crate::IngestError;

/// Parses one RFC 5322 message.
pub fn parse_eml(
    bytes: &[u8],
    source_id: &str,
    ctx: &InboxContext,
) -> Result<Vec<Item>, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let parsed = mailparse::parse_mail(bytes).map_err(|e| IngestError::Parse {
        format: "eml",
        detail: e.to_string(),
    })?;
    Ok(message_items(
        &parsed,
        0,
        bytes.len(),
        source_id,
        &ctx.date_context(),
    ))
}

/// Parses an mbox file: messages separated by `From ` lines at column zero.
pub fn parse_mbox(
    bytes: &[u8],
    source_id: &str,
    ctx: &InboxContext,
) -> Result<Vec<Item>, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let mut items = Vec::new();
    let date_ctx = ctx.date_context();
    for (start, end) in mbox_ranges(bytes) {
        let slice = &bytes[start..end];
        // Skip the "From " envelope line itself.
        let body_start = slice
            .iter()
            .position(|b| *b == b'\n')
            .map_or(slice.len(), |p| p + 1);
        let message = &slice[body_start..];
        if message.iter().all(u8::is_ascii_whitespace) {
            continue;
        }
        match mailparse::parse_mail(message) {
            Ok(parsed) => items.extend(message_items(
                &parsed,
                start + body_start,
                end,
                source_id,
                &date_ctx,
            )),
            Err(e) => {
                let mut fields = IndexMap::new();
                fields.insert("error".to_string(), Value::Text(e.to_string()));
                fields.insert(
                    "text".to_string(),
                    Value::Text(String::from_utf8_lossy(message).to_string()),
                );
                items.push(Item {
                    id: stable_id("it", &[source_id, "mbox_error", &start.to_string()]),
                    kind: ItemKind::Event,
                    source_id: source_id.to_string(),
                    locator: Locator::ByteRange { start, end },
                    fields,
                    time: None,
                    entity_mentions: Vec::new(),
                });
            }
        }
    }
    if items.is_empty() {
        return Err(IngestError::Unrecognized("mbox sem mensagens".to_string()));
    }
    Ok(items)
}

/// Byte ranges of the messages in an mbox (each starting at its `From ` line).
fn mbox_ranges(bytes: &[u8]) -> Vec<(usize, usize)> {
    let mut starts = Vec::new();
    if bytes.starts_with(b"From ") {
        starts.push(0);
    }
    let mut i = 0;
    while let Some(p) = bytes[i..].windows(6).position(|w| w == b"\nFrom ") {
        starts.push(i + p + 1);
        i += p + 1;
    }
    if starts.is_empty() {
        return vec![(0, bytes.len())];
    }
    let mut ranges = Vec::with_capacity(starts.len());
    for (k, s) in starts.iter().enumerate() {
        let end = starts.get(k + 1).copied().unwrap_or(bytes.len());
        ranges.push((*s, end));
    }
    ranges
}

fn header(parsed: &ParsedMail<'_>, name: &str) -> Option<String> {
    parsed
        .headers
        .get_first_value(name)
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty())
}

/// Text body: the first `text/plain` part, else the first `text/html` part with tags removed.
fn body_text(parsed: &ParsedMail<'_>) -> String {
    fn walk<'a>(part: &'a ParsedMail<'a>, plain: &mut Option<String>, html: &mut Option<String>) {
        if part.subparts.is_empty() {
            let disposition = part.get_content_disposition();
            if disposition.disposition == DispositionType::Attachment {
                return;
            }
            match part.ctype.mimetype.to_ascii_lowercase().as_str() {
                "text/plain" if plain.is_none() => *plain = part.get_body().ok(),
                "text/html" if html.is_none() => *html = part.get_body().ok(),
                _ => {}
            }
        } else {
            for sub in &part.subparts {
                walk(sub, plain, html);
            }
        }
    }
    let mut plain = None;
    let mut html = None;
    walk(parsed, &mut plain, &mut html);
    plain
        .or_else(|| html.map(|h| strip_html(&h)))
        .unwrap_or_default()
}

/// Removes tags and decodes the common entities; keeps line breaks for block elements.
pub(crate) fn strip_html(html: &str) -> String {
    let mut out = String::with_capacity(html.len());
    let mut in_tag = false;
    let mut tag = String::new();
    for c in html.chars() {
        if in_tag {
            if c == '>' {
                in_tag = false;
                let t = tag.to_ascii_lowercase();
                if t.starts_with("br")
                    || t.starts_with("/p")
                    || t.starts_with("/div")
                    || t.starts_with("/tr")
                    || t.starts_with("/li")
                    || t.starts_with("/h")
                {
                    out.push('\n');
                }
                tag.clear();
            } else {
                tag.push(c);
            }
        } else if c == '<' {
            in_tag = true;
        } else {
            out.push(c);
        }
    }
    let decoded = out
        .replace("&nbsp;", " ")
        .replace("&amp;", "&")
        .replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&quot;", "\"")
        .replace("&#39;", "'");
    decoded
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .collect::<Vec<_>>()
        .join("\n")
}

fn attachments(parsed: &ParsedMail<'_>) -> Vec<String> {
    fn walk<'a>(part: &'a ParsedMail<'a>, out: &mut Vec<String>) {
        let disposition = part.get_content_disposition();
        if disposition.disposition == DispositionType::Attachment {
            if let Some(name) = disposition
                .params
                .get("filename")
                .or_else(|| part.ctype.params.get("name"))
            {
                out.push(name.clone());
            }
        }
        for sub in &part.subparts {
            walk(sub, out);
        }
    }
    let mut out = Vec::new();
    walk(parsed, &mut out);
    out
}

fn message_items(
    parsed: &ParsedMail<'_>,
    start: usize,
    end: usize,
    source_id: &str,
    date_ctx: &DateContext,
) -> Vec<Item> {
    let mut items = Vec::new();
    let event_id = stable_id(
        "it",
        &[source_id, "eml", &start.to_string(), &end.to_string()],
    );
    let mut fields: IndexMap<String, Value> = IndexMap::new();
    let from = header(parsed, "From").unwrap_or_default();
    for (name, key) in [
        ("From", "from"),
        ("To", "to"),
        ("Cc", "cc"),
        ("Date", "date"),
        ("Subject", "subject"),
        ("Message-ID", "message_id"),
    ] {
        if let Some(v) = header(parsed, name) {
            fields.insert(key.to_string(), Value::Text(v));
        }
    }
    let body = body_text(parsed).replace("\r\n", "\n");
    let body = body.trim().to_string();
    let attached = attachments(parsed);
    if !attached.is_empty() {
        fields.insert("attachments".to_string(), Value::Text(attached.join("; ")));
    }
    fields.insert("text".to_string(), Value::from(body.as_str()));
    if let Some(addr) = from.rsplit('@').next().filter(|_| from.contains('@')) {
        let domain: String = addr
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
    let time = header(parsed, "Date").and_then(|d| parse_date(&d, date_ctx));
    let mut mentions: Vec<EntityMention> = Vec::new();
    let subject = header(parsed, "Subject").unwrap_or_default();
    for m in extract_mentions(&subject, date_ctx)
        .into_iter()
        .chain(extract_mentions(&from, date_ctx))
    {
        let mut em = m.to_entity_mention();
        em.span = None;
        mentions.push(em);
    }
    mentions.extend(
        extract_mentions(&body, date_ctx)
            .into_iter()
            .map(|m| m.to_entity_mention()),
    );
    items.push(Item {
        id: event_id.clone(),
        kind: ItemKind::Event,
        source_id: source_id.to_string(),
        locator: Locator::ByteRange { start, end },
        fields,
        time: time.clone(),
        entity_mentions: mentions,
    });
    // Paragraph spans: byte ranges are relative to the decoded body, which the locator cannot
    // point into (the raw message is encoded), so they point at the whole message and carry
    // the paragraph index.
    for (i, para) in body
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
            locator: Locator::ByteRange { start, end },
            fields,
            time: time.clone(),
            entity_mentions: extract_mentions(para, date_ctx)
                .into_iter()
                .map(|m| m.to_entity_mention())
                .collect(),
        });
    }
    items
}

fn parse_date(value: &str, ctx: &DateContext) -> Option<TimeRef> {
    if let Ok(dt) = chrono::DateTime::parse_from_rfc2822(value) {
        return Some(TimeRef::Instant {
            at: dt.naive_local(),
        });
    }
    if let Ok(secs) = mailparse::dateparse(value) {
        return chrono::DateTime::from_timestamp(secs, 0)
            .map(|dt| TimeRef::Instant { at: dt.naive_utc() });
    }
    let p = dm_normalize::date_ptbr(value, ctx).ok()?;
    Some(match p.time {
        Some(_) => TimeRef::Instant { at: p.datetime() },
        None => TimeRef::Date { date: p.date },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    const EML: &str = "From: Rafael <compras@example.com>\r\nTo: financeiro@example.com\r\nSubject: NF 088231\r\nDate: Thu, 06 Mar 2025 10:00:00 -0300\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSegue a NF 088231 de R$ 18.450,00.\r\n\r\nVencimento 20/03/2025.\r\n--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"nf_088231.pdf\"\r\n\r\n%PDF-fake\r\n--b1--\r\n";

    #[test]
    fn eml_with_attachment() {
        let items = parse_eml(EML.as_bytes(), "src_eml", &InboxContext::default()).unwrap();
        let event = &items[0];
        assert_eq!(event.kind, ItemKind::Event);
        assert_eq!(
            event.field("subject").and_then(Value::as_text),
            Some("NF 088231")
        );
        assert_eq!(
            event.field("attachments").and_then(Value::as_text),
            Some("nf_088231.pdf")
        );
        assert_eq!(
            event.field("sender_domain").and_then(Value::as_text),
            Some("example.com")
        );
        assert!(event.text().unwrap().contains("Segue a NF 088231"));
        assert!(
            matches!(event.time, Some(TimeRef::Instant { at }) if at.format("%H:%M").to_string() == "10:00")
        );
        assert!(event
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("1845000")));
        assert_eq!(
            items
                .iter()
                .filter(|i| i.kind == ItemKind::TextSpan)
                .count(),
            2
        );
    }

    #[test]
    fn html_only_body_is_stripped() {
        let eml = "From: a@example.com\r\nSubject: x\r\nContent-Type: text/html\r\n\r\n<html><body><p>Ol&aacute; <b>NF 12</b></p><p>R$ 10,00</p></body></html>\r\n";
        let items = parse_eml(eml.as_bytes(), "s", &InboxContext::default()).unwrap();
        assert_eq!(items[0].text(), Some("Ol&aacute; NF 12\nR$ 10,00"));
        assert_eq!(strip_html("a<br>b &amp; c"), "a\nb & c");
    }

    #[test]
    fn mbox_splits_messages() {
        let mbox = format!("From compras@example.com Thu Mar  6 10:00:00 2025\n{}\nFrom ana@example.com Fri Mar  7 10:00:00 2025\nFrom: ana@example.com\nSubject: Re: NF 088231\n\nok\n", EML.replace("\r\n", "\n"));
        let items = parse_mbox(mbox.as_bytes(), "src_mbox", &InboxContext::default()).unwrap();
        let events: Vec<&Item> = items.iter().filter(|i| i.kind == ItemKind::Event).collect();
        assert_eq!(events.len(), 2);
        assert_eq!(events[1].text(), Some("ok"));
        assert_eq!(mbox_ranges(b"x").len(), 1);
        assert!(parse_mbox(b"", "s", &InboxContext::default()).is_err());
    }
}
