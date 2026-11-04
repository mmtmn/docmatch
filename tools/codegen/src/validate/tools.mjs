// External linters installed as npm dependencies: Redocly CLI (OpenAPI 3.1) and buf (proto).
// Both run with packages/contracts as cwd so they pick up redocly.yaml and buf.yaml.
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { paths } from "../paths.mjs";

function run(bin, args, extraEnv = {}) {
  if (!existsSync(bin)) {
    return { ok: false, output: `${bin} não existe; rode \`make -C packages/contracts setup\`` };
  }
  const r = spawnSync(bin, args, {
    cwd: paths.contracts,
    encoding: "utf8",
    env: { ...process.env, NO_COLOR: "1", FORCE_COLOR: "0", ...extraEnv },
    maxBuffer: 64 * 1024 * 1024,
  });
  if (r.error) return { ok: false, output: String(r.error.message ?? r.error) };
  return { ok: r.status === 0, status: r.status, output: `${r.stdout ?? ""}${r.stderr ?? ""}` };
}

/** `redocly lint` over both OpenAPI files; warnings count as failures. */
export function runRedocly() {
  const r = run(paths.bin("redocly"), ["lint", "--format=stylish"], {
    REDOCLY_TELEMETRY: "off",
    REDOCLY_SUPPRESS_UPDATE_NOTICE: "true",
  });
  if (r.ok) {
    const m = /(\d+) warnings?/.exec(r.output);
    if (m && Number(m[1]) > 0) return { ...r, ok: false, output: `${r.output}\n(avisos contam como falha)` };
  }
  return r;
}

/** `buf lint` over harness.proto with packages/contracts/buf.yaml. */
export function runBuf() {
  return run(paths.bin("buf"), ["lint"]);
}
