// `smoke`: compiles the generated files with whatever toolchains are installed user-locally.
// Missing toolchains skip with a warning (Kotlin and Swift are usually absent on the CI box);
// present ones must succeed: gofmt/go vet, rustfmt/rustc, node --check for TypeScript.
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { paths } from "./paths.mjs";

export function findBin(name) {
  for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, name);
    if (existsSync(candidate)) return candidate;
  }
  return null;
}

function exec(bin, args, options = {}) {
  const r = spawnSync(bin, args, {
    encoding: "utf8",
    env: { ...process.env, NO_COLOR: "1", GOTOOLCHAIN: "local", GOFLAGS: "-mod=mod", ...options.env },
    cwd: options.cwd,
    maxBuffer: 16 * 1024 * 1024,
  });
  if (r.error) return { ok: false, output: String(r.error.message ?? r.error) };
  return { ok: r.status === 0, output: `${r.stdout ?? ""}${r.stderr ?? ""}`.trim() };
}

/**
 * @returns {Array<{ tool: string, ok: boolean, skipped: boolean, output: string }>}
 */
export function smoke(genDir = paths.genDir) {
  const results = [];
  const tmp = mkdtempSync(path.join(os.tmpdir(), "docmatch-smoke-"));
  const push = (tool, r) => results.push({ tool, ok: r.ok, skipped: false, output: r.output });
  const skip = (tool, why) => results.push({ tool, ok: true, skipped: true, output: why });
  try {
    for (const file of ["limits.ts", "errors.ts"]) {
      push(`node --check ${file}`, exec(process.execPath, ["--check", path.join(genDir, file)]));
    }

    const go = findBin("go");
    const gofmt = findBin("gofmt");
    if (gofmt) {
      const r = exec(gofmt, ["-l", path.join(genDir, "limits.go"), path.join(genDir, "errors.go")]);
      push("gofmt -l", { ok: r.ok && r.output === "", output: r.output || "(formatado)" });
    } else {
      skip("gofmt", "gofmt não encontrado; formatação do Go não verificada");
    }
    if (go) {
      push("go vet", exec(go, ["vet", "./..."], { cwd: genDir }));
    } else {
      skip("go vet", "go não encontrado; limits.go e errors.go não compilados");
    }

    const rustc = findBin("rustc");
    const rustfmt = findBin("rustfmt");
    for (const file of ["limits.rs", "errors.rs"]) {
      if (rustfmt) {
        push(`rustfmt --check ${file}`, exec(rustfmt, ["--check", "--edition", "2021", path.join(genDir, file)]));
      } else {
        skip(`rustfmt ${file}`, "rustfmt não encontrado");
      }
      if (rustc) {
        push(
          `rustc ${file}`,
          exec(rustc, [
            "--edition",
            "2021",
            "--crate-type",
            "lib",
            "-D",
            "warnings",
            "--out-dir",
            tmp,
            path.join(genDir, file),
          ]),
        );
      } else {
        skip(`rustc ${file}`, "rustc não encontrado");
      }
    }

    const kotlinc = findBin("kotlinc");
    if (kotlinc) {
      push("kotlinc Limits.kt", exec(kotlinc, [path.join(genDir, "Limits.kt"), "-d", path.join(tmp, "kt")]));
    } else {
      skip("kotlinc Limits.kt", "kotlinc não encontrado; Limits.kt não compilado (só verificado por teste de snapshot)");
    }

    const swiftc = findBin("swiftc");
    if (swiftc) {
      push("swiftc -parse Limits.swift", exec(swiftc, ["-parse", path.join(genDir, "Limits.swift")]));
    } else {
      skip("swiftc Limits.swift", "swiftc não encontrado; Limits.swift não compilado (só verificado por teste de snapshot)");
    }
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
  return results;
}
