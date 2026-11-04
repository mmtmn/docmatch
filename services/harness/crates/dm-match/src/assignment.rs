//! Global one-to-one assignment (Hungarian algorithm, `pathfinding::kuhn_munkres`): no line
//! is used twice, the total cost is minimal, and pairs above a cost ceiling are dropped.

use pathfinding::prelude::{kuhn_munkres_min, Matrix};
use serde::{Deserialize, Serialize};

/// One assigned pair.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Assignment {
    /// Row (left) index.
    pub left: usize,
    /// Column (right) index.
    pub right: usize,
    /// Cost of the pair.
    pub cost: i64,
}

/// Cost that marks "never pair these two"; also used to pad rectangular matrices.
pub const FORBIDDEN: i64 = i64::MAX / 4;

/// Minimum-cost one-to-one assignment of rows to columns.
///
/// `costs[i][j]` is the cost of pairing left `i` with right `j`; use [`FORBIDDEN`] for pairs
/// that must not happen. Rectangular inputs are padded. Pairs whose cost is at or above
/// `max_cost` (or forbidden) are not returned, so the result may leave rows unassigned.
pub fn assign_one_to_one(costs: &[Vec<i64>], max_cost: i64) -> Vec<Assignment> {
    let rows = costs.len();
    let cols = costs.iter().map(Vec::len).max().unwrap_or(0);
    if rows == 0 || cols == 0 {
        return Vec::new();
    }
    let n = rows.max(cols);
    let padded: Vec<Vec<i64>> = (0..n)
        .map(|i| {
            (0..n)
                .map(|j| {
                    costs
                        .get(i)
                        .and_then(|r| r.get(j))
                        .copied()
                        .unwrap_or(FORBIDDEN)
                        .min(FORBIDDEN)
                })
                .collect()
        })
        .collect();
    let Ok(matrix) = Matrix::from_rows(padded) else {
        return Vec::new();
    };
    let (_, assignment) = kuhn_munkres_min(&matrix);
    assignment
        .into_iter()
        .enumerate()
        .filter(|(i, j)| *i < rows && *j < cols)
        .filter_map(|(i, j)| {
            let cost = *costs[i].get(j)?;
            (cost < max_cost && cost < FORBIDDEN).then_some(Assignment {
                left: i,
                right: j,
                cost,
            })
        })
        .collect()
}

/// Assignment from similarity scores (higher is better): scores below `min_score` are
/// forbidden, the rest become costs `-round(score * 1000)`.
pub fn assign_by_scores(scores: &[Vec<f64>], min_score: f64) -> Vec<Assignment> {
    let costs: Vec<Vec<i64>> = scores
        .iter()
        .map(|row| {
            row.iter()
                .map(|s| {
                    if s.is_finite() && *s >= min_score {
                        -(s * 1000.0).round() as i64
                    } else {
                        FORBIDDEN
                    }
                })
                .collect()
        })
        .collect();
    assign_one_to_one(&costs, FORBIDDEN)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn square_optimum() {
        // classic example: optimum is 0-1, 1-0, 2-2 with total 2+1+2 = 5? rows: [4,1,3],[2,0,5],[3,2,2]
        let costs = vec![vec![4, 1, 3], vec![2, 0, 5], vec![3, 2, 2]];
        let a = assign_one_to_one(&costs, i64::MAX);
        let total: i64 = a.iter().map(|x| x.cost).sum();
        assert_eq!(total, 5);
        let mut lefts: Vec<usize> = a.iter().map(|x| x.left).collect();
        lefts.sort_unstable();
        assert_eq!(lefts, vec![0, 1, 2]);
        let mut rights: Vec<usize> = a.iter().map(|x| x.right).collect();
        rights.sort_unstable();
        assert_eq!(rights, vec![0, 1, 2]);
    }

    #[test]
    fn rectangular_and_forbidden() {
        // 2 titles, 3 bank lines; title 1 may only go to line 2
        let costs = vec![vec![1, 5, 9], vec![FORBIDDEN, FORBIDDEN, 2]];
        let a = assign_one_to_one(&costs, i64::MAX);
        assert_eq!(a.len(), 2);
        assert!(a.contains(&Assignment {
            left: 0,
            right: 0,
            cost: 1
        }));
        assert!(a.contains(&Assignment {
            left: 1,
            right: 2,
            cost: 2
        }));
        // more rows than columns: one row stays unassigned
        let costs = vec![vec![1], vec![2], vec![3]];
        let a = assign_one_to_one(&costs, i64::MAX);
        assert_eq!(
            a,
            vec![Assignment {
                left: 0,
                right: 0,
                cost: 1
            }]
        );
        // a ceiling drops expensive pairs
        let costs = vec![vec![1, 100], vec![100, 1]];
        assert_eq!(assign_one_to_one(&costs, 50).len(), 2);
        let costs = vec![vec![100, 1], vec![100, 100]];
        let a = assign_one_to_one(&costs, 50);
        assert_eq!(
            a,
            vec![Assignment {
                left: 0,
                right: 1,
                cost: 1
            }]
        );
        assert!(assign_one_to_one(&[], 1).is_empty());
        assert!(assign_one_to_one(&[vec![]], 1).is_empty());
    }

    #[test]
    fn no_line_used_twice_even_with_ties() {
        // every title prefers bank line 0; the assignment must still spread them out
        let costs = vec![vec![0, 10, 20], vec![0, 10, 20], vec![0, 10, 20]];
        let a = assign_one_to_one(&costs, i64::MAX);
        let mut rights: Vec<usize> = a.iter().map(|x| x.right).collect();
        rights.sort_unstable();
        assert_eq!(rights, vec![0, 1, 2]);
    }

    #[test]
    fn scores_to_costs() {
        let scores = vec![vec![12.5, 1.0], vec![0.5, 9.0]];
        let a = assign_by_scores(&scores, 2.0);
        assert_eq!(a.len(), 2);
        assert!(a.contains(&Assignment {
            left: 0,
            right: 0,
            cost: -12_500
        }));
        assert!(a.contains(&Assignment {
            left: 1,
            right: 1,
            cost: -9_000
        }));
        let a = assign_by_scores(&[vec![1.0, 1.5]], 2.0);
        assert!(a.is_empty());
        let a = assign_by_scores(&[vec![f64::NAN, 3.0]], 2.0);
        assert_eq!(
            a,
            vec![Assignment {
                left: 0,
                right: 1,
                cost: -3_000
            }]
        );
    }
}
