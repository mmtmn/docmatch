//! CNAB 240 / 400 remittance and return files: layout detection is implemented; the record
//! parsers are signatures only (phase 1 of the plan wires the FEBRABAN segment layouts).

use dm_core::Table;
use serde::{Deserialize, Serialize};

use crate::IngestError;

/// CNAB layout, by record length.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CnabLayout {
    /// 240-character records (FEBRABAN multi-bank).
    Cnab240,
    /// 400-character records (legacy per-bank layouts).
    Cnab400,
}

/// Detects the layout from the record lengths (every non-empty line must have the same
/// length, 240 or 400, ignoring line endings).
pub fn detect_cnab_layout(bytes: &[u8]) -> Option<CnabLayout> {
    let text = String::from_utf8_lossy(bytes);
    let mut lens = text
        .lines()
        .map(|l| l.trim_end_matches('\r'))
        .filter(|l| !l.trim().is_empty())
        .map(|l| l.chars().count());
    let first = lens.next()?;
    if !lens.all(|l| l == first) {
        return None;
    }
    match first {
        240 => Some(CnabLayout::Cnab240),
        400 => Some(CnabLayout::Cnab400),
        _ => None,
    }
}

/// Parses a CNAB 240 file into one row per detail segment.
///
/// TODO(fase 1): segmentos T/U (retorno de cobrança) e A/J (pagamentos) conforme o layout
/// FEBRABAN 240 v10; por ora devolve [`IngestError::NotImplemented`] após validar o layout.
pub fn parse_cnab240(bytes: &[u8], _name: &str, _source_id: &str) -> Result<Table, IngestError> {
    match detect_cnab_layout(bytes) {
        Some(CnabLayout::Cnab240) => Err(IngestError::NotImplemented("CNAB 240")),
        _ => Err(IngestError::Unrecognized(
            "registros não têm 240 posições".to_string(),
        )),
    }
}

/// Parses a CNAB 400 file into one row per detail record.
///
/// TODO(fase 1): registros tipo 1 (detalhe) dos layouts Itaú/Bradesco/BB; por ora devolve
/// [`IngestError::NotImplemented`] após validar o layout.
pub fn parse_cnab400(bytes: &[u8], _name: &str, _source_id: &str) -> Result<Table, IngestError> {
    match detect_cnab_layout(bytes) {
        Some(CnabLayout::Cnab400) => Err(IngestError::NotImplemented("CNAB 400")),
        _ => Err(IngestError::Unrecognized(
            "registros não têm 400 posições".to_string(),
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_layouts_and_reports_not_implemented() {
        let c240 = format!("{}\r\n{}\r\n", "0".repeat(240), "1".repeat(240));
        assert_eq!(
            detect_cnab_layout(c240.as_bytes()),
            Some(CnabLayout::Cnab240)
        );
        assert_eq!(
            parse_cnab240(c240.as_bytes(), "x", "s"),
            Err(IngestError::NotImplemented("CNAB 240"))
        );
        let c400 = format!("{}\n", "0".repeat(400));
        assert_eq!(
            detect_cnab_layout(c400.as_bytes()),
            Some(CnabLayout::Cnab400)
        );
        assert_eq!(
            parse_cnab400(c400.as_bytes(), "x", "s"),
            Err(IngestError::NotImplemented("CNAB 400"))
        );
        assert_eq!(detect_cnab_layout(b"abc\n"), None);
        assert_eq!(detect_cnab_layout(b""), None);
        assert!(matches!(
            parse_cnab240(b"abc", "x", "s"),
            Err(IngestError::Unrecognized(_))
        ));
    }
}
