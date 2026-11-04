//! Casamento determinístico: chaves exatas, tolerâncias (multa, juros, retenções, D+1),
//! soma de subconjuntos limitada, pontuação Fellegi–Sunter e atribuição global um-para-um.
//!
//! Cada bloco funciona isolado sobre [`dm_core::Item`]s ou mapas de campos; a fase 1 do plano
//! encadeia tudo na cascata e grava os [`dm_core::Fact`]s resultantes no grafo.
#![forbid(unsafe_code)]

pub mod assignment;
pub mod key;
pub mod scorer;
pub mod subset_sum;
pub mod tolerance;

pub use assignment::{assign_by_scores, assign_one_to_one, Assignment};
pub use key::{exact_match, ExactMatch, KeyPart, KeySpec, KeyTransform, MatchGroup, MatchPair};
pub use scorer::{Agreement, Decision, FieldMap, FieldScore, FieldSpec, Score, Scorer};
pub use subset_sum::{subset_sum, SubsetSolution};
pub use tolerance::{explain_difference, AmountContext, Explanation, ToleranceRule};
