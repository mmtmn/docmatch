//! Golden cases: the public twin always, the private Vantara case when DM_VANTARA_PATH is set.

use std::path::{Path, PathBuf};

use dm_eval::{find_twin_dir, run_golden};
use dm_recon::EngineConfig;

fn twin() -> PathBuf {
    find_twin_dir(Path::new(
        &std::env::var("CARGO_MANIFEST_DIR")
            .expect("cargo define CARGO_MANIFEST_DIR ao rodar os testes"),
    ))
    .unwrap_or_else(|| panic!("testdata/golden/vantara-twin não encontrado"))
}

#[test]
fn twin_scores_100_and_is_deterministic() {
    let run = run_golden(&twin(), &EngineConfig::default(), 10).unwrap_or_else(|e| panic!("{e}"));
    assert!(
        run.scorecard.perfect(),
        "gêmeo:\n{}",
        run.scorecard.report()
    );
    assert!(run.identical, "dez execuções deram resultados diferentes");
    assert_eq!(
        run.scorecard.summary(),
        "18/18 linhas, 17/17 títulos, 23/23 achados"
    );
}

#[test]
fn private_case_scores_100_when_available() {
    let Ok(path) = std::env::var("DM_VANTARA_PATH") else {
        eprintln!("skipped: DM_VANTARA_PATH não definido");
        return;
    };
    let run = run_golden(Path::new(&path), &EngineConfig::default(), 10)
        .unwrap_or_else(|e| panic!("{e}"));
    // Only counts and kinds go to the output: never values from the private case.
    assert!(
        run.scorecard.perfect(),
        "caso privado: {}",
        run.scorecard.summary()
    );
    assert!(run.identical, "dez execuções deram resultados diferentes");
}
