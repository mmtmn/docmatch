//! Error type shared by the core types.

use thiserror::Error;

/// Errors raised by the core types (parsing money, building tables, invalid references).
#[derive(Debug, Clone, PartialEq, Error)]
pub enum CoreError {
    /// A monetary string could not be parsed.
    #[error("valor monetário inválido: {0:?}")]
    InvalidMoney(String),
    /// A decimal had more precision than centavos allow.
    #[error("valor com mais de duas casas decimais: {0}")]
    SubCentavoPrecision(String),
    /// Arithmetic overflowed the decimal range.
    #[error("estouro aritmético em valor monetário")]
    Overflow,
    /// A row has a different number of values than the table has columns.
    #[error("linha {row} tem {got} valores, a tabela tem {expected} colunas")]
    RowArity {
        /// Zero-based row index.
        row: usize,
        /// Number of values received.
        got: usize,
        /// Number of columns in the table.
        expected: usize,
    },
    /// A confidence outside `0..=1` was given.
    #[error("confiança fora de 0..1: {0}")]
    ConfidenceRange(f64),
}
