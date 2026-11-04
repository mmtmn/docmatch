// Repository paths used by every command. The root is found by walking up from this file
// until both packages/contracts/limits.yaml and tools/codegen/package.json exist, so the
// CLI works from any cwd (worktrees included).
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export function findRepoRoot(start = here) {
  let dir = start;
  for (let i = 0; i < 8; i += 1) {
    if (
      existsSync(path.join(dir, "packages", "contracts", "limits.yaml")) &&
      existsSync(path.join(dir, "tools", "codegen", "package.json"))
    ) {
      return dir;
    }
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  throw new Error(`não encontrei a raiz do repositório a partir de ${start}`);
}

export const ROOT = findRepoRoot();
const CONTRACTS = path.join(ROOT, "packages", "contracts");

export const paths = Object.freeze({
  root: ROOT,
  contracts: CONTRACTS,
  limits: path.join(CONTRACTS, "limits.yaml"),
  errors: path.join(CONTRACTS, "errors.yaml"),
  openapi: path.join(CONTRACTS, "openapi.yaml"),
  openapiAdmin: path.join(CONTRACTS, "openapi-admin.yaml"),
  proto: path.join(CONTRACTS, "harness.proto"),
  schemasDir: path.join(CONTRACTS, "schemas"),
  examplesDir: path.join(CONTRACTS, "schemas", "examples"),
  genDir: path.join(CONTRACTS, "gen"),
  designTokens: path.join(ROOT, "packages", "design-tokens"),
  codegen: path.join(ROOT, "tools", "codegen"),
  bin: (name) => path.join(ROOT, "tools", "codegen", "node_modules", ".bin", name),
});

/** Path relative to the repository root, for messages. */
export function rel(file) {
  return path.relative(ROOT, file) || ".";
}
