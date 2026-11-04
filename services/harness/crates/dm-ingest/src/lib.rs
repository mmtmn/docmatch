//! Ingestão: detecta o formato e a codificação de qualquer arquivo e o transforma em
//! [`Table`]s tipadas e [`Item`]s de evidência.
//!
//! Formatos: CSV (qualquer separador e codificação), XLSX/XLS/ODS, dumps de texto de caixas
//! de e-mail e conversas (WhatsApp), `.eml` e `mbox`, OFX e NF-e XML (mínimos); CNAB 240/400 e
//! texto de PDF têm só a assinatura (fase 1). Todo parser é total: entrada arbitrária nunca
//! causa pânico, só erro ou tabela vazia (ver `tests/never_panics.rs`).
#![forbid(unsafe_code)]

pub mod cnab;
pub mod delimited;
pub mod inbox;
pub mod mail;
pub mod mentions;
pub mod nfe_xml;
pub mod ofx;
pub mod pdf;
pub mod roles;
pub mod sniff;
pub mod source;
pub mod spreadsheet;

use thiserror::Error;

pub use delimited::parse_csv;
pub use inbox::{parse_txt_inbox, InboxContext};
pub use mail::{parse_eml, parse_mbox};
pub use mentions::{extract_mentions, Mention, MentionKind};
pub use nfe_xml::parse_nfe_xml;
pub use ofx::parse_ofx;
pub use roles::{infer_columns, ColumnGuess};
pub use sniff::{sniff, DateStyle, HeaderLanguage, Sniff, SourceKind};
pub use source::{parse_bytes, parse_file, source_id_for, table_items, ParsedSource};
pub use spreadsheet::parse_xlsx;

/// Errors of the ingest stage. Every parser returns one of these instead of panicking.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum IngestError {
    /// The bytes are not the format the parser expected.
    #[error("formato não reconhecido: {0}")]
    Unrecognized(String),
    /// The file could not be decoded as text.
    #[error("não foi possível decodificar o texto ({0})")]
    Decode(String),
    /// A library failed to parse the file.
    #[error("falha ao ler {format}: {detail}")]
    Parse {
        /// Format name.
        format: &'static str,
        /// Library message.
        detail: String,
    },
    /// Parser not implemented yet (CNAB, PDF).
    #[error("parser de {0} ainda não implementado (fase 1 do plano)")]
    NotImplemented(&'static str),
    /// The file is empty.
    #[error("arquivo vazio")]
    Empty,
    /// I/O error reading a path.
    #[error("erro de leitura: {0}")]
    Io(String),
}
