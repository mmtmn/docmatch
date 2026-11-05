//! What the cascade means for every title: status, amounts, timing, flags and the late fee
//! the company did not charge.

use chrono::NaiveDate;
use dm_core::Money;
use dm_validators::business_days::{business_days_between, roll_forward};
use rust_decimal::Decimal;

use crate::cascade::Cascade;
use crate::config::EngineConfig;
use crate::sources::Dataset;

/// Settlement status.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Status {
    /// Fully settled (a withheld tax may still be pending).
    Paid,
    /// Part of the principal is still open.
    Partial,
    /// Nothing received.
    Open,
    /// A copy of a title from another ledger.
    Duplicate,
}

impl Status {
    /// Golden-format code.
    pub fn code(self) -> &'static str {
        match self {
            Status::Paid => "paid",
            Status::Partial => "partial",
            Status::Open => "open",
            Status::Duplicate => "duplicate",
        }
    }
}

/// Flags in canonical order.
pub const FLAG_ORDER: [&str; 7] = [
    "late",
    "early",
    "overdue",
    "fine_received",
    "withholding_pending",
    "proposed",
    "intercompany",
];

/// The outcome of one title.
#[derive(Clone, Debug, PartialEq)]
pub struct TitleOutcome {
    /// Status.
    pub status: Status,
    /// Title it duplicates.
    pub duplicate_of: Option<usize>,
    /// Date of the last payment.
    pub paid_on: Option<NaiveDate>,
    /// Cash received (fines included).
    pub paid_amount: Money,
    /// Still open (pending withholding included).
    pub open_amount: Money,
    /// Due date rolled to a business day, when different.
    pub effective_due: Option<NaiveDate>,
    /// Calendar days after the due date (paid: payment date; otherwise period end).
    pub days_late: Option<i64>,
    /// Business days after the rolled due date (only when the due date was rolled).
    pub business_days_late: Option<i64>,
    /// Flags in [`FLAG_ORDER`].
    pub flags: Vec<&'static str>,
    /// Fine received.
    pub fine: Money,
    /// Tax withheld by the customer.
    pub withholding: Money,
    /// Bank lines (numbers) that paid it.
    pub lines: Vec<usize>,
    /// Uncharged late fee estimate, when the title was paid late at face value.
    pub fee_estimate: Option<Money>,
}

impl TitleOutcome {
    /// True when `flag` is set.
    pub fn has(&self, flag: &str) -> bool {
        self.flags.contains(&flag)
    }
}

/// Late fee the contract allows: fine plus interest pro rata by calendar day.
pub fn late_fee(amount: Money, days: i64, cfg: &EngineConfig) -> Money {
    let a = amount.as_decimal();
    let fee =
        a * cfg.fine_rate() + a * cfg.interest_month() * Decimal::from(days) / Decimal::from(30);
    Money::from_decimal(fee)
}

/// Derives every title's outcome.
pub fn outcomes(ds: &Dataset, cascade: &Cascade, cfg: &EngineConfig) -> Vec<TitleOutcome> {
    let period_end = ds.period.map(|p| p.end);
    ds.titles
        .iter()
        .enumerate()
        .map(|(ti, title)| {
            if let Some(of) = cascade.duplicate_of[ti] {
                return TitleOutcome {
                    status: Status::Duplicate,
                    duplicate_of: Some(of),
                    paid_on: None,
                    paid_amount: Money::ZERO,
                    open_amount: Money::ZERO,
                    effective_due: None,
                    days_late: None,
                    business_days_late: None,
                    flags: Vec::new(),
                    fine: Money::ZERO,
                    withholding: Money::ZERO,
                    lines: Vec::new(),
                    fee_estimate: None,
                };
            }
            let mut cash = Money::ZERO;
            let mut principal = Money::ZERO;
            let mut fine = Money::ZERO;
            let mut withholding = Money::ZERO;
            let mut paid_on: Option<NaiveDate> = None;
            let mut lines = Vec::new();
            let mut proposed = false;
            let mut by_document_only = true;
            for (li, outcome) in cascade.lines.iter().enumerate() {
                for a in outcome.applied.iter().filter(|a| a.title == ti) {
                    cash = cash.saturating_add(a.cash);
                    principal = principal.saturating_add(a.principal);
                    fine = fine.saturating_add(a.fine);
                    withholding = withholding.saturating_add(a.withholding);
                    let date = ds.lines[li].date;
                    paid_on = Some(paid_on.map_or(date, |d| d.max(date)));
                    lines.push(ds.lines[li].n);
                    proposed |= outcome.proposed;
                    by_document_only &= outcome.rule.starts_with("nosso_numero");
                }
            }
            let settled = principal.saturating_add(withholding);
            let status = if principal.is_zero() {
                Status::Open
            } else if settled >= title.amount {
                Status::Paid
            } else {
                Status::Partial
            };
            let rolled = roll_forward(title.due);
            let effective_due = (rolled != title.due).then_some(rolled);
            let reference_due = effective_due.unwrap_or(title.due);
            let open_amount = match status {
                Status::Paid => withholding,
                _ => title.amount.checked_sub(principal).unwrap_or(title.amount),
            };
            let mut flags = Vec::new();
            let (days_late, business_days_late) = match status {
                Status::Paid => {
                    let paid = paid_on.unwrap_or(title.due);
                    let late = paid > reference_due;
                    if late {
                        flags.push("late");
                    } else if paid < title.due {
                        flags.push("early");
                    }
                    let business = effective_due
                        .filter(|_| late)
                        .map(|e| i64::from(business_days_between(e, paid)));
                    (Some((paid - title.due).num_days()), business)
                }
                Status::Partial | Status::Open => {
                    if paid_on.is_some_and(|p| p > reference_due) {
                        flags.push("late");
                    }
                    let end = period_end.unwrap_or(title.due);
                    if end > reference_due {
                        flags.push("overdue");
                    }
                    (Some((end - title.due).num_days()), None)
                }
                Status::Duplicate => (None, None),
            };
            if fine.is_positive() {
                flags.push("fine_received");
            }
            if withholding.is_positive() {
                flags.push("withholding_pending");
            }
            if proposed {
                flags.push("proposed");
            }
            if !ds.ledgers[title.ledger].main && !lines.is_empty() {
                flags.push("intercompany");
            }
            flags.sort_by_key(|f| FLAG_ORDER.iter().position(|o| o == f));
            let fee_estimate =
                (status == Status::Paid && flags.contains(&"late") && fine.is_zero())
                    .then(|| {
                        let paid = paid_on.unwrap_or(title.due);
                        let days = (paid - reference_due).num_days();
                        let lag_tolerated = by_document_only && days <= cfg.boleto_credit_lag_days;
                        (!lag_tolerated).then(|| late_fee(title.amount, days, cfg))
                    })
                    .flatten();
            TitleOutcome {
                status,
                duplicate_of: None,
                paid_on,
                paid_amount: cash,
                open_amount,
                effective_due,
                days_late,
                business_days_late,
                flags,
                fine,
                withholding,
                lines,
                fee_estimate,
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn late_fee_is_fine_plus_pro_rata_interest() {
        let cfg = EngineConfig::default();
        // 2% + 1%/month × 1/30 on 38.640,50 = 772,81 + 12,88 = 785,69.
        assert_eq!(
            late_fee(Money::from_centavos(3_864_050), 1, &cfg),
            Money::from_centavos(78_569)
        );
        // 9 days on 6.300,00 = 126,00 + 18,90.
        assert_eq!(
            late_fee(Money::from_centavos(630_000), 9, &cfg),
            Money::from_centavos(14_490)
        );
    }
}
