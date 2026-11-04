//! One entry point: bytes in, sniff result, tables and items out.

use std::path::Path;

use dm_core::{
    stable_id, ColumnRole, EntityKind, EntityMention, Item, ItemKind, Locator, Table, TimeRef,
    Value,
};
use dm_normalize::{doc_number, tax_id, TaxIdKind};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::inbox::InboxContext;
use crate::sniff::{sniff, Sniff, SourceKind};
use crate::IngestError;

/// Everything ingested from one source.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ParsedSource {
    /// Deterministic source id (`src_...`), from the file name and content hash.
    pub source_id: String,
    /// File name (stem used for table names).
    pub name: String,
    /// What was sniffed.
    pub sniff: Sniff,
    /// Tables produced (none for text sources).
    pub tables: Vec<Table>,
    /// Items produced directly (text sources); table rows become items via [`table_items`].
    pub items: Vec<Item>,
    /// Non-fatal problems, in pt-BR.
    pub warnings: Vec<String>,
}

/// Source id from the name and the SHA-256 of the content.
pub fn source_id_for(name: &str, bytes: &[u8]) -> String {
    let digest = hex::encode(Sha256::digest(bytes));
    stable_id("src", &[name, &digest])
}

/// Sniffs and parses bytes under a file name. Unknown and not-yet-implemented formats produce
/// a source with no tables and a warning, never an error, so a run can continue with the
/// other files.
pub fn parse_bytes(
    name: &str,
    bytes: &[u8],
    ctx: &InboxContext,
) -> Result<ParsedSource, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let sniffed = sniff(bytes);
    let source_id = source_id_for(name, bytes);
    let stem = Path::new(name)
        .file_stem()
        .and_then(|s| s.to_str())
        .unwrap_or(name)
        .to_string();
    let mut out = ParsedSource {
        source_id: source_id.clone(),
        name: name.to_string(),
        sniff: sniffed.clone(),
        tables: Vec::new(),
        items: Vec::new(),
        warnings: Vec::new(),
    };
    let note = |out: &mut ParsedSource, e: IngestError| out.warnings.push(format!("{name}: {e}"));
    match sniffed.kind {
        SourceKind::Csv => match crate::delimited::parse_csv(bytes, &sniffed, &stem, &source_id) {
            Ok(t) => out.tables.push(t),
            Err(e) => note(&mut out, e),
        },
        SourceKind::Spreadsheet => match crate::spreadsheet::parse_xlsx(bytes, &stem, &source_id) {
            Ok((tables, warnings)) => {
                out.tables.extend(tables);
                out.warnings
                    .extend(warnings.into_iter().map(|w| format!("{name}: {w}")));
            }
            Err(e) => note(&mut out, e),
        },
        SourceKind::Ofx => match crate::ofx::parse_ofx(bytes, &stem, &source_id) {
            Ok(t) => out.tables.push(t),
            Err(e) => note(&mut out, e),
        },
        SourceKind::NfeXml => match crate::nfe_xml::parse_nfe_xml(bytes, &stem, &source_id) {
            Ok(t) => out.tables.push(t),
            Err(e) => note(&mut out, e),
        },
        SourceKind::Eml => match crate::mail::parse_eml(bytes, &source_id, ctx) {
            Ok(items) => out.items = items,
            Err(e) => note(&mut out, e),
        },
        SourceKind::Mbox => match crate::mail::parse_mbox(bytes, &source_id, ctx) {
            Ok(items) => out.items = items,
            Err(e) => note(&mut out, e),
        },
        SourceKind::TxtInbox | SourceKind::Text => {
            let text = crate::delimited::decode(bytes, &sniffed)?;
            out.items = crate::inbox::parse_txt_inbox(&text, &source_id, ctx);
        }
        SourceKind::Cnab240 => note(
            &mut out,
            crate::cnab::parse_cnab240(bytes, &stem, &source_id)
                .err()
                .unwrap_or(IngestError::NotImplemented("CNAB 240")),
        ),
        SourceKind::Cnab400 => note(
            &mut out,
            crate::cnab::parse_cnab400(bytes, &stem, &source_id)
                .err()
                .unwrap_or(IngestError::NotImplemented("CNAB 400")),
        ),
        SourceKind::Pdf => note(
            &mut out,
            crate::pdf::parse_pdf_text(bytes, &source_id)
                .err()
                .unwrap_or(IngestError::NotImplemented("PDF")),
        ),
        SourceKind::Xml | SourceKind::Json | SourceKind::Unknown => note(
            &mut out,
            IngestError::Unrecognized(format!("{:?}", sniffed.kind)),
        ),
    }
    Ok(out)
}

/// Reads and parses a file.
pub fn parse_file(path: &Path, ctx: &InboxContext) -> Result<ParsedSource, IngestError> {
    let bytes = std::fs::read(path).map_err(|e| IngestError::Io(e.to_string()))?;
    let name = path
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("arquivo");
    parse_bytes(name, &bytes, ctx)
}

/// Turns every row of a table into a `row` item with its typed fields, time and mentions.
pub fn table_items(table: &Table) -> Vec<Item> {
    let date_cols = table.columns_with_role(ColumnRole::Date);
    let due_col = table.column_with_subrole(ColumnRole::Date, "due");
    table
        .rows
        .iter()
        .map(|record| {
            let mut fields = indexmap::IndexMap::new();
            let mut mentions = Vec::new();
            for (col, value) in table.columns.iter().zip(record.values.iter()) {
                fields.insert(col.name.clone(), value.clone());
                match (col.role, value) {
                    (ColumnRole::TaxId, Value::Text(t)) => {
                        let id = tax_id(t);
                        let kind = match id.kind {
                            TaxIdKind::Cnpj => EntityKind::Company,
                            TaxIdKind::Cpf => EntityKind::Person,
                            TaxIdKind::Unknown => EntityKind::Other,
                        };
                        mentions.push(EntityMention {
                            entity_id: None,
                            kind,
                            text: t.clone(),
                            normalized: Some(id.digits),
                            span: None,
                        });
                    }
                    (ColumnRole::DocumentId | ColumnRole::NossoNumero, Value::Text(t)) => {
                        let d = doc_number(t);
                        if !d.canonical.is_empty() {
                            mentions.push(EntityMention {
                                entity_id: None,
                                kind: EntityKind::Document,
                                text: t.clone(),
                                normalized: Some(d.canonical),
                                span: None,
                            });
                        }
                    }
                    (ColumnRole::Name, Value::Text(t)) => {
                        mentions.push(EntityMention {
                            entity_id: None,
                            kind: EntityKind::Company,
                            text: t.clone(),
                            normalized: Some(dm_normalize::name_key(t).core),
                            span: None,
                        });
                    }
                    _ => {}
                }
            }
            let time = due_col
                .into_iter()
                .chain(date_cols.iter().copied())
                .find_map(|c| record.values.get(c).and_then(Value::as_date))
                .map(|date| TimeRef::Date { date });
            Item {
                id: stable_id(
                    "it",
                    &[
                        &table.source_id,
                        &table.name,
                        "row",
                        &record.row.to_string(),
                    ],
                ),
                kind: ItemKind::Row,
                source_id: table.source_id.clone(),
                locator: Locator::Row {
                    table: table.name.clone(),
                    row: record.row,
                },
                fields,
                time,
                entity_mentions: mentions,
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dispatches_by_kind() {
        let ctx = InboxContext::default();
        let csv = parse_bytes("titulos.csv", b"Vencimento;Valor\n05/03/2025;1,00\n", &ctx).unwrap();
        assert_eq!(csv.tables.len(), 1);
        assert_eq!(csv.tables[0].name, "titulos");
        assert!(csv.source_id.starts_with("src_"));
        let chat = parse_bytes("chat.txt", b"[05/03 10:00] Ana: NF 12 paga\n", &ctx).unwrap();
        assert_eq!(
            chat.items.len(),
            1,
            "{:?} {:?}",
            chat.sniff.kind,
            chat.warnings
        );
        let pdf = parse_bytes("doc.pdf", b"%PDF-1.4", &ctx).unwrap();
        assert!(pdf.tables.is_empty());
        assert_eq!(pdf.warnings.len(), 1);
        assert!(pdf.warnings[0].contains("PDF"));
        let unknown = parse_bytes("bin", &[0u8, 1, 2, 3], &ctx).unwrap();
        assert_eq!(unknown.warnings.len(), 1);
        assert_eq!(parse_bytes("x", b"", &ctx), Err(IngestError::Empty));
        assert_eq!(source_id_for("a", b"x"), source_id_for("a", b"x"));
        assert_ne!(source_id_for("a", b"x"), source_id_for("b", b"x"));
    }

    #[test]
    fn rows_become_items_with_mentions() {
        let ctx = InboxContext::default();
        let src = parse_bytes("t.csv", b"Vencimento;Fornecedor;Documento;Valor;CNPJ\n05/03/2025;Padaria Central Ltda;088231;10,00;11.222.333/0001-81\n", &ctx).unwrap();
        let items = table_items(&src.tables[0]);
        assert_eq!(items.len(), 1);
        let it = &items[0];
        assert_eq!(it.kind, ItemKind::Row);
        assert_eq!(
            it.locator,
            Locator::Row {
                table: "t".into(),
                row: 0
            }
        );
        assert!(
            matches!(it.time, Some(TimeRef::Date { date }) if date == chrono::NaiveDate::from_ymd_opt(2025, 3, 5).unwrap())
        );
        assert!(it
            .entity_mentions
            .iter()
            .any(|m| m.kind == EntityKind::Company
                && m.normalized.as_deref() == Some("11222333000181")));
        assert!(it
            .entity_mentions
            .iter()
            .any(|m| m.kind == EntityKind::Document && m.normalized.as_deref() == Some("88231")));
        assert!(it
            .entity_mentions
            .iter()
            .any(|m| m.normalized.as_deref() == Some("PADARIA CENTRAL")));
        assert_eq!(
            it.field("valor").and_then(Value::as_money),
            Some(dm_core::Money::from_centavos(1_000))
        );
    }

    #[test]
    fn parse_file_reads_from_disk() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("x.csv");
        std::fs::write(&path, b"a;b\n1;2\n").unwrap();
        let src = parse_file(&path, &InboxContext::default()).unwrap();
        assert_eq!(src.name, "x.csv");
        assert_eq!(src.tables.len(), 1);
        assert!(matches!(
            parse_file(&dir.path().join("missing.csv"), &InboxContext::default()),
            Err(IngestError::Io(_))
        ));
    }
}
