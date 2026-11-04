//! Tipos centrais do harness docmatch.
//!
//! Tudo o que os outros crates compartilham mora aqui: dinheiro em centavos ([`Money`]),
//! tabelas tipadas com papéis inferidos ([`Table`], [`ColumnRole`]) e o modelo de evidência
//! ([`Item`], [`Fact`], [`Finding`], [`Rule`]). Todos os tipos serializam em JSON com nomes de
//! campo estáveis, os mesmos de `packages/contracts/schemas`.
#![forbid(unsafe_code)]

pub mod error;
pub mod evidence;
pub mod ids;
pub mod money;
pub mod table;

pub use error::CoreError;
pub use evidence::{
    Entity, EntityKey, EntityKind, EntityMention, EvidenceRef, Fact, FactArg, FactRel, Finding,
    FindingKind, FindingStatus, Item, ItemKind, Lineage, Locator, Qualifier, Rule, RuleProvenance,
    RuleScope, RuleStats, RuleStatus, Severity, TimeRef,
};
pub use ids::{stable_id, ItemId, RuleId, SourceId};
pub use money::Money;
pub use rust_decimal::Decimal;
pub use table::{Column, ColumnRole, Record, Table, Value};
