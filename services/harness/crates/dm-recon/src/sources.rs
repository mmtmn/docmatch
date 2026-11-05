//! Reads a case folder: classifies every file by its columns (receivables ledger, bank
//! statement, inbox), extracts titles, bank lines, e-mails and chat messages, and resolves
//! customers across ledgers into entities (tax id first, then name).
//!
//! Every format the ingest stage reads goes through the same classification: CSV,
//! spreadsheets, tables of DOCX/ODT documents and HTML pages and records of JSON/XML files by
//! their inferred columns, OFX and PDF statements as bank statements, CNAB collection files as
//! receivables ledgers, e-mails and chats (text dumps, `.eml`, `.mbox`, `.msg`, a thread pasted
//! in a document) as the inbox, ZIP and 7z entries and `.msg` attachments as files of their
//! own. A file that does not take part in the run is never dropped silently: the reason goes
//! to [`Dataset::warnings`].
//!
//! An incidental document must not change the run: a table in a DOCX, ODT or HTML page is a
//! bank statement only with the signs of one (a credit/debit side, a balance or enough lines),
//! the statement is the table with the most lines, and the inbox is every message source
//! together (deduplicated, in time order); documents holding messages make the inbox only
//! when no message source does. Each file is read on its own: a parser failure is that file's
//! warning, and a cancelled run stops between files.

use std::borrow::Cow;
use std::collections::{BTreeMap, BTreeSet, HashSet};
use std::path::Path;

use chrono::{Datelike, Months, NaiveDate, NaiveDateTime};
use dm_core::{ColumnRole, Item, Money, Table, TimeRef, Value};
use dm_ingest::{parse_bytes, InboxContext, ParsedSource, SourceKind};
use dm_normalize::{doc_number, fold_ascii, money_ptbr, name_key, tax_id, TaxIdKind};
use sha2::{Digest, Sha256};

use crate::config::EngineConfig;
use crate::golden::Period;
use crate::ReconError;

/// Files that are part of a case folder but not inputs.
const SKIPPED_FILES: [&str; 4] = [
    "expected.yaml",
    "expected.yml",
    "result.json",
    "relatorio.md",
];

/// A receivables ledger (one ERP export).
#[derive(Clone, Debug)]
pub struct Ledger {
    /// Source key (`protheus`, `omie`, `ledger2`).
    pub key: String,
    /// File name.
    pub file: String,
    /// Label for texts (`unidade Norte`).
    pub label: String,
    /// Prefix of its title ids.
    pub prefix: String,
    /// True for the ledger of the operation that owns the bank account.
    pub main: bool,
}

/// One open receivable title.
#[derive(Clone, Debug)]
pub struct Title {
    /// Id (`P01`).
    pub id: String,
    /// Index of its ledger.
    pub ledger: usize,
    /// Zero-based row in the ledger file.
    pub row: usize,
    /// Document reference as written.
    pub reference: String,
    /// Customer name as written.
    pub customer: String,
    /// Tax id digits.
    pub tax_id: Option<String>,
    /// True when the tax id's check digits fail.
    pub tax_id_invalid: bool,
    /// Boleto number without leading zeros.
    pub nosso_numero: Option<String>,
    /// Boleto number as written.
    pub nosso_numero_raw: Option<String>,
    /// Title kind, folded (`PRODUTO`, `SERVICO`), empty when the ledger has none.
    pub kind: String,
    /// Due date.
    pub due: NaiveDate,
    /// Face amount.
    pub amount: Money,
    /// Index of the resolved customer.
    pub entity: usize,
}

/// Credit or debit.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Side {
    /// Money in.
    Credit,
    /// Money out.
    Debit,
}

impl Side {
    /// `C` or `D`.
    pub fn code(self) -> &'static str {
        match self {
            Side::Credit => "C",
            Side::Debit => "D",
        }
    }
}

/// One bank statement line.
#[derive(Clone, Debug)]
pub struct BankLine {
    /// One-based position in the statement.
    pub n: usize,
    /// Posting date.
    pub date: NaiveDate,
    /// Description as written.
    pub description: String,
    /// Description folded (uppercase, no accents).
    pub folded: String,
    /// Document column without leading zeros.
    pub document: Option<String>,
    /// Absolute amount.
    pub amount: Money,
    /// Credit or debit.
    pub side: Side,
}

/// One e-mail of the inbox.
#[derive(Clone, Debug)]
pub struct Email {
    /// One-based position in the inbox.
    pub n: usize,
    /// The file (or archive entry, or attachment) it came from.
    pub file: String,
    /// Sender address, lowercase.
    pub from: String,
    /// Sender domain, lowercase.
    pub domain: String,
    /// When it was sent.
    pub at: Option<NaiveDateTime>,
    /// Subject.
    pub subject: String,
    /// Body.
    pub body: String,
    /// Subject and body folded.
    pub folded: String,
}

/// One chat message.
#[derive(Clone, Debug)]
pub struct ChatMessage {
    /// The file it came from.
    pub file: String,
    /// When it was sent.
    pub at: NaiveDateTime,
    /// Author.
    pub author: String,
    /// Text.
    pub text: String,
    /// Text folded.
    pub folded: String,
}

/// A customer resolved across ledgers.
#[derive(Clone, Debug, Default)]
pub struct Entity {
    /// Tax id digits, when known.
    pub tax_id: Option<String>,
    /// Titles of the customer, in ledger then row order.
    pub titles: Vec<usize>,
}

/// Everything read from a case folder.
#[derive(Clone, Debug, Default)]
pub struct Dataset {
    /// Case name (folder name).
    pub case: String,
    /// Receivables ledgers.
    pub ledgers: Vec<Ledger>,
    /// Open titles of every ledger.
    pub titles: Vec<Title>,
    /// Bank statement lines.
    pub lines: Vec<BankLine>,
    /// Inbox e-mails.
    pub emails: Vec<Email>,
    /// Chat messages.
    pub chat: Vec<ChatMessage>,
    /// Resolved customers.
    pub entities: Vec<Entity>,
    /// Reporting period.
    pub period: Option<Period>,
    /// Source key → file.
    pub sources: BTreeMap<String, String>,
    /// File → SHA-256.
    pub inputs: BTreeMap<String, String>,
    /// Sum of every ledger's open titles, by ledger index.
    pub ledger_open: Vec<Money>,
    /// Problems with the input files, in pt-BR, each naming the file: parse errors, the
    /// parsers' own warnings and files that did not take part in the run.
    pub warnings: Vec<String>,
}

impl Dataset {
    /// The bank line with number `n`.
    pub fn line(&self, n: usize) -> &BankLine {
        &self.lines[n - 1]
    }

    /// Title index by id.
    pub fn title_index(&self, id: &str) -> Option<usize> {
        self.titles.iter().position(|t| t.id == id)
    }
}

/// Raw chat message before the period (and so the year) is known.
struct RawChat {
    date: String,
    author: String,
    text: String,
}

/// Lines a table of a document needs to be read as a bank statement when it has neither a
/// credit/debit side nor a balance column (a payment schedule in a letter has a few).
const MIN_DOCUMENT_STATEMENT_LINES: usize = 5;

/// A source that brings e-mails or chat messages.
struct InboxSource {
    file: String,
    /// A message format (mailbox or chat dump, `.eml`, `.mbox`, `.msg`), not a document that
    /// holds a pasted thread.
    message: bool,
    items: Vec<Item>,
}

/// What one input file (and everything inside it) brought.
#[derive(Default)]
struct FileOutcome {
    ledgers: Vec<(String, Table)>,
    banks: Vec<(String, Table)>,
    inboxes: Vec<InboxSource>,
    warnings: Vec<String>,
}

/// Reads and classifies every input file of `dir`.
pub fn load_dir(dir: &Path, cfg: &EngineConfig) -> Result<Dataset, ReconError> {
    let mut paths: Vec<std::path::PathBuf> = std::fs::read_dir(dir)
        .map_err(|e| ReconError::Io(format!("{}: {e}", dir.display())))?
        .filter_map(Result::ok)
        .map(|e| e.path())
        .filter(|p| p.is_file())
        .filter(|p| is_input_name(p.file_name().and_then(|n| n.to_str()).unwrap_or_default()))
        .collect();
    paths.sort();

    let mut ds = Dataset {
        case: dir
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("caso")
            .to_string(),
        ..Dataset::default()
    };
    let mut ledger_tables: Vec<(String, Table)> = Vec::new();
    let mut bank_tables: Vec<(String, Table)> = Vec::new();
    let mut inbox_sources: Vec<InboxSource> = Vec::new();

    for path in &paths {
        if dm_ingest::cancel::cancelled() {
            return Err(ReconError::Cancelled);
        }
        let name = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("arquivo")
            .to_string();
        let bytes = std::fs::read(path).map_err(|e| ReconError::Io(format!("{name}: {e}")))?;
        ds.inputs
            .insert(name.clone(), hex::encode(Sha256::digest(&bytes)));
        // One file's failure is that file's warning; the run goes on with the others.
        match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| read_file(&name, &bytes))) {
            Ok(outcome) => {
                ledger_tables.extend(outcome.ledgers);
                bank_tables.extend(outcome.banks);
                inbox_sources.extend(outcome.inboxes);
                ds.warnings.extend(outcome.warnings);
            }
            Err(_) => ds.warnings.push(format!(
                "{name}: falha interna ao ler o arquivo; ele ficou fora desta conciliação"
            )),
        }
    }
    if dm_ingest::cancel::cancelled() {
        return Err(ReconError::Cancelled);
    }

    // Bank first: its dates fix the period and its documents tell which ledger is main. The
    // statement is the table with the most lines (ties go to file order).
    let main_bank = bank_tables
        .iter()
        .enumerate()
        .max_by_key(|(i, (_, t))| (bank_lines(t).len(), std::cmp::Reverse(*i)))
        .map(|(i, _)| i);
    if let Some(b) = main_bank {
        let (file, table) = &bank_tables[b];
        ds.lines = bank_lines(table);
        ds.sources.insert("bank".to_string(), file.clone());
        for (other, other_table) in bank_tables
            .iter()
            .enumerate()
            .filter(|(i, _)| *i != b)
            .map(|(_, x)| x)
        {
            ds.warnings.push(if other == file {
                format!(
                    "{other}: a tabela {} é um segundo extrato bancário; só a tabela {} entra nesta conciliação",
                    other_table.name, table.name
                )
            } else {
                format!("{other}: segundo extrato bancário; só {file} entra nesta conciliação")
            });
        }
    }
    ds.period = period_of(&ds.lines, &ledger_tables);

    let mut used_keys = BTreeSet::new();
    let mut used_prefixes = BTreeSet::new();
    for (i, (file, table)) in ledger_tables.iter().enumerate() {
        let key = ledger_key(file, cfg, &used_keys, i);
        let prefix = ledger_prefix(&key, &used_prefixes);
        let ledger = Ledger {
            label: ledger_label(file, &key),
            key: key.clone(),
            file: file.clone(),
            prefix: prefix.clone(),
            main: false,
        };
        let (titles, open_total) = ledger_titles(table, ds.ledgers.len(), &ledger);
        if titles.is_empty() {
            // A ledger of settled titles (a CNAB return of liquidations, an export of paid
            // invoices) has nothing open to reconcile; it must not become the main ledger.
            ds.warnings.push(format!(
                "{file}: nenhum título em aberto (todos pagos, liquidados, baixados ou rejeitados); o arquivo não entrou como carteira nesta conciliação"
            ));
            continue;
        }
        used_keys.insert(key.clone());
        used_prefixes.insert(prefix);
        ds.sources.insert(key, file.clone());
        ds.ledgers.push(ledger);
        ds.ledger_open.push(open_total);
        ds.titles.extend(titles);
    }
    mark_main_ledger(&mut ds);
    resolve_entities(&mut ds);

    // The inbox: every message source together; documents holding messages only when no
    // message source exists.
    let messages = inbox_sources.iter().any(|x| x.message);
    let (chosen, left_out): (Vec<&InboxSource>, Vec<&InboxSource>) =
        inbox_sources.iter().partition(|x| x.message || !messages);
    let files: Vec<&str> = chosen.iter().map(|x| x.file.as_str()).collect();
    for doc in &left_out {
        ds.warnings.push(format!(
            "{}: tem mensagens, mas a caixa de entrada desta conciliação vem de {}; o documento ficou fora dela",
            doc.file,
            files.join(", ")
        ));
    }
    // `ap_inbox` names the source with the most messages (ties go to file order).
    if let Some(main) = chosen
        .iter()
        .enumerate()
        .max_by_key(|(i, x)| {
            (
                x.items.iter().filter(|it| is_message(it)).count(),
                std::cmp::Reverse(*i),
            )
        })
        .map(|(_, x)| x)
    {
        ds.sources.insert("ap_inbox".to_string(), main.file.clone());
    }
    let year = ds.period.map_or(2000, |p| p.end.year());
    let mut email_runs: Vec<Vec<(Email, Option<String>)>> = Vec::new();
    let mut chat_runs: Vec<Vec<ChatMessage>> = Vec::new();
    for x in &chosen {
        let (emails, raw_chat) = inbox(&x.file, &x.items);
        email_runs.push(emails);
        chat_runs.push(
            raw_chat
                .into_iter()
                .filter_map(|c| {
                    let at = chat_datetime(&c.date, year)?;
                    Some(ChatMessage {
                        file: x.file.clone(),
                        at,
                        folded: fold_ascii(&c.text),
                        author: c.author,
                        text: c.text,
                    })
                })
                .collect(),
        );
    }
    ds.emails = merge_emails(email_runs);
    let mut seen_chat = HashSet::new();
    ds.chat = merge_by_time(
        chat_runs,
        |c| Some(c.at),
        |c| format!("{}\u{0}{}\u{0}{}", c.at, c.author, collapsed(&c.folded)),
    )
    .into_iter()
    .filter(|(_, key)| seen_chat.insert(key.clone()))
    .map(|(c, _)| c)
    .collect();
    Ok(ds)
}

/// Reads one input file: its tables by role, its messages and its warnings.
fn read_file(name: &str, bytes: &[u8]) -> FileOutcome {
    let mut out = FileOutcome::default();
    let ctx = InboxContext::default();
    let parsed = match parse_bytes(name, bytes, &ctx) {
        Ok(p) => p,
        Err(e) => {
            out.warnings.push(format!("{name}: {e}"));
            return out;
        }
    };
    for src in flatten(parsed) {
        // Entries of an archive follow the same rules as the files of the folder: a zipped
        // case folder brings its README and answer key along.
        let base = src.name.rsplit('/').next().unwrap_or(&src.name);
        if src.name.contains('/') && !is_input_name(base) {
            continue;
        }
        out.warnings.extend(src.warnings.iter().cloned());
        let kind = src.sniff.kind;
        if matches!(kind, SourceKind::Zip | SourceKind::SevenZ) {
            // A container: its entries come next, each with its own outcome.
            continue;
        }
        let mut used = false;
        let mut unused_tables: Vec<&str> = Vec::new();
        // Tables of any format (a DOCX or an HTML page has them next to its text).
        for table in &src.tables {
            match classify_source_table(kind, table) {
                TableRole::Ledger => {
                    // A CNAB collection file has one row per movement; the ledger wants one
                    // per título.
                    let table = if matches!(kind, SourceKind::Cnab240 | SourceKind::Cnab400) {
                        dm_ingest::cnab::titles_view(table)
                    } else {
                        table.clone()
                    };
                    out.ledgers.push((src.name.clone(), table));
                }
                TableRole::Bank => out.banks.push((src.name.clone(), table.clone())),
                TableRole::Unknown => {
                    unused_tables.push(&table.name);
                    continue;
                }
            }
            used = true;
        }
        // A document's tables that are neither ledger nor statement (an installment plan,
        // new bank details) are part of what it says: its text is read again with them.
        let items: Cow<'_, [Item]> = if src.text_parts.is_empty() || unused_tables.is_empty() {
            Cow::Borrowed(&src.items)
        } else {
            let keep: BTreeSet<&str> = unused_tables.iter().copied().collect();
            let text = dm_ingest::source::document_text(&src.text_parts, |t| keep.contains(t));
            Cow::Owned(dm_ingest::source::text_items(&text, &src.source_id, &ctx))
        };
        // Only e-mails and chat messages make an inbox (a text dump, an .eml, an Outlook
        // message, a document holding a pasted thread); notes or a README do not.
        if items.iter().any(is_message) {
            out.inboxes.push(InboxSource {
                file: src.name.clone(),
                message: message_kind(kind),
                items: items.into_owned(),
            });
            used = true;
        }
        if used {
            if !unused_tables.is_empty() {
                out.warnings.push(format!(
                    "{}: tabela(s) {} não reconhecida(s) como títulos a receber ou extrato bancário; ficaram fora desta conciliação",
                    src.name,
                    unused_tables.join(", ")
                ));
            }
            continue;
        }
        if !src.tables.is_empty() || !src.items.is_empty() {
            out.warnings.push(format!(
                "{}: lido ({}), mas nada foi reconhecido como títulos a receber, extrato bancário ou caixa de entrada; o arquivo não entrou nesta conciliação",
                src.name,
                kind.label_ptbr()
            ));
        } else if src.warnings.is_empty() {
            out.warnings.push(format!(
                "{}: nenhum conteúdo aproveitável; o arquivo não entrou nesta conciliação",
                src.name
            ));
        }
    }
    out
}

/// Formats made of messages: mailbox and chat dumps, e-mails, Outlook messages.
fn message_kind(kind: SourceKind) -> bool {
    matches!(
        kind,
        SourceKind::TxtInbox
            | SourceKind::Text
            | SourceKind::Eml
            | SourceKind::Mbox
            | SourceKind::Msg
    )
}

/// Documents written for people, whose tables may be anything.
fn document_kind(kind: SourceKind) -> bool {
    matches!(kind, SourceKind::Docx | SourceKind::Odt | SourceKind::Html)
}

/// [`classify_for`], and a document's table is a statement only with the signs of one.
fn classify_source_table(kind: SourceKind, t: &Table) -> TableRole {
    match classify_for(kind, t) {
        TableRole::Bank if document_kind(kind) && !statement_signs(t) => TableRole::Unknown,
        role => role,
    }
}

/// What a bank statement has and a payment schedule or an invoice table does not: a
/// credit/debit side, a balance, or [`MIN_DOCUMENT_STATEMENT_LINES`] lines.
fn statement_signs(t: &Table) -> bool {
    side_column(t).is_some()
        || t.columns.iter().any(|c| {
            c.role == ColumnRole::Amount
                && matches!(c.subrole.as_deref(), Some("credit" | "debit" | "balance"))
        })
        || bank_lines(t).len() >= MIN_DOCUMENT_STATEMENT_LINES
}

/// The e-mail address of a sender (`Ana <ana@x.com>` → `ana@x.com`), lowercase.
fn address_of(from: &str) -> String {
    let inner = from
        .rsplit_once('<')
        .and_then(|(_, rest)| rest.split_once('>'))
        .map_or(from, |(a, _)| a);
    inner.trim().to_ascii_lowercase()
}

fn collapsed(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// The inbox from every source's e-mails: each source keeps its order, sources interleave by
/// time, and a message seen before (same Message-ID, or same sender, minute, subject and
/// body) is not counted twice.
fn merge_emails(runs: Vec<Vec<(Email, Option<String>)>>) -> Vec<Email> {
    let mut seen_ids: HashSet<String> = HashSet::new();
    let mut seen_content: HashSet<String> = HashSet::new();
    let merged = merge_by_time(
        runs,
        |(e, _)| e.at,
        |(e, _)| {
            let body = hex::encode(Sha256::digest(collapsed(&fold_ascii(&e.body)).as_bytes()));
            format!(
                "{}\u{0}{}\u{0}{}\u{0}{body}",
                address_of(&e.from),
                e.at.map(|t| t.format("%Y-%m-%dT%H:%M").to_string())
                    .unwrap_or_default(),
                collapsed(&fold_ascii(&e.subject))
            )
        },
    );
    let mut out = Vec::new();
    for ((mut e, id), content) in merged {
        let id = id
            .map(|i| i.trim().trim_matches(['<', '>']).to_ascii_lowercase())
            .filter(|i| !i.is_empty());
        if id.as_ref().is_some_and(|i| seen_ids.contains(i)) || seen_content.contains(&content) {
            continue;
        }
        if let Some(i) = id {
            seen_ids.insert(i);
        }
        seen_content.insert(content);
        e.n = out.len() + 1;
        out.push(e);
    }
    out
}

/// Interleaves runs by time: the earliest head goes first (ties to the earlier run), so a run
/// keeps its own order; a message without a time takes its run's last known time. Returns
/// each item with its deduplication key, for the caller to drop repeats.
fn merge_by_time<T, K: Ord + Copy>(
    runs: Vec<Vec<T>>,
    time: impl Fn(&T) -> Option<K>,
    key: impl Fn(&T) -> String,
) -> Vec<(T, String)>
where
    T: Clone,
{
    // The time each item sorts by: its own, else the last one before it in its run.
    let timed: Vec<Vec<(Option<K>, T)>> = runs
        .into_iter()
        .map(|run| {
            let mut last: Option<K> = None;
            run.into_iter()
                .map(|item| {
                    if let Some(t) = time(&item) {
                        last = Some(t);
                    }
                    (last, item)
                })
                .collect()
        })
        .collect();
    let mut heads = vec![0usize; timed.len()];
    let mut out = Vec::new();
    loop {
        let next = (0..timed.len())
            .filter(|r| heads[*r] < timed[*r].len())
            .min_by_key(|r| (timed[*r][heads[*r]].0, *r));
        let Some(r) = next else {
            break;
        };
        let item = timed[r][heads[r]].1.clone();
        heads[r] += 1;
        let k = key(&item);
        out.push((item, k));
    }
    out
}

/// True for a file (or archive entry) that is an input of the case: not hidden, not the
/// answer key, a previous result or a Markdown note.
fn is_input_name(name: &str) -> bool {
    let lower = name.to_ascii_lowercase();
    let markdown = std::path::Path::new(name)
        .extension()
        .and_then(|x| x.to_str())
        .is_some_and(|x| x.eq_ignore_ascii_case("md") || x.eq_ignore_ascii_case("markdown"));
    !name.starts_with('.') && !SKIPPED_FILES.contains(&lower.as_str()) && !markdown
}

/// An e-mail or a chat message (what [`inbox`] reads), as opposed to a plain paragraph.
fn is_message(item: &Item) -> bool {
    item.fields.contains_key("from") || item.fields.contains_key("sender")
}

/// A parsed file and the files inside it (ZIP entries), in order.
fn flatten(mut src: ParsedSource) -> Vec<ParsedSource> {
    let entries = std::mem::take(&mut src.entries);
    let mut out = vec![src];
    for e in entries {
        out.extend(flatten(e));
    }
    out
}

enum TableRole {
    Ledger,
    Bank,
    Unknown,
}

/// Formats whose meaning is known from the format itself are not guessed from the columns:
/// an OFX or a PDF statement is a bank statement (an OFX names the payee, which would read
/// as a ledger), a CNAB collection file is a receivables ledger and its payments batch is
/// not, and SPED books have nothing this engine reconciles yet.
fn classify_for(kind: SourceKind, t: &Table) -> TableRole {
    match kind {
        SourceKind::Ofx | SourceKind::Pdf => {
            if has(t, ColumnRole::Amount) && has(t, ColumnRole::Date) {
                TableRole::Bank
            } else {
                TableRole::Unknown
            }
        }
        SourceKind::Cnab240 | SourceKind::Cnab400 => {
            if dm_ingest::cnab::is_collection_table(t) {
                TableRole::Ledger
            } else {
                TableRole::Unknown
            }
        }
        SourceKind::Sped => TableRole::Unknown,
        _ => classify(t),
    }
}

fn has(t: &Table, role: ColumnRole) -> bool {
    t.columns.iter().any(|c| c.role == role)
}

fn col(t: &Table, role: ColumnRole) -> Option<usize> {
    t.columns.iter().position(|c| c.role == role)
}

fn col_sub(t: &Table, role: ColumnRole, sub: &str) -> Option<usize> {
    t.columns
        .iter()
        .position(|c| c.role == role && c.subrole.as_deref() == Some(sub))
}

fn classify(t: &Table) -> TableRole {
    let amount = has(t, ColumnRole::Amount);
    let date = has(t, ColumnRole::Date);
    if amount
        && date
        && has(t, ColumnRole::Name)
        && (has(t, ColumnRole::TaxId) || has(t, ColumnRole::DocumentId))
    {
        TableRole::Ledger
    } else if amount && date && (has(t, ColumnRole::FreeText) || side_column(t).is_some()) {
        TableRole::Bank
    } else {
        TableRole::Unknown
    }
}

fn text_of(v: &Value) -> String {
    match v {
        Value::Null => String::new(),
        Value::Text(s) => s.trim().to_string(),
        other => other.render(),
    }
}

fn money_of(v: &Value) -> Option<Money> {
    match v {
        Value::Money(m) => Some(*m),
        Value::Text(s) => money_ptbr(s).ok(),
        Value::Integer(i) => Some(Money::from_reais(*i)),
        _ => None,
    }
}

fn date_of(v: &Value) -> Option<NaiveDate> {
    v.as_date()
}

fn canonical_digits(s: &str) -> Option<String> {
    let digits: String = s.chars().filter(char::is_ascii_digit).collect();
    if digits.is_empty() {
        return None;
    }
    let trimmed = digits.trim_start_matches('0');
    Some(if trimmed.is_empty() {
        "0".to_string()
    } else {
        trimmed.to_string()
    })
}

/// The column holding C/D (or crédito/débito) markers, if any.
fn side_column(t: &Table) -> Option<usize> {
    t.columns.iter().enumerate().find_map(|(i, c)| {
        if !matches!(
            c.role,
            ColumnRole::Kind | ColumnRole::Status | ColumnRole::Unknown
        ) {
            return None;
        }
        let mut seen = 0usize;
        let ok = t.rows.iter().all(|r| {
            let v = fold_ascii(&text_of(&r.values[i]));
            seen += usize::from(!v.is_empty());
            v.is_empty() || side_of(&v).is_some()
        });
        (ok && seen > 0).then_some(i)
    })
}

fn side_of(folded: &str) -> Option<Side> {
    match folded {
        "C" | "CR" | "CRED" | "CREDITO" | "CREDIT" | "+" => Some(Side::Credit),
        "D" | "DB" | "DEB" | "DEBITO" | "DEBIT" | "-" => Some(Side::Debit),
        _ => None,
    }
}

fn bank_lines(t: &Table) -> Vec<BankLine> {
    let date_col = col(t, ColumnRole::Date);
    let text_col = col(t, ColumnRole::FreeText);
    // An OFX transaction id (FITID) is not a document number.
    let doc_col = t
        .columns
        .iter()
        .position(|c| c.role == ColumnRole::DocumentId && c.subrole.as_deref() != Some("fitid"));
    // A statement that names the payer in its own column (OFX `NAME`) gets it appended to
    // the description, where the name rules look for it.
    let name_col = col(t, ColumnRole::Name);
    let amount_col = col(t, ColumnRole::Amount);
    let side_col = side_column(t);
    let mut out = Vec::new();
    for record in &t.rows {
        let get = |c: Option<usize>| c.and_then(|c| record.values.get(c));
        let (Some(date), Some(amount)) = (
            get(date_col).and_then(date_of),
            get(amount_col).and_then(money_of),
        ) else {
            continue;
        };
        let mut description = get(text_col).map(text_of).unwrap_or_default();
        if let Some(name) = get(name_col).map(text_of).filter(|n| !n.is_empty()) {
            if !fold_ascii(&description).contains(&fold_ascii(&name)) {
                if !description.is_empty() {
                    description.push(' ');
                }
                description.push_str(&name);
            }
        }
        let side = get(side_col)
            .and_then(|v| side_of(&fold_ascii(&text_of(v))))
            .unwrap_or(if amount.is_negative() {
                Side::Debit
            } else {
                Side::Credit
            });
        out.push(BankLine {
            n: out.len() + 1,
            date,
            folded: fold_ascii(&description),
            description,
            document: get(doc_col).and_then(|v| canonical_digits(&text_of(v))),
            amount: amount.abs(),
            side,
        });
    }
    out
}

fn last_day_of_month(d: NaiveDate) -> NaiveDate {
    let first = d.with_day(1).unwrap_or(d);
    first
        .checked_add_months(Months::new(1))
        .and_then(|n| n.pred_opt())
        .unwrap_or(d)
}

fn period_of(lines: &[BankLine], ledgers: &[(String, Table)]) -> Option<Period> {
    let mut dates: Vec<NaiveDate> = lines.iter().map(|l| l.date).collect();
    if dates.is_empty() {
        for (_, t) in ledgers {
            let due = col_sub(t, ColumnRole::Date, "due").or_else(|| col(t, ColumnRole::Date));
            if let Some(c) = due {
                dates.extend(
                    t.rows
                        .iter()
                        .filter_map(|r| r.values.get(c).and_then(date_of)),
                );
            }
        }
    }
    let min = *dates.iter().min()?;
    let max = *dates.iter().max()?;
    Some(Period {
        start: min.with_day(1).unwrap_or(min),
        end: last_day_of_month(max),
    })
}

fn tokens_of(file: &str) -> Vec<String> {
    let stem = file.rsplit_once('.').map_or(file, |(s, _)| s);
    stem.to_ascii_lowercase()
        .split(|c: char| !c.is_ascii_alphanumeric())
        .filter(|t| !t.is_empty())
        .map(ToString::to_string)
        .collect()
}

fn ledger_key(file: &str, cfg: &EngineConfig, used: &BTreeSet<String>, index: usize) -> String {
    let tokens = tokens_of(file);
    let base = cfg
        .known_systems
        .iter()
        .find(|s| tokens.iter().any(|t| t == s.as_str()))
        .cloned()
        .unwrap_or_else(|| format!("ledger{}", index + 1));
    let mut key = base.clone();
    let mut k = 2;
    while used.contains(&key) || key == "bank" || key == "ap_inbox" {
        key = format!("{base}{k}");
        k += 1;
    }
    key
}

fn ledger_label(file: &str, key: &str) -> String {
    let tokens = tokens_of(file);
    for w in tokens.windows(2) {
        if w[0] == "unidade" || w[0] == "filial" {
            let mut name = w[1].clone();
            if let Some(first) = name.get(..1) {
                name = first.to_ascii_uppercase() + name.get(1..).unwrap_or_default();
            }
            return format!("{} {name}", w[0]);
        }
    }
    key.to_string()
}

fn ledger_prefix(key: &str, used: &BTreeSet<String>) -> String {
    let candidates = key
        .chars()
        .filter(char::is_ascii_alphabetic)
        .map(|c| c.to_ascii_uppercase().to_string())
        .chain(('A'..='Z').map(|c| c.to_string()));
    for c in candidates {
        if !used.contains(&c) {
            return c;
        }
    }
    format!("L{}", used.len() + 1)
}

/// True when a ledger's status cell means "still open".
fn is_open_status(header_folded: &str, value_folded: &str) -> bool {
    let flag_column = ["PAGO", "PAID", "QUITADO", "LIQUIDADO", "BAIXADO"]
        .iter()
        .any(|w| header_folded.contains(w));
    if flag_column {
        return !matches!(value_folded, "S" | "SIM" | "Y" | "YES" | "TRUE" | "1" | "X");
    }
    // A rejected entry or a fee record of a bank collection file is not an open title.
    !matches!(
        value_folded,
        "PAGO"
            | "PAGA"
            | "PAID"
            | "QUITADO"
            | "LIQUIDADO"
            | "BAIXADO"
            | "RECEBIDO"
            | "CANCELADO"
            | "REJEITADO"
            | "TARIFA"
    )
}

fn ledger_titles(t: &Table, ledger: usize, info: &Ledger) -> (Vec<Title>, Money) {
    let name_col = col(t, ColumnRole::Name);
    let tax_col = col(t, ColumnRole::TaxId);
    let doc_col = col(t, ColumnRole::DocumentId);
    let nosso_col = col(t, ColumnRole::NossoNumero);
    let kind_col = col(t, ColumnRole::Kind);
    let status_col = col(t, ColumnRole::Status);
    let amount_col = col(t, ColumnRole::Amount);
    let due_col = col_sub(t, ColumnRole::Date, "due")
        .or_else(|| t.columns.iter().rposition(|c| c.role == ColumnRole::Date));
    let status_header = status_col
        .map(|c| fold_ascii(&t.columns[c].header))
        .unwrap_or_default();
    let mut titles = Vec::new();
    let mut open_total = Money::ZERO;
    for record in &t.rows {
        let get = |c: Option<usize>| c.and_then(|c| record.values.get(c));
        let (Some(due), Some(amount)) = (
            get(due_col).and_then(date_of),
            get(amount_col).and_then(money_of),
        ) else {
            continue;
        };
        if let Some(v) = get(status_col) {
            if !is_open_status(&status_header, &fold_ascii(&text_of(v))) {
                continue;
            }
        }
        open_total = open_total.saturating_add(amount);
        let tax = get(tax_col)
            .map(text_of)
            .filter(|s| !s.is_empty())
            .map(|s| tax_id(&s));
        let nosso_raw = get(nosso_col).map(text_of).filter(|s| !s.is_empty());
        titles.push(Title {
            id: format!("{}{:02}", info.prefix, titles.len() + 1),
            ledger,
            row: record.row,
            reference: get(doc_col).map(text_of).unwrap_or_default(),
            customer: get(name_col).map(text_of).unwrap_or_default(),
            tax_id_invalid: tax.as_ref().is_some_and(|t| {
                t.kind != TaxIdKind::Unknown && t.verdict != dm_validators::Verdict::Valid
            }),
            tax_id: tax
                .filter(|t| t.kind != TaxIdKind::Unknown)
                .map(|t| t.digits),
            nosso_numero: nosso_raw.as_deref().and_then(canonical_digits),
            nosso_numero_raw: nosso_raw,
            kind: get(kind_col)
                .map(|v| fold_ascii(&text_of(v)))
                .unwrap_or_default(),
            due,
            amount,
            entity: 0,
        });
    }
    (titles, open_total)
}

/// The main ledger is the one whose boleto numbers the bank statement liquidates; ties go
/// to the ledger that has boleto numbers at all, then to file order.
fn mark_main_ledger(ds: &mut Dataset) {
    if ds.ledgers.is_empty() {
        return;
    }
    let docs: BTreeSet<&str> = ds
        .lines
        .iter()
        .filter_map(|l| l.document.as_deref())
        .collect();
    let score = |li: usize| -> (usize, usize) {
        let titles = ds.titles.iter().filter(|t| t.ledger == li);
        let hits = titles
            .clone()
            .filter(|t| t.nosso_numero.as_deref().is_some_and(|n| docs.contains(n)))
            .count();
        let with_nosso = titles.filter(|t| t.nosso_numero.is_some()).count();
        (hits, with_nosso)
    };
    let mut best = 0;
    for li in 1..ds.ledgers.len() {
        if score(li) > score(best) {
            best = li;
        }
    }
    ds.ledgers[best].main = true;
}

/// Groups titles into customers: same tax id across ledgers, otherwise the same name.
fn resolve_entities(ds: &mut Dataset) {
    let mut by_key: BTreeMap<String, usize> = BTreeMap::new();
    for i in 0..ds.titles.len() {
        let key = match &ds.titles[i].tax_id {
            Some(t) => format!("tax:{t}"),
            None => format!("name:{}", name_key(&ds.titles[i].customer).core),
        };
        let e = *by_key.entry(key).or_insert_with(|| {
            ds.entities.push(Entity::default());
            ds.entities.len() - 1
        });
        ds.titles[i].entity = e;
        ds.entities[e].titles.push(i);
        if ds.entities[e].tax_id.is_none() {
            ds.entities[e].tax_id.clone_from(&ds.titles[i].tax_id);
        }
    }
}

fn field(item: &Item, name: &str) -> Option<String> {
    item.fields.get(name).map(text_of).filter(|s| !s.is_empty())
}

/// The e-mails of one source (with their Message-ID) and its chat messages, in its order.
fn inbox(file: &str, items: &[Item]) -> (Vec<(Email, Option<String>)>, Vec<RawChat>) {
    let mut emails = Vec::new();
    let mut chat = Vec::new();
    for item in items {
        if let Some(from) = field(item, "from") {
            let from = from.to_ascii_lowercase();
            let domain = field(item, "sender_domain")
                .map(|d| d.to_ascii_lowercase())
                .or_else(|| {
                    from.split_once('@')
                        .map(|(_, d)| d.trim_end_matches('>').to_string())
                })
                .unwrap_or_default();
            let subject = field(item, "subject").unwrap_or_default();
            let body = field(item, "text").unwrap_or_default();
            // A dump writes `dd/mm/aaaa hh:mm`; an .eml keeps the RFC 2822 header, whose time
            // the parser already read.
            let at = field(item, "date")
                .and_then(|d| email_datetime(&d))
                .or_else(|| match &item.time {
                    Some(TimeRef::Instant { at }) => Some(*at),
                    Some(TimeRef::Date { date }) => date.and_hms_opt(0, 0, 0),
                    _ => None,
                });
            emails.push((
                Email {
                    n: emails.len() + 1,
                    file: file.to_string(),
                    folded: fold_ascii(&format!("{subject}\n{body}")),
                    from,
                    domain,
                    at,
                    subject,
                    body,
                },
                field(item, "message_id"),
            ));
        } else if let (Some(author), Some(date)) = (field(item, "sender"), field(item, "date")) {
            chat.push(RawChat {
                date,
                author,
                text: field(item, "text").unwrap_or_default(),
            });
        }
    }
    (emails, chat)
}

fn email_datetime(s: &str) -> Option<NaiveDateTime> {
    let s = s.trim();
    for fmt in [
        "%d/%m/%Y %H:%M",
        "%d/%m/%Y %H:%M:%S",
        "%Y-%m-%d %H:%M",
        "%Y-%m-%dT%H:%M:%S",
    ] {
        if let Ok(dt) = NaiveDateTime::parse_from_str(s, fmt) {
            return Some(dt);
        }
    }
    NaiveDate::parse_from_str(s, "%d/%m/%Y")
        .ok()
        .and_then(|d| d.and_hms_opt(0, 0, 0))
}

fn chat_datetime(s: &str, year: i32) -> Option<NaiveDateTime> {
    if let Some(dt) = email_datetime(s) {
        return Some(dt);
    }
    NaiveDateTime::parse_from_str(&format!("{} {year}", s.trim()), "%d/%m %H:%M %Y").ok()
}

/// Every invoice-like number in a text (`NF 055120` → `55120`), canonical.
pub fn document_numbers(text: &str) -> Vec<String> {
    let mut out: Vec<String> = text
        .split(|c: char| !c.is_ascii_alphanumeric())
        .filter(|t| t.len() >= 3 && t.chars().all(|c| c.is_ascii_digit()))
        .filter_map(|t| {
            let d = doc_number(t);
            (!d.canonical.is_empty() && d.canonical != "0").then_some(d.canonical)
        })
        .collect();
    out.sort();
    out.dedup();
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_semantics_follow_the_header() {
        assert!(is_open_status("PAID_FLAG", "N"));
        assert!(!is_open_status("PAID_FLAG", "S"));
        assert!(is_open_status("STATUS", "ABERTO"));
        assert!(!is_open_status("STATUS", "PAGO"));
        assert!(!is_open_status("SITUACAO", "REJEITADO"));
        assert!(is_open_status("SITUACAO", "OUTRO"));
    }

    #[test]
    fn keys_labels_and_prefixes() {
        let cfg = EngineConfig::default();
        let used = BTreeSet::new();
        assert_eq!(
            ledger_key("01_contas_protheus_jul.csv", &cfg, &used, 0),
            "protheus"
        );
        assert_eq!(ledger_key("titulos.csv", &cfg, &used, 1), "ledger2");
        assert_eq!(
            ledger_label("02_titulos_unidade_norte_omie.csv", "omie"),
            "unidade Norte"
        );
        let mut p = BTreeSet::new();
        p.insert("P".to_string());
        assert_eq!(ledger_prefix("protheus", &p), "R");
    }

    #[test]
    fn document_numbers_drop_leading_zeros() {
        assert_eq!(
            document_numbers("Nota fiscal 055120 e PC 7310"),
            vec!["55120", "7310"]
        );
        assert!(document_numbers("sem numeros").is_empty());
    }

    #[test]
    fn chat_dates_take_the_period_year() {
        let at = chat_datetime("19/07 08:33", 2026).unwrap_or_default();
        assert_eq!(at.format("%Y-%m-%dT%H:%M").to_string(), "2026-07-19T08:33");
        assert!(chat_datetime("x", 2026).is_none());
    }

    fn mail(file: &str, from: &str, at: Option<&str>, subject: &str, body: &str) -> Email {
        Email {
            n: 0,
            file: file.to_string(),
            from: from.to_string(),
            domain: String::new(),
            at: at.and_then(email_datetime),
            subject: subject.to_string(),
            body: body.to_string(),
            folded: fold_ascii(&format!("{subject}\n{body}")),
        }
    }

    #[test]
    fn inboxes_merge_in_time_order_without_repeats() {
        let dump = vec![
            (
                mail(
                    "caixa.txt",
                    "a@x.com",
                    Some("06/07/2026 09:14"),
                    "NF 1",
                    "corpo 1",
                ),
                None,
            ),
            (
                mail(
                    "caixa.txt",
                    "c@x.com",
                    Some("18/07/2026 16:52"),
                    "NF 2",
                    "corpo 2",
                ),
                None,
            ),
            (
                mail(
                    "caixa.txt",
                    "b@x.com",
                    Some("09/07/2026 07:41"),
                    "NF 3",
                    "corpo 3",
                ),
                None,
            ),
        ];
        let loose = vec![
            // The same as the dump's first, as an .eml: same sender, minute, subject, body.
            (
                mail(
                    "1.eml",
                    "Ana <A@x.com>",
                    Some("06/07/2026 09:14"),
                    "NF 1",
                    "corpo   1",
                ),
                Some("<m1@x.com>".to_string()),
            ),
            (
                mail(
                    "2.eml",
                    "d@x.com",
                    Some("10/07/2026 08:00"),
                    "NF 4",
                    "corpo 4",
                ),
                Some("<m4@x.com>".to_string()),
            ),
            // The same Message-ID again (an mbox copy).
            (
                mail(
                    "c.mbox",
                    "d@x.com",
                    Some("10/07/2026 08:00"),
                    "Fwd",
                    "outro",
                ),
                Some("M4@X.COM".to_string()),
            ),
            (mail("2.eml", "e@x.com", None, "sem data", "corpo 5"), None),
        ];
        let merged = merge_emails(vec![dump, loose]);
        let order: Vec<(usize, &str)> = merged.iter().map(|e| (e.n, e.subject.as_str())).collect();
        // The dump keeps its order (NF 2 before NF 3); the loose ones fit by time; an undated
        // one follows the message before it in its own file.
        assert_eq!(
            order,
            vec![
                (1, "NF 1"),
                (2, "NF 4"),
                (3, "sem data"),
                (4, "NF 2"),
                (5, "NF 3")
            ]
        );
        assert_eq!(address_of("Ana <A@x.com>"), "a@x.com");
        // One source alone is unchanged.
        let single = vec![
            (
                mail("t", "a@x.com", Some("06/07/2026 09:14"), "1", "a"),
                None,
            ),
            (
                mail("t", "a@x.com", Some("01/07/2026 09:14"), "2", "b"),
                None,
            ),
        ];
        let kept: Vec<String> = merge_emails(vec![single])
            .into_iter()
            .map(|e| e.subject)
            .collect();
        assert_eq!(kept, vec!["1", "2"]);
    }

    #[test]
    fn a_cancelled_run_stops_between_files() {
        use std::sync::atomic::AtomicBool;
        use std::sync::Arc;
        let dir = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
        std::fs::write(
            dir.path().join("a.csv"),
            "Data;Histórico;Valor\n10/07/2026;PIX;1,00\n",
        )
        .unwrap_or_else(|e| panic!("{e}"));
        let cancelled = dm_ingest::cancel::scope(Arc::new(AtomicBool::new(true)), || {
            load_dir(dir.path(), &EngineConfig::default())
        });
        assert!(matches!(cancelled, Err(ReconError::Cancelled)));
        assert!(load_dir(dir.path(), &EngineConfig::default()).is_ok());
    }
}
