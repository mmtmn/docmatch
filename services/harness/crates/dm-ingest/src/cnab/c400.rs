//! CNAB 400 collection return (retorno de cobrança).
//!
//! The 400 layouts are per bank. Positions live in YAML files under `layouts/` (embedded at
//! build time): `comum.yaml` holds the fields every consulted manual agrees on, and each bank
//! file adds or overrides fields, selected by the bank code of the header (positions
//! 077-079). A new bank is a new layout file plus one line in [`LAYOUT_FILES`]. A bank without
//! a layout, or a layout marked `partial`, reads only the common fields and says so.
//!
//! Header (record 0), standard across banks: 002 `2` retorno / `1` remessa, 003-009
//! `RETORNO`, 077-079 bank code, 080-094 bank name, 095-100 generation date (DDMMAA).

use std::collections::BTreeMap;
use std::sync::LazyLock;

use serde::Deserialize;

use super::{
    collection_table, money_field, records, unrecognized, CnabParsed, CollectionRow, Problems,
    Record, Situation,
};
use crate::IngestError;

/// Embedded layout files: the common base first, then one per bank.
const LAYOUT_FILES: [(&str, &str); 7] = [
    ("comum.yaml", include_str!("layouts/comum.yaml")),
    ("itau.yaml", include_str!("layouts/itau.yaml")),
    ("bradesco.yaml", include_str!("layouts/bradesco.yaml")),
    ("santander.yaml", include_str!("layouts/santander.yaml")),
    (
        "banco_do_brasil.yaml",
        include_str!("layouts/banco_do_brasil.yaml"),
    ),
    ("caixa.yaml", include_str!("layouts/caixa.yaml")),
    ("sicoob.yaml", include_str!("layouts/sicoob.yaml")),
];

/// Field ids a layout may position. Dates are `DDMMAA`, amounts carry 2 implied decimals.
const KNOWN_FIELDS: [&str; 19] = [
    "codigo_ocorrencia",
    "data_ocorrencia",
    "seu_numero",
    "nosso_numero",
    "uso_empresa",
    "sacado",
    "vencimento",
    "valor_titulo",
    "banco_cobrador",
    "tarifa",
    "outras_despesas",
    "iof",
    "abatimento",
    "desconto",
    "valor_pago",
    "juros_multa",
    "outros_creditos",
    "data_credito",
    "motivo",
];

#[derive(Debug, Clone, Deserialize)]
struct Layout {
    #[serde(default)]
    bank: Option<String>,
    name: String,
    #[allow(dead_code)] // documentation of the source, kept with the positions
    source: String,
    #[serde(default)]
    detail_records: Vec<String>,
    #[serde(default)]
    fields: BTreeMap<String, [usize; 2]>,
    #[serde(default)]
    occurrences: BTreeMap<String, (String, Situation)>,
    #[serde(default)]
    partial: bool,
    #[serde(default)]
    note: Option<String>,
}

struct Registry {
    common: Option<Layout>,
    banks: BTreeMap<String, Layout>,
    errors: Vec<String>,
}

fn validate(file: &str, layout: &Layout) -> Result<(), String> {
    for (field, [start, end]) in &layout.fields {
        if !KNOWN_FIELDS.contains(&field.as_str()) {
            return Err(format!("{file}: campo desconhecido {field}"));
        }
        if *start == 0 || end < start || *end > 400 {
            return Err(format!("{file}: posições inválidas para {field}"));
        }
    }
    Ok(())
}

static REGISTRY: LazyLock<Registry> = LazyLock::new(|| {
    let mut reg = Registry {
        common: None,
        banks: BTreeMap::new(),
        errors: Vec::new(),
    };
    for (file, text) in LAYOUT_FILES {
        let layout: Layout = match serde_saphyr::from_str(text) {
            Ok(l) => l,
            Err(e) => {
                reg.errors.push(format!("{file}: {e}"));
                continue;
            }
        };
        if let Err(e) = validate(file, &layout) {
            reg.errors.push(e);
            continue;
        }
        match layout.bank.clone() {
            None => reg.common = Some(layout),
            Some(bank) => {
                reg.banks.insert(bank, layout);
            }
        }
    }
    reg
});

/// Bank codes with a CNAB 400 layout file, and the bank names.
pub fn layout_banks() -> Vec<(String, String)> {
    REGISTRY
        .banks
        .iter()
        .map(|(code, l)| (code.clone(), l.name.clone()))
        .collect()
}

/// The effective layout of a detail record: common fields, then the bank's on top.
struct Effective<'a> {
    fields: BTreeMap<&'a str, [usize; 2]>,
    occurrences: BTreeMap<&'a str, (&'a str, Situation)>,
}

fn effective<'a>(common: &'a Layout, bank: Option<&'a Layout>) -> Effective<'a> {
    let mut fields: BTreeMap<&str, [usize; 2]> = common
        .fields
        .iter()
        .map(|(k, v)| (k.as_str(), *v))
        .collect();
    let mut occurrences: BTreeMap<&str, (&str, Situation)> = common
        .occurrences
        .iter()
        .map(|(k, (l, s))| (k.as_str(), (l.as_str(), *s)))
        .collect();
    if let Some(b) = bank {
        fields.extend(b.fields.iter().map(|(k, v)| (k.as_str(), *v)));
        occurrences.extend(
            b.occurrences
                .iter()
                .map(|(k, (l, s))| (k.as_str(), (l.as_str(), *s))),
        );
    }
    Effective {
        fields,
        occurrences,
    }
}

impl Effective<'_> {
    fn text(&self, r: &Record, field: &str) -> String {
        self.fields
            .get(field)
            .map(|[s, e]| r.text(*s, *e))
            .unwrap_or_default()
    }

    fn raw(&self, r: &Record, field: &str) -> String {
        self.fields
            .get(field)
            .map(|[s, e]| r.raw(*s, *e))
            .unwrap_or_default()
    }

    fn row(&self, r: &Record, bank: &str, problems: &mut Problems) -> CollectionRow {
        let code = self.text(r, "codigo_ocorrencia");
        let (label, situation) = self
            .occurrences
            .get(code.as_str())
            .copied()
            .unwrap_or(("", Situation::Outro));
        CollectionRow {
            line: r.line,
            bank: bank.to_string(),
            movement: label.to_string(),
            situation: Some(situation),
            movement_code: code,
            nosso_numero: self.text(r, "nosso_numero"),
            seu_numero: self.text(r, "seu_numero"),
            company_ref: self.text(r, "uso_empresa"),
            payer: self.text(r, "sacado"),
            due: problems.date(&self.raw(r, "vencimento"), true),
            face_value: money_field(&self.raw(r, "valor_titulo")),
            paid: money_field(&self.raw(r, "valor_pago")),
            interest: money_field(&self.raw(r, "juros_multa")),
            discount: money_field(&self.raw(r, "desconto")),
            rebate: money_field(&self.raw(r, "abatimento")),
            iof: money_field(&self.raw(r, "iof")),
            fee: money_field(&self.raw(r, "tarifa")),
            other_expenses: money_field(&self.raw(r, "outras_despesas")),
            other_credits: money_field(&self.raw(r, "outros_creditos")),
            occurred: problems.date(&self.raw(r, "data_ocorrencia"), true),
            credited: problems.date(&self.raw(r, "data_credito"), true),
            reason: self.text(r, "motivo"),
            ..CollectionRow::default()
        }
    }
}

/// Parses a CNAB 400 collection return into the collection table.
pub fn parse_cnab400(bytes: &[u8], name: &str, source_id: &str) -> Result<CnabParsed, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let reg = &*REGISTRY;
    let Some(common) = reg.common.as_ref() else {
        return Err(IngestError::Parse {
            format: "cnab400",
            detail: format!("layouts inválidos: {}", reg.errors.join("; ")),
        });
    };
    let text = super::decode(bytes);
    let (recs, padded, long) = records(&text, 400);
    if recs.is_empty() {
        return Err(unrecognized("registros não têm 400 posições"));
    }
    let mut problems = Problems {
        padded,
        long,
        ..Problems::default()
    };
    let header = recs.iter().find(|r| r.at(1) == '0');
    if let Some(h) = header {
        if h.at(2) == '1' || h.text(3, 9).eq_ignore_ascii_case("REMESSA") {
            return Err(unrecognized(
                "CNAB 400 de remessa: só o retorno de cobrança é lido",
            ));
        }
    } else {
        problems
            .other
            .push("sem header de arquivo (registro 0): banco desconhecido".to_string());
    }
    let bank = header.map(|h| h.text(77, 79)).unwrap_or_default();
    let bank_layout = reg.banks.get(&bank);
    match bank_layout {
        Some(l) if l.partial => {
            if let Some(note) = &l.note {
                problems.other.push(note.clone());
            }
        }
        Some(_) => {}
        None if !bank.is_empty() => problems.other.push(format!(
            "banco {bank} sem layout CNAB 400 próprio: lidos só os campos comuns (ocorrência, datas, seu número, vencimento e valores)"
        )),
        None => {}
    }
    let detail_types: Vec<char> = bank_layout
        .unwrap_or(common)
        .detail_records
        .iter()
        .filter_map(|s| s.chars().next())
        .collect();
    let full = effective(common, bank_layout.filter(|l| !l.partial));
    let base = effective(common, None);
    let mut rows = Vec::new();
    let mut common_only = 0usize;
    let mut trailer = false;
    let mut other_types: BTreeMap<char, usize> = BTreeMap::new();
    for r in &recs {
        let kind = r.at(1);
        if detail_types.contains(&kind) {
            rows.push(full.row(r, &bank, &mut problems));
        } else if kind == '1' {
            // A standard detail record of a layout this bank file does not position.
            common_only += 1;
            rows.push(base.row(r, &bank, &mut problems));
        } else if kind == '9' {
            trailer = true;
        } else if kind != '0' {
            *other_types.entry(kind).or_default() += 1;
        }
    }
    if rows.is_empty() {
        return Err(unrecognized("CNAB 400 sem registros de detalhe"));
    }
    if common_only > 0 {
        let note = bank_layout.and_then(|l| l.note.clone()).unwrap_or_else(|| {
            "registros de detalhe tipo 1 lidos só com os campos comuns".to_string()
        });
        problems
            .other
            .push(format!("{note} ({common_only} registro(s))"));
    }
    if !trailer {
        problems
            .other
            .push("sem trailer (registro 9): o arquivo pode estar incompleto".to_string());
    }
    for (kind, n) in other_types {
        problems
            .other
            .push(format!("{n} registro(s) do tipo '{kind}' não lido(s)"));
    }
    Ok(CnabParsed {
        tables: vec![collection_table(name, source_id, &rows)],
        warnings: problems.into_warnings(400),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;
    use dm_core::{Money, Value};

    fn rec(fields: &[(usize, &str)]) -> String {
        let mut line = vec![' '; 400];
        for (start, value) in fields {
            for (i, c) in value.chars().enumerate() {
                line[start - 1 + i] = c;
            }
        }
        line.into_iter().collect()
    }

    fn header(bank: &str) -> String {
        rec(&[(1, "02RETORNO01COBRANCA"), (77, bank), (95, "100726")])
    }

    fn common_detail(kind: &str) -> Vec<(usize, &str)> {
        vec![
            (1, kind),
            (109, "06"),
            (111, "100726"),
            (117, "NF 200311"),
            (147, "100726"),
            (153, "0000002430000"),
            (166, "999"),
            (254, "0000002431000"),
            (267, "0000000001000"),
        ]
    }

    #[test]
    fn every_layout_file_parses_and_validates() {
        assert!(REGISTRY.errors.is_empty(), "{:?}", REGISTRY.errors);
        assert!(REGISTRY.common.is_some());
        let banks: Vec<String> = layout_banks().into_iter().map(|(c, _)| c).collect();
        assert_eq!(banks, vec!["001", "033", "104", "237", "341", "756"]);
    }

    #[test]
    fn itau_reads_its_own_positions() {
        let mut d = common_detail("1");
        d.extend([
            (63, "98765001"),
            (296, "110726"),
            (325, "PAO E ARTE PANIFICADORA"),
            (176, "0000000000350"),
        ]);
        let text = [header("341"), rec(&d), rec(&[(1, "9")])].join("\r\n");
        let parsed = parse_cnab400(text.as_bytes(), "itau", "src").unwrap();
        assert!(parsed.warnings.is_empty(), "{:?}", parsed.warnings);
        let t = &parsed.tables[0];
        let col = |n: &str| t.column_index(n).unwrap();
        assert_eq!(
            t.value(0, col("nosso_numero")).unwrap().as_text(),
            Some("98765001")
        );
        assert_eq!(
            t.value(0, col("seu_numero")).unwrap().as_text(),
            Some("NF 200311")
        );
        assert_eq!(
            t.value(0, col("sacado")).unwrap().as_text(),
            Some("PAO E ARTE PANIFICADORA")
        );
        assert_eq!(
            t.value(0, col("valor_do_titulo")),
            Some(&Value::Money(Money::from_centavos(2_430_000)))
        );
        assert_eq!(
            t.value(0, col("valor_pago")),
            Some(&Value::Money(Money::from_centavos(2_431_000)))
        );
        assert_eq!(
            t.value(0, col("juros_e_multa")),
            Some(&Value::Money(Money::from_centavos(1_000)))
        );
        assert_eq!(
            t.value(0, col("tarifa")),
            Some(&Value::Money(Money::from_centavos(350)))
        );
        assert_eq!(
            t.value(0, col("data_do_credito")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 11).unwrap()))
        );
        assert_eq!(
            t.value(0, col("vencimento")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 10).unwrap()))
        );
        assert_eq!(
            t.value(0, col("situacao")).unwrap().as_text(),
            Some("Liquidado")
        );
        assert_eq!(
            t.value(0, col("movimento")).unwrap().as_text(),
            Some("Liquidação normal")
        );
    }

    #[test]
    fn bradesco_and_banco_do_brasil() {
        let mut d = common_detail("1");
        d.push((71, "000098765001"));
        let text = [header("237"), rec(&d), rec(&[(1, "9")])].join("\n");
        let parsed = parse_cnab400(text.as_bytes(), "bradesco", "src").unwrap();
        let t = &parsed.tables[0];
        assert_eq!(
            t.value(0, t.column_index("nosso_numero").unwrap())
                .unwrap()
                .as_text(),
            Some("000098765001")
        );
        assert_eq!(
            t.value(0, t.column_index("sacado").unwrap()),
            Some(&Value::Null)
        );

        let mut d7 = common_detail("7");
        d7.extend([(64, "12345670000000001"), (176, "110726"), (182, "0000350")]);
        let d1 = common_detail("1");
        let text = [header("001"), rec(&d7), rec(&d1), rec(&[(1, "9")])].join("\n");
        let parsed = parse_cnab400(text.as_bytes(), "bb", "src").unwrap();
        let t = &parsed.tables[0];
        assert_eq!(t.len(), 2);
        let col = |n: &str| t.column_index(n).unwrap();
        assert_eq!(
            t.value(0, col("nosso_numero")).unwrap().as_text(),
            Some("12345670000000001")
        );
        assert_eq!(
            t.value(0, col("tarifa")),
            Some(&Value::Money(Money::from_centavos(350)))
        );
        assert_eq!(
            t.value(0, col("data_do_credito")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 11).unwrap()))
        );
        // The type-1 record falls back to the common fields, with a warning.
        assert_eq!(t.value(1, col("nosso_numero")), Some(&Value::Null));
        assert_eq!(
            t.value(1, col("valor_pago")),
            Some(&Value::Money(Money::from_centavos(2_431_000)))
        );
        assert!(
            parsed
                .warnings
                .iter()
                .any(|w| w.contains("convênio de 6 posições")),
            "{:?}",
            parsed.warnings
        );
    }

    #[test]
    fn partial_and_unknown_banks_use_the_common_layout_with_a_warning() {
        for bank in ["104", "756", "999"] {
            let mut d = common_detail("1");
            d.push((63, "98765001"));
            let text = [header(bank), rec(&d), rec(&[(1, "9")])].join("\n");
            let parsed = parse_cnab400(text.as_bytes(), "x", "src").unwrap();
            assert_eq!(parsed.warnings.len(), 1, "{bank}: {:?}", parsed.warnings);
            assert!(
                parsed.warnings[0].contains("campos comuns"),
                "{:?}",
                parsed.warnings
            );
            let t = &parsed.tables[0];
            assert_eq!(
                t.value(0, t.column_index("nosso_numero").unwrap()),
                Some(&Value::Null)
            );
            assert_eq!(
                t.value(0, t.column_index("valor_do_titulo").unwrap()),
                Some(&Value::Money(Money::from_centavos(2_430_000)))
            );
        }
    }

    #[test]
    fn remittance_and_garbage_are_refused() {
        let remessa = [
            rec(&[(1, "01REMESSA01COBRANCA"), (77, "341")]),
            rec(&common_detail("1")),
        ]
        .join("\n");
        assert!(matches!(
            parse_cnab400(remessa.as_bytes(), "x", "s"),
            Err(IngestError::Unrecognized(m)) if m.contains("remessa")
        ));
        assert_eq!(
            parse_cnab400(b"", "x", "s").unwrap_err(),
            IngestError::Empty
        );
        assert!(matches!(
            parse_cnab400(b"abc", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        let only_header = header("341");
        assert!(matches!(
            parse_cnab400(only_header.as_bytes(), "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
    }
}
