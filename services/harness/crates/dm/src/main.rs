//! `dm`: CLI do harness para inspecionar arquivos, validar documentos, contar dias úteis,
//! sondar tabelas no sandbox e rodar a conciliação determinística em uma pasta.
#![forbid(unsafe_code)]
#![allow(clippy::print_stdout, clippy::print_stderr)]

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};
use chrono::NaiveDate;
use clap::{Parser, Subcommand, ValueEnum};
use dm_core::{ColumnRole, Item, Table};
use dm_ingest::{parse_file, sniff, table_items, InboxContext, ParsedSource};
use dm_match::{
    exact_match, explain_difference, AmountContext, KeySpec, KeyTransform, ToleranceRule,
};
use dm_normalize::{date_ptbr, DateContext};
use dm_sandbox::{Sandbox, SandboxConfig};
use dm_validators::{business_days, cnpj_dv, cpf_dv, nfe_key_dv, parse_boleto, Verdict};
use serde::Serialize;

#[derive(Parser)]
#[command(
    name = "dm",
    version,
    about = "docmatch — harness de conciliação (determinístico primeiro, modelo depois)"
)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Detecta formato, codificação, separador e estilo de um arquivo
    Sniff {
        /// Arquivo a inspecionar
        file: PathBuf,
    },
    /// Converte um arquivo em tabelas/itens e mostra o resultado
    Parse {
        /// Arquivo a converter
        file: PathBuf,
        /// Imprime o resultado completo em JSON
        #[arg(long)]
        json: bool,
        /// Ano de referência para datas sem ano (dd/mm)
        #[arg(long)]
        year: Option<i32>,
    },
    /// Valida CNPJ, CPF, linha digitável de boleto ou chave de NF-e
    Validate {
        /// Tipo do documento
        kind: DocKind,
        /// Valor (com ou sem máscara)
        value: String,
    },
    /// Conta dias úteis bancários entre duas datas e lista os feriados do intervalo
    Bizdays {
        /// Data inicial (dd/mm/aaaa ou ISO)
        from: String,
        /// Data final
        to: String,
    },
    /// Roda uma consulta SELECT no sandbox sobre arquivos registrados como tabelas
    Probe {
        /// SQL (uma instrução SELECT/WITH)
        sql: String,
        /// Tabela no formato nome=arquivo (repetível)
        #[arg(long = "table", value_name = "NOME=ARQUIVO")]
        tables: Vec<String>,
        /// Imprime o resultado em JSON
        #[arg(long)]
        json: bool,
    },
    /// Ingere todos os arquivos de uma pasta e roda o casamento exato
    Run {
        /// Pasta com os arquivos
        dir: PathBuf,
        /// Arquivo JSON de saída
        #[arg(long, default_value = "report.json")]
        out: PathBuf,
        /// Ano de referência para datas sem ano (dd/mm)
        #[arg(long)]
        year: Option<i32>,
    },
}

#[derive(Clone, Copy, ValueEnum)]
enum DocKind {
    Cnpj,
    Cpf,
    Boleto,
    Nfe,
}

fn main() {
    let cli = Cli::parse();
    let code = match run(cli) {
        Ok(code) => code,
        Err(e) => {
            eprintln!("erro: {e:#}");
            2
        }
    };
    std::process::exit(code);
}

fn run(cli: Cli) -> Result<i32> {
    match cli.command {
        Command::Sniff { file } => {
            let bytes =
                std::fs::read(&file).with_context(|| format!("lendo {}", file.display()))?;
            println!("{}", serde_json::to_string_pretty(&sniff(&bytes))?);
            Ok(0)
        }
        Command::Parse { file, json, year } => cmd_parse(&file, json, year),
        Command::Validate { kind, value } => cmd_validate(kind, &value),
        Command::Bizdays { from, to } => cmd_bizdays(&from, &to),
        Command::Probe { sql, tables, json } => cmd_probe(&sql, &tables, json),
        Command::Run { dir, out, year } => cmd_run(&dir, &out, year),
    }
}

fn inbox_context(year: Option<i32>) -> InboxContext {
    InboxContext {
        reference_year: year,
        reference_date: None,
    }
}

fn cmd_parse(file: &Path, json: bool, year: Option<i32>) -> Result<i32> {
    let src = parse_file(file, &inbox_context(year))
        .with_context(|| format!("convertendo {}", file.display()))?;
    if json {
        println!("{}", serde_json::to_string_pretty(&src)?);
        return Ok(0);
    }
    print_source_summary(&src);
    Ok(0)
}

fn print_source_summary(src: &ParsedSource) {
    println!(
        "{} ({}, {}): {} tabela(s), {} item(ns)",
        src.name,
        src.sniff.kind.label_ptbr(),
        src.sniff.encoding,
        src.tables.len(),
        src.items.len()
    );
    for t in &src.tables {
        println!("  tabela {} — {} linha(s)", t.name, t.len());
        for c in &t.columns {
            let sub = c
                .subrole
                .as_deref()
                .map(|s| format!("/{s}"))
                .unwrap_or_default();
            println!(
                "    {:<28} {}{} ({:.0}%)",
                c.header,
                c.role,
                sub,
                c.confidence * 100.0
            );
        }
    }
    for item in src.items.iter().take(5) {
        let text: String = item.text().unwrap_or("").chars().take(70).collect();
        println!(
            "  item {:?} {} — {} menção(ões): {}",
            item.kind,
            item.locator.describe_ptbr(),
            item.entity_mentions.len(),
            text.replace('\n', " ")
        );
    }
    if src.items.len() > 5 {
        println!("  ... e mais {} item(ns)", src.items.len() - 5);
    }
    for w in &src.warnings {
        println!("  aviso: {w}");
    }
}

fn verdict_ptbr(v: Verdict) -> &'static str {
    match v {
        Verdict::Valid => "válido",
        Verdict::Invalid => "inválido (dígito verificador não confere)",
        Verdict::Malformed => "malformado",
    }
}

fn cmd_validate(kind: DocKind, value: &str) -> Result<i32> {
    match kind {
        DocKind::Cnpj | DocKind::Cpf => {
            let r = if matches!(kind, DocKind::Cnpj) {
                cnpj_dv(value)
            } else {
                cpf_dv(value)
            };
            println!(
                "{}: {}",
                r.formatted.as_deref().unwrap_or(value),
                verdict_ptbr(r.verdict)
            );
            if let (Verdict::Invalid, Some(dv)) = (r.verdict, &r.expected_dv) {
                println!("dígitos esperados: {dv}");
            }
            Ok(i32::from(r.verdict != Verdict::Valid))
        }
        DocKind::Boleto => {
            let b = parse_boleto(value).map_err(|e| anyhow!("{e}"))?;
            println!("{}", serde_json::to_string_pretty(&b)?);
            println!("{}", if b.valid { "válido" } else { "inválido" });
            Ok(i32::from(!b.valid))
        }
        DocKind::Nfe => {
            if let Some(k) = nfe_key_dv(value) {
                println!("{}", serde_json::to_string_pretty(&k)?);
                println!("{}", verdict_ptbr(k.verdict));
                Ok(i32::from(k.verdict != Verdict::Valid))
            } else {
                println!("malformado: a chave precisa ter 44 dígitos");
                Ok(1)
            }
        }
    }
}

fn parse_date_arg(s: &str) -> Result<NaiveDate> {
    date_ptbr(s, &DateContext::default())
        .map(|p| p.date)
        .map_err(|e| anyhow!("{e}"))
}

fn cmd_bizdays(from: &str, to: &str) -> Result<i32> {
    let (a, b) = (parse_date_arg(from)?, parse_date_arg(to)?);
    let n = business_days::business_days_between(a, b);
    println!(
        "dias úteis bancários entre {} e {}: {n}",
        a.format("%d/%m/%Y"),
        b.format("%d/%m/%Y")
    );
    let (lo, hi) = if a <= b { (a, b) } else { (b, a) };
    for year in chrono::Datelike::year(&lo)..=chrono::Datelike::year(&hi) {
        for (date, name) in business_days::holidays(year) {
            if date >= lo && date <= hi {
                println!("  feriado {}: {name}", date.format("%d/%m/%Y"));
            }
        }
    }
    if !business_days::is_business_day(hi) {
        println!(
            "  {} não é dia útil; rola para {}",
            hi.format("%d/%m/%Y"),
            business_days::roll_forward(hi).format("%d/%m/%Y")
        );
    }
    Ok(0)
}

fn cmd_probe(sql: &str, tables: &[String], json: bool) -> Result<i32> {
    let sandbox = Sandbox::new(SandboxConfig::default()).map_err(|e| anyhow!("{e}"))?;
    for spec in tables {
        let (name, path) = spec
            .split_once('=')
            .ok_or_else(|| anyhow!("--table espera nome=arquivo, recebeu {spec:?}"))?;
        let src = parse_file(Path::new(path), &InboxContext::default())
            .with_context(|| format!("convertendo {path}"))?;
        let table = src
            .tables
            .into_iter()
            .next()
            .ok_or_else(|| anyhow!("{path} não produziu tabela"))?;
        sandbox
            .register_table(name, &table)
            .map_err(|e| anyhow!("{e}"))?;
        eprintln!("tabela {name}: {} linha(s)", table.len());
    }
    match sandbox.run_probe(sql) {
        Ok(result) => {
            if json {
                println!("{}", serde_json::to_string_pretty(&result)?);
            } else {
                println!("{}", result.columns.join(" | "));
                for row in &result.rows {
                    let cells: Vec<String> = row
                        .iter()
                        .map(|v| match v {
                            serde_json::Value::String(s) => s.clone(),
                            serde_json::Value::Null => String::new(),
                            other => other.to_string(),
                        })
                        .collect();
                    println!("{}", cells.join(" | "));
                }
                println!(
                    "({} linha(s){}, {} ms)",
                    result.rows.len(),
                    if result.truncated { ", truncado" } else { "" },
                    result.elapsed_ms
                );
            }
            Ok(0)
        }
        Err(e) => {
            eprintln!("{e}");
            Ok(1)
        }
    }
}

#[derive(Serialize)]
struct RunReport {
    sources: Vec<SourceReport>,
    matches: Vec<TableMatch>,
    findings: Vec<dm_core::Finding>,
    /// Every row of every parsed table, with or without a document column.
    table_rows: usize,
    /// Rows that an exact match paired.
    explained_rows: usize,
    /// `table_rows - explained_rows`.
    unexplained_rows: usize,
    /// Text items (e-mails, chat lines); exact matching never explains them, so all of
    /// them are unexplained at this stage.
    text_items: usize,
    warnings: Vec<String>,
}

#[derive(Serialize)]
struct SourceReport {
    name: String,
    kind: String,
    tables: Vec<String>,
    items: usize,
    warnings: Vec<String>,
}

#[derive(Serialize)]
struct TableMatch {
    left: String,
    right: String,
    key_left: String,
    key_right: String,
    pairs: Vec<PairReport>,
    ambiguous_groups: usize,
    unmatched_left: Vec<String>,
    unmatched_right: Vec<String>,
}

#[derive(Serialize)]
struct PairReport {
    left_id: String,
    right_id: String,
    key: String,
    amount_rule: Option<String>,
    amount_residual: Option<String>,
}

fn cmd_run(dir: &Path, out: &Path, year: Option<i32>) -> Result<i32> {
    let mut entries: Vec<PathBuf> = std::fs::read_dir(dir)
        .with_context(|| format!("lendo a pasta {}", dir.display()))?
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.is_file())
        .collect();
    entries.sort();
    if entries.is_empty() {
        bail!("nenhum arquivo em {}", dir.display());
    }
    let ctx = inbox_context(year);
    let mut report = RunReport {
        sources: Vec::new(),
        matches: Vec::new(),
        findings: Vec::new(),
        table_rows: 0,
        explained_rows: 0,
        unexplained_rows: 0,
        text_items: 0,
        warnings: Vec::new(),
    };
    let mut tables: Vec<Table> = Vec::new();
    let mut text_items: Vec<Item> = Vec::new();
    for path in &entries {
        match parse_file(path, &ctx) {
            Ok(src) => {
                print_source_summary(&src);
                report.sources.push(SourceReport {
                    name: src.name.clone(),
                    kind: src.sniff.kind.id().to_string(),
                    tables: src.tables.iter().map(|t| t.name.clone()).collect(),
                    items: src.items.len(),
                    warnings: src.warnings.clone(),
                });
                tables.extend(src.tables);
                text_items.extend(src.items);
            }
            Err(e) => report.warnings.push(format!("{}: {e}", path.display())),
        }
    }
    // Injected instructions in text sources become findings and are never acted upon.
    let guard = dm_guard::screen_document(&text_items);
    report.findings.extend(guard.findings);

    // Exact matching on document numbers between every pair of tables that have one.
    struct TableItems {
        name: String,
        items: Vec<Item>,
        doc_col: Option<String>,
        amount_col: Option<String>,
    }
    let items_by_table: Vec<TableItems> = tables
        .iter()
        .map(|t| TableItems {
            name: t.name.clone(),
            items: table_items(t),
            doc_col: t
                .first_column_with_role(ColumnRole::DocumentId)
                .map(|i| t.columns[i].name.clone()),
            amount_col: t
                .first_column_with_role(ColumnRole::Amount)
                .map(|i| t.columns[i].name.clone()),
        })
        .collect();
    // Every table row starts unexplained, including rows of tables without a document
    // column: the tally below must say what the run could not explain, not only what it
    // tried to.
    let mut explained: BTreeMap<String, bool> = items_by_table
        .iter()
        .flat_map(|t| t.items.iter())
        .map(|item| (item.id.clone(), false))
        .collect();
    for (i, left) in items_by_table.iter().enumerate() {
        let (left_name, left_items, left_amount) = (&left.name, &left.items, &left.amount_col);
        let Some(left_doc) = &left.doc_col else {
            continue;
        };
        for right in items_by_table.iter().skip(i + 1) {
            let (right_name, right_items, right_amount) =
                (&right.name, &right.items, &right.amount_col);
            let Some(right_doc) = &right.doc_col else {
                continue;
            };
            let left_spec = KeySpec::single(left_doc.clone(), KeyTransform::DocNumber);
            let right_spec = KeySpec::single(right_doc.clone(), KeyTransform::DocNumber);
            let m = exact_match(left_items, right_items, &left_spec, &right_spec);
            let mut pairs = Vec::with_capacity(m.pairs.len());
            for p in &m.pairs {
                explained.insert(p.left_id.clone(), true);
                explained.insert(p.right_id.clone(), true);
                let (rule, residual) = match (left_amount, right_amount) {
                    (Some(la), Some(ra)) => {
                        let find = |items: &[Item], id: &str, col: &str| {
                            items
                                .iter()
                                .find(|it| it.id == id)
                                .and_then(|it| it.field(col))
                                .and_then(dm_core::Value::as_money)
                        };
                        match (
                            find(left_items, &p.left_id, la),
                            find(right_items, &p.right_id, ra),
                        ) {
                            (Some(left_value), Some(right_value)) => {
                                // Bank debits are negative: the positive side is the title
                                // (expected), the other side the payment (observed).
                                let (expected, observed) =
                                    if left_value.is_negative() && !right_value.is_negative() {
                                        (right_value.abs(), left_value.abs())
                                    } else {
                                        (left_value.abs(), right_value.abs())
                                    };
                                let ctx = AmountContext {
                                    expected,
                                    observed,
                                    due: None,
                                    paid: None,
                                };
                                match explain_difference(&ctx, &ToleranceRule::defaults()) {
                                    Some(e) => (
                                        Some(
                                            e.rules
                                                .iter()
                                                .map(ToleranceRule::label_ptbr)
                                                .collect::<Vec<_>>()
                                                .join(" + "),
                                        ),
                                        Some(e.residual.format_brl()),
                                    ),
                                    None => (None, Some(expected.abs_diff(observed).format_brl())),
                                }
                            }
                            _ => (None, None),
                        }
                    }
                    _ => (None, None),
                };
                pairs.push(PairReport {
                    left_id: p.left_id.clone(),
                    right_id: p.right_id.clone(),
                    key: p.key.clone(),
                    amount_rule: rule,
                    amount_residual: residual,
                });
            }
            report.matches.push(TableMatch {
                left: left_name.clone(),
                right: right_name.clone(),
                key_left: left_doc.clone(),
                key_right: right_doc.clone(),
                pairs,
                ambiguous_groups: m.groups.len(),
                unmatched_left: m.unmatched_left,
                unmatched_right: m.unmatched_right,
            });
        }
    }
    report.table_rows = explained.len();
    report.explained_rows = explained.values().filter(|v| **v).count();
    report.unexplained_rows = report.table_rows - report.explained_rows;
    report.text_items = text_items.len();
    std::fs::write(out, serde_json::to_vec_pretty(&report)?)
        .with_context(|| format!("gravando {}", out.display()))?;

    println!();
    println!(
        "fontes: {} | tabelas: {} | itens de texto: {}",
        report.sources.len(),
        tables.len(),
        text_items.len()
    );
    for m in &report.matches {
        println!("{} x {} (chave {} = {}): {} par(es) exatos, {} grupo(s) ambíguo(s), {} sem par à esquerda, {} sem par à direita", m.left, m.right, m.key_left, m.key_right, m.pairs.len(), m.ambiguous_groups, m.unmatched_left.len(), m.unmatched_right.len());
        for p in m.pairs.iter().take(10) {
            match (&p.amount_rule, &p.amount_residual) {
                (Some(rule), Some(res)) => println!(
                    "  {} <-> {} [{}] explicado por {rule} (resíduo {res})",
                    p.left_id, p.right_id, p.key
                ),
                (None, Some(res)) => println!(
                    "  {} <-> {} [{}] valores divergem em {res}: não explicado",
                    p.left_id, p.right_id, p.key
                ),
                _ => println!("  {} <-> {} [{}]", p.left_id, p.right_id, p.key),
            }
        }
    }
    if report.matches.is_empty() {
        println!("nenhum par de tabelas com número de documento para casar");
    }
    println!(
        "linhas de tabela: {} | explicadas: {} | não explicadas: {} | itens de texto ainda sem casamento: {} | achados de injeção: {}",
        report.table_rows,
        report.explained_rows,
        report.unexplained_rows,
        report.text_items,
        report.findings.len()
    );
    for w in &report.warnings {
        println!("aviso: {w}");
    }
    println!("relatório gravado em {}", out.display());
    Ok(0)
}
