//! `dm`: CLI do harness para inspecionar arquivos, validar documentos, contar dias úteis,
//! sondar tabelas no sandbox e rodar a conciliação determinística em uma pasta.
#![forbid(unsafe_code)]
#![allow(clippy::print_stdout, clippy::print_stderr)]

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use anyhow::{anyhow, Context, Result};
use chrono::NaiveDate;
use clap::{Parser, Subcommand, ValueEnum};
use dm_ingest::{parse_file, sniff, InboxContext, ParsedSource};
use dm_normalize::{date_ptbr, DateContext};
use dm_recon::{run_dir, EngineConfig};
use dm_sandbox::{Sandbox, SandboxConfig};
use dm_validators::{business_days, cnpj_dv, cpf_dv, nfe_key_dv, parse_boleto, Verdict};

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
    /// Concilia uma pasta (títulos a receber, extrato, caixa de contas a pagar) e grava o resultado
    Run {
        /// Pasta com os arquivos
        dir: PathBuf,
        /// Arquivo JSON de saída (formato dourado)
        #[arg(long, default_value = "result.json")]
        out: PathBuf,
        /// Configuração YAML do motor (padrões em services/harness/config/recon.yaml)
        #[arg(long)]
        config: Option<PathBuf>,
    },
    /// Avalia resultados contra gabaritos
    Eval {
        #[command(subcommand)]
        command: EvalCommand,
    },
}

#[derive(Subcommand)]
enum EvalCommand {
    /// Compara um result.json com um expected.yaml e lista as diferenças
    Score {
        /// Gabarito (expected.yaml)
        #[arg(long)]
        expected: PathBuf,
        /// Resultado (result.json)
        #[arg(long)]
        actual: PathBuf,
    },
    /// Roda o gêmeo público e, com DM_VANTARA_PATH, o caso privado; exige 100% e execuções idênticas
    Golden {
        /// Execuções por caso
        #[arg(long, default_value_t = 10)]
        runs: usize,
        /// Pastas de casos (padrão: o gêmeo público, suas variações de formato e DM_VANTARA_PATH)
        #[arg(long = "dir")]
        dirs: Vec<PathBuf>,
        /// Configuração YAML do motor
        #[arg(long)]
        config: Option<PathBuf>,
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
        Command::Run { dir, out, config } => cmd_run(&dir, &out, config.as_deref()),
        Command::Eval { command } => match command {
            EvalCommand::Score { expected, actual } => cmd_eval_score(&expected, &actual),
            EvalCommand::Golden { runs, dirs, config } => {
                cmd_eval_golden(runs, &dirs, config.as_deref())
            }
        },
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

fn load_config(path: Option<&Path>) -> Result<EngineConfig> {
    match path {
        None => Ok(EngineConfig::default()),
        Some(p) => {
            let text =
                std::fs::read_to_string(p).with_context(|| format!("lendo {}", p.display()))?;
            EngineConfig::from_yaml(&text).map_err(|e| anyhow!(e))
        }
    }
}

fn cmd_run(dir: &Path, out: &Path, config: Option<&Path>) -> Result<i32> {
    let cfg = load_config(config)?;
    let run = run_dir(dir, &cfg).map_err(|e| anyhow!(e))?;
    let result = &run.result;
    std::fs::write(out, result.to_json()).with_context(|| format!("gravando {}", out.display()))?;
    let explained = result
        .bank_lines
        .iter()
        .filter(|l| l.kind != "unapplied")
        .count();
    let mut status: BTreeMap<&str, usize> = BTreeMap::new();
    for t in &result.titles {
        *status.entry(t.status.as_str()).or_default() += 1;
    }
    let mut severity: BTreeMap<&str, usize> = BTreeMap::new();
    for f in &result.findings {
        *severity.entry(f.severity.as_str()).or_default() += 1;
    }
    println!("{} ({})", result.case, result.run_id);
    println!(
        "  extrato: {explained}/{} linhas explicadas · títulos: {}",
        result.bank_lines.len(),
        status
            .iter()
            .map(|(k, v)| format!("{v} {k}"))
            .collect::<Vec<_>>()
            .join(", ")
    );
    println!(
        "  achados: {} ({})",
        result.findings.len(),
        severity
            .iter()
            .map(|(k, v)| format!("{v} {k}"))
            .collect::<Vec<_>>()
            .join(", ")
    );
    let mut top: Vec<_> = result.findings.iter().collect();
    top.sort_by(|a, b| {
        b.amount_at_risk_brl
            .cmp(&a.amount_at_risk_brl)
            .then(a.code.cmp(&b.code))
    });
    for f in top.iter().take(5) {
        println!(
            "    {} [{}] R$ {} — {}",
            f.code, f.severity, f.amount_at_risk_brl, f.title_ptbr
        );
    }
    println!("  resultado em {}", out.display());
    Ok(0)
}

fn cmd_eval_score(expected: &Path, actual: &Path) -> Result<i32> {
    let e = dm_eval::load(expected).map_err(|e| anyhow!(e))?;
    let a = dm_eval::load(actual).map_err(|e| anyhow!(e))?;
    let card = dm_eval::score(&e, &a);
    print!("{}", card.report());
    Ok(i32::from(!card.perfect()))
}

fn cmd_eval_golden(runs: usize, dirs: &[PathBuf], config: Option<&Path>) -> Result<i32> {
    let cfg = load_config(config)?;
    // (folder, private): private cases print only counts, never their values.
    let mut cases: Vec<(PathBuf, bool)> = dirs.iter().map(|d| (d.clone(), false)).collect();
    if cases.is_empty() {
        let cwd = std::env::current_dir()?;
        let public = dm_eval::find_golden_dirs(&cwd);
        if public.is_empty() {
            return Err(anyhow!(
                "testdata/golden/vantara-twin não encontrado a partir de {}",
                cwd.display()
            ));
        }
        cases.extend(public.into_iter().map(|d| (d, false)));
        match std::env::var("DM_VANTARA_PATH") {
            Ok(p) if !p.is_empty() => cases.push((PathBuf::from(p), true)),
            _ => println!("caso privado: pulado (DM_VANTARA_PATH não definido)"),
        }
    }
    let mut code = 0;
    for (dir, private) in &cases {
        let run = dm_eval::run_golden(dir, &cfg, runs).map_err(|e| anyhow!(e))?;
        let label = if *private {
            "caso privado".to_string()
        } else {
            dir.display().to_string()
        };
        let ok = run.scorecard.perfect() && run.identical;
        println!(
            "{label}: {} · totais {}/{} · {} execuções {} · {}",
            run.scorecard.summary(),
            run.scorecard.totals.ok,
            run.scorecard.totals.total,
            run.runs,
            if run.identical {
                "idênticas"
            } else {
                "DIFERENTES"
            },
            if ok { "100%" } else { "FALHOU" }
        );
        if !ok {
            code = 1;
            if !*private {
                print!("{}", run.scorecard.report());
            }
        }
    }
    Ok(code)
}
