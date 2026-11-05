//! SPED digital bookkeeping: EFD ICMS/IPI, EFD Contribuições and ECD.
//!
//! Every record is `|REG|campo|campo|...|`; the file opens with `|0000|` and closes with
//! `|9999|` (a signed file carries the PVA signature after it, which is not read). Each
//! register type becomes one table (`<arquivo>:<REG>`), with the official field names for
//! the registers that matter to a reconciliation and positional names (`CAMPO_02`, ...) for
//! the rest. Field names follow:
//!
//! * EFD ICMS/IPI — Guia Prático da EFD ICMS/IPI (versão 3.1.x): 0000, 0150, C100, C170,
//!   C190, E110;
//! * EFD Contribuições — Guia Prático da EFD-Contribuições (versão 1.35): 0000, 0150, C100,
//!   C170 (C190 means something else there and stays positional);
//! * ECD — Manual de Orientação do Leiaute da ECD (leiaute 9): the first ten fields of 0000,
//!   I200, I250.
//!
//! Values: `VL_*` are amounts with a decimal comma, `DT_*` are `DDMMAAAA`, `ALIQ_*`, `QTD`
//! and `QUANT_*` are decimals; everything else is kept verbatim (codes keep their zeros). Child
//! records point to their parent's line (`LINHA_PAI`), and the `9900` record counts are
//! checked against the file.

use std::collections::BTreeMap;
use std::str::FromStr;

use chrono::NaiveDate;
use dm_core::{Column, ColumnRole, Decimal, Money, Table, Value};
use indexmap::IndexMap;

use crate::IngestError;

/// Which SPED book a file is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SpedFlavor {
    /// EFD ICMS/IPI (Sped Fiscal).
    EfdIcmsIpi,
    /// EFD Contribuições (PIS/COFINS).
    EfdContribuicoes,
    /// ECD (Sped Contábil).
    Ecd,
    /// Opens with `|0000|` but matches none of the above.
    Unknown,
}

impl SpedFlavor {
    /// pt-BR name.
    pub fn label(self) -> &'static str {
        match self {
            SpedFlavor::EfdIcmsIpi => "EFD ICMS/IPI",
            SpedFlavor::EfdContribuicoes => "EFD Contribuições",
            SpedFlavor::Ecd => "ECD",
            SpedFlavor::Unknown => "SPED não identificado",
        }
    }
}

const EFD_0150: &[&str] = &[
    "REG", "COD_PART", "NOME", "COD_PAIS", "CNPJ", "CPF", "IE", "COD_MUN", "SUFRAMA", "END", "NUM",
    "COMPL", "BAIRRO",
];

const EFD_C100: &[&str] = &[
    "REG",
    "IND_OPER",
    "IND_EMIT",
    "COD_PART",
    "COD_MOD",
    "COD_SIT",
    "SER",
    "NUM_DOC",
    "CHV_NFE",
    "DT_DOC",
    "DT_E_S",
    "VL_DOC",
    "IND_PGTO",
    "VL_DESC",
    "VL_ABAT_NT",
    "VL_MERC",
    "IND_FRT",
    "VL_FRT",
    "VL_SEG",
    "VL_OUT_DA",
    "VL_BC_ICMS",
    "VL_ICMS",
    "VL_BC_ICMS_ST",
    "VL_ICMS_ST",
    "VL_IPI",
    "VL_PIS",
    "VL_COFINS",
    "VL_PIS_ST",
    "VL_COFINS_ST",
];

const EFD_C170: &[&str] = &[
    "REG",
    "NUM_ITEM",
    "COD_ITEM",
    "DESCR_COMPL",
    "QTD",
    "UNID",
    "VL_ITEM",
    "VL_DESC",
    "IND_MOV",
    "CST_ICMS",
    "CFOP",
    "COD_NAT",
    "VL_BC_ICMS",
    "ALIQ_ICMS",
    "VL_ICMS",
    "VL_BC_ICMS_ST",
    "ALIQ_ST",
    "VL_ICMS_ST",
    "IND_APUR",
    "CST_IPI",
    "COD_ENQ",
    "VL_BC_IPI",
    "ALIQ_IPI",
    "VL_IPI",
    "CST_PIS",
    "VL_BC_PIS",
    "ALIQ_PIS",
    "QUANT_BC_PIS",
    "ALIQ_PIS_QUANT",
    "VL_PIS",
    "CST_COFINS",
    "VL_BC_COFINS",
    "ALIQ_COFINS",
    "QUANT_BC_COFINS",
    "ALIQ_COFINS_QUANT",
    "VL_COFINS",
    "COD_CTA",
];

fn field_names(flavor: SpedFlavor, reg: &str) -> &'static [&'static str] {
    match (flavor, reg) {
        (SpedFlavor::EfdIcmsIpi, "0000") => &[
            "REG",
            "COD_VER",
            "COD_FIN",
            "DT_INI",
            "DT_FIN",
            "NOME",
            "CNPJ",
            "CPF",
            "UF",
            "IE",
            "COD_MUN",
            "IM",
            "SUFRAMA",
            "IND_PERFIL",
            "IND_ATIV",
        ],
        (SpedFlavor::EfdContribuicoes, "0000") => &[
            "REG",
            "COD_VER",
            "TIPO_ESCRIT",
            "IND_SIT_ESP",
            "NUM_REC_ANTERIOR",
            "DT_INI",
            "DT_FIN",
            "NOME",
            "CNPJ",
            "UF",
            "COD_MUN",
            "SUFRAMA",
            "IND_NAT_PJ",
            "IND_ATIV",
        ],
        (SpedFlavor::Ecd, "0000") => &[
            "REG", "LECD", "DT_INI", "DT_FIN", "NOME", "CNPJ", "UF", "IE", "COD_MUN", "IM",
        ],
        (SpedFlavor::EfdIcmsIpi | SpedFlavor::EfdContribuicoes, "0150") => EFD_0150,
        (SpedFlavor::EfdIcmsIpi | SpedFlavor::EfdContribuicoes, "C100") => EFD_C100,
        (SpedFlavor::EfdIcmsIpi | SpedFlavor::EfdContribuicoes, "C170") => EFD_C170,
        (SpedFlavor::EfdIcmsIpi, "C190") => &[
            "REG",
            "CST_ICMS",
            "CFOP",
            "ALIQ_ICMS",
            "VL_OPR",
            "VL_BC_ICMS",
            "VL_ICMS",
            "VL_BC_ICMS_ST",
            "VL_ICMS_ST",
            "VL_RED_BC",
            "VL_IPI",
            "COD_OBS",
        ],
        (SpedFlavor::EfdIcmsIpi, "E110") => &[
            "REG",
            "VL_TOT_DEBITOS",
            "VL_AJ_DEBITOS",
            "VL_TOT_AJ_DEBITOS",
            "VL_ESTORNOS_CRED",
            "VL_TOT_CREDITOS",
            "VL_AJ_CREDITOS",
            "VL_TOT_AJ_CREDITOS",
            "VL_ESTORNOS_DEB",
            "VL_SLD_CREDOR_ANT",
            "VL_SLD_APURADO",
            "VL_TOT_DED",
            "VL_ICMS_RECOLHER",
            "VL_SLD_CREDOR_TRANSPORTAR",
            "DEB_ESP",
        ],
        (SpedFlavor::Ecd, "I200") => &[
            "REG",
            "NUM_LCTO",
            "DT_LCTO",
            "VL_LCTO",
            "IND_LCTO",
            "DT_LCTO_EXT",
        ],
        (SpedFlavor::Ecd, "I250") => &[
            "REG",
            "COD_CTA",
            "COD_CCUS",
            "VL_DC",
            "IND_DC",
            "NUM_ARQ",
            "COD_HIST_PAD",
            "HIST",
            "COD_PART",
        ],
        _ => &[],
    }
}

/// Parent register of the child records whose lines are linked (`LINHA_PAI`). In EFD
/// Contribuições C190 is a consolidation under C010, not a child of C100.
fn parent_of(book: SpedFlavor, reg: &str) -> Option<&'static str> {
    match (book, reg) {
        (SpedFlavor::EfdIcmsIpi | SpedFlavor::EfdContribuicoes, "C170")
        | (SpedFlavor::EfdIcmsIpi, "C190") => Some("C100"),
        (SpedFlavor::EfdContribuicoes, "C190") => Some("C010"),
        (SpedFlavor::EfdIcmsIpi, "E110") => Some("E100"),
        (SpedFlavor::Ecd, "I250") => Some("I200"),
        _ => None,
    }
}

/// Role, sub-role and typing of a named field.
fn role_of(field: &str) -> (ColumnRole, Option<&'static str>) {
    match field {
        "NOME" => (ColumnRole::Name, None),
        "CNPJ" | "CPF" => (ColumnRole::TaxId, None),
        "NUM_DOC" => (ColumnRole::DocumentId, Some("nfe")),
        "CHV_NFE" => (ColumnRole::DocumentId, Some("key")),
        "NUM_LCTO" => (ColumnRole::DocumentId, Some("entry")),
        "VL_DOC" | "VL_LCTO" => (ColumnRole::Amount, Some("total")),
        "DT_DOC" => (ColumnRole::Date, Some("issue")),
        "DT_E_S" | "DT_LCTO" => (ColumnRole::Date, Some("posting")),
        "DT_INI" => (ColumnRole::Date, Some("period_start")),
        "DT_FIN" => (ColumnRole::Date, Some("period_end")),
        "HIST" | "DESCR_COMPL" => (ColumnRole::FreeText, None),
        "IND_OPER" | "IND_EMIT" | "COD_MOD" | "COD_SIT" | "IND_PGTO" | "CFOP" | "IND_DC"
        | "IND_LCTO" | "IND_MOV" => (ColumnRole::Kind, None),
        f if f.starts_with("CST_") => (ColumnRole::Kind, None),
        f if f.starts_with("VL_") || f == "DEB_ESP" => (ColumnRole::Amount, None),
        f if f.starts_with("DT_") => (ColumnRole::Date, None),
        _ => (ColumnRole::Unknown, None),
    }
}

/// True for a record line: `|` + four characters of a register code + `|`.
pub fn is_record_line(line: &str) -> bool {
    let b = line.as_bytes();
    b.len() >= 6
        && b[0] == b'|'
        && b[5] == b'|'
        && b[1..5]
            .iter()
            .all(|c| c.is_ascii_digit() || c.is_ascii_uppercase())
}

/// The record part of the file: everything up to the end of the `|9999|` line (a signed
/// file continues with the binary signature).
pub(crate) fn record_bytes(bytes: &[u8]) -> &[u8] {
    let mut start = 0;
    while start < bytes.len() {
        let end = bytes[start..]
            .iter()
            .position(|b| *b == b'\n')
            .map_or(bytes.len(), |p| start + p + 1);
        if bytes[start..end].starts_with(b"|9999|") {
            return &bytes[..end];
        }
        start = end;
    }
    bytes
}

/// A SPED file opens with `|0000|` (after an optional BOM and blank lines) and its first
/// lines are all records.
pub fn looks_like_sped(bytes: &[u8]) -> bool {
    const SAMPLE: usize = 16 * 1024;
    let head = &bytes[..bytes.len().min(SAMPLE)];
    let head = head.strip_prefix(b"\xEF\xBB\xBF").unwrap_or(head);
    let text = String::from_utf8_lossy(head);
    let mut lines: Vec<&str> = text.split('\n').collect();
    if bytes.len() > SAMPLE {
        // The sample window may cut the last line.
        lines.pop();
    }
    let mut records = lines
        .into_iter()
        .map(|l| l.trim_end_matches('\r').trim())
        .filter(|l| !l.is_empty());
    match records.next() {
        Some(first) if first.starts_with("|0000|") && is_record_line(first) => {}
        _ => return false,
    }
    for line in records.take(20) {
        if line.starts_with("|9999|") {
            // A signed file continues with the binary signature.
            return true;
        }
        if !is_record_line(line) {
            return false;
        }
    }
    true
}

fn is_date8(s: &str) -> bool {
    s.len() == 8 && s.chars().all(|c| c.is_ascii_digit()) && date8(s).is_some()
}

fn date8(s: &str) -> Option<NaiveDate> {
    if s.len() != 8 || !s.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    NaiveDate::from_ymd_opt(
        s[4..8].parse().ok()?,
        s[2..4].parse().ok()?,
        s[0..2].parse().ok()?,
    )
}

/// Identifies the book from the `0000` record.
pub fn flavor(fields: &[&str]) -> SpedFlavor {
    let at = |i: usize| fields.get(i).copied().unwrap_or_default();
    if at(1) == "LECD" {
        SpedFlavor::Ecd
    } else if is_date8(at(3)) && is_date8(at(4)) {
        SpedFlavor::EfdIcmsIpi
    } else if is_date8(at(5)) && is_date8(at(6)) {
        SpedFlavor::EfdContribuicoes
    } else {
        SpedFlavor::Unknown
    }
}

fn decimal_comma(s: &str) -> Option<Decimal> {
    let t = s.trim();
    if t.is_empty() || t.contains('.') {
        return None;
    }
    Decimal::from_str(&t.replace(',', ".")).ok()
}

fn typed(field: &str, raw: &str, bad: &mut usize) -> Value {
    let s = raw.trim();
    if s.is_empty() {
        return Value::Null;
    }
    let numeric = field.starts_with("ALIQ_") || field == "QTD" || field.starts_with("QUANT_");
    if field.starts_with("VL_") || field == "DEB_ESP" {
        if let Some(m) = decimal_comma(s).and_then(|d| Money::try_from_decimal_exact(d).ok()) {
            return Value::Money(m);
        }
        *bad += 1;
    } else if field.starts_with("DT_") {
        if let Some(d) = date8(s) {
            return Value::Date(d);
        }
        *bad += 1;
    } else if numeric {
        if let Some(d) = decimal_comma(s) {
            return Value::Number(d);
        }
        *bad += 1;
    }
    Value::Text(s.to_string())
}

struct RegisterTable {
    names: Vec<String>,
    rows: Vec<(usize, Option<usize>, Vec<String>)>,
}

/// Parses a SPED file into one table per register type, with pt-BR warnings.
pub fn parse_sped(
    bytes: &[u8],
    name: &str,
    source_id: &str,
) -> Result<(Vec<Table>, Vec<String>), IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    let records = record_bytes(bytes);
    let text = match std::str::from_utf8(records) {
        Ok(s) => s.to_string(),
        // SPED files are ISO-8859-1 by specification; windows-1252 is a superset of it.
        Err(_) => encoding_rs::WINDOWS_1252.decode(records).0.into_owned(),
    };
    let text = text.trim_start_matches('\u{feff}');
    let mut book = SpedFlavor::Unknown;
    let mut registers: IndexMap<String, RegisterTable> = IndexMap::new();
    let mut last_line_of: BTreeMap<String, usize> = BTreeMap::new();
    let mut declared: BTreeMap<String, usize> = BTreeMap::new();
    let mut counted: BTreeMap<String, usize> = BTreeMap::new();
    let mut not_records = 0usize;
    let mut closed = false;
    let mut first = true;
    for (i, raw) in text.split('\n').enumerate() {
        let line = raw.trim_end_matches('\r').trim();
        if line.is_empty() {
            continue;
        }
        if !is_record_line(line) {
            not_records += 1;
            continue;
        }
        let inner = line.strip_prefix('|').unwrap_or(line);
        let inner = inner.strip_suffix('|').unwrap_or(inner);
        let fields: Vec<&str> = inner.split('|').collect();
        let reg = fields[0].to_string();
        let line_no = i + 1;
        if first {
            if reg != "0000" {
                return Err(IngestError::Unrecognized(
                    "SPED sem registro de abertura 0000".to_string(),
                ));
            }
            book = flavor(&fields);
            first = false;
        }
        *counted.entry(reg.clone()).or_default() += 1;
        if reg == "9900" {
            if let (Some(r), Some(n)) = (fields.get(1), fields.get(2).and_then(|n| n.parse().ok()))
            {
                declared.insert((*r).to_string(), n);
            }
        }
        if reg == "9999" {
            closed = true;
        }
        let parent = parent_of(book, &reg).and_then(|p| last_line_of.get(p).copied());
        last_line_of.insert(reg.clone(), line_no);
        // Block openings and closings (X001, X990) and block 9 are control records; their
        // counts are checked below and they carry nothing to reconcile.
        if reg.ends_with("001") || reg.ends_with("990") || reg.starts_with('9') {
            continue;
        }
        let entry = registers.entry(reg.clone()).or_insert_with(|| {
            let official = field_names(book, &reg);
            RegisterTable {
                names: official.iter().map(ToString::to_string).collect(),
                rows: Vec::new(),
            }
        });
        if entry.names.len() < fields.len() {
            for k in entry.names.len()..fields.len() {
                entry.names.push(format!("CAMPO_{:02}", k + 1));
            }
        }
        entry.rows.push((
            line_no,
            parent,
            fields.iter().map(ToString::to_string).collect(),
        ));
    }
    if first {
        return Err(IngestError::Unrecognized("SPED sem registros".to_string()));
    }
    let mut warnings = Vec::new();
    if book == SpedFlavor::Unknown {
        warnings.push(
            "registro 0000 não identifica EFD ICMS/IPI, EFD Contribuições nem ECD: campos com nomes posicionais".to_string(),
        );
    }
    if !closed {
        warnings
            .push("sem registro 9999 de encerramento: o arquivo pode estar incompleto".to_string());
    }
    if not_records > 0 {
        warnings.push(format!(
            "{not_records} linha(s) fora do formato |REG|...| ignorada(s)"
        ));
    }
    for (reg, n) in &declared {
        let got = counted.get(reg).copied().unwrap_or(0);
        if got != *n {
            warnings.push(format!(
                "registro {reg}: o bloco 9 declara {n}, o arquivo tem {got}"
            ));
        }
    }
    let mut bad_values = 0usize;
    let mut tables = Vec::new();
    for (reg, data) in registers {
        let mut columns = vec![Column::new("LINHA", ColumnRole::Unknown).with_subrole("line")];
        let has_parent = parent_of(book, &reg).is_some();
        if has_parent {
            columns.push(Column::new("LINHA_PAI", ColumnRole::Unknown).with_subrole("parent_line"));
        }
        // REG is constant within a table and becomes the table name.
        for field in data.names.iter().skip(1) {
            let (role, sub) = if field.starts_with("CAMPO_") {
                (ColumnRole::Unknown, None)
            } else {
                role_of(field)
            };
            let mut col = Column::new(field.clone(), role);
            if let Some(s) = sub {
                col = col.with_subrole(s);
            }
            columns.push(col);
        }
        let width = columns.len();
        let mut table = Table::new(format!("{name}:{reg}"), source_id, columns);
        for (line_no, parent, fields) in data.rows {
            let mut values = Vec::with_capacity(width);
            values.push(Value::Integer(i64::try_from(line_no).unwrap_or(i64::MAX)));
            if has_parent {
                values.push(parent.map_or(Value::Null, |p| {
                    Value::Integer(i64::try_from(p).unwrap_or(i64::MAX))
                }));
            }
            for (k, field) in data.names.iter().enumerate().skip(1) {
                let raw = fields.get(k).map_or("", String::as_str);
                values.push(if field.starts_with("CAMPO_") {
                    Value::from(raw.trim())
                } else {
                    typed(field, raw, &mut bad_values)
                });
            }
            values.resize(width, Value::Null);
            table.push_row(values).map_err(|e| IngestError::Parse {
                format: "sped",
                detail: e.to_string(),
            })?;
        }
        tables.push(table);
    }
    if bad_values > 0 {
        warnings.push(format!(
            "{bad_values} valor(es) ou data(s) fora do formato SPED mantido(s) como texto"
        ));
    }
    if tables.is_empty() {
        warnings.push(format!("{}: só registros de controle", book.label()));
    }
    Ok((tables, warnings))
}

#[cfg(test)]
mod tests {
    use super::*;

    const EFD: &str = "|0000|017|0|01072026|31072026|EMPRESA SINTETICA LTDA|11222333000181||SP|110042490114||3550308|||A|1|\r\n\
|0001|0|\r\n\
|0150|P001|PADARIA CENTRAL LTDA|01058|45723174000110||110042490118|3550308||RUA UM|10||CENTRO|\r\n\
|0990|4|\r\n\
|C001|0|\r\n\
|C100|1|0|P001|55|00|1|088231|35260711222333000181550010000882311000882310|05072026|05072026|17380,00|1|0,00|0,00|17380,00|9|0,00|0,00|0,00|17380,00|3128,40|0,00|0,00|0,00|286,77|1320,88|||\r\n\
|C170|1|ITEM01|FARINHA|100,00000|SC|17380,00|0,00|0|000|5102||17380,00|18,00|3128,40|0,00|0,00|0,00|0|||0,00|0,00|0,00|01|17380,00|1,65|||286,77|01|17380,00|7,60|||1320,88|3.01.01|\r\n\
|C190|000|5102|18,00|17380,00|17380,00|3128,40|0,00|0,00|0,00|0,00||\r\n\
|C990|5|\r\n\
|E001|0|\r\n\
|E100|01072026|31072026|\r\n\
|E110|3128,40|0,00|0,00|0,00|0,00|0,00|0,00|0,00|0,00|3128,40|0,00|3128,40|0,00|0,00|\r\n\
|E990|4|\r\n\
|9001|0|\r\n\
|9900|0000|1|\r\n\
|9900|C100|1|\r\n\
|9900|C170|2|\r\n\
|9990|5|\r\n\
|9999|19|\r\n";

    #[test]
    fn efd_icms_ipi_registers_become_tables_with_official_names() {
        let (tables, warnings) = parse_sped(EFD.as_bytes(), "efd", "src").unwrap();
        let names: Vec<&str> = tables.iter().map(|t| t.name.as_str()).collect();
        assert_eq!(
            names,
            vec![
                "efd:0000", "efd:0150", "efd:C100", "efd:C170", "efd:C190", "efd:E100", "efd:E110"
            ]
        );
        let c100 = &tables[2];
        let col = |t: &Table, n: &str| t.column_index(n).unwrap_or_else(|| panic!("{n}"));
        assert_eq!(
            c100.value(0, col(c100, "num_doc")).unwrap().as_text(),
            Some("088231")
        );
        assert_eq!(
            c100.value(0, col(c100, "vl_doc")),
            Some(&Value::Money(Money::from_centavos(1_738_000)))
        );
        assert_eq!(
            c100.value(0, col(c100, "dt_doc")),
            Some(&Value::Date(NaiveDate::from_ymd_opt(2026, 7, 5).unwrap()))
        );
        assert_eq!(
            c100.columns[col(c100, "chv_nfe")].subrole.as_deref(),
            Some("key")
        );
        let c170 = &tables[3];
        assert_eq!(
            c170.value(0, col(c170, "linha_pai")),
            Some(&Value::Integer(6))
        );
        assert_eq!(
            c170.value(0, col(c170, "aliq_icms")),
            Some(&Value::Number(Decimal::new(18, 0)))
        );
        assert_eq!(
            c170.value(0, col(c170, "cod_cta")).unwrap().as_text(),
            Some("3.01.01")
        );
        let e110 = &tables[6];
        assert_eq!(
            e110.value(0, col(e110, "vl_icms_recolher")),
            Some(&Value::Money(Money::from_centavos(312_840)))
        );
        let e100 = &tables[5];
        assert!(
            e100.column_index("campo_02").is_some(),
            "positional names for unknown registers"
        );
        let p0150 = &tables[1];
        assert_eq!(p0150.columns[col(p0150, "nome")].role, ColumnRole::Name);
        // 9900 declares two C170 but the file has one.
        assert_eq!(
            warnings,
            vec!["registro C170: o bloco 9 declara 2, o arquivo tem 1".to_string()]
        );
    }

    #[test]
    fn flavors_and_signature() {
        assert_eq!(
            flavor(&["0000", "LECD", "01012025", "31122025"]),
            SpedFlavor::Ecd
        );
        assert_eq!(
            flavor(&["0000", "006", "0", "", "", "01072026", "31072026"]),
            SpedFlavor::EfdContribuicoes
        );
        assert_eq!(flavor(&["0000", "x"]), SpedFlavor::Unknown);
        let ecd = "|0000|LECD|01012025|31122025|EMPRESA SINTETICA LTDA|11222333000181|SP||3550308|||0|\n|I200|L1|15032025|1500,00|N||\n|I250|1.1.01|CC1|1500,00|D||H1|RECEBIMENTO NF 088231|P001|\n|I250|3.1.01||1500,00|C|||RECEITA||\n|9999|5|\n\u{0}\u{1}SIGNATURE";
        let (tables, warnings) = parse_sped(ecd.as_bytes(), "ecd", "src").unwrap();
        assert!(warnings.is_empty(), "{warnings:?}");
        let i250 = tables.iter().find(|t| t.name == "ecd:I250").unwrap();
        assert_eq!(i250.len(), 2);
        assert_eq!(
            i250.value(1, i250.column_index("ind_dc").unwrap())
                .unwrap()
                .as_text(),
            Some("C")
        );
        assert_eq!(
            i250.value(0, i250.column_index("linha_pai").unwrap()),
            Some(&Value::Integer(2))
        );
        assert!(looks_like_sped(ecd.as_bytes()));
        assert!(!looks_like_sped(b"|a|b|\n|1|2|\n"));
    }

    #[test]
    fn truncated_and_malformed_files_warn() {
        let (tables, warnings) = parse_sped(b"|0000|x|\n|C100|1|bad|\nlixo\n", "x", "src").unwrap();
        assert_eq!(tables.len(), 2);
        let w = warnings.join(" | ");
        assert!(w.contains("nomes posicionais"), "{w}");
        assert!(w.contains("9999"), "{w}");
        assert!(w.contains("1 linha(s) fora do formato"), "{w}");
        assert!(matches!(
            parse_sped(b"|C100|1|\n", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        assert!(matches!(
            parse_sped(b"lixo\n", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        assert_eq!(parse_sped(b"", "x", "s"), Err(IngestError::Empty));
    }
}
