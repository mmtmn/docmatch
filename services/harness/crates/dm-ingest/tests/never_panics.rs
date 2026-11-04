//! Every parser is total: arbitrary bytes (and structured-looking text) never panic.

use dm_ingest::inbox::InboxContext;
use dm_ingest::{
    parse_bytes, parse_csv, parse_eml, parse_mbox, parse_nfe_xml, parse_ofx, parse_txt_inbox,
    parse_xlsx, sniff,
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
}
