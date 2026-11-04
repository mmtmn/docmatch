// `gen` and `check`: produce every generated file from limits.yaml and errors.yaml, write
// them to packages/contracts/gen, or regenerate into a temp dir and diff against the
// committed copies (so a stale commit fails CI instead of drifting).
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { emitErrorsGo, emitErrorsRs, emitErrorsTs } from "./emit/errors.mjs";
import { emitLimitsGo, emitLimitsKt, emitLimitsRs, emitLimitsSwift, emitLimitsTs } from "./emit/limits.mjs";
import { loadErrors, loadLimits } from "./load.mjs";
import { paths } from "./paths.mjs";

/** File names written under packages/contracts/gen, in a stable order. */
export const GENERATED_FILES = Object.freeze([
  "limits.ts",
  "limits.go",
  "limits.rs",
  "Limits.kt",
  "Limits.swift",
  "errors.ts",
  "errors.go",
  "errors.rs",
]);

/**
 * Generates every file in memory.
 * @returns {Map<string, string>} file name -> content
 */
export function generateAll({ limits = loadLimits(), errors = loadErrors() } = {}) {
  const out = new Map();
  out.set("limits.ts", emitLimitsTs(limits));
  out.set("limits.go", emitLimitsGo(limits));
  out.set("limits.rs", emitLimitsRs(limits));
  out.set("Limits.kt", emitLimitsKt(limits));
  out.set("Limits.swift", emitLimitsSwift(limits));
  out.set("errors.ts", emitErrorsTs(errors));
  out.set("errors.go", emitErrorsGo(errors));
  out.set("errors.rs", emitErrorsRs(errors));
  for (const name of GENERATED_FILES) {
    if (!out.has(name)) throw new Error(`gerador sem saída para ${name}`);
  }
  return out;
}

export function writeAll(dir, files) {
  mkdirSync(dir, { recursive: true });
  const written = [];
  for (const [name, content] of files) {
    const file = path.join(dir, name);
    const current = existsSync(file) ? readFileSync(file, "utf8") : null;
    if (current !== content) {
      writeFileSync(file, content, "utf8");
      written.push(name);
    }
  }
  return written;
}

/**
 * Compares the files in `candidateDir` (fresh output) with the ones in `committedDir`.
 * @returns {{ stale: string[], missing: string[] }}
 */
export function diffDirs(candidateDir, committedDir) {
  const stale = [];
  const missing = [];
  for (const name of GENERATED_FILES) {
    const committed = path.join(committedDir, name);
    if (!existsSync(committed)) {
      missing.push(name);
      continue;
    }
    const fresh = readFileSync(path.join(candidateDir, name), "utf8");
    if (readFileSync(committed, "utf8") !== fresh) stale.push(name);
  }
  return { stale, missing };
}

/** Regenerates into a temp dir and compares with `genDir`; the temp dir is always removed. */
export function checkGenerated(genDir = paths.genDir) {
  const files = generateAll();
  const tmp = mkdtempSync(path.join(os.tmpdir(), "docmatch-codegen-"));
  try {
    writeAll(tmp, files);
    return diffDirs(tmp, genDir);
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}
