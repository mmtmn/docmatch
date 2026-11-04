//! Exact matching: join two item sets on normalized keys built from a [`KeySpec`].

use std::collections::BTreeMap;

use dm_core::{Item, Value};
use dm_normalize::{doc_number, name_key, tax_id};
use serde::{Deserialize, Serialize};

/// How a field is normalized before it joins the key.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum KeyTransform {
    /// Trimmed text as is.
    Raw,
    /// Uppercase, trimmed.
    Upper,
    /// Document number without prefix and leading zeros.
    DocNumber,
    /// Tax id digits.
    TaxId,
    /// Company name core (no legal form, abbreviations expanded).
    NameCore,
    /// Sorted token set of the name core.
    NameTokens,
    /// Date as ISO `yyyy-mm-dd`.
    Date,
    /// Money as centavos.
    Money,
    /// Absolute money as centavos (bank debits are negative, titles positive).
    MoneyAbs,
}

/// One part of a key.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct KeyPart {
    /// Item field name.
    pub field: String,
    /// Normalization.
    pub transform: KeyTransform,
}

/// A key built from one or more item fields.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct KeySpec {
    /// Parts, joined with `|`.
    pub parts: Vec<KeyPart>,
}

impl KeySpec {
    /// Key with a single part.
    pub fn single(field: impl Into<String>, transform: KeyTransform) -> Self {
        KeySpec {
            parts: vec![KeyPart {
                field: field.into(),
                transform,
            }],
        }
    }

    /// Adds a part.
    pub fn and(mut self, field: impl Into<String>, transform: KeyTransform) -> Self {
        self.parts.push(KeyPart {
            field: field.into(),
            transform,
        });
        self
    }

    /// Key of an item; `None` when any part is missing or empty after normalization.
    pub fn key_of(&self, item: &Item) -> Option<String> {
        let mut out = Vec::with_capacity(self.parts.len());
        for part in &self.parts {
            let value = item.field(&part.field)?;
            let k = normalize_value(value, part.transform)?;
            if k.is_empty() {
                return None;
            }
            out.push(k);
        }
        Some(out.join("|"))
    }
}

/// Applies a transform to a value.
pub fn normalize_value(value: &Value, transform: KeyTransform) -> Option<String> {
    match transform {
        KeyTransform::Raw => Some(value.render().trim().to_string()),
        KeyTransform::Upper => Some(value.render().trim().to_uppercase()),
        KeyTransform::DocNumber => {
            let text = value.render();
            let d = doc_number(&text);
            (!d.canonical.is_empty()).then_some(d.canonical)
        }
        KeyTransform::TaxId => {
            let text = value.render();
            let t = tax_id(&text);
            (!t.digits.is_empty()).then_some(t.digits)
        }
        KeyTransform::NameCore => Some(name_key(&value.render()).core),
        KeyTransform::NameTokens => Some(name_key(&value.render()).tokens.join(" ")),
        KeyTransform::Date => value.as_date().map(|d| d.format("%Y-%m-%d").to_string()),
        KeyTransform::Money => value.as_money().map(|m| m.centavos().to_string()),
        KeyTransform::MoneyAbs => value.as_money().map(|m| m.abs().centavos().to_string()),
    }
}

/// A one-to-one pair found by exact key.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct MatchPair {
    /// Left item id.
    pub left_id: String,
    /// Right item id.
    pub right_id: String,
    /// Key both share.
    pub key: String,
}

/// A key shared by several items on at least one side (needs the assignment stage).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct MatchGroup {
    /// Key.
    pub key: String,
    /// Left ids.
    pub left_ids: Vec<String>,
    /// Right ids.
    pub right_ids: Vec<String>,
}

/// Result of an exact join.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct ExactMatch {
    /// Unambiguous pairs.
    pub pairs: Vec<MatchPair>,
    /// Ambiguous groups (many-to-one or many-to-many).
    pub groups: Vec<MatchGroup>,
    /// Left ids with no counterpart.
    pub unmatched_left: Vec<String>,
    /// Right ids with no counterpart.
    pub unmatched_right: Vec<String>,
    /// Left ids whose key could not be built.
    pub unkeyed_left: Vec<String>,
    /// Right ids whose key could not be built.
    pub unkeyed_right: Vec<String>,
}

/// Joins two item sets on their keys. Keys unique on both sides become pairs; everything
/// else with a shared key becomes a group.
pub fn exact_match(
    left: &[Item],
    right: &[Item],
    left_spec: &KeySpec,
    right_spec: &KeySpec,
) -> ExactMatch {
    let mut out = ExactMatch::default();
    let mut by_key: BTreeMap<String, (Vec<String>, Vec<String>)> = BTreeMap::new();
    for item in left {
        match left_spec.key_of(item) {
            Some(k) => by_key.entry(k).or_default().0.push(item.id.clone()),
            None => out.unkeyed_left.push(item.id.clone()),
        }
    }
    for item in right {
        match right_spec.key_of(item) {
            Some(k) => by_key.entry(k).or_default().1.push(item.id.clone()),
            None => out.unkeyed_right.push(item.id.clone()),
        }
    }
    for (key, (l, r)) in by_key {
        match (l.len(), r.len()) {
            (1, 1) => out.pairs.push(MatchPair {
                left_id: l[0].clone(),
                right_id: r[0].clone(),
                key,
            }),
            (_, 0) => out.unmatched_left.extend(l),
            (0, _) => out.unmatched_right.extend(r),
            _ => out.groups.push(MatchGroup {
                key,
                left_ids: l,
                right_ids: r,
            }),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use dm_core::{ItemKind, Locator, Money};
    use indexmap::IndexMap;

    fn item(id: &str, fields: &[(&str, Value)]) -> Item {
        let mut map = IndexMap::new();
        for (k, v) in fields {
            map.insert((*k).to_string(), v.clone());
        }
        Item {
            id: id.into(),
            kind: ItemKind::Row,
            source_id: "s".into(),
            locator: Locator::Row {
                table: "t".into(),
                row: 0,
            },
            fields: map,
            time: None,
            entity_mentions: vec![],
        }
    }

    #[test]
    #[allow(clippy::many_single_char_names)]
    fn keys_normalize_each_part() {
        let spec = KeySpec::single("documento", KeyTransform::DocNumber)
            .and("valor", KeyTransform::MoneyAbs);
        let a = item(
            "a",
            &[
                ("documento", Value::from("NF 088231")),
                ("valor", Value::Money(Money::from_centavos(-1_000))),
            ],
        );
        let b = item(
            "b",
            &[
                ("documento", Value::from("88231")),
                ("valor", Value::Money(Money::from_centavos(1_000))),
            ],
        );
        assert_eq!(spec.key_of(&a), Some("88231|1000".into()));
        assert_eq!(spec.key_of(&a), spec.key_of(&b));
        let c = item("c", &[("documento", Value::Null)]);
        assert_eq!(spec.key_of(&c), None);
        let n = KeySpec::single("nome", KeyTransform::NameTokens);
        let x = item("x", &[("nome", Value::from("BELA VISTA EMPORIO LTDA"))]);
        let y = item("y", &[("nome", Value::from("Empório Bela Vista"))]);
        assert_eq!(n.key_of(&x), n.key_of(&y));
        let t = KeySpec::single("cnpj", KeyTransform::TaxId);
        assert_eq!(
            t.key_of(&item("z", &[("cnpj", Value::from("11.222.333/0001-81"))])),
            Some("11222333000181".into())
        );
        let d = KeySpec::single("data", KeyTransform::Date);
        assert_eq!(
            d.key_of(&item(
                "w",
                &[(
                    "data",
                    Value::Date(chrono::NaiveDate::from_ymd_opt(2025, 3, 5).unwrap())
                )]
            )),
            Some("2025-03-05".into())
        );
    }

    #[test]
    fn joins_pairs_groups_and_leftovers() {
        let spec = KeySpec::single("doc", KeyTransform::DocNumber);
        let left = vec![
            item("l1", &[("doc", Value::from("088231"))]),
            item("l2", &[("doc", Value::from("088232"))]),
            item("l3", &[("doc", Value::from("088233"))]),
            item("l4", &[("doc", Value::from("088233"))]),
            item("l5", &[("doc", Value::Null)]),
        ];
        let right = vec![
            item("r1", &[("doc", Value::from("NF 88231"))]),
            item("r3", &[("doc", Value::from("88233"))]),
            item("r9", &[("doc", Value::from("99999"))]),
        ];
        let m = exact_match(&left, &right, &spec, &spec);
        assert_eq!(
            m.pairs,
            vec![MatchPair {
                left_id: "l1".into(),
                right_id: "r1".into(),
                key: "88231".into()
            }]
        );
        assert_eq!(m.groups.len(), 1);
        assert_eq!(m.groups[0].left_ids, vec!["l3", "l4"]);
        assert_eq!(m.groups[0].right_ids, vec!["r3"]);
        assert_eq!(m.unmatched_left, vec!["l2"]);
        assert_eq!(m.unmatched_right, vec!["r9"]);
        assert_eq!(m.unkeyed_left, vec!["l5"]);
        assert!(m.unkeyed_right.is_empty());
    }
}
