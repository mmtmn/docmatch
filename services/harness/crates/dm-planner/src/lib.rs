//! Planejamento de execução: estágios da cascata e orçamento por estágio (esqueleto; a fase
//! 1 liga o plano ao orçamento assinado da API).
//!
//! Regra fixa do produto: código primeiro, modelo depois. Estágios determinísticos nunca
//! recebem tokens; o resíduo que sobra deles é o que os estágios de modelo veem.
#![forbid(unsafe_code)]

use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Errors of the planner.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum PlanError {
    /// The budget cannot cover the minimum.
    #[error("orçamento de {0} tokens abaixo do mínimo de {1}")]
    BudgetTooSmall(u64, u64),
}

/// Pipeline stages, in order.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Stage {
    /// Sniff and parse every file.
    Ingest,
    /// Normalize values and build keys.
    Normalize,
    /// Exact key joins.
    ExactMatch,
    /// Tolerance rules.
    ToleranceMatch,
    /// Subset sums within groups.
    SubsetSum,
    /// Fellegi–Sunter scoring and assignment.
    Scoring,
    /// Rule discovery (model).
    Discovery,
    /// Adjudication of the residue (model).
    Adjudication,
    /// Cross-family verification (model).
    Verification,
    /// Report rendering.
    Report,
}

impl Stage {
    /// Every stage in pipeline order.
    pub const ALL: [Stage; 10] = [
        Stage::Ingest,
        Stage::Normalize,
        Stage::ExactMatch,
        Stage::ToleranceMatch,
        Stage::SubsetSum,
        Stage::Scoring,
        Stage::Discovery,
        Stage::Adjudication,
        Stage::Verification,
        Stage::Report,
    ];

    /// True for stages that call a model.
    pub fn uses_model(self) -> bool {
        matches!(
            self,
            Stage::Discovery | Stage::Adjudication | Stage::Verification
        )
    }

    /// Share of the model budget a model stage gets (sums to 1 across model stages).
    fn model_share(self) -> f64 {
        match self {
            Stage::Discovery => 0.4,
            Stage::Adjudication => 0.35,
            Stage::Verification => 0.25,
            _ => 0.0,
        }
    }

    /// pt-BR label.
    pub fn label_ptbr(self) -> &'static str {
        match self {
            Stage::Ingest => "ingestão",
            Stage::Normalize => "normalização",
            Stage::ExactMatch => "casamento exato",
            Stage::ToleranceMatch => "casamento por tolerância",
            Stage::SubsetSum => "soma de subconjuntos",
            Stage::Scoring => "pontuação e atribuição",
            Stage::Discovery => "descoberta de regras",
            Stage::Adjudication => "adjudicação",
            Stage::Verification => "verificação cruzada",
            Stage::Report => "relatório",
        }
    }
}

/// Budget of one stage.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct StageBudget {
    /// Stage.
    pub stage: Stage,
    /// Model tokens (always 0 for deterministic stages).
    pub max_tokens: u64,
    /// Wall-clock seconds.
    pub max_seconds: u64,
}

/// An execution plan.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Plan {
    /// Stages in order.
    pub stages: Vec<StageBudget>,
    /// Total model tokens.
    pub total_tokens: u64,
}

/// Minimum tokens a run needs for the model stages to be worth starting.
pub const MIN_MODEL_TOKENS: u64 = 1_000;

impl Plan {
    /// Deterministic-first plan: model stages split `total_tokens` by fixed shares; with
    /// zero tokens the model stages are skipped (`DM_LLM_MODE=off` runs).
    pub fn deterministic_first(total_tokens: u64, total_seconds: u64) -> Result<Plan, PlanError> {
        if total_tokens > 0 && total_tokens < MIN_MODEL_TOKENS {
            return Err(PlanError::BudgetTooSmall(total_tokens, MIN_MODEL_TOKENS));
        }
        let model_stages = Stage::ALL.iter().filter(|s| s.uses_model()).count() as u64;
        let deterministic = Stage::ALL.len() as u64 - model_stages;
        let det_seconds = total_seconds / 2 / deterministic.max(1);
        let model_seconds = if total_tokens == 0 {
            0
        } else {
            total_seconds / 2 / model_stages.max(1)
        };
        let mut stages: Vec<StageBudget> = Stage::ALL
            .iter()
            .map(|s| StageBudget {
                stage: *s,
                max_tokens: if s.uses_model() {
                    (total_tokens as f64 * s.model_share()).floor() as u64
                } else {
                    0
                },
                max_seconds: if s.uses_model() {
                    model_seconds
                } else {
                    det_seconds
                },
            })
            .collect();
        // largest-remainder: hand leftover tokens to the first model stage
        let assigned: u64 = stages.iter().map(|s| s.max_tokens).sum();
        if let Some(first_model) = stages.iter_mut().find(|s| s.stage.uses_model()) {
            first_model.max_tokens += total_tokens - assigned;
        }
        Ok(Plan {
            stages,
            total_tokens,
        })
    }

    /// Stages that will run (model stages only when they have tokens).
    pub fn active_stages(&self) -> Vec<Stage> {
        self.stages
            .iter()
            .filter(|s| !s.stage.uses_model() || s.max_tokens > 0)
            .map(|s| s.stage)
            .collect()
    }

    /// The stage after `current`, skipping inactive ones.
    pub fn next_stage(&self, current: Stage) -> Option<Stage> {
        let active = self.active_stages();
        let idx = active.iter().position(|s| *s == current)?;
        active.get(idx + 1).copied()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deterministic_stages_never_get_tokens() {
        let plan = Plan::deterministic_first(10_000, 600).unwrap();
        assert_eq!(plan.stages.len(), 10);
        for s in &plan.stages {
            if s.stage.uses_model() {
                assert!(s.max_tokens > 0);
            } else {
                assert_eq!(s.max_tokens, 0, "{:?}", s.stage);
            }
        }
        assert_eq!(
            plan.stages.iter().map(|s| s.max_tokens).sum::<u64>(),
            10_000
        );
        assert_eq!(plan.active_stages().len(), 10);
        assert_eq!(plan.next_stage(Stage::Scoring), Some(Stage::Discovery));
        assert_eq!(plan.next_stage(Stage::Report), None);
    }

    #[test]
    fn off_mode_skips_model_stages() {
        let plan = Plan::deterministic_first(0, 600).unwrap();
        assert_eq!(plan.active_stages().len(), 7);
        assert_eq!(plan.next_stage(Stage::Scoring), Some(Stage::Report));
        assert!(matches!(
            Plan::deterministic_first(10, 60),
            Err(PlanError::BudgetTooSmall(10, MIN_MODEL_TOKENS))
        ));
        assert_eq!(Stage::Discovery.label_ptbr(), "descoberta de regras");
    }
}
