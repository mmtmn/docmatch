//! Tolerance matching: explains the difference between an expected amount (a title) and an
//! observed amount (a payment) with the rules Brazilian payables follow.

use chrono::NaiveDate;
use dm_core::{Decimal, Money};
use dm_validators::business_days::{add_business_days, business_days_between, roll_forward};
use serde::{Deserialize, Serialize};

/// A rule that may explain an amount or date difference.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ToleranceRule {
    /// Amounts must be identical.
    Exact,
    /// Rounding slack in centavos (bank rounding, `0,01` differences).
    Rounding {
        /// Maximum absolute difference tolerated.
        centavos: i64,
    },
    /// Late-payment fine as a percentage of the title (`2` for 2%).
    Fine {
        /// Percentage.
        pct: Decimal,
    },
    /// Monthly interest pro-rata by calendar days late (`1` for 1% a.m.).
    Interest {
        /// Monthly percentage.
        monthly_pct: Decimal,
    },
    /// Tax withheld at source as a percentage of the title (`1.5`, `2`, `3`, `4.65`, `5`, `6.15`).
    Withholding {
        /// Percentage.
        pct: Decimal,
    },
    /// Discount as a percentage of the title.
    Discount {
        /// Percentage.
        pct: Decimal,
    },
    /// Payment credited up to N business days after the due date (D+1 credit).
    CreditDays {
        /// Business days.
        business_days: u32,
    },
}

impl ToleranceRule {
    /// The rules the harness ships with, in the order they are tried.
    pub fn defaults() -> Vec<ToleranceRule> {
        let pct = |s: &str| ToleranceRule::Withholding {
            pct: s.parse().unwrap_or_default(),
        };
        vec![
            ToleranceRule::Exact,
            ToleranceRule::Rounding { centavos: 1 },
            ToleranceRule::Fine { pct: Decimal::TWO },
            ToleranceRule::Interest {
                monthly_pct: Decimal::ONE,
            },
            pct("1.5"),
            pct("2"),
            pct("3"),
            pct("4.65"),
            pct("5"),
            pct("6.15"),
            ToleranceRule::CreditDays { business_days: 1 },
        ]
    }

    /// pt-BR label.
    pub fn label_ptbr(&self) -> String {
        match self {
            ToleranceRule::Exact => "valor exato".to_string(),
            ToleranceRule::Rounding { centavos } => {
                format!("arredondamento de até {centavos} centavo(s)")
            }
            ToleranceRule::Fine { pct } => format!("multa de {}% por atraso", pct.normalize()),
            ToleranceRule::Interest { monthly_pct } => {
                format!("juros de {}% ao mês pro rata", monthly_pct.normalize())
            }
            ToleranceRule::Withholding { pct } => {
                format!("retenção de {}% na fonte", pct.normalize())
            }
            ToleranceRule::Discount { pct } => format!("desconto de {}%", pct.normalize()),
            ToleranceRule::CreditDays { business_days } => format!("crédito em D+{business_days}"),
        }
    }
}

/// What is known about a title and its payment.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AmountContext {
    /// Title amount.
    pub expected: Money,
    /// Paid amount (absolute value).
    pub observed: Money,
    /// Due date, when known.
    pub due: Option<NaiveDate>,
    /// Payment or posting date, when known.
    pub paid: Option<NaiveDate>,
}

/// A difference explained by rules.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Explanation {
    /// Rules applied, in order.
    pub rules: Vec<ToleranceRule>,
    /// Adjustment the rules account for (positive adds to the title, negative subtracts).
    pub adjustment: Money,
    /// Difference left unexplained (zero when fully explained).
    pub residual: Money,
    /// Calendar days late, when both dates are known and payment is late.
    pub days_late: Option<i64>,
    /// pt-BR explanation.
    pub explanation: String,
}

/// Difference between the dates in business days when both are known.
fn business_days_late(ctx: &AmountContext) -> Option<i32> {
    Some(business_days_between(roll_forward(ctx.due?), ctx.paid?))
}

fn calendar_days_late(ctx: &AmountContext) -> Option<i64> {
    let late = (ctx.paid? - roll_forward(ctx.due?)).num_days();
    (late > 0).then_some(late)
}

/// Adjustment (to add to `expected`) a rule predicts for this context, or `None` when the
/// rule does not apply (interest without a late payment, D+1 without dates).
fn adjustment_for(rule: &ToleranceRule, ctx: &AmountContext) -> Option<Money> {
    match rule {
        ToleranceRule::Exact | ToleranceRule::Rounding { .. } => Some(Money::ZERO),
        ToleranceRule::Fine { pct } => calendar_days_late(ctx).map(|_| ctx.expected.percent(*pct)),
        ToleranceRule::Interest { monthly_pct } => {
            let days = calendar_days_late(ctx)?;
            let rate =
                *monthly_pct / Decimal::ONE_HUNDRED * Decimal::from(days) / Decimal::from(30);
            Some(ctx.expected.mul_rate(rate))
        }
        ToleranceRule::Withholding { pct } | ToleranceRule::Discount { pct } => {
            Some(-ctx.expected.percent(*pct))
        }
        ToleranceRule::CreditDays { business_days } => {
            let late = business_days_late(ctx)?;
            (late >= 0 && late <= *business_days as i32).then_some(Money::ZERO)
        }
    }
}

fn slack(rules: &[&ToleranceRule]) -> Money {
    rules
        .iter()
        .filter_map(|r| match r {
            ToleranceRule::Rounding { centavos } => Some(Money::from_centavos(*centavos)),
            _ => None,
        })
        .max()
        .unwrap_or(Money::ZERO)
}

/// Explains `observed - expected` with one rule, or with fine + interest together (the usual
/// late-payment combination), optionally with rounding slack. Returns the explanation with
/// the smallest residual; `None` when nothing brings the residual within the slack.
pub fn explain_difference(ctx: &AmountContext, rules: &[ToleranceRule]) -> Option<Explanation> {
    let diff = ctx.observed - ctx.expected;
    let rounding = rules
        .iter()
        .find(|r| matches!(r, ToleranceRule::Rounding { .. }));
    let mut candidates: Vec<Vec<&ToleranceRule>> = rules.iter().map(|r| vec![r]).collect();
    // fine + interest, with and without D+1
    let fines: Vec<&ToleranceRule> = rules
        .iter()
        .filter(|r| matches!(r, ToleranceRule::Fine { .. }))
        .collect();
    let interests: Vec<&ToleranceRule> = rules
        .iter()
        .filter(|r| matches!(r, ToleranceRule::Interest { .. }))
        .collect();
    for f in &fines {
        for i in &interests {
            candidates.push(vec![f, i]);
        }
    }
    // withholding + rounding
    if let Some(r) = rounding {
        let extra: Vec<Vec<&ToleranceRule>> = rules
            .iter()
            .filter(|x| !matches!(x, ToleranceRule::Rounding { .. } | ToleranceRule::Exact))
            .map(|x| vec![x, r])
            .collect();
        candidates.extend(extra);
    }
    let mut best: Option<Explanation> = None;
    for combo in candidates {
        let mut adjustment = Money::ZERO;
        let mut applicable = true;
        for rule in &combo {
            if let Some(a) = adjustment_for(rule, ctx) {
                adjustment += a;
            } else {
                applicable = false;
                break;
            }
        }
        if !applicable {
            continue;
        }
        let residual = diff - adjustment;
        if residual.abs() > slack(&combo) {
            continue;
        }
        let better = best.as_ref().is_none_or(|b| {
            residual.abs() < b.residual.abs()
                || (residual.abs() == b.residual.abs() && combo.len() < b.rules.len())
        });
        if better {
            let rules_owned: Vec<ToleranceRule> = combo.iter().map(|r| (*r).clone()).collect();
            let labels: Vec<String> = rules_owned.iter().map(ToleranceRule::label_ptbr).collect();
            let days_late = calendar_days_late(ctx);
            let explanation = match days_late {
                Some(d) if adjustment != Money::ZERO => format!(
                    "Diferença de {} explicada por {} ({} dia(s) de atraso).",
                    diff.format_brl(),
                    labels.join(" + "),
                    d
                ),
                _ if adjustment != Money::ZERO => format!(
                    "Diferença de {} explicada por {}.",
                    diff.format_brl(),
                    labels.join(" + ")
                ),
                _ => format!("Valores conciliados ({}).", labels.join(" + ")),
            };
            best = Some(Explanation {
                rules: rules_owned,
                adjustment,
                residual,
                days_late,
                explanation,
            });
        }
    }
    best
}

/// Dates compatible with a D+N credit window (the payment lands between the rolled-forward due
/// date and N business days after it).
pub fn credited_within(due: NaiveDate, paid: NaiveDate, business_days: u32) -> bool {
    let start = roll_forward(due);
    let end = add_business_days(start, business_days as i32);
    paid >= start && paid <= end
}

#[cfg(test)]
mod tests {
    use super::*;

    fn d(y: i32, m: u32, day: u32) -> NaiveDate {
        NaiveDate::from_ymd_opt(y, m, day).unwrap()
    }

    fn ctx(
        expected: i64,
        observed: i64,
        due: Option<NaiveDate>,
        paid: Option<NaiveDate>,
    ) -> AmountContext {
        AmountContext {
            expected: Money::from_centavos(expected),
            observed: Money::from_centavos(observed),
            due,
            paid,
        }
    }

    #[test]
    fn exact_and_rounding() {
        let rules = ToleranceRule::defaults();
        let e = explain_difference(&ctx(100_000, 100_000, None, None), &rules).unwrap();
        assert_eq!(e.rules, vec![ToleranceRule::Exact]);
        assert_eq!(e.residual, Money::ZERO);
        let e = explain_difference(&ctx(100_000, 100_001, None, None), &rules).unwrap();
        assert_eq!(e.rules, vec![ToleranceRule::Rounding { centavos: 1 }]);
        assert_eq!(e.residual, Money::from_centavos(1));
        assert!(explain_difference(&ctx(100_000, 100_050, None, None), &rules).is_none());
    }

    #[test]
    fn fine_and_interest_on_late_payment() {
        let rules = ToleranceRule::defaults();
        // R$ 1.000,00 due 10/03/2025 (Monday), paid 25/03/2025: 15 days late
        // fine 2% = 20,00; interest 1% a.m. pro rata 15/30 = 5,00 -> 1.025,00
        let e = explain_difference(
            &ctx(100_000, 102_500, Some(d(2025, 3, 10)), Some(d(2025, 3, 25))),
            &rules,
        )
        .unwrap();
        assert_eq!(
            e.rules,
            vec![
                ToleranceRule::Fine { pct: Decimal::TWO },
                ToleranceRule::Interest {
                    monthly_pct: Decimal::ONE
                }
            ]
        );
        assert_eq!(e.adjustment, Money::from_centavos(2_500));
        assert_eq!(e.days_late, Some(15));
        assert!(e.explanation.contains("15 dia(s)"));
        // fine alone
        let e = explain_difference(
            &ctx(100_000, 102_000, Some(d(2025, 3, 10)), Some(d(2025, 3, 25))),
            &rules,
        )
        .unwrap();
        assert_eq!(e.rules, vec![ToleranceRule::Fine { pct: Decimal::TWO }]);
        // not late: fine does not apply
        assert!(explain_difference(
            &ctx(100_000, 102_000, Some(d(2025, 3, 10)), Some(d(2025, 3, 10))),
            &rules
        )
        .is_none());
        // due on a Saturday rolls to Monday, payment on Monday is not late
        assert!(explain_difference(
            &ctx(100_000, 102_000, Some(d(2025, 3, 8)), Some(d(2025, 3, 10))),
            &rules
        )
        .is_none());
    }

    #[test]
    fn withholdings() {
        let rules = ToleranceRule::defaults();
        for (pct, paid) in [
            ("1.5", 98_500),
            ("2", 98_000),
            ("3", 97_000),
            ("4.65", 95_350),
            ("5", 95_000),
            ("6.15", 93_850),
        ] {
            let e = explain_difference(&ctx(100_000, paid, None, None), &rules)
                .unwrap_or_else(|| panic!("{pct}"));
            assert_eq!(
                e.rules,
                vec![ToleranceRule::Withholding {
                    pct: pct.parse().unwrap()
                }],
                "{pct}"
            );
            assert_eq!(e.residual, Money::ZERO);
        }
        // 4,65% of 1.234,56 = 57,41 (57,4070 rounded) -> 1.177,15, bank pays 1.177,16: rounding slack
        let e = explain_difference(&ctx(123_456, 117_716, None, None), &rules).unwrap();
        assert!(e.rules.contains(&ToleranceRule::Withholding {
            pct: "4.65".parse().unwrap()
        }));
        assert_eq!(e.residual.abs(), Money::from_centavos(1));
    }

    #[test]
    fn credit_window() {
        let rules = ToleranceRule::defaults();
        // Friday due, credited Monday (D+1 business day)
        let e = explain_difference(
            &ctx(5_000, 5_000, Some(d(2025, 3, 7)), Some(d(2025, 3, 10))),
            &rules,
        )
        .unwrap();
        assert_eq!(e.rules, vec![ToleranceRule::Exact]);
        assert!(credited_within(d(2025, 3, 7), d(2025, 3, 10), 1));
        assert!(!credited_within(d(2025, 3, 7), d(2025, 3, 11), 1));
        assert!(credited_within(d(2025, 3, 8), d(2025, 3, 10), 0));
        let only_credit = [ToleranceRule::CreditDays { business_days: 1 }];
        assert!(explain_difference(
            &ctx(5_000, 5_000, Some(d(2025, 3, 7)), Some(d(2025, 3, 10))),
            &only_credit
        )
        .is_some());
        assert!(explain_difference(
            &ctx(5_000, 5_000, Some(d(2025, 3, 7)), Some(d(2025, 3, 12))),
            &only_credit
        )
        .is_none());
        assert!(explain_difference(&ctx(5_000, 5_000, None, None), &only_credit).is_none());
    }

    #[test]
    fn labels() {
        assert_eq!(
            ToleranceRule::Fine { pct: Decimal::TWO }.label_ptbr(),
            "multa de 2% por atraso"
        );
        assert_eq!(
            ToleranceRule::Withholding {
                pct: "4.65".parse().unwrap()
            }
            .label_ptbr(),
            "retenção de 4.65% na fonte"
        );
        assert_eq!(
            ToleranceRule::CreditDays { business_days: 1 }.label_ptbr(),
            "crédito em D+1"
        );
    }
}
