//! Guarda contra injeção de prompt (OWASP LLM01) e contra saídas fora do contrato.
//!
//! Texto de documento é sempre dado, nunca instrução: [`spotlight`] marca e delimita o texto
//! não confiável antes de ir ao modelo; [`screen_request`] detecta instruções suspeitas em
//! pt-BR e inglês; [`screen_document`] transforma instruções embutidas em um [`Finding`] do
//! tipo `injected_instructions` (nunca executado); [`screen_output`] rejeita saídas com URLs,
//! comandos ou texto livre que não seja pt-BR dentro dos limites.
#![forbid(unsafe_code)]

pub mod document;
pub mod output;
pub mod screen;
pub mod spotlight;

pub use document::{screen_document, DocumentScreen};
pub use output::{screen_output, OutputPolicy, OutputViolation};
pub use screen::{screen_request, Category, PatternHit, ScreenVerdict};
pub use spotlight::{spotlight, spotlight_with, unspotlight, SpotlightConfig, Spotlighted};
