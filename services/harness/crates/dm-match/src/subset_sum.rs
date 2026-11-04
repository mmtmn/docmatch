//! Bounded subset sum: which titles add up to one payment (or which payments settle one
//! title). Meet-in-the-middle over at most twelve candidates, exact in centavos.

use dm_core::Money;
use serde::{Deserialize, Serialize};

/// Hard cap on candidates (2^12 subsets, split in two halves of 2^6).
pub const MAX_CANDIDATES: usize = 12;

/// A subset whose amounts reach the target.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SubsetSolution {
    /// Ids of the chosen candidates, in input order.
    pub ids: Vec<String>,
    /// Sum of the chosen amounts.
    pub sum: Money,
    /// `sum - target`.
    pub residual: Money,
}

/// Finds subsets of `candidates` whose sum is within `tolerance` of `target`.
///
/// Returns solutions sorted by (smallest residual, fewest items, input order), at most
/// `max_solutions`. Returns an empty list when there are more than [`MAX_CANDIDATES`]
/// candidates: the caller must pre-filter (by counterparty, date window) instead of asking
/// for an exponential search.
pub fn subset_sum(
    candidates: &[(String, Money)],
    target: Money,
    tolerance: Money,
    max_solutions: usize,
) -> Vec<SubsetSolution> {
    if candidates.is_empty() || candidates.len() > MAX_CANDIDATES || max_solutions == 0 {
        return Vec::new();
    }
    let amounts: Vec<i64> = candidates.iter().map(|(_, m)| m.centavos()).collect();
    let target_c = target.centavos();
    let tol = tolerance.abs().centavos();
    let (left, right) = amounts.split_at(amounts.len() / 2);
    let left_sums = half_sums(left);
    let mut right_sums = half_sums(right);
    right_sums.sort_unstable_by_key(|(sum, _)| *sum);
    let mut solutions: Vec<(i64, usize, u32, Vec<String>, i64)> = Vec::new();
    for (lsum, lmask) in &left_sums {
        let lo = target_c - tol - lsum;
        let hi = target_c + tol - lsum;
        let start = right_sums.partition_point(|(s, _)| *s < lo);
        for (rsum, rmask) in right_sums[start..].iter().take_while(|(s, _)| *s <= hi) {
            let mask = u32::from(*lmask) | (u32::from(*rmask) << left.len());
            if mask == 0 {
                continue;
            }
            let sum = lsum + rsum;
            let ids: Vec<String> = candidates
                .iter()
                .enumerate()
                .filter(|(i, _)| mask & (1 << i) != 0)
                .map(|(_, (id, _))| id.clone())
                .collect();
            solutions.push(((sum - target_c).abs(), ids.len(), mask, ids, sum));
        }
    }
    solutions.sort_by(|a, b| a.0.cmp(&b.0).then(a.1.cmp(&b.1)).then(a.2.cmp(&b.2)));
    solutions
        .into_iter()
        .take(max_solutions)
        .map(|(_, _, _, ids, sum)| SubsetSolution {
            ids,
            sum: Money::from_centavos(sum),
            residual: Money::from_centavos(sum - target_c),
        })
        .collect()
}

/// Sums of every subset of a half, with the subset bitmask.
fn half_sums(values: &[i64]) -> Vec<(i64, u16)> {
    let n = values.len();
    let mut out = Vec::with_capacity(1 << n);
    for mask in 0u16..(1u16 << n) {
        let sum: i64 = (0..n)
            .filter(|i| mask & (1 << i) != 0)
            .map(|i| values[i])
            .sum();
        out.push((sum, mask));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn c(id: &str, v: i64) -> (String, Money) {
        (id.to_string(), Money::from_centavos(v))
    }

    #[test]
    fn finds_exact_subsets() {
        let candidates = vec![
            c("a", 10_000),
            c("b", 25_000),
            c("c", 30_000),
            c("d", 5_000),
            c("e", 70_000),
        ];
        let s = subset_sum(&candidates, Money::from_centavos(40_000), Money::ZERO, 10);
        assert!(!s.is_empty());
        assert_eq!(s[0].residual, Money::ZERO);
        assert_eq!(s[0].ids, vec!["a", "c"]);
        // 400 = a + c = b + a + d ; fewest items first
        assert_eq!(s.len(), 2);
        assert_eq!(s[1].ids, vec!["a", "b", "d"]);
        let single = subset_sum(&candidates, Money::from_centavos(70_000), Money::ZERO, 1);
        assert_eq!(single[0].ids, vec!["e"]);
    }

    #[test]
    fn tolerance_and_ordering() {
        let candidates = vec![c("a", 10_000), c("b", 20_000), c("c", 30_001)];
        let exact = subset_sum(&candidates, Money::from_centavos(60_000), Money::ZERO, 5);
        assert!(exact.is_empty());
        let s = subset_sum(
            &candidates,
            Money::from_centavos(60_000),
            Money::from_centavos(1),
            5,
        );
        assert_eq!(s.len(), 1);
        assert_eq!(s[0].ids, vec!["a", "b", "c"]);
        assert_eq!(s[0].residual, Money::from_centavos(1));
        // nearest residual first
        let s = subset_sum(
            &candidates,
            Money::from_centavos(30_000),
            Money::from_centavos(5),
            5,
        );
        assert_eq!(s[0].ids, vec!["a", "b"]);
        assert_eq!(s[1].ids, vec!["c"]);
    }

    #[test]
    fn bounds() {
        let many: Vec<(String, Money)> = (0..13).map(|i| c(&i.to_string(), 1)).collect();
        assert!(subset_sum(&many, Money::from_centavos(3), Money::ZERO, 5).is_empty());
        assert!(subset_sum(&[], Money::from_centavos(3), Money::ZERO, 5).is_empty());
        assert!(subset_sum(&[c("a", 1)], Money::from_centavos(1), Money::ZERO, 0).is_empty());
        // zero target never returns the empty subset
        assert!(subset_sum(&[c("a", 1)], Money::ZERO, Money::ZERO, 5).is_empty());
        // twelve candidates run instantly
        let twelve: Vec<(String, Money)> = (1..=12).map(|i| c(&i.to_string(), i * 100)).collect();
        let s = subset_sum(&twelve, Money::from_centavos(7_800), Money::ZERO, 1);
        assert_eq!(s[0].ids.len(), 12);
        // negative amounts (bank debits) work the same way
        let debits = vec![c("a", -100), c("b", -250)];
        assert_eq!(
            subset_sum(&debits, Money::from_centavos(-350), Money::ZERO, 1)[0].ids,
            vec!["a", "b"]
        );
    }
}
