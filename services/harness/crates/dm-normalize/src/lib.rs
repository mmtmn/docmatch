//! Normalização pt-BR dos valores que chegam sujos de planilhas, extratos e mensagens.
//!
//! Dinheiro (`17.380,00`, `R$ 1.234,56`, `(1.234,56)`), datas (`dd/mm/aaaa`, ISO, `dd/mm hh:mm`
//! sem ano, meses por extenso), CNPJ/CPF com ou sem máscara, nomes de empresas (acentos,
//! abreviações, formas jurídicas, descritores truncados de extrato), números de documento
//! (zeros à esquerda), percentuais/taxas e status de pagamento. Tudo determinístico.
#![forbid(unsafe_code)]

pub mod date;
pub mod doc_number;
pub mod money;
pub mod name;
pub mod percent;
pub mod status;
pub mod tax_id;
pub mod text;

use thiserror::Error;

pub use date::{date_ptbr, DateContext, DateFormat, ParsedDate};
pub use doc_number::{doc_number, same_doc_number, DocNumber};
pub use money::{money_ptbr, money_with_style, sniff_decimal_style, DecimalStyle};
pub use name::{expand_abbreviation, name_key, prefix_match, NameKey};
pub use percent::{percent_ptbr, rate_ptbr, RateParse, RatePeriod};
pub use status::{status_ptbr, PaymentStatus};
pub use tax_id::{tax_id, TaxId, TaxIdKind};
pub use text::{fold_ascii, squash_spaces, strip_accents};

/// Errors of the normalizers.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum NormalizeError {
    /// Not a monetary value.
    #[error("valor monetário não reconhecido: {0:?}")]
    Money(String),
    /// Not a date in any accepted format.
    #[error("data não reconhecida: {0:?}")]
    Date(String),
    /// Date without year and no reference year to complete it.
    #[error("data sem ano e sem ano de referência: {0:?}")]
    DateNeedsYear(String),
    /// Not a percentage or rate.
    #[error("percentual não reconhecido: {0:?}")]
    Percent(String),
}
