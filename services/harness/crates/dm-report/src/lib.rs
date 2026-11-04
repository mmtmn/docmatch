//! Relatórios: Markdown em pt-BR, JSON, CSV e XLSX, com escape contra injeção de fórmula.
#![forbid(unsafe_code)]

use dm_core::{Finding, Money, Severity};
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Errors while rendering.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum ReportError {
    /// CSV writer failure.
    #[error("csv: {0}")]
    Csv(String),
    /// XLSX writer failure.
    #[error("xlsx: {0}")]
    Xlsx(String),
    /// A total overflowed the `Money` range (only absurd inputs get here, but a wrong total
    /// must never be printed as if it were right).
    #[error("estouro ao somar {0}")]
    Overflow(&'static str),
}

/// Sum of the amounts at risk, with overflow reported.
fn total_at_risk(findings: &[Finding]) -> Result<Money, ReportError> {
    Money::checked_sum(findings.iter().map(|f| f.amount_at_risk))
        .map_err(|_| ReportError::Overflow("valor em risco"))
}

/// Sum of the reconciled amounts, with overflow reported.
fn total_reconciled(matches: &[MatchRecord]) -> Result<Money, ReportError> {
    Money::checked_sum(matches.iter().map(|m| m.amount))
        .map_err(|_| ReportError::Overflow("valor conciliado"))
}

/// A reconciled pair or group, as the report shows it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct MatchRecord {
    /// Left item id (title).
    pub left_id: String,
    /// Right item id (payment).
    pub right_id: String,
    /// Rule that paired them.
    pub rule: String,
    /// Amount reconciled.
    pub amount: Money,
    /// pt-BR explanation.
    pub explanation: String,
}

/// Prefixes a single quote when a cell could be read as a formula by a spreadsheet
/// (`=`, `+`, `-`, `@`, tab, carriage return, also after leading whitespace), so a document
/// cannot plant a formula in the exported report.
pub fn escape_cell(cell: &str) -> String {
    let leading = cell.trim_start_matches([' ', '\u{a0}']);
    let dangerous = leading.starts_with(['=', '+', '-', '@', '\t', '\r', '|'])
        || leading.starts_with("\u{feff}");
    if dangerous {
        format!("'{cell}")
    } else {
        cell.to_string()
    }
}

/// Markdown report in pt-BR: summary, findings (one section each with evidence) and matches.
pub fn render_markdown_ptbr(
    findings: &[Finding],
    matches: &[MatchRecord],
) -> Result<String, ReportError> {
    let mut md = String::new();
    md.push_str("# Relatório de conciliação\n\n");
    let at_risk = total_at_risk(findings)?;
    let reconciled = total_reconciled(matches)?;
    md.push_str(&format!(
        "- Achados: **{}** (valor em risco: {})\n",
        findings.len(),
        at_risk.format_brl()
    ));
    md.push_str(&format!(
        "- Conciliados: **{}** (valor conciliado: {})\n\n",
        matches.len(),
        reconciled.format_brl()
    ));
    if !findings.is_empty() {
        md.push_str("## Achados\n\n| Código | Severidade | Valor em risco | Título |\n| --- | --- | ---: | --- |\n");
        let mut sorted: Vec<&Finding> = findings.iter().collect();
        sorted.sort_by(|a, b| {
            b.severity
                .cmp(&a.severity)
                .then(b.amount_at_risk.cmp(&a.amount_at_risk))
        });
        for f in &sorted {
            md.push_str(&format!(
                "| `{}` | {} | {} | {} |\n",
                f.code,
                f.severity.label_ptbr(),
                f.amount_at_risk.format_brl(),
                md_escape(&f.title)
            ));
        }
        md.push('\n');
        for f in &sorted {
            md.push_str(&format!(
                "### {} ({})\n\n{}\n\n",
                md_escape(&f.title),
                severity_badge(f.severity),
                md_escape(&f.explanation)
            ));
            if !f.evidence.is_empty() {
                md.push_str("Evidência:\n\n");
                for e in &f.evidence {
                    let excerpt = e
                        .excerpt
                        .as_deref()
                        .map(|x| format!(" — \u{201c}{}\u{201d}", md_escape(x)))
                        .unwrap_or_default();
                    md.push_str(&format!(
                        "- {} ({}){excerpt}\n",
                        e.locator.describe_ptbr(),
                        e.item_id
                    ));
                }
                md.push('\n');
            }
            md.push_str(&format!("Confiança: {:.0}%\n\n", f.confidence * 100.0));
        }
    }
    if !matches.is_empty() {
        md.push_str("## Conciliados\n\n| Título | Pagamento | Regra | Valor |\n| --- | --- | --- | ---: |\n");
        for m in matches {
            md.push_str(&format!(
                "| {} | {} | {} | {} |\n",
                m.left_id,
                m.right_id,
                md_escape(&m.rule),
                m.amount.format_brl()
            ));
        }
        md.push('\n');
    }
    Ok(md)
}

fn severity_badge(s: Severity) -> String {
    format!("severidade {}", s.label_ptbr())
}

fn md_escape(s: &str) -> String {
    s.replace('|', "\\|").replace('\n', " ")
}

/// JSON document with both lists.
pub fn to_json(
    findings: &[Finding],
    matches: &[MatchRecord],
) -> Result<serde_json::Value, ReportError> {
    Ok(serde_json::json!({
        "findings": findings,
        "matches": matches,
        "summary": {
            "findings": findings.len(),
            "matches": matches.len(),
            "amount_at_risk": total_at_risk(findings)?,
            "amount_reconciled": total_reconciled(matches)?,
        }
    }))
}

/// CSV of the findings (`;` separated, pt-BR headers), every text cell escaped.
pub fn to_csv(findings: &[Finding]) -> Result<String, ReportError> {
    let mut w = csv::WriterBuilder::new()
        .delimiter(b';')
        .from_writer(Vec::new());
    w.write_record([
        "codigo",
        "tipo",
        "severidade",
        "valor_em_risco",
        "titulo",
        "explicacao",
        "confianca",
        "status",
        "evidencias",
    ])
    .map_err(|e| ReportError::Csv(e.to_string()))?;
    for f in findings {
        let kind = serde_json::to_value(f.kind)
            .ok()
            .and_then(|v| v.as_str().map(String::from))
            .unwrap_or_default();
        let status = serde_json::to_value(f.status)
            .ok()
            .and_then(|v| v.as_str().map(String::from))
            .unwrap_or_default();
        let evidence: Vec<String> = f.evidence.iter().map(|e| e.item_id.clone()).collect();
        w.write_record([
            escape_cell(&f.code),
            escape_cell(&kind),
            f.severity.label_ptbr().to_string(),
            f.amount_at_risk.format_plain(),
            escape_cell(&f.title),
            escape_cell(&f.explanation),
            format!("{:.2}", f.confidence),
            status,
            escape_cell(&evidence.join(" ")),
        ])
        .map_err(|e| ReportError::Csv(e.to_string()))?;
    }
    let bytes = w
        .into_inner()
        .map_err(|e| ReportError::Csv(e.to_string()))?;
    String::from_utf8(bytes).map_err(|e| ReportError::Csv(e.to_string()))
}

/// XLSX workbook with a sheet of findings and a sheet of matches, every text cell escaped.
pub fn to_xlsx(findings: &[Finding], matches: &[MatchRecord]) -> Result<Vec<u8>, ReportError> {
    use rust_xlsxwriter::Workbook;
    let x = |e: rust_xlsxwriter::XlsxError| ReportError::Xlsx(e.to_string());
    let mut wb = Workbook::new();
    let sheet = wb.add_worksheet();
    sheet.set_name("Achados").map_err(x)?;
    for (c, h) in [
        "Código",
        "Severidade",
        "Valor em risco",
        "Título",
        "Explicação",
        "Confiança",
        "Evidências",
    ]
    .iter()
    .enumerate()
    {
        sheet.write_string(0, c as u16, *h).map_err(x)?;
    }
    for (i, f) in findings.iter().enumerate() {
        let row = (i + 1) as u32;
        let evidence: Vec<String> = f.evidence.iter().map(|e| e.item_id.clone()).collect();
        sheet
            .write_string(row, 0, escape_cell(&f.code))
            .map_err(x)?;
        sheet
            .write_string(row, 1, f.severity.label_ptbr())
            .map_err(x)?;
        sheet
            .write_number(
                row,
                2,
                f.amount_at_risk
                    .as_decimal()
                    .to_string()
                    .parse::<f64>()
                    .unwrap_or(0.0),
            )
            .map_err(x)?;
        sheet
            .write_string(row, 3, escape_cell(&f.title))
            .map_err(x)?;
        sheet
            .write_string(row, 4, escape_cell(&f.explanation))
            .map_err(x)?;
        sheet.write_number(row, 5, f.confidence).map_err(x)?;
        sheet
            .write_string(row, 6, escape_cell(&evidence.join(" ")))
            .map_err(x)?;
    }
    let sheet = wb.add_worksheet();
    sheet.set_name("Conciliados").map_err(x)?;
    for (c, h) in ["Título", "Pagamento", "Regra", "Valor", "Explicação"]
        .iter()
        .enumerate()
    {
        sheet.write_string(0, c as u16, *h).map_err(x)?;
    }
    for (i, m) in matches.iter().enumerate() {
        let row = (i + 1) as u32;
        sheet
            .write_string(row, 0, escape_cell(&m.left_id))
            .map_err(x)?;
        sheet
            .write_string(row, 1, escape_cell(&m.right_id))
            .map_err(x)?;
        sheet
            .write_string(row, 2, escape_cell(&m.rule))
            .map_err(x)?;
        sheet
            .write_number(
                row,
                3,
                m.amount
                    .as_decimal()
                    .to_string()
                    .parse::<f64>()
                    .unwrap_or(0.0),
            )
            .map_err(x)?;
        sheet
            .write_string(row, 4, escape_cell(&m.explanation))
            .map_err(x)?;
    }
    wb.save_to_buffer().map_err(x)
}

#[cfg(test)]
mod tests {
    use super::*;
    use dm_core::{EvidenceRef, FindingKind, FindingStatus, Locator};

    fn finding(code: &str, title: &str, centavos: i64, severity: Severity) -> Finding {
        Finding {
            id: format!("fnd_{code}"),
            code: code.into(),
            kind: FindingKind::MissingPayment,
            severity,
            amount_at_risk: Money::from_centavos(centavos),
            title: title.into(),
            explanation: "A NF 88231 venceu em 20/03/2025 e não há crédito | correspondente."
                .into(),
            confidence: 0.97,
            evidence: vec![EvidenceRef {
                item_id: "it_1".into(),
                source_id: "src".into(),
                locator: Locator::Row {
                    table: "titulos".into(),
                    row: 4,
                },
                excerpt: Some("NF 088231".into()),
                role: None,
            }],
            status: FindingStatus::Open,
            fact_ids: vec![],
            rule_id: None,
        }
    }

    #[test]
    fn escapes_formula_cells() {
        assert_eq!(escape_cell("=1+1"), "'=1+1");
        assert_eq!(escape_cell("+1"), "'+1");
        assert_eq!(escape_cell("-1"), "'-1");
        assert_eq!(escape_cell("@SUM(A1)"), "'@SUM(A1)");
        assert_eq!(escape_cell("  =HYPERLINK()"), "'  =HYPERLINK()");
        assert_eq!(escape_cell("\t=x"), "'\t=x");
        assert_eq!(escape_cell("Padaria Central"), "Padaria Central");
        assert_eq!(escape_cell("1.234,56"), "1.234,56");
        assert_eq!(escape_cell(""), "");
    }

    #[test]
    fn markdown_and_json() {
        let findings = vec![
            finding(
                "TITLE_WITHOUT_PAYMENT",
                "Título sem pagamento",
                1_845_000,
                Severity::High,
            ),
            finding("LATE", "Pago com atraso", 100, Severity::Low),
        ];
        let matches = vec![MatchRecord {
            left_id: "it_t".into(),
            right_id: "it_b".into(),
            rule: "valor exato".into(),
            amount: Money::from_centavos(50_000),
            explanation: "ok".into(),
        }];
        let md = render_markdown_ptbr(&findings, &matches).unwrap();
        assert!(md.starts_with("# Relatório de conciliação"));
        assert!(md.contains("Achados: **2** (valor em risco: R$ 18.451,00)"));
        assert!(
            md.contains("| `TITLE_WITHOUT_PAYMENT` | alta | R$ 18.450,00 | Título sem pagamento |")
        );
        assert!(
            md.contains("crédito \\| correspondente"),
            "pipes are escaped inside tables"
        );
        assert!(md.contains("titulos, linha 5 (it_1)"));
        assert!(md.contains("## Conciliados"));
        assert!(
            md.find("TITLE_WITHOUT_PAYMENT").unwrap() < md.find("`LATE`").unwrap(),
            "higher severity first"
        );
        let j = to_json(&findings, &matches).unwrap();
        assert_eq!(j["summary"]["findings"], 2);
        assert_eq!(j["summary"]["amount_at_risk"], 1_845_100);
        assert_eq!(j["findings"][0]["code"], "TITLE_WITHOUT_PAYMENT");
        assert!(render_markdown_ptbr(&[], &[])
            .unwrap()
            .contains("Achados: **0**"));
    }

    #[test]
    fn totals_report_overflow_instead_of_a_wrong_number() {
        let huge = Money::from_decimal(dm_core::Decimal::MAX);
        let findings = vec![
            finding("A", "a", 1, Severity::Info),
            finding("B", "b", 1, Severity::Info),
        ];
        let mut absurd = findings.clone();
        absurd[0].amount_at_risk = huge;
        absurd[1].amount_at_risk = huge;
        assert_eq!(
            render_markdown_ptbr(&absurd, &[]),
            Err(ReportError::Overflow("valor em risco"))
        );
        let matches = vec![
            MatchRecord {
                left_id: "a".into(),
                right_id: "b".into(),
                rule: "x".into(),
                amount: huge,
                explanation: String::new(),
            };
            2
        ];
        assert_eq!(
            to_json(&findings, &matches),
            Err(ReportError::Overflow("valor conciliado"))
        );
    }

    #[test]
    fn csv_and_xlsx_escape_cells() {
        let findings = vec![finding("=CMD()", "+injetado", 1, Severity::Info)];
        let csv_text = to_csv(&findings).unwrap();
        let mut reader = csv::ReaderBuilder::new()
            .delimiter(b';')
            .from_reader(csv_text.as_bytes());
        let record = reader.records().next().unwrap().unwrap();
        assert_eq!(&record[0], "'=CMD()");
        assert_eq!(&record[4], "'+injetado");
        assert_eq!(&record[3], "0,01");
        let bytes = to_xlsx(
            &findings,
            &[MatchRecord {
                left_id: "-a".into(),
                right_id: "b".into(),
                rule: "r".into(),
                amount: Money::from_centavos(5),
                explanation: "e".into(),
            }],
        )
        .unwrap();
        assert!(bytes.starts_with(b"PK"));
        assert!(bytes.len() > 1000);
    }
}
