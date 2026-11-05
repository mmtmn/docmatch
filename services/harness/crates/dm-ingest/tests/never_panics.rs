//! Every parser is total: arbitrary bytes (and structured-looking text) never panic.

use dm_ingest::inbox::InboxContext;
use dm_ingest::{
    parse_bytes, parse_cnab240, parse_cnab400, parse_csv, parse_eml, parse_mbox, parse_nfe_xml,
    parse_ofx, parse_pdf, parse_sped, parse_txt_inbox, parse_xlsx, sniff,
};
use dm_normalize::DateContext;
use proptest::prelude::*;

/// Text drawn from an alphabet that exercises delimiters, quotes, digits, markers and tags.
fn structured_text() -> impl Strategy<Value = String> {
    let atoms = prop_oneof![
        Just(";".to_string()),
        Just(",".to_string()),
        Just("\t".to_string()),
        Just("|".to_string()),
        Just("\"".to_string()),
        Just("\n".to_string()),
        Just("\r\n".to_string()),
        Just(" ".to_string()),
        Just("R$ 1.234,56".to_string()),
        Just("05/03/2025".to_string()),
        Just("[05/03 14:30] Ana: oi".to_string()),
        Just("--- E-MAIL 1 ---".to_string()),
        Just("De: a@example.com".to_string()),
        Just("From: b@example.com".to_string()),
        Just("Assunto: NF 088231".to_string()),
        Just("<STMTTRN><TRNAMT>-1.50<DTPOSTED>20250305</STMTTRN>".to_string()),
        Just("<NFe><infNFe Id=\"NFe1\"><ide><nNF>1</nNF></ide></infNFe></NFe>".to_string()),
        Just("<a><b></a>".to_string()),
        Just("11.222.333/0001-81".to_string()),
        Just("Vencimento".to_string()),
        Just("Valor".to_string()),
        Just("ação".to_string()),
        Just("\u{feff}".to_string()),
        Just("|0000|017|0|01072026|31072026|X|11222333000181||SP|1||1|||A|1|".to_string()),
        Just("|C100|1|0|P1|55|00|1|1||05072026|05072026|1,00|".to_string()),
        Just("|9999|3|".to_string()),
        Just("%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj".to_string()),
        Just("BT /F1 9 Tf 40 700 Td (05/07/2026 PIX 1.234,56 C) Tj ET".to_string()),
        Just("xref\n0 1\n0000000000 65535 f \ntrailer << /Root 1 0 R >>".to_string()),
        "[0-9 TUPQAJB]{240}".prop_map(|s| s),
        "[0-9 ]{400}".prop_map(|s| s),
        Just("\u{0}".to_string()),
        "[0-9]{1,50}".prop_map(|s| s),
        "[a-zA-Z ]{0,12}".prop_map(|s| s),
    ];
    proptest::collection::vec(atoms, 0..40).prop_map(|v| v.concat())
}

fn exercise(bytes: &[u8]) {
    let ctx = InboxContext {
        reference_year: Some(2025),
        reference_date: None,
    };
    let sniffed = sniff(bytes);
    let _ = parse_csv(bytes, &sniffed, "t", "src");
    let _ = parse_xlsx(bytes, "t", "src");
    let _ = parse_ofx(bytes, "t", "src");
    let _ = parse_nfe_xml(bytes, "t", "src");
    let _ = parse_eml(bytes, "src", &ctx);
    let _ = parse_mbox(bytes, "src", &ctx);
    let _ = parse_bytes("arquivo.bin", bytes, &ctx);
    let _ = parse_pdf(bytes, "t", "src");
    let _ = parse_cnab240(bytes, "t", "src");
    let _ = parse_cnab400(bytes, "t", "src");
    let _ = parse_sped(bytes, "t", "src");
    let _ = dm_ingest::archive::read_zip(bytes, &dm_ingest::archive::ZipLimits::default());
    let text = String::from_utf8_lossy(bytes);
    let _ = parse_txt_inbox(&text, "src", &ctx);
    let _ = dm_ingest::extract_mentions(&text, &DateContext::with_year(2025));
}

proptest! {
    #[test]
    fn arbitrary_bytes_never_panic(bytes in proptest::collection::vec(any::<u8>(), 0..2048)) {
        exercise(&bytes);
    }

    #[test]
    fn structured_text_never_panics(text in structured_text()) {
        exercise(text.as_bytes());
    }

    #[test]
    fn latin1_bytes_never_panic(bytes in proptest::collection::vec(0x20u8..=0xFF, 0..1024)) {
        exercise(&bytes);
    }

    /// Damaged copies of the real fixtures: a byte overwritten anywhere, then a cut.
    #[test]
    fn damaged_fixtures_never_panic(
        which in 0usize..FIXTURES.len(),
        at in any::<usize>(),
        byte in any::<u8>(),
        cut in any::<usize>(),
    ) {
        let mut bytes = fixture(which);
        if !bytes.is_empty() {
            let i = at % bytes.len();
            bytes[i] = byte;
            let keep = cut % (bytes.len() + 1);
            bytes.truncate(keep.max(i + 1));
        }
        exercise(&bytes);
    }
}

/// Fixtures whose damaged copies are fed to every parser.
const FIXTURES: [&str; 9] = [
    "pdf/extrato_credito_debito.pdf",
    "pdf/carta_cobranca.pdf",
    "pdf/extrato_digitalizado.pdf",
    "cnab240/retorno_cobranca.ret",
    "cnab240/retorno_pagamentos.ret",
    "cnab400/retorno_itau_341.ret",
    "sped/efd_icms_ipi_jul2026.txt",
    "zip/pacote_jul2026.zip",
    "vantara-twin-pdf-cnab240/03_extrato_bancario_jul2026.pdf",
];

fn fixture(which: usize) -> Vec<u8> {
    let path = std::path::Path::new(
        &std::env::var("CARGO_MANIFEST_DIR")
            .expect("cargo define CARGO_MANIFEST_DIR ao rodar os testes"),
    )
    .join("../../../../testdata/golden")
    .join(FIXTURES[which]);
    std::fs::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

/// Every truncation of the PDF and ZIP fixtures (step 7 bytes): the parsers must never panic on
/// a file cut short by an upload.
#[test]
fn truncated_containers_never_panic() {
    for which in [0, 7, 8] {
        let bytes = fixture(which);
        for len in (0..bytes.len()).step_by(7) {
            exercise(&bytes[..len]);
        }
    }
}
