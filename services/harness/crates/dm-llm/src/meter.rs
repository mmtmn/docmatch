//! Token and cost metering per call, with run totals.

use std::str::FromStr;

use dm_core::Decimal;
use serde::{Deserialize, Serialize};

/// Tokens and cost reported by the provider for one call.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Usage {
    /// Prompt tokens.
    pub prompt_tokens: u64,
    /// Completion tokens.
    pub completion_tokens: u64,
    /// Total tokens.
    pub total_tokens: u64,
    /// Cost in USD when the provider reports it (OpenRouter with usage accounting).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cost_usd: Option<Decimal>,
}

impl Usage {
    /// Reads an OpenAI-style `usage` object. The cost is parsed from the number's text so no
    /// float conversion happens.
    pub fn from_openai_json(usage: &serde_json::Value) -> Usage {
        let int = |k: &str| {
            usage
                .get(k)
                .and_then(serde_json::Value::as_u64)
                .unwrap_or(0)
        };
        let cost_usd = usage.get("cost").and_then(|c| match c {
            serde_json::Value::Number(n) => Decimal::from_str(&n.to_string()).ok(),
            serde_json::Value::String(s) => Decimal::from_str(s).ok(),
            _ => None,
        });
        let prompt_tokens = int("prompt_tokens");
        let completion_tokens = int("completion_tokens");
        let total_tokens = usage
            .get("total_tokens")
            .and_then(serde_json::Value::as_u64)
            .unwrap_or(prompt_tokens + completion_tokens);
        Usage {
            prompt_tokens,
            completion_tokens,
            total_tokens,
            cost_usd,
        }
    }
}

/// Metering of one call.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CallMeter {
    /// Role requested.
    pub role: String,
    /// Model that answered.
    pub model: String,
    /// Usage.
    pub usage: Usage,
    /// True when served from the response cache (no network, no cost).
    pub cached: bool,
    /// HTTP attempts made (0 when cached).
    pub attempts: u32,
    /// Wall time.
    pub latency_ms: u64,
}

/// Totals over a run.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct MeterTotals {
    /// Calls recorded.
    pub calls: u64,
    /// Calls served from cache.
    pub cached_calls: u64,
    /// Prompt tokens (non-cached calls).
    pub prompt_tokens: u64,
    /// Completion tokens (non-cached calls).
    pub completion_tokens: u64,
    /// Total tokens (non-cached calls).
    pub total_tokens: u64,
    /// Cost in USD (non-cached calls with a reported cost).
    pub cost_usd: Decimal,
}

/// Accumulates [`CallMeter`]s.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Meter {
    calls: Vec<CallMeter>,
}

impl Meter {
    /// Records a call.
    pub fn record(&mut self, call: CallMeter) {
        self.calls.push(call);
    }

    /// Every call so far.
    pub fn calls(&self) -> &[CallMeter] {
        &self.calls
    }

    /// Totals; cached calls count as calls but add no tokens or cost.
    pub fn totals(&self) -> MeterTotals {
        let mut t = MeterTotals::default();
        for c in &self.calls {
            t.calls += 1;
            if c.cached {
                t.cached_calls += 1;
                continue;
            }
            t.prompt_tokens += c.usage.prompt_tokens;
            t.completion_tokens += c.usage.completion_tokens;
            t.total_tokens += c.usage.total_tokens;
            if let Some(cost) = c.usage.cost_usd {
                t.cost_usd += cost;
            }
        }
        t
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn parses_usage_and_totals() {
        let u = Usage::from_openai_json(
            &json!({"prompt_tokens": 10, "completion_tokens": 5, "cost": 0.000_123}),
        );
        assert_eq!(u.total_tokens, 15);
        assert_eq!(u.cost_usd, Some(Decimal::from_str("0.000123").unwrap()));
        let u2 = Usage::from_openai_json(
            &json!({"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 7, "cost": "0.5"}),
        );
        assert_eq!(u2.total_tokens, 7);
        assert_eq!(u2.cost_usd, Some(Decimal::from_str("0.5").unwrap()));
        assert_eq!(Usage::from_openai_json(&json!({})), Usage::default());

        let mut m = Meter::default();
        m.record(CallMeter {
            role: "r".into(),
            model: "m".into(),
            usage: u.clone(),
            cached: false,
            attempts: 1,
            latency_ms: 5,
        });
        m.record(CallMeter {
            role: "r".into(),
            model: "m".into(),
            usage: u,
            cached: true,
            attempts: 0,
            latency_ms: 0,
        });
        let t = m.totals();
        assert_eq!(t.calls, 2);
        assert_eq!(t.cached_calls, 1);
        assert_eq!(t.total_tokens, 15);
        assert_eq!(t.cost_usd, Decimal::from_str("0.000123").unwrap());
        assert_eq!(m.calls().len(), 2);
    }
}
