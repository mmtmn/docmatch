//! Validadores determinísticos de documentos brasileiros.
//!
//! Dígitos verificadores de CNPJ (numérico e alfanumérico), CPF, chave de acesso da NF-e,
//! linha digitável de boletos (bancários, 47 dígitos; arrecadação, 48 dígitos), PIS e CEP,
//! além do calendário de dias úteis bancários. Nenhuma função entra em pânico com entrada
//! arbitrária: tudo devolve [`Verdict::Malformed`] ou um erro tipado.
#![forbid(unsafe_code)]

pub mod boleto;
pub mod business_days;
pub mod cep;
pub mod cnpj;
pub mod cpf;
pub mod digits;
pub mod nfe;
pub mod pis;

use serde::{Deserialize, Serialize};

pub use boleto::{
    compose_arrecadacao_line, compose_bank_line, expected_factor, factor_to_dates, parse_boleto,
    Boleto, BoletoKind, FactorCycle, FieldCheck,
};
pub use business_days::{
    add_business_days, business_days_between, easter, holidays, is_business_day, is_holiday,
    next_business_day, previous_business_day, roll_forward,
};
pub use cep::validate_cep;
pub use cnpj::{cnpj_dv, CnpjFormat};
pub use cpf::cpf_dv;
pub use nfe::{nfe_key_dv, NfeKey};
pub use pis::validate_pis;

/// Outcome of a check-digit validation.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Verdict {
    /// Well-formed and the check digits agree.
    Valid,
    /// Well-formed but the check digits (or a structural rule) disagree.
    Invalid,
    /// Wrong length or characters; nothing to check.
    Malformed,
}

impl Verdict {
    /// True only for [`Verdict::Valid`].
    pub fn is_valid(self) -> bool {
        matches!(self, Verdict::Valid)
    }
}

/// Result of validating an identifier: the verdict plus the normalized and formatted forms.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CheckResult {
    /// Verdict.
    pub verdict: Verdict,
    /// Normalized identifier (mask removed, uppercase), empty when malformed.
    pub digits: String,
    /// Canonical mask (`12.345.678/0001-95`), present when well-formed.
    pub formatted: Option<String>,
    /// Check digits the base would need to be valid, when well-formed.
    pub expected_dv: Option<String>,
}

impl CheckResult {
    fn malformed() -> Self {
        CheckResult {
            verdict: Verdict::Malformed,
            digits: String::new(),
            formatted: None,
            expected_dv: None,
        }
    }
}

/// Removes mask characters (`.`, `-`, `/`, spaces) and returns the remaining characters.
pub(crate) fn strip_mask(input: &str) -> String {
    input
        .chars()
        .filter(|c| !matches!(c, '.' | '-' | '/' | ' ' | '\t' | '\u{a0}'))
        .collect()
}
