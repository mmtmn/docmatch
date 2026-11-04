//! Lexical validator for probes: one `SELECT`/`WITH` statement, no comments, no second
//! statement, none of the keywords or functions that touch files, settings, extensions or
//! state. Deliberately conservative: a false positive costs a rephrase, a false negative
//! would cost the sandbox.

use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Why a statement was refused.
#[derive(Debug, Clone, PartialEq, Eq, Error, Serialize, Deserialize)]
pub enum SqlRejection {
    /// Nothing to run.
    #[error("consulta vazia")]
    Empty,
    /// Longer than the configured maximum.
    #[error("consulta com {0} caracteres excede o máximo de {1}")]
    TooLong(usize, usize),
    /// Comments are not allowed.
    #[error("comentários não são permitidos em sondas")]
    Comment,
    /// A second statement follows the first.
    #[error("apenas uma instrução por sonda")]
    MultipleStatements,
    /// The statement is not a `SELECT` or `WITH`.
    #[error("a sonda precisa começar com SELECT ou WITH (começa com {0:?})")]
    NotSelect(String),
    /// A banned keyword or function appeared outside a string literal.
    #[error("palavra proibida em sonda: {0}")]
    Banned(String),
    /// A string literal or quoted identifier was left open.
    #[error("aspas sem fechamento")]
    UnterminatedQuote,
}

/// Keywords that change state, touch files or configuration, or run other statements.
const BANNED: &[&str] = &[
    "ATTACH",
    "DETACH",
    "COPY",
    "INSTALL",
    "LOAD",
    "PRAGMA",
    "SET",
    "RESET",
    "CREATE",
    "INSERT",
    "UPDATE",
    "DELETE",
    "DROP",
    "ALTER",
    "EXPORT",
    "IMPORT",
    "CALL",
    "EXECUTE",
    "PREPARE",
    "DEALLOCATE",
    "VACUUM",
    "CHECKPOINT",
    "BEGIN",
    "COMMIT",
    "ROLLBACK",
    "TRANSACTION",
    "GRANT",
    "REVOKE",
    "TRUNCATE",
    "MERGE",
    "USE",
    "COMMENT",
    "FORCE",
    "READ_CSV",
    "READ_CSV_AUTO",
    "READ_PARQUET",
    "READ_JSON",
    "READ_JSON_AUTO",
    "READ_JSON_OBJECTS",
    "READ_NDJSON",
    "READ_NDJSON_AUTO",
    "READ_TEXT",
    "READ_BLOB",
    "READ_XLSX",
    "GLOB",
    "GETENV",
    "PARQUET_SCAN",
    "PARQUET_METADATA",
    "PARQUET_SCHEMA",
    "JSON_SCAN",
    "SNIFF_CSV",
    "QUERY",
    "QUERY_TABLE",
    "HTTPFS",
    "ICEBERG_SCAN",
    "DELTA_SCAN",
    "SQLITE_SCAN",
    "POSTGRES_SCAN",
    "MYSQL_SCAN",
    "DUCKDB_SECRETS",
    "WRITE_CSV",
    "WRITE_PARQUET",
];

/// Validates a probe. `max_chars` bounds the statement length.
pub fn validate_sql(sql: &str, max_chars: usize) -> Result<(), SqlRejection> {
    let trimmed = sql.trim();
    if trimmed.is_empty() {
        return Err(SqlRejection::Empty);
    }
    let len = trimmed.chars().count();
    if len > max_chars {
        return Err(SqlRejection::TooLong(len, max_chars));
    }
    if trimmed.contains("--") || trimmed.contains("/*") || trimmed.contains("*/") {
        return Err(SqlRejection::Comment);
    }
    let body = trimmed.trim_end_matches(';').trim_end();
    let first_word: String = body
        .trim_start_matches(['(', ' '])
        .chars()
        .take_while(char::is_ascii_alphabetic)
        .collect::<String>()
        .to_ascii_uppercase();
    if first_word != "SELECT" && first_word != "WITH" {
        return Err(SqlRejection::NotSelect(first_word));
    }
    // Tokenize: single-quoted strings are data and skipped; bare words and double-quoted
    // identifiers are checked against the banned list; a `;` outside a string ends the
    // statement and anything after it is a second statement.
    let chars: Vec<char> = body.chars().collect();
    let mut i = 0;
    let mut word = String::new();
    let check = |word: &mut String| -> Result<(), SqlRejection> {
        if word.is_empty() {
            return Ok(());
        }
        let upper = word.to_ascii_uppercase();
        word.clear();
        if BANNED.contains(&upper.as_str()) {
            return Err(SqlRejection::Banned(upper));
        }
        Ok(())
    };
    while i < chars.len() {
        let c = chars[i];
        match c {
            '\'' => {
                check(&mut word)?;
                i += 1;
                loop {
                    match chars.get(i) {
                        None => return Err(SqlRejection::UnterminatedQuote),
                        Some('\'') if chars.get(i + 1) == Some(&'\'') => i += 2,
                        Some('\'') => break,
                        Some(_) => i += 1,
                    }
                }
            }
            '"' => {
                check(&mut word)?;
                i += 1;
                let mut quoted = String::new();
                loop {
                    match chars.get(i) {
                        None => return Err(SqlRejection::UnterminatedQuote),
                        Some('"') if chars.get(i + 1) == Some(&'"') => {
                            quoted.push('"');
                            i += 2;
                        }
                        Some('"') => break,
                        Some(ch) => {
                            quoted.push(*ch);
                            i += 1;
                        }
                    }
                }
                let mut q = quoted.trim().to_string();
                check(&mut q)?;
            }
            ';' => {
                check(&mut word)?;
                if chars[i + 1..].iter().any(|c| !c.is_whitespace()) {
                    return Err(SqlRejection::MultipleStatements);
                }
            }
            '$' => {
                // dollar-quoted strings ($$...$$ or $tag$...$tag$) are refused outright
                return Err(SqlRejection::Banned("$".to_string()));
            }
            c if c.is_alphanumeric() || c == '_' => word.push(c),
            _ => check(&mut word)?,
        }
        i += 1;
    }
    check(&mut word)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_plain_selects() {
        for ok in [
            "SELECT 1",
            "select * from t where x = 'ATTACH'",
            "WITH x AS (SELECT 1) SELECT * FROM x",
            "SELECT 1;",
            "(SELECT 1)",
            "SELECT \"weird col\" FROM t",
            "SELECT 'it''s' AS s",
            "SELECT * FROM t WHERE memo LIKE '%COPY%'",
        ] {
            assert_eq!(validate_sql(ok, 1000), Ok(()), "{ok}");
        }
    }

    #[test]
    fn rejects_shapes() {
        assert_eq!(validate_sql("", 10), Err(SqlRejection::Empty));
        assert_eq!(
            validate_sql("SELECT 1", 3),
            Err(SqlRejection::TooLong(8, 3))
        );
        assert_eq!(
            validate_sql("SELECT 1 -- x", 100),
            Err(SqlRejection::Comment)
        );
        assert_eq!(
            validate_sql("/* a */ SELECT 1", 100),
            Err(SqlRejection::Comment)
        );
        assert_eq!(
            validate_sql("SELECT 1; SELECT 2", 100),
            Err(SqlRejection::MultipleStatements)
        );
        assert_eq!(
            validate_sql("DROP TABLE t", 100),
            Err(SqlRejection::NotSelect("DROP".into()))
        );
        assert_eq!(
            validate_sql("SELECT 'abc", 100),
            Err(SqlRejection::UnterminatedQuote)
        );
        assert_eq!(
            validate_sql("SELECT \"abc", 100),
            Err(SqlRejection::UnterminatedQuote)
        );
        assert_eq!(
            validate_sql("SELECT $$ATTACH$$", 100),
            Err(SqlRejection::Banned("$".into()))
        );
    }

    #[test]
    fn rejects_banned_words_bare_and_quoted() {
        assert_eq!(
            validate_sql("SELECT * FROM read_csv('x')", 100),
            Err(SqlRejection::Banned("READ_CSV".into()))
        );
        assert_eq!(
            validate_sql("SELECT * FROM \"read_csv\"('x')", 100),
            Err(SqlRejection::Banned("READ_CSV".into()))
        );
        assert_eq!(
            validate_sql("SELECT * FROM \"Read_Csv\"('x')", 100),
            Err(SqlRejection::Banned("READ_CSV".into()))
        );
        assert_eq!(
            validate_sql("WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", 100),
            Err(SqlRejection::Banned("INSERT".into()))
        );
        assert_eq!(
            validate_sql("SELECT getenv('HOME')", 100),
            Err(SqlRejection::Banned("GETENV".into()))
        );
    }
}
