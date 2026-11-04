//! Escape corpus: every attempt to leave the sandbox (files, settings, extensions, state
//! changes, second statements, obfuscation) is refused, by the validator or by DuckDB itself.

use dm_sandbox::{Sandbox, SandboxError};

/// Attempts the lexical validator must refuse.
const REFUSED_BY_VALIDATOR: &[&str] = &[
    "ATTACH 'other.db' AS other",
    "COPY (SELECT 1) TO 'out.csv'",
    "INSTALL httpfs",
    "LOAD httpfs",
    "PRAGMA database_list",
    "SET enable_external_access=true",
    "RESET memory_limit",
    "CREATE TABLE x AS SELECT 1",
    "CREATE OR REPLACE VIEW v AS SELECT 1",
    "INSERT INTO t VALUES (1)",
    "UPDATE t SET a = 1",
    "DELETE FROM t",
    "DROP TABLE t",
    "ALTER TABLE t ADD COLUMN x INT",
    "EXPORT DATABASE 'dir'",
    "IMPORT DATABASE 'dir'",
    "CALL pragma_version()",
    "PREPARE p AS SELECT 1",
    "EXECUTE p",
    "BEGIN TRANSACTION",
    "CHECKPOINT",
    "VACUUM",
    "USE memory",
    "SELECT * FROM read_csv('secrets.csv')",
    "SELECT * FROM read_csv_auto('secrets.csv')",
    "SELECT * FROM read_parquet('data.parquet')",
    "SELECT * FROM read_json('data.json')",
    "SELECT * FROM read_text('hosts')",
    "SELECT * FROM read_blob('hosts')",
    "SELECT * FROM glob('*')",
    "SELECT getenv('HOME')",
    "SELECT * FROM sniff_csv('x.csv')",
    "SELECT * FROM parquet_scan('x.parquet')",
    "SELECT * FROM query('ATTACH ''x.db''')",
    "SELECT * FROM query_table('t')",
    "SELECT 1; SELECT 2",
    "SELECT 1; DROP TABLE t",
    "SELECT 1; ATTACH 'x.db'",
    "select * from \"read_csv\"('secrets.csv')",
    "select * from \"READ_PARQUET\"('x')",
    "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x",
    "WITH x AS (SELECT 1) UPDATE t SET a = 1",
    "SELECT 1 -- ; ATTACH 'x.db'",
    "/* innocent */ SELECT * FROM read_csv('x')",
    "SELECT $$ATTACH 'x'$$",
    "FROM t SELECT *",
    "DESCRIBE t",
    "SHOW TABLES",
    "SUMMARIZE t",
    "SELECT * FROM 'x.csv'; LOAD httpfs",
    "",
    "   ;   ",
];

/// Attempts the validator lets through but DuckDB refuses with external access disabled.
const REFUSED_AT_RUNTIME: &[&str] = &[
    "SELECT * FROM 'secrets.csv'",
    "SELECT * FROM 'data.parquet'",
    "SELECT * FROM 'data.json'",
    "SELECT * FROM '/etc/hostname'",
    "SELECT * FROM 'x.db'",
    "SELECT * FROM nonexistent_table_zz",
];

#[test]
fn every_escape_attempt_is_refused() {
    let sb = Sandbox::with_defaults().unwrap();
    assert!(REFUSED_BY_VALIDATOR.len() + REFUSED_AT_RUNTIME.len() >= 25);
    for sql in REFUSED_BY_VALIDATOR {
        let r = sb.run_probe(sql);
        assert!(
            matches!(r, Err(SandboxError::Rejected(_))),
            "validator should refuse {sql:?}, got {r:?}"
        );
    }
    for sql in REFUSED_AT_RUNTIME {
        let r = sb.run_probe(sql);
        assert!(
            matches!(r, Err(SandboxError::Query(_))),
            "duckdb should refuse {sql:?}, got {r:?}"
        );
    }
    // nothing above changed the hardening
    assert_eq!(sb.setting("enable_external_access").unwrap(), "false");
    assert_eq!(sb.setting("lock_configuration").unwrap(), "true");
    // and ordinary probes still run
    assert_eq!(
        sb.run_probe("SELECT 1 + 1").unwrap().rows[0][0],
        serde_json::json!(2)
    );
}

#[test]
fn string_literals_are_data_not_commands() {
    let sb = Sandbox::with_defaults().unwrap();
    let r = sb
        .run_probe("SELECT 'ATTACH ''x.db''' AS texto, 'COPY' AS k, '; DROP TABLE t' AS j")
        .unwrap();
    assert_eq!(r.rows[0][0], serde_json::json!("ATTACH 'x.db'"));
    assert_eq!(r.rows[0][1], serde_json::json!("COPY"));
}
