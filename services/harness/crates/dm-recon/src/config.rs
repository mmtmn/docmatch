//! Engine configuration. Every keyword list, rate, tolerance and threshold the rules use
//! lives here with a default; `services/harness/config/recon.yaml` documents the same values
//! and a test keeps the two in sync. Text patterns are compared against folded text
//! (uppercase, no accents, single spaces), so they are written that way.

use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};

fn strings(items: &[&str]) -> Vec<String> {
    items.iter().map(|s| (*s).to_string()).collect()
}

/// Configuration of the deterministic reconciliation engine.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct EngineConfig {
    /// Words that open a bank descriptor before the payer (removed, longest first, repeatedly).
    pub descriptor_prefixes: Vec<String>,
    /// Descriptor fragments meaning "unidentified depositor".
    pub unidentified_markers: Vec<String>,
    /// Name tokens too common to identify a customer on their own.
    pub generic_name_tokens: Vec<String>,
    /// Debit descriptors that are bank fees (word prefixes).
    pub fee_patterns: Vec<String>,
    /// Title kinds that are services (candidates for tax withholding).
    pub service_kinds: Vec<String>,
    /// Withholding rates (percent) tried on service titles: ISS 2 to 5, IRRF 1,5, PCC 4,65, both 6,15.
    pub withholding_rates_pct: Vec<Decimal>,
    /// Contractual late fine (percent).
    pub fine_rate_pct: Decimal,
    /// Late interest per month (percent), pro rata by calendar day.
    pub interest_month_pct: Decimal,
    /// Days a boleto liquidation may be credited after the due date without being late for fee purposes.
    pub boleto_credit_lag_days: i64,
    /// Amount tolerance in centavos for "equal" amounts.
    pub amount_tolerance_centavos: i64,
    /// Uncharged fee at or above which a late title gets its own finding (below: only the aggregate).
    pub late_fee_materiality_brl: Decimal,
    /// Largest number of open titles of one customer searched for a subset that sums to a payment.
    pub subset_max_titles: usize,
    /// ERP and system names recognized in file names (they become the source keys).
    pub known_systems: Vec<String>,
    /// Accounts-payable inbox rules.
    pub ap: ApConfig,
}

/// Rules for the accounts-payable inbox and its approval chat.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct ApConfig {
    /// Fragments of a dunning or re-charge e-mail.
    pub dunning_patterns: Vec<String>,
    /// Regexes of a chat question asking whether something was already paid.
    pub already_paid_question_patterns: Vec<String>,
    /// Regexes of a chat approval.
    pub approval_patterns: Vec<String>,
    /// Regexes of a hedged, unconfirmed statement.
    pub hedge_patterns: Vec<String>,
    /// Largest acceptable gap (days) between a boleto's encoded due date and the claimed one.
    pub max_boleto_due_gap_days: i64,
}

impl Default for EngineConfig {
    fn default() -> Self {
        EngineConfig {
            descriptor_prefixes: strings(&[
                "LIQUIDACAO COBRANCA SIMPLES",
                "LIQUIDACAO COBRANCA",
                "LIQUIDACAO",
                "PIX RECEBIDO",
                "PIX RECEB",
                "PIX",
                "TED RECEBIDA",
                "TED",
                "DOC RECEBIDO",
                "DOC",
                "TRANSFERENCIA RECEBIDA",
                "TRANSFERENCIA",
                "TRANSF",
                "DEPOSITO",
                "DEP",
                "CREDITO",
                "CRED",
                "RECEBIMENTO",
                "CNPJ",
                "CPF",
            ]),
            unidentified_markers: strings(&[
                "NAO IDENTIFICADO",
                "NAO IDENTIFICADA",
                "NAO IDENTIF",
                "SEM IDENTIFICACAO",
            ]),
            generic_name_tokens: strings(&[
                "COMERCIO",
                "COMERCIAL",
                "ALIMENTOS",
                "INDUSTRIA",
                "INDUSTRIAL",
                "SERVICOS",
                "DISTRIBUIDORA",
                "PRODUTOS",
                "EMPREENDIMENTOS",
                "PARTICIPACOES",
                "COMPANHIA",
                "GRUPO",
                "HOLDING",
                "BRASIL",
                "NACIONAL",
                "INTERNACIONAL",
            ]),
            fee_patterns: strings(&[
                "TARIFA",
                "TAR",
                "IOF",
                "MANUTENCAO",
                "PACOTE",
                "CESTA",
                "ENCARGO",
                "ANUIDADE",
                "TAXA",
            ]),
            service_kinds: strings(&["SERVICO", "SERVICOS", "SERVICE", "SERV"]),
            withholding_rates_pct: vec![
                Decimal::new(15, 1),
                Decimal::new(2, 0),
                Decimal::new(3, 0),
                Decimal::new(465, 2),
                Decimal::new(5, 0),
                Decimal::new(615, 2),
            ],
            fine_rate_pct: Decimal::new(2, 0),
            interest_month_pct: Decimal::new(1, 0),
            boleto_credit_lag_days: 1,
            amount_tolerance_centavos: 1,
            late_fee_materiality_brl: Decimal::new(500, 0),
            subset_max_titles: 12,
            known_systems: strings(&[
                "protheus",
                "omie",
                "sap",
                "totvs",
                "datasul",
                "bling",
                "tiny",
                "contaazul",
                "sankhya",
                "senior",
                "oracle",
                "netsuite",
                "quickbooks",
                "xero",
                "winthor",
                "nibo",
                "linx",
            ]),
            ap: ApConfig::default(),
        }
    }
}

impl Default for ApConfig {
    fn default() -> Self {
        ApConfig {
            dunning_patterns: strings(&[
                "NAO IDENTIFICAMOS O PAGAMENTO",
                "COBRANCA EM ABERTO",
                "EM ABERTO",
                "REENVIO O BOLETO",
                "SEGUNDA VIA",
                "VALOR ATUALIZADO",
            ]),
            already_paid_question_patterns: strings(&[
                r"\bJA\s+(PAGAMOS|PAGARAM|PAGOU|FOI\s+PAG[OA])\b",
                r"\bFOI\s+PAG[OA]\s*\?",
                r"\bPAGAMOS\s*\?",
            ]),
            approval_patterns: strings(&[
                r"\bAPROVAD[OA]\b",
                r"\bPODE\s+PAGAR\b",
                r"\bLIBERAD[OA]\b",
            ]),
            hedge_patterns: strings(&[
                r"\bACHO\s+QUE\b",
                r"\bACHO\s+Q\b",
                r"\bTALVEZ\b",
                r"\bNAO\s+SEI\b",
                r"\bPROVAVELMENTE\b",
                r"\bCREIO\s+QUE\b",
                r"\bDEVE\s+TER\b",
            ]),
            max_boleto_due_gap_days: 30,
        }
    }
}

impl EngineConfig {
    /// Reads a YAML configuration; missing fields keep their defaults.
    pub fn from_yaml(text: &str) -> Result<EngineConfig, String> {
        serde_saphyr::from_str(text).map_err(|e| format!("configuração inválida: {e}"))
    }

    /// Fine rate as a fraction (`0.02`).
    pub fn fine_rate(&self) -> Decimal {
        self.fine_rate_pct / Decimal::ONE_HUNDRED
    }

    /// Monthly interest as a fraction (`0.01`).
    pub fn interest_month(&self) -> Decimal {
        self.interest_month_pct / Decimal::ONE_HUNDRED
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn shipped_yaml_matches_defaults() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/recon.yaml");
        let text = std::fs::read_to_string(path).unwrap_or_else(|e| panic!("{path}: {e}"));
        let cfg = EngineConfig::from_yaml(&text).unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(
            cfg,
            EngineConfig::default(),
            "config/recon.yaml diverge dos padrões do código"
        );
    }

    #[test]
    fn partial_yaml_keeps_defaults_and_unknown_keys_fail() {
        let cfg = EngineConfig::from_yaml("fine_rate_pct: 10\n").unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(cfg.fine_rate_pct, Decimal::new(10, 0));
        assert_eq!(cfg.subset_max_titles, 12);
        assert!(EngineConfig::from_yaml("nao_existe: 1\n").is_err());
    }
}
