//! The evidence model: items, entities, facts, findings and rules.
//!
//! Every claim from every source becomes an [`Item`]; relations between items are [`Fact`]s;
//! what the user sees are [`Finding`]s, each pointing at the items that prove it. Field names
//! are the JSON contract shared with the API and the apps (`packages/contracts/schemas`).

use chrono::{DateTime, NaiveDate, NaiveDateTime, Utc};
use indexmap::IndexMap;
use rust_decimal::Decimal;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};

use crate::money::Money;
use crate::table::Value;

/// Where inside a source an item lives.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Locator {
    /// A row of a parsed table.
    Row {
        /// Table name.
        table: String,
        /// Zero-based data row.
        row: usize,
    },
    /// A region of a page (PDF, image), bounding box in page units `[x0, y0, x1, y1]`.
    Page {
        /// One-based page number.
        page: u32,
        /// Bounding box `[x0, y0, x1, y1]`, normalized to `0..=1` of the page size.
        bbox: [f32; 4],
    },
    /// A byte range of a text source (`start..end`, end exclusive).
    ByteRange {
        /// First byte.
        start: usize,
        /// One past the last byte.
        end: usize,
    },
    /// A time range of an audio or video source, in milliseconds.
    TimeRange {
        /// Start, ms from the beginning of the media.
        start_ms: u64,
        /// End, ms.
        end_ms: u64,
    },
    /// A spreadsheet cell.
    Cell {
        /// Sheet name.
        sheet: String,
        /// Zero-based row.
        row: usize,
        /// Zero-based column.
        col: usize,
    },
}

impl Locator {
    /// Short human-readable rendering in pt-BR (`linha 3`, `página 2`, `bytes 10–40`).
    pub fn describe_ptbr(&self) -> String {
        match self {
            Locator::Row { table, row } => format!("{table}, linha {}", row + 1),
            Locator::Page { page, .. } => format!("página {page}"),
            Locator::ByteRange { start, end } => format!("bytes {start}\u{2013}{end}"),
            Locator::TimeRange { start_ms, end_ms } => {
                format!("{}\u{2013}{}", fmt_ms(*start_ms), fmt_ms(*end_ms))
            }
            Locator::Cell { sheet, row, col } => {
                format!("{sheet}!{}{}", column_letters(*col), row + 1)
            }
        }
    }
}

fn fmt_ms(ms: u64) -> String {
    let s = ms / 1000;
    format!("{:02}:{:02}:{:02}", s / 3600, (s / 60) % 60, s % 60)
}

fn column_letters(mut col: usize) -> String {
    let mut out = Vec::new();
    loop {
        out.push(b'A' + (col % 26) as u8);
        if col < 26 {
            break;
        }
        col = col / 26 - 1;
    }
    out.reverse();
    String::from_utf8(out).unwrap_or_default()
}

/// When something happened: a point in time, a calendar day or an interval.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum TimeRef {
    /// A date without time (most document fields).
    Date {
        /// The day.
        date: NaiveDate,
    },
    /// A local timestamp (chat messages, e-mails, bank postings with time).
    Instant {
        /// Local date-time; Brazilian sources carry no zone.
        at: NaiveDateTime,
    },
    /// A closed interval.
    Interval {
        /// Start.
        start: NaiveDateTime,
        /// End.
        end: NaiveDateTime,
    },
}

impl TimeRef {
    /// The calendar day this reference starts on.
    pub fn date(&self) -> NaiveDate {
        match self {
            TimeRef::Date { date } => *date,
            TimeRef::Instant { at } => at.date(),
            TimeRef::Interval { start, .. } => start.date(),
        }
    }
}

/// What kind of thing an item is.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ItemKind {
    /// A row of a table.
    Row,
    /// A span of text (paragraph, e-mail body, message).
    TextSpan,
    /// A region of a page.
    PageRegion,
    /// A segment of audio.
    AudioSegment,
    /// A segment of video.
    VideoSegment,
    /// A region of an image.
    ImageRegion,
    /// A discrete event (an e-mail sent, a chat message, a posting).
    Event,
}

/// Kind of real-world entity a mention refers to.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum EntityKind {
    /// Legal entity (CNPJ).
    Company,
    /// Natural person (CPF).
    Person,
    /// Bank account (banco/agência/conta).
    BankAccount,
    /// A document (invoice, order, bill, contract).
    Document,
    /// A bank slip (boleto).
    Boleto,
    /// An e-mail address.
    Email,
    /// A phone number.
    Phone,
    /// A monetary amount mentioned in text.
    Amount,
    /// A date mentioned in text.
    Date,
    /// Anything else.
    Other,
}

/// A mention of an entity inside an item.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct EntityMention {
    /// Resolved entity id, when known.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub entity_id: Option<String>,
    /// Kind of entity.
    pub kind: EntityKind,
    /// Text as it appears in the source.
    pub text: String,
    /// Normalized form (digits only for ids, centavos for amounts, ISO for dates).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub normalized: Option<String>,
    /// Character offsets of the mention within the item text, when it comes from text.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub span: Option<(usize, usize)>,
}

/// A unit of evidence: a row, a span of text, a region, a segment or an event.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Item {
    /// Deterministic id (`it_...`).
    pub id: String,
    /// Kind of item.
    pub kind: ItemKind,
    /// Source document id (`src_...`).
    pub source_id: String,
    /// Where in the source.
    pub locator: Locator,
    /// Typed fields (column name to value, or `text`, `subject`, `from`, ... for text items).
    #[serde(default)]
    pub fields: IndexMap<String, Value>,
    /// When the item happened, when known.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub time: Option<TimeRef>,
    /// Entities mentioned by the item.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub entity_mentions: Vec<EntityMention>,
}

impl Item {
    /// The `text` field, if present.
    pub fn text(&self) -> Option<&str> {
        self.fields.get("text").and_then(Value::as_text)
    }

    /// A field by name.
    pub fn field(&self, name: &str) -> Option<&Value> {
        self.fields.get(name)
    }
}

/// A key that identifies an entity across sources.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
pub struct EntityKey {
    /// Key kind: `cnpj`, `cpf`, `email`, `bank_account`, `document_number`, ...
    pub kind: String,
    /// Normalized key value.
    pub value: String,
}

/// A real-world entity (company, person, account, document) resolved from mentions.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Entity {
    /// Deterministic id (`ent_...`).
    pub id: String,
    /// Kind.
    pub kind: EntityKind,
    /// Best display name.
    pub canonical_name: String,
    /// Identifying keys.
    #[serde(default)]
    pub keys: Vec<EntityKey>,
    /// Other names seen for this entity.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub aliases: Vec<String>,
}

/// Relation asserted by a fact.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum FactRel {
    /// Two mentions refer to the same entity.
    SameEntity,
    /// Two items describe the same event.
    SameEvent,
    /// One item is part of another (a line of an invoice).
    PartOf,
    /// One item's amount is the sum of others.
    SumOf,
    /// Temporal order: first argument before the second.
    Before,
    /// Temporal order: first argument after the second.
    After,
    /// Items contradict each other.
    Contradicts,
    /// Items match (reconciled pair or group).
    Matches,
}

/// An argument of a fact.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct FactArg {
    /// Role of this argument in the relation (`whole`, `part`, `left`, `right`, `payment`, ...).
    pub role: String,
    /// Item the argument points at.
    pub item_id: String,
    /// Position for ordered relations.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ord: Option<u32>,
    /// Quantity or weight (how much of the item participates).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub qty: Option<Decimal>,
}

/// A qualifier on a fact (`tolerance = 0.02`, `rule = fine_2pct`).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Qualifier {
    /// Qualifier name.
    pub key: String,
    /// Qualifier value.
    pub value: Value,
}

/// How a fact or finding was produced.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema, Default)]
pub struct Lineage {
    /// Pipeline stage (`ingest`, `exact_match`, `tolerance_match`, `discover`, `adjudicate`).
    pub stage: String,
    /// Ids of facts or items this was derived from.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub inputs: Vec<String>,
    /// Tool that produced it, when a generated tool was involved.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool: Option<String>,
    /// Model that produced it, when a model was involved.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    /// Run id.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
}

/// A relation between items, with bitemporal stamps and provenance.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Fact {
    /// Deterministic id (`fact_...`).
    pub id: String,
    /// Relation.
    pub rel: FactRel,
    /// Arguments.
    pub args: Vec<FactArg>,
    /// Qualifiers.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub qualifiers: Vec<Qualifier>,
    /// When the relation holds in the real world.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub valid_time: Option<TimeRef>,
    /// When the harness recorded it.
    pub tx_time: DateTime<Utc>,
    /// Confidence `0..=1`.
    pub confidence: f64,
    /// Rule that produced it, when any.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rule_id: Option<String>,
    /// Provenance.
    #[serde(default)]
    pub lineage: Lineage,
}

/// Severity of a finding.
#[derive(
    Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize, JsonSchema,
)]
#[serde(rename_all = "snake_case")]
pub enum Severity {
    /// Informational.
    Info,
    /// Low.
    Low,
    /// Medium.
    Medium,
    /// High.
    High,
    /// Critical.
    Critical,
}

impl Severity {
    /// pt-BR label.
    pub fn label_ptbr(self) -> &'static str {
        match self {
            Severity::Info => "informativo",
            Severity::Low => "baixa",
            Severity::Medium => "média",
            Severity::High => "alta",
            Severity::Critical => "crítica",
        }
    }
}

/// What kind of divergence a finding reports.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum FindingKind {
    /// A title (invoice, bill) with no payment.
    MissingPayment,
    /// A bank line nothing explains.
    UnexplainedBankLine,
    /// Paid amount differs from the title.
    AmountMismatch,
    /// Paid on a different date than due (late or early).
    DateMismatch,
    /// Same title paid or posted twice.
    Duplicate,
    /// Paid more than owed.
    Overpayment,
    /// Paid less than owed.
    Underpayment,
    /// A withholding tax explains the difference.
    Withholding,
    /// Fine or interest explains the difference.
    FineOrInterest,
    /// Sources contradict each other.
    Contradiction,
    /// A document fails its own check digits or format.
    InvalidDocument,
    /// A document contains instructions aimed at the model; never acted upon.
    InjectedInstructions,
    /// A bank account differs from the one on record (possible fraud).
    BankAccountChange,
    /// Anything else (also what unknown kinds deserialize to).
    #[serde(other)]
    Other,
}

/// Lifecycle of a finding.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema, Default)]
#[serde(rename_all = "snake_case")]
pub enum FindingStatus {
    /// Raised and not yet reviewed.
    #[default]
    Open,
    /// Confirmed by a person.
    Confirmed,
    /// Dismissed by a person (false positive).
    Dismissed,
    /// Resolved (fixed at the source).
    Resolved,
}

/// A pointer from a finding to the evidence that proves it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct EvidenceRef {
    /// Item id.
    pub item_id: String,
    /// Source id.
    pub source_id: String,
    /// Where inside the source.
    pub locator: Locator,
    /// Short quote from the item (never longer than a line).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub excerpt: Option<String>,
    /// Role in the finding (`title`, `payment`, `message`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub role: Option<String>,
}

/// A divergence shown to the user, with its proof.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Finding {
    /// Deterministic id (`fnd_...`).
    pub id: String,
    /// Stable machine code (`TITLE_WITHOUT_PAYMENT`, `BANK_LINE_UNEXPLAINED`, ...).
    pub code: String,
    /// Kind.
    pub kind: FindingKind,
    /// Severity.
    pub severity: Severity,
    /// Money at stake, in centavos.
    pub amount_at_risk: Money,
    /// Title in pt-BR (one line).
    pub title: String,
    /// Explanation in pt-BR (a short paragraph, cites the evidence).
    pub explanation: String,
    /// Confidence `0..=1`.
    pub confidence: f64,
    /// Evidence that proves the finding.
    #[serde(default)]
    pub evidence: Vec<EvidenceRef>,
    /// Status.
    #[serde(default)]
    pub status: FindingStatus,
    /// Facts the finding was derived from.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fact_ids: Vec<String>,
    /// Rule that raised it, when any.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rule_id: Option<String>,
}

/// Where a rule applies.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum RuleScope {
    /// Shipped with the harness, applies everywhere.
    Global,
    /// Learned or written for one workspace.
    Workspace {
        /// Workspace id.
        workspace_id: String,
    },
    /// Proposed within one run, not yet promoted.
    Run {
        /// Run id.
        run_id: String,
    },
}

/// Where a rule came from.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum RuleProvenance {
    /// Built into the harness.
    Builtin,
    /// Written by a person.
    UserAuthored,
    /// Discovered by the rule-discovery stage.
    Discovered {
        /// Hypothesis id.
        hypothesis_id: String,
        /// Model that proposed it.
        model: String,
    },
    /// Imported from another workspace or a library.
    Imported {
        /// Origin.
        origin: String,
    },
}

/// Evidence collected about a rule over time.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema, Default)]
pub struct RuleStats {
    /// Times the rule fired.
    pub applied: u64,
    /// Confirmed hits.
    pub true_positives: u64,
    /// Dismissed hits.
    pub false_positives: u64,
    /// Money the rule explained, in centavos.
    pub money_explained: Money,
    /// Exceptions the rule needs (counts against simplicity).
    pub exceptions: u32,
}

/// Lifecycle of a rule.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema, Default)]
#[serde(rename_all = "snake_case")]
pub enum RuleStatus {
    /// Proposed, not yet validated.
    #[default]
    Candidate,
    /// Validated and applied.
    Active,
    /// Turned off after drift or feedback.
    Retired,
    /// Rejected during validation.
    Rejected,
}

/// A reconciliation rule: natural language plus executable SQL.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Rule {
    /// Id (`rule_...`).
    pub id: String,
    /// Scope.
    pub scope: RuleScope,
    /// Short title in pt-BR.
    pub title: String,
    /// Natural-language body in pt-BR.
    pub body_nl: String,
    /// Executable SQL (a single SELECT over the evidence tables).
    pub body_sql: String,
    /// Provenance.
    pub provenance: RuleProvenance,
    /// Statistics.
    #[serde(default)]
    pub stats: RuleStats,
    /// Version, incremented on every edit.
    pub version: u32,
    /// Status.
    #[serde(default)]
    pub status: RuleStatus,
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn locator_descriptions() {
        assert_eq!(
            Locator::Row {
                table: "t".into(),
                row: 2
            }
            .describe_ptbr(),
            "t, linha 3"
        );
        assert_eq!(
            Locator::Page {
                page: 2,
                bbox: [0.0; 4]
            }
            .describe_ptbr(),
            "página 2"
        );
        assert_eq!(
            Locator::ByteRange { start: 1, end: 9 }.describe_ptbr(),
            "bytes 1\u{2013}9"
        );
        assert_eq!(
            Locator::TimeRange {
                start_ms: 61_000,
                end_ms: 3_600_000
            }
            .describe_ptbr(),
            "00:01:01\u{2013}01:00:00"
        );
        assert_eq!(
            Locator::Cell {
                sheet: "Plan1".into(),
                row: 0,
                col: 27
            }
            .describe_ptbr(),
            "Plan1!AB1"
        );
        assert_eq!(column_letters(0), "A");
        assert_eq!(column_letters(25), "Z");
        assert_eq!(column_letters(26), "AA");
        assert_eq!(column_letters(701), "ZZ");
        assert_eq!(column_letters(702), "AAA");
    }

    #[test]
    fn finding_json_contract_field_names() {
        let f = Finding {
            id: "fnd_1".into(),
            code: "TITLE_WITHOUT_PAYMENT".into(),
            kind: FindingKind::MissingPayment,
            severity: Severity::High,
            amount_at_risk: Money::from_centavos(1_845_000),
            title: "Título sem pagamento".into(),
            explanation: "A NF 88231 venceu e não há crédito correspondente.".into(),
            confidence: 0.97,
            evidence: vec![EvidenceRef {
                item_id: "it_1".into(),
                source_id: "src_1".into(),
                locator: Locator::Row {
                    table: "titulos".into(),
                    row: 4,
                },
                excerpt: Some("NF 088231".into()),
                role: Some("title".into()),
            }],
            status: FindingStatus::Open,
            fact_ids: vec![],
            rule_id: None,
        };
        let v = serde_json::to_value(&f).unwrap();
        let mut keys: Vec<&str> = v.as_object().unwrap().keys().map(String::as_str).collect();
        keys.sort_unstable();
        let mut expected = vec![
            "id",
            "code",
            "kind",
            "severity",
            "amount_at_risk",
            "title",
            "explanation",
            "confidence",
            "evidence",
            "status",
        ];
        expected.sort_unstable();
        assert_eq!(keys, expected);
        assert_eq!(v["severity"], "high");
        assert_eq!(v["kind"], "missing_payment");
        assert_eq!(v["amount_at_risk"], 1_845_000);
        assert_eq!(v["status"], "open");
        assert_eq!(
            v["evidence"][0]["locator"],
            json!({"kind": "row", "table": "titulos", "row": 4})
        );
        let back: Finding = serde_json::from_value(v).unwrap();
        assert_eq!(back, f);
    }

    #[test]
    fn unknown_finding_kind_deserializes_to_other() {
        let v = json!({
            "id": "fnd_2", "code": "X", "kind": "something_new", "severity": "info",
            "amount_at_risk": 0, "title": "t", "explanation": "e", "confidence": 0.5
        });
        let f: Finding = serde_json::from_value(v).unwrap();
        assert_eq!(f.kind, FindingKind::Other);
        assert!(f.evidence.is_empty());
        assert_eq!(f.status, FindingStatus::Open);
    }

    #[test]
    fn severity_ordering_and_labels() {
        assert!(Severity::Info < Severity::Critical);
        assert_eq!(Severity::Medium.label_ptbr(), "média");
        assert_eq!(
            serde_json::to_value(Severity::Critical).unwrap(),
            "critical"
        );
    }

    #[test]
    fn item_and_fact_round_trip() {
        let mut fields = IndexMap::new();
        fields.insert(
            "text".to_string(),
            Value::from("Pagamento da NF 88231 cai amanhã"),
        );
        let item = Item {
            id: "it_1".into(),
            kind: ItemKind::TextSpan,
            source_id: "src_mail".into(),
            locator: Locator::ByteRange { start: 0, end: 32 },
            fields,
            time: Some(TimeRef::Date {
                date: NaiveDate::from_ymd_opt(2025, 1, 10).unwrap(),
            }),
            entity_mentions: vec![EntityMention {
                entity_id: None,
                kind: EntityKind::Document,
                text: "NF 88231".into(),
                normalized: Some("88231".into()),
                span: Some((13, 21)),
            }],
        };
        assert_eq!(item.text(), Some("Pagamento da NF 88231 cai amanhã"));
        let v = serde_json::to_value(&item).unwrap();
        assert_eq!(v["kind"], "text_span");
        assert_eq!(v["time"], json!({"kind": "date", "date": "2025-01-10"}));
        assert_eq!(v["fields"]["text"]["type"], "text");
        let back: Item = serde_json::from_value(v).unwrap();
        assert_eq!(back, item);

        let fact = Fact {
            id: "fact_1".into(),
            rel: FactRel::SumOf,
            args: vec![
                FactArg {
                    role: "whole".into(),
                    item_id: "it_a".into(),
                    ord: None,
                    qty: None,
                },
                FactArg {
                    role: "part".into(),
                    item_id: "it_b".into(),
                    ord: Some(1),
                    qty: Some(Decimal::ONE),
                },
            ],
            qualifiers: vec![Qualifier {
                key: "tolerance".into(),
                value: Value::Integer(0),
            }],
            valid_time: None,
            tx_time: DateTime::<Utc>::from_timestamp(1_700_000_000, 0).unwrap(),
            confidence: 1.0,
            rule_id: Some("rule_sum".into()),
            lineage: Lineage {
                stage: "subset_sum".into(),
                ..Lineage::default()
            },
        };
        let v = serde_json::to_value(&fact).unwrap();
        assert_eq!(v["rel"], "sum_of");
        assert_eq!(v["args"][1]["ord"], 1);
        let back: Fact = serde_json::from_value(v).unwrap();
        assert_eq!(back, fact);
    }

    #[test]
    fn rule_round_trip() {
        let rule = Rule {
            id: "rule_1".into(),
            scope: RuleScope::Workspace {
                workspace_id: "ws_1".into(),
            },
            title: "Multa de 2% após o vencimento".into(),
            body_nl: "Pagamentos após o vencimento somam 2% de multa.".into(),
            body_sql: "SELECT 1".into(),
            provenance: RuleProvenance::Discovered {
                hypothesis_id: "hyp_1".into(),
                model: "x".into(),
            },
            stats: RuleStats::default(),
            version: 1,
            status: RuleStatus::Active,
        };
        let v = serde_json::to_value(&rule).unwrap();
        assert_eq!(
            v["scope"],
            json!({"kind": "workspace", "workspace_id": "ws_1"})
        );
        assert_eq!(v["provenance"]["kind"], "discovered");
        assert_eq!(v["status"], "active");
        let back: Rule = serde_json::from_value(v).unwrap();
        assert_eq!(back, rule);
    }

    #[test]
    fn schemas_generate() {
        for schema in [
            schemars::schema_for!(Finding),
            schemars::schema_for!(Item),
            schemars::schema_for!(Fact),
            schemars::schema_for!(Rule),
            schemars::schema_for!(Entity),
        ] {
            let v = serde_json::to_value(schema).unwrap();
            assert!(v.get("$schema").is_some());
        }
    }
}
