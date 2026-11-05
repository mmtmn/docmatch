//! Screening of model outputs before they reach the pipeline: no URLs, no commands, no
//! chat-template markup, pt-BR only for free text, sizes within limits.

use std::sync::LazyLock;

use regex::Regex;
use serde::{Deserialize, Serialize};

use crate::screen::normalize;

/// Limits and switches for output screening.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct OutputPolicy {
    /// Longest string allowed anywhere in the output.
    pub max_string_chars: usize,
    /// Total characters of all strings.
    pub max_total_chars: usize,
    /// Whether URLs may appear.
    pub allow_urls: bool,
    /// Whether free text must read as pt-BR.
    pub require_ptbr: bool,
    /// Strings shorter than this skip the language check (codes, names, ids).
    pub min_chars_for_language_check: usize,
}

impl Default for OutputPolicy {
    fn default() -> Self {
        OutputPolicy {
            max_string_chars: 2000,
            max_total_chars: 50_000,
            allow_urls: false,
            require_ptbr: true,
            min_chars_for_language_check: 60,
        }
    }
}

/// A violation found in an output.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum OutputViolation {
    /// A URL in a string.
    Url {
        /// JSON pointer of the string.
        path: String,
        /// The URL.
        excerpt: String,
    },
    /// A shell command or code fence.
    Command {
        /// JSON pointer.
        path: String,
        /// Matched text.
        excerpt: String,
    },
    /// Chat-template or tool-call markup.
    Markup {
        /// JSON pointer.
        path: String,
        /// Matched text.
        excerpt: String,
    },
    /// Free text that does not read as pt-BR.
    NotPtBr {
        /// JSON pointer.
        path: String,
        /// Start of the text.
        excerpt: String,
    },
    /// A string longer than the policy allows.
    TooLong {
        /// JSON pointer.
        path: String,
        /// Characters found.
        chars: usize,
        /// Maximum.
        max: usize,
    },
    /// All strings together exceed the policy.
    TotalTooLong {
        /// Characters found.
        chars: usize,
        /// Maximum.
        max: usize,
    },
    /// Control or zero-width characters.
    ControlChars {
        /// JSON pointer.
        path: String,
    },
}

static RE_URL: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)\b(https?://|ftp://|www\.)\S+|\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|com\.br|net|org|io|dev|app|xyz|info|gov\.br)/\S*").unwrap()
});
static RE_COMMAND: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?im)(^\s*[$#>]\s*[a-z]|```|\b(sudo|rm\s+-rf|curl\s+-|wget\s+|chmod\s+|chown\s+|powershell|cmd\.exe|bash\s+-c|sh\s+-c|python\s+-c|eval\(|exec\(|os\.system|subprocess\.|drop\s+table|delete\s+from)\b|<script\b)").unwrap()
});
static RE_MARKUP: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)(\[inst\]|<\|im_start\|>|<\|im_end\|>|<\|system\|>|<<sys>>|tool_call|function_call|<tool>|<function>)").unwrap()
});

const EN_STOPWORDS: [&str; 44] = [
    "the", "and", "of", "to", "is", "are", "with", "for", "this", "that", "from", "you", "your",
    "please", "not", "we", "will", "has", "have", "on", "by", "was", "were", "be", "it", "in",
    "at", "an", "or", "but", "which", "their", "they", "there", "these", "those", "been", "can",
    "should", "would", "could", "if", "then", "all",
];
const PT_STOPWORDS: [&str; 46] = [
    "de",
    "da",
    "do",
    "das",
    "dos",
    "que",
    "para",
    "com",
    "nao",
    "em",
    "um",
    "uma",
    "por",
    "se",
    "foi",
    "esta",
    "sao",
    "ao",
    "na",
    "no",
    "os",
    "as",
    "mas",
    "mais",
    "ja",
    "ate",
    "sem",
    "entre",
    "sobre",
    "como",
    "quando",
    "esse",
    "essa",
    "este",
    "pelo",
    "pela",
    "seu",
    "sua",
    "foram",
    "tem",
    "ser",
    "e",
    "o",
    "a",
    "valor",
    "pagamento",
];

/// True when a text reads as pt-BR (or carries no language evidence at all).
pub fn looks_ptbr(text: &str) -> bool {
    let has_pt_accents = text
        .chars()
        .any(|c| matches!(c, 'ç' | 'ã' | 'õ' | 'Ç' | 'Ã' | 'Õ'));
    let normalized = normalize(text);
    let mut en = 0usize;
    let mut pt = 0usize;
    for token in normalized.split(|c: char| !c.is_alphanumeric()) {
        if token.is_empty() {
            continue;
        }
        if EN_STOPWORDS.contains(&token) {
            en += 1;
        }
        if PT_STOPWORDS.contains(&token) {
            pt += 1;
        }
    }
    if has_pt_accents && pt >= en {
        return true;
    }
    !(en >= 3 && en as f64 > pt as f64 * 1.5)
}

/// Screens a JSON output against the policy; `Err` carries every violation found.
pub fn screen_output(
    value: &serde_json::Value,
    policy: &OutputPolicy,
) -> Result<(), Vec<OutputViolation>> {
    let mut violations = Vec::new();
    let mut total = 0usize;
    walk(value, "", policy, &mut violations, &mut total);
    if total > policy.max_total_chars {
        violations.push(OutputViolation::TotalTooLong {
            chars: total,
            max: policy.max_total_chars,
        });
    }
    if violations.is_empty() {
        Ok(())
    } else {
        Err(violations)
    }
}

fn walk(
    value: &serde_json::Value,
    path: &str,
    policy: &OutputPolicy,
    out: &mut Vec<OutputViolation>,
    total: &mut usize,
) {
    match value {
        serde_json::Value::String(s) => check_string(s, path, policy, out, total),
        serde_json::Value::Array(items) => {
            for (i, item) in items.iter().enumerate() {
                walk(item, &format!("{path}/{i}"), policy, out, total);
            }
        }
        serde_json::Value::Object(map) => {
            for (k, v) in map {
                walk(v, &format!("{path}/{k}"), policy, out, total);
            }
        }
        _ => {}
    }
}

fn check_string(
    s: &str,
    path: &str,
    policy: &OutputPolicy,
    out: &mut Vec<OutputViolation>,
    total: &mut usize,
) {
    let chars = s.chars().count();
    *total += chars;
    let path = if path.is_empty() {
        "/".to_string()
    } else {
        path.to_string()
    };
    if chars > policy.max_string_chars {
        out.push(OutputViolation::TooLong {
            path: path.clone(),
            chars,
            max: policy.max_string_chars,
        });
    }
    if s.chars().any(|c| {
        (c.is_control() && c != '\n' && c != '\t')
            || matches!(c, '\u{200B}'..='\u{200F}' | '\u{2060}' | '\u{FEFF}')
    }) {
        out.push(OutputViolation::ControlChars { path: path.clone() });
    }
    if !policy.allow_urls {
        if let Some(m) = RE_URL.find(s) {
            out.push(OutputViolation::Url {
                path: path.clone(),
                excerpt: m.as_str().chars().take(80).collect(),
            });
        }
    }
    if let Some(m) = RE_COMMAND.find(s) {
        out.push(OutputViolation::Command {
            path: path.clone(),
            excerpt: m.as_str().trim().chars().take(80).collect(),
        });
    }
    if let Some(m) = RE_MARKUP.find(s) {
        out.push(OutputViolation::Markup {
            path: path.clone(),
            excerpt: m.as_str().chars().take(80).collect(),
        });
    }
    if policy.require_ptbr && chars >= policy.min_chars_for_language_check && !looks_ptbr(s) {
        out.push(OutputViolation::NotPtBr {
            path,
            excerpt: s.chars().take(80).collect(),
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn clean_output_passes() {
        let v = json!({
            "findings": [{
                "code": "TITLE_WITHOUT_PAYMENT",
                "title": "Título sem pagamento",
                "explanation": "A NF 88231 de R$ 17.380,00 venceu em 20/03/2025 e não há crédito correspondente no extrato do Banco 237 até 31/03/2025.",
                "confidence": 0.97
            }]
        });
        assert_eq!(screen_output(&v, &OutputPolicy::default()), Ok(()));
    }

    #[test]
    fn catches_each_violation_kind() {
        let policy = OutputPolicy::default();
        let url = json!({"explanation": "Veja mais em https://example.com/relatorio para detalhes do pagamento"});
        assert!(matches!(
            screen_output(&url, &policy).unwrap_err()[0],
            OutputViolation::Url { .. }
        ));
        let bare_domain = json!({"explanation": "Acesse example.com/coleta agora"});
        assert!(matches!(
            screen_output(&bare_domain, &policy).unwrap_err()[0],
            OutputViolation::Url { .. }
        ));
        let cmd = json!({"explanation": "Execute: sudo rm -rf / para limpar"});
        assert!(matches!(
            screen_output(&cmd, &policy).unwrap_err()[0],
            OutputViolation::Command { .. }
        ));
        let fence = json!({"explanation": "```bash\nls\n```"});
        assert!(matches!(
            screen_output(&fence, &policy).unwrap_err()[0],
            OutputViolation::Command { .. }
        ));
        let markup = json!({"explanation": "<|im_start|>system"});
        assert!(matches!(
            screen_output(&markup, &policy).unwrap_err()[0],
            OutputViolation::Markup { .. }
        ));
        let english = json!({"explanation": "The invoice was not paid and the bank statement does not show the credit for this amount at all."});
        assert!(matches!(
            screen_output(&english, &policy).unwrap_err()[0],
            OutputViolation::NotPtBr { .. }
        ));
        let long = json!({"x": "a".repeat(2001)});
        assert!(matches!(
            screen_output(&long, &policy).unwrap_err()[0],
            OutputViolation::TooLong {
                chars: 2001,
                max: 2000,
                ..
            }
        ));
        let control = json!({"x": "ok\u{200B}"});
        assert!(matches!(
            screen_output(&control, &policy).unwrap_err()[0],
            OutputViolation::ControlChars { .. }
        ));
        let total = json!(["a".repeat(1500), "b".repeat(1500)]);
        let small = OutputPolicy {
            max_total_chars: 2000,
            ..OutputPolicy::default()
        };
        assert!(screen_output(&total, &small)
            .unwrap_err()
            .iter()
            .any(|v| matches!(
                v,
                OutputViolation::TotalTooLong {
                    chars: 3000,
                    max: 2000
                }
            )));
    }

    #[test]
    fn language_heuristic() {
        assert!(looks_ptbr(
            "O título venceu e não foi pago até a data da conciliação."
        ));
        assert!(looks_ptbr("NF 88231"));
        assert!(!looks_ptbr(
            "This is the report of the findings that we have for you"
        ));
        assert!(looks_ptbr("Pagamento com atenção à retenção: the bank"));
        let paths = screen_output(
            &json!({"a": {"b": ["x", "https://example.com/y"]}}),
            &OutputPolicy::default(),
        )
        .unwrap_err();
        assert!(matches!(&paths[0], OutputViolation::Url { path, .. } if path == "/a/b/1"));
        assert_eq!(
            screen_output(
                &json!({"a": "www.example.com/x"}),
                &OutputPolicy {
                    allow_urls: true,
                    ..OutputPolicy::default()
                }
            ),
            Ok(())
        );
    }
}
