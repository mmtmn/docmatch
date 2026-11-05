//! Ingestão: detecta o formato e a codificação de qualquer arquivo e o transforma em
//! [`Table`]s tipadas e [`Item`]s de evidência.
//!
//! Formatos: CSV (qualquer separador e codificação), XLSX/XLS/ODS, dumps de texto de caixas
//! de e-mail e conversas (WhatsApp), `.eml`, `mbox` e `.msg` do Outlook (anexos viram fontes),
//! OFX e NF-e XML (mínimos), PDF com camada de texto (extratos viram tabela, o resto vira
//! itens com página), CNAB 240 (cobrança e pagamentos) e CNAB 400 (retorno de cobrança,
//! layouts por banco), SPED (EFD ICMS/IPI, EFD Contribuições, ECD), documentos DOCX e ODT e
//! páginas HTML (tabelas viram tabelas, o texto vira itens), JSON, JSON Lines e outros XML
//! (listas de registros viram tabelas) e ZIP e 7z (cada arquivo de dentro é uma fonte). Todo
//! parser é total: entrada arbitrária nunca causa pânico, só erro ou tabela vazia (ver
//! `tests/never_panics.rs`).
#![forbid(unsafe_code)]

pub mod archive;
pub mod cnab;
pub mod delimited;
pub(crate) mod grid;
pub mod html;
pub mod inbox;
pub mod mail;
pub mod mentions;
pub mod msg;
pub mod nfe_xml;
pub mod office;
pub mod ofx;
pub mod pdf;
pub mod roles;
pub mod sevenz;
pub mod sniff;
pub mod source;
pub mod sped;
pub mod spreadsheet;
pub mod structured;

use thiserror::Error;

pub use cnab::{parse_cnab240, parse_cnab400, CnabParsed};
pub use delimited::parse_csv;
pub use html::{parse_html, HtmlParsed};
pub use inbox::{parse_txt_inbox, InboxContext};
pub use mail::{parse_eml, parse_mbox};
pub use mentions::{extract_mentions, Mention, MentionKind};
pub use msg::{parse_msg, MsgParsed};
pub use nfe_xml::parse_nfe_xml;
pub use office::{parse_docx, parse_odt, DocParsed};
pub use ofx::parse_ofx;
pub use pdf::{parse_pdf, PdfParsed};
pub use roles::{infer_columns, ColumnGuess};
pub use sevenz::read_7z;
pub use sniff::{sniff, DateStyle, HeaderLanguage, Sniff, SourceKind};
pub use source::{parse_bytes, parse_file, source_id_for, table_items, ParsedSource};
pub use sped::parse_sped;
pub use spreadsheet::parse_xlsx;
pub use structured::{parse_json, parse_jsonl, parse_xml, StructuredParsed};

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
    /// Parser not implemented yet.
    #[error("parser de {0} ainda não implementado")]
    NotImplemented(&'static str),
    /// The file is empty.
    #[error("arquivo vazio")]
    Empty,
    /// I/O error reading a path.
    #[error("erro de leitura: {0}")]
    Io(String),
}
