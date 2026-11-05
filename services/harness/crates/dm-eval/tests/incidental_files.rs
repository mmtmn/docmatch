//! An incidental document in a case folder does not change the reconciliation: a payment
//! schedule in a Word letter is not the bank statement, the statement is the table with the
//! most lines, and the inbox is every message source of the folder together.

use std::path::{Path, PathBuf};

use dm_eval::{find_twin_dir, run_golden};
use dm_recon::golden::GFinding;
use dm_recon::{run_dir, EngineConfig};

fn manifest() -> PathBuf {
    PathBuf::from(
        std::env::var("CARGO_MANIFEST_DIR")
            .expect("cargo define CARGO_MANIFEST_DIR ao rodar os testes"),
    )
}

fn twin() -> PathBuf {
    find_twin_dir(&manifest())
        .unwrap_or_else(|| panic!("testdata/golden/vantara-twin não encontrado"))
}

fn copy_case(from: &Path, skip: &[&str]) -> tempfile::TempDir {
    let dir = tempfile::tempdir().unwrap_or_else(|e| panic!("{e}"));
    for entry in std::fs::read_dir(from).unwrap_or_else(|e| panic!("{e}")) {
        let entry = entry.unwrap_or_else(|e| panic!("{e}"));
        let name = entry.file_name().to_string_lossy().to_string();
        if entry.path().is_file() && !skip.contains(&name.as_str()) {
            std::fs::copy(entry.path(), dir.path().join(&name)).unwrap_or_else(|e| panic!("{e}"));
        }
    }
    dir
}

fn w_par(text: &str) -> String {
    format!("<w:p><w:r><w:t xml:space=\"preserve\">{text}</w:t></w:r></w:p>")
}

fn w_table(rows: &[&[&str]]) -> String {
    let mut out = String::from("<w:tbl>");
    for row in rows {
        out.push_str("<w:tr>");
        for cell in *row {
            out.push_str(&format!("<w:tc>{}</w:tc>", w_par(cell)));
        }
        out.push_str("</w:tr>");
    }
    out.push_str("</w:tbl>");
    out
}

fn docx(body: &str) -> Vec<u8> {
    let mut out = std::io::Cursor::new(Vec::new());
    {
        use std::io::Write;
        let mut w = zip::ZipWriter::new(&mut out);
        let opts = zip::write::SimpleFileOptions::default();
        let document = format!(
            "<w:document xmlns:w=\"http://schemas.openxmlformats.org/wordprocessingml/2006/main\"><w:body>{body}</w:body></w:document>"
        );
        for (name, data) in [
            ("[Content_Types].xml", "<Types/>".to_string()),
            ("word/document.xml", document),
        ] {
            w.start_file(name, opts).unwrap_or_else(|e| panic!("{e}"));
            w.write_all(data.as_bytes())
                .unwrap_or_else(|e| panic!("{e}"));
        }
        w.finish().unwrap_or_else(|e| panic!("{e}"));
    }
    out.into_inner()
}

fn assert_like_the_twin(dir: &Path) {
    let run = run_golden(dir, &EngineConfig::default(), 2).unwrap_or_else(|e| panic!("{e}"));
    assert!(run.scorecard.perfect(), "{}", run.scorecard.report());
    assert!(run.identical);
}

/// A Word letter with a two-row payment schedule (`Vencimento | Descrição | Valor`) sorts
/// before the statement and reads like one: it stays a letter, and the run is the twin's.
#[test]
fn a_payment_schedule_in_a_letter_is_not_the_statement() {
    let dir = copy_case(&twin(), &[]);
    let body = format!(
        "{}{}{}{}",
        w_par("Acordo de pagamento - Padaria Pão Dourado"),
        w_par("Fica acordado o pagamento do saldo em duas parcelas:"),
        w_table(&[
            &["Vencimento", "Descrição", "Valor"],
            &["10/08/2026", "Parcela 1 de 2", "1.900,00"],
            &["10/09/2026", "Parcela 2 de 2", "1.900,00"],
        ]),
        w_par("Depósito na conta 12345-6, agência 0001."),
    );
    std::fs::write(dir.path().join("00_acordo_padaria.docx"), docx(&body))
        .unwrap_or_else(|e| panic!("{e}"));
    assert_like_the_twin(dir.path());
    let ds = run_dir(dir.path(), &EngineConfig::default())
        .unwrap_or_else(|e| panic!("{e}"))
        .dataset;
    assert_eq!(
        ds.sources.get("bank").map(String::as_str),
        Some("03_extrato_bancario_jul2026.csv")
    );
    assert!(
        ds.warnings
            .iter()
            .any(|w| w.starts_with("00_acordo_padaria.docx: lido")),
        "{:?}",
        ds.warnings
    );
}

/// The same schedule in an e-mail pasted into Word, next to the exported inbox: the document
/// holds messages but the message sources make the inbox; the schedule and the bank details
/// are its text.
#[test]
fn a_pasted_e_mail_next_to_the_inbox_stays_out_of_it() {
    let dir = copy_case(&twin(), &[]);
    let body = format!(
        "{}{}{}{}{}{}{}",
        w_par("De: financeiro@padaria.invalid"),
        w_par("Para: pagar@exemplo-sintetico.invalid"),
        w_par("Data: 01/08/2026 10:00"),
        w_par("Assunto: Acordo NF 9001"),
        w_par(""),
        w_table(&[
            &["Vencimento", "Descrição", "Valor"],
            &["10/08/2026", "Parcela 1 de 2", "1.900,00"],
            &["10/09/2026", "Parcela 2 de 2", "1.900,00"],
        ]),
        w_table(&[
            &["Banco", "341"],
            &["Agência", "0987"],
            &["Conta", "54321-0"]
        ]),
    );
    let bytes = docx(&body);
    let parsed = dm_ingest::parse_bytes(
        "00_acordo.docx",
        &bytes,
        &dm_ingest::InboxContext::default(),
    )
    .unwrap_or_else(|e| panic!("{e}"));
    assert_eq!(
        parsed.tables.len(),
        1,
        "the bank details box is not a table"
    );
    let text = dm_ingest::source::document_text(&parsed.text_parts, |_| true);
    assert!(
        text.contains("10/08/2026\tParcela 1 de 2\t1.900,00"),
        "{text}"
    );
    assert!(text.contains("Banco\t341"), "{text}");
    std::fs::write(dir.path().join("00_acordo.docx"), bytes).unwrap_or_else(|e| panic!("{e}"));
    assert_like_the_twin(dir.path());
    let ds = run_dir(dir.path(), &EngineConfig::default())
        .unwrap_or_else(|e| panic!("{e}"))
        .dataset;
    assert_eq!(ds.emails.len(), 3);
    assert!(
        ds.warnings
            .iter()
            .any(|w| w.starts_with("00_acordo.docx: tem mensagens")),
        "{:?}",
        ds.warnings
    );
    // Alone, the document is the inbox, its tables' text in the e-mail body.
    let alone = copy_case(&twin(), &["04_caixa_de_entrada_contas_a_pagar.txt"]);
    std::fs::write(alone.path().join("00_acordo.docx"), docx(&body))
        .unwrap_or_else(|e| panic!("{e}"));
    let ds = run_dir(alone.path(), &EngineConfig::default())
        .unwrap_or_else(|e| panic!("{e}"))
        .dataset;
    assert_eq!(ds.emails.len(), 1);
    assert!(
        ds.emails[0].body.contains("Banco\t341"),
        "{}",
        ds.emails[0].body
    );
    assert!(ds.emails[0].body.contains("Parcela 2 de 2"));
}

/// A second, shorter statement sorting first does not replace the real one.
#[test]
fn the_statement_is_the_table_with_the_most_lines() {
    let dir = copy_case(&twin(), &[]);
    std::fs::write(
        dir.path().join("00_extrato_parcial.csv"),
        "data;historico;documento;valor;tipo\n01/07/2026;SALDO ANTERIOR;;0,00;C\n02/07/2026;TARIFA;;12,00;D\n",
    )
    .unwrap_or_else(|e| panic!("{e}"));
    assert_like_the_twin(dir.path());
    let ds = run_dir(dir.path(), &EngineConfig::default())
        .unwrap_or_else(|e| panic!("{e}"))
        .dataset;
    assert_eq!(ds.lines.len(), 18);
    assert!(
        ds.warnings
            .iter()
            .any(|w| w.starts_with("00_extrato_parcial.csv: segundo extrato")),
        "{:?}",
        ds.warnings
    );
}

/// One e-mail of the twin's inbox dump as an `.eml` (`Data: dd/mm/aaaa hh:mm` becomes an RFC
/// 2822 date at UTC-03:00).
struct Mail {
    from: String,
    to: String,
    date: String,
    subject: String,
    body: String,
}

impl Mail {
    fn eml(&self) -> String {
        const MONTHS: [&str; 12] = [
            "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
        ];
        let (day, rest) = self.date.split_once('/').unwrap_or_default();
        let (month, rest) = rest.split_once('/').unwrap_or_default();
        let (year, time) = rest.split_once(' ').unwrap_or_default();
        let m: usize = month.parse().unwrap_or(1);
        format!(
            "From: {}\r\nTo: {}\r\nDate: {day} {} {year} {time}:00 -0300\r\nSubject: {}\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n{}\r\n",
            self.from,
            self.to,
            MONTHS[m - 1],
            self.subject,
            self.body.replace('\n', "\r\n")
        )
    }
}

/// The e-mails of the twin's dump, in its order, and the chat section as it is written.
fn twin_mails() -> (Vec<Mail>, String) {
    let text = std::fs::read_to_string(twin().join("04_caixa_de_entrada_contas_a_pagar.txt"))
        .unwrap_or_else(|e| panic!("{e}"));
    let mut mails = Vec::new();
    let mut chat = String::new();
    for section in text.split("\n--- ").skip(1) {
        if section.starts_with("TRECHO") {
            chat = format!("--- {section}");
            continue;
        }
        let (head, body) = section.split_once("\n\n").unwrap_or_default();
        let value = |key: &str| {
            head.lines()
                .find_map(|l| l.strip_prefix(key))
                .unwrap_or_default()
                .trim()
                .to_string()
        };
        mails.push(Mail {
            from: value("De:"),
            to: value("Para:"),
            date: value("Data:"),
            subject: value("Assunto:"),
            body: body.trim().to_string(),
        });
    }
    (mails, chat)
}

fn ap(findings: &[GFinding]) -> Vec<(String, i64)> {
    let mut out: Vec<(String, i64)> = findings
        .iter()
        .filter(|f| f.kind.starts_with("ap_"))
        .map(|f| (f.kind.clone(), f.amount_at_risk_brl.0))
        .collect();
    out.sort();
    out
}

/// The `.msg` variant with its inbox dump replaced by the twin's second e-mail as an `.eml`:
/// the banker's message and the supplier's are one inbox (before, the tie went to the first
/// file, the banker's, and the supplier's e-mail was dropped), and the supplier's e-mail still
/// gives its finding.
#[test]
fn a_single_eml_and_an_outlook_message_are_one_inbox() {
    let variant = twin().with_file_name("vantara-twin-msg-7z");
    let dir = copy_case(
        &variant,
        &["04_caixa_de_entrada_contas_a_pagar.txt", "expected.yaml"],
    );
    let (mails, _) = twin_mails();
    std::fs::write(dir.path().join("04_cobranca_boa_vista.eml"), mails[1].eml())
        .unwrap_or_else(|e| panic!("{e}"));
    let run = run_dir(dir.path(), &EngineConfig::default()).unwrap_or_else(|e| panic!("{e}"));
    let ds = &run.dataset;
    let files: Vec<&str> = ds.emails.iter().map(|e| e.file.as_str()).collect();
    assert_eq!(
        files,
        vec![
            "04_cobranca_boa_vista.eml",
            "03_extrato_bancario_jul2026.msg"
        ],
        "time order: 18/07 before 03/08"
    );
    assert!(
        !ds.warnings.iter().any(|w| w.contains("caixa de entrada")),
        "{:?}",
        ds.warnings
    );
    // What the e-mail says on its own: the boleto line is invalid. The findings that compare
    // it with the first invoice e-mail (already paid, early recharge, changed sender) need
    // that e-mail, which this folder does not have.
    assert_eq!(
        ap(&run.result.findings),
        vec![("ap_boleto_invalid".to_string(), 1_499_400)]
    );
    let boleto = run
        .result
        .findings
        .iter()
        .find(|f| f.kind == "ap_boleto_invalid")
        .map(|f| f.evidence.clone());
    assert_eq!(boleto, Some(vec!["email:1".to_string()]));
}

/// The twin's three e-mails as three loose `.eml` files and its chat as a text dump make one
/// inbox, in time order, with the twin's payables findings.
#[test]
fn loose_eml_files_form_one_inbox() {
    let dir = copy_case(
        &twin(),
        &["04_caixa_de_entrada_contas_a_pagar.txt", "expected.yaml"],
    );
    let (mails, chat) = twin_mails();
    for (i, mail) in mails.iter().enumerate() {
        std::fs::write(
            dir.path().join(format!("04_email_{}.eml", i + 1)),
            mail.eml(),
        )
        .unwrap_or_else(|e| panic!("{e}"));
    }
    std::fs::write(dir.path().join("05_grupo_aprovacoes.txt"), chat)
        .unwrap_or_else(|e| panic!("{e}"));
    // The same three e-mails once more, in an mbox: duplicates are not counted twice.
    let mut mbox = String::new();
    for m in &mails {
        mbox.push_str("From MAILER-DAEMON Thu Jan  1 00:00:00 2026\n");
        mbox.push_str(&m.eml().replace("\r\n", "\n"));
        mbox.push('\n');
    }
    std::fs::write(dir.path().join("06_copia.mbox"), mbox).unwrap_or_else(|e| panic!("{e}"));
    let run = run_dir(dir.path(), &EngineConfig::default()).unwrap_or_else(|e| panic!("{e}"));
    let ds = &run.dataset;
    let subjects: Vec<&str> = ds.emails.iter().map(|e| e.subject.as_str()).collect();
    assert_eq!(
        subjects,
        vec![
            "NF 55120 - Fatura de embalagens",
            "Faturas semana 27",
            "RE: cobranca em aberto - Caixas Boa Vista",
        ],
        "time order, each e-mail once"
    );
    assert_eq!(ds.chat.len(), 7);
    let twin_run = run_dir(&twin(), &EngineConfig::default()).unwrap_or_else(|e| panic!("{e}"));
    assert_eq!(ap(&run.result.findings), ap(&twin_run.result.findings));
}
