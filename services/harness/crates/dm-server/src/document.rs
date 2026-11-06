//! The editable reconciliation output, kept separately from diagnostic findings.
use dm_recon::Run;
use prost_types::{value::Kind, ListValue, Struct, Value};
use serde_json::json;

use crate::convert::SourceIndex;

fn source_id(file: &str, index: &SourceIndex) -> String {
    index
        .get(file)
        .or_else(|| {
            file.split_once('/')
                .and_then(|(archive, _)| index.get(archive))
        })
        .cloned()
        .unwrap_or_default()
}

fn proto(value: serde_json::Value) -> Value {
    let kind = match value {
        serde_json::Value::Null => Kind::NullValue(0),
        serde_json::Value::Bool(v) => Kind::BoolValue(v),
        serde_json::Value::Number(v) => Kind::NumberValue(v.as_f64().unwrap_or_default()),
        serde_json::Value::String(v) => Kind::StringValue(v),
        serde_json::Value::Array(v) => Kind::ListValue(ListValue {
            values: v.into_iter().map(proto).collect(),
        }),
        serde_json::Value::Object(v) => Kind::StructValue(Struct {
            fields: v.into_iter().map(|(k, v)| (k, proto(v))).collect(),
        }),
    };
    Value { kind: Some(kind) }
}

/// A source-backed ledger: only confirmed allocations reduce the open balance. A duplicate
/// stays visible, with its original value and the reference that removes double counting.
pub fn build(run: &Run, index: &SourceIndex) -> Struct {
    let mut original = 0i64;
    let mut duplicate = 0i64;
    let mut principal = 0i64;
    let mut cash = 0i64;
    let mut pending = 0i64;
    let mut fines = 0i64;
    let mut withholding = 0i64;
    let mut rows = Vec::new();
    for (ti, title) in run.dataset.titles.iter().enumerate() {
        let ledger = &run.dataset.ledgers[title.ledger];
        let outcome = &run.outcomes[ti];
        let mut received = 0i64;
        let mut applied = 0i64;
        let mut proposed = 0i64;
        let mut extra = 0i64;
        let mut tax = 0i64;
        let mut dates = Vec::new();
        let mut bank_rows = Vec::new();
        for (li, line) in run.cascade.lines.iter().enumerate() {
            for allocation in line.applied.iter().filter(|a| a.title == ti) {
                bank_rows.push(run.dataset.lines[li].n);
                if line.proposed {
                    proposed = proposed.saturating_add(allocation.principal.centavos());
                } else {
                    received = received.saturating_add(allocation.cash.centavos());
                    applied = applied.saturating_add(allocation.principal.centavos());
                    extra = extra.saturating_add(allocation.fine.centavos());
                    tax = tax.saturating_add(allocation.withholding.centavos());
                    dates.push(run.dataset.lines[li].date);
                }
            }
        }
        let face = title.amount.centavos();
        let duplicate_of = outcome
            .duplicate_of
            .map(|i| run.dataset.titles[i].id.clone());
        let is_duplicate = duplicate_of.is_some();
        let open = if is_duplicate {
            0
        } else {
            face.saturating_sub(applied).max(0)
        };
        let status = if is_duplicate {
            "duplicate"
        } else if proposed > 0 {
            "review"
        } else if tax > 0 {
            "withholding"
        } else if open == 0 {
            "paid"
        } else if applied > 0 {
            "partial"
        } else {
            "open"
        };
        original = original.saturating_add(face);
        if is_duplicate {
            duplicate = duplicate.saturating_add(face);
        }
        principal = principal.saturating_add(applied);
        cash = cash.saturating_add(received);
        pending = pending.saturating_add(proposed);
        fines = fines.saturating_add(extra);
        withholding = withholding.saturating_add(tax);
        rows.push(json!({
            "id": title.id, "source_id": source_id(&ledger.file,index), "source_name": ledger.file,
            "source_row": title.row + 1, "reference": title.reference, "customer": title.customer,
            "tax_id": title.tax_id.clone().unwrap_or_default(), "due_date": title.due.to_string(),
            "paid_on": dates.iter().max().map(ToString::to_string).unwrap_or_default(),
            "original_amount": face, "received_amount": received, "applied_principal": applied,
            "open_amount": open, "proposed_amount": proposed, "fine_received": extra,
            "withholding_pending": tax, "status": status, "duplicate_of": duplicate_of.unwrap_or_default(),
            "bank_rows": bank_rows, "flags": outcome.flags,
        }));
    }
    let bank_file = run.result.sources.get("bank").cloned().unwrap_or_default();
    let bank: Vec<_> = run.result.bank_lines.iter().map(|line| json!({
        "row": line.n, "date": line.date.to_string(), "description": line.description,
        "amount": if line.side == "D" { -line.amount.0 } else { line.amount.0 },
        "kind": line.kind, "proposed": line.proposed, "matched_ids": line.matched.iter().map(dm_recon::FlexStr::as_str).collect::<Vec<_>>(),
        "rule": line.rule, "source_id": source_id(&bank_file,index), "source_name": bank_file,
    })).collect();
    let fees = run
        .result
        .bank_lines
        .iter()
        .filter(|l| l.kind == "fee")
        .map(|l| l.amount.0)
        .fold(0i64, i64::saturating_add);
    let unapplied = run
        .result
        .bank_lines
        .iter()
        .filter(|l| l.side == "C" && (l.kind == "unapplied" || l.proposed))
        .map(|l| l.amount.0)
        .fold(0i64, i64::saturating_add);
    let open = original.saturating_sub(duplicate).saturating_sub(principal);
    let doc = json!({
        "version": 1, "title": if rows.is_empty() { "Conciliação bancária" } else { "Conciliação de recebíveis" },
        "currency": "BRL", "period_start": run.dataset.period.map(|p| p.start.to_string()).unwrap_or_default(),
        "period_end": run.dataset.period.map(|p| p.end.to_string()).unwrap_or_default(),
        "receivables": rows, "bank_entries": bank,
        "totals": { "original_amount": original, "duplicate_excluded": duplicate,
            "received_confirmed": cash, "principal_confirmed": principal, "open_balance": open,
            "proposed_amount": pending, "projected_open_balance": open.saturating_sub(pending),
            "unapplied_cash": unapplied, "bank_fees": fees, "fines_received": fines, "withholding_pending": withholding },
    });
    match proto(doc).kind {
        Some(Kind::StructValue(value)) => value,
        _ => unreachable!("document is an object"),
    }
}
