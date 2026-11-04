#!/usr/bin/env node
// docmatch-codegen: gera e valida os contratos de packages/contracts.
//
//   gen       escreve packages/contracts/gen/* a partir de limits.yaml e errors.yaml
//   check     regenera em uma pasta temporária e falha se algo commitado estiver desatualizado
//   validate  valida openapi.yaml, openapi-admin.yaml, schemas + exemplos, cruzamentos e harness.proto
//   smoke     compila os arquivos gerados com as toolchains disponíveis (go, rustc, node)
import { checkGenerated, generateAll, writeAll } from "./src/gen.mjs";
import { loadErrors, loadLimits } from "./src/load.mjs";
import { fail, info, ok, table, warn } from "./src/log.mjs";
import { paths, rel } from "./src/paths.mjs";
import { smoke } from "./src/smoke.mjs";
import { validateSchemas } from "./src/validate/schemas.mjs";
import { runBuf, runRedocly } from "./src/validate/tools.mjs";
import { xrefContracts } from "./src/validate/xref.mjs";

const USAGE = `uso: docmatch-codegen <gen|check|validate|smoke>

  gen       escreve ${rel(paths.genDir)}/* a partir de limits.yaml e errors.yaml
  check     regenera em pasta temporária e falha se algo commitado estiver desatualizado
  validate  valida OpenAPI (público e admin), esquemas + exemplos, cruzamentos e harness.proto
  smoke     compila os arquivos gerados com as toolchains instaladas (go, rustc, node)
`;

function loadSources() {
  const limits = loadLimits();
  const errors = loadErrors();
  return { limits, errors };
}

function cmdGen() {
  const sources = loadSources();
  const files = generateAll(sources);
  const written = writeAll(paths.genDir, files);
  for (const name of files.keys()) {
    info(`${written.includes(name) ? "escrito" : "inalterado"}: ${rel(paths.genDir)}/${name}`);
  }
  ok(`${files.size} arquivos gerados (${sources.limits.byKey.size} limites, ${sources.errors.errors.length} códigos de erro)`);
  return 0;
}

function cmdCheck() {
  const { stale, missing } = checkGenerated(paths.genDir);
  if (stale.length === 0 && missing.length === 0) {
    ok(`${rel(paths.genDir)} está atualizado`);
    return 0;
  }
  for (const f of missing) fail(`${rel(paths.genDir)}/${f} não existe`);
  for (const f of stale) fail(`${rel(paths.genDir)}/${f} está desatualizado`);
  fail("rode `make -C packages/contracts gen` e commite o resultado");
  return 1;
}

function cmdValidate() {
  const rows = [];
  let sources;
  try {
    sources = loadSources();
    rows.push({ name: "limits.yaml + errors.yaml", ok: true, detail: `${sources.limits.byKey.size} limites, ${sources.errors.errors.length} códigos` });
  } catch (err) {
    rows.push({ name: "limits.yaml + errors.yaml", ok: false, detail: err.message });
    table(rows);
    return 1;
  }

  const xref = xrefContracts(sources);
  rows.push({ name: "cruzamentos (x-limit, x-error-codes, x-spends-credits, x-sse-events)", ok: xref.length === 0, detail: xref.length ? `${xref.length} problema(s)` : "" });
  for (const issue of xref) fail(issue);

  const schemas = validateSchemas();
  rows.push({
    name: "schemas + exemplos (Ajv 2020-12)",
    ok: schemas.issues.length === 0,
    detail: `${schemas.schemas} esquemas, ${schemas.valid} exemplos válidos, ${schemas.invalid} inválidos rejeitados`,
  });
  for (const issue of schemas.issues) fail(issue);

  const redocly = runRedocly();
  rows.push({ name: "redocly lint (openapi.yaml, openapi-admin.yaml)", ok: redocly.ok, detail: redocly.ok ? "" : "ver saída acima" });
  if (!redocly.ok) process.stderr.write(`${redocly.output}\n`);

  const buf = runBuf();
  rows.push({ name: "buf lint (harness.proto)", ok: buf.ok, detail: buf.ok ? "" : "ver saída acima" });
  if (!buf.ok) process.stderr.write(`${buf.output}\n`);

  table(rows);
  return rows.every((r) => r.ok) ? 0 : 1;
}

function cmdSmoke() {
  const results = smoke(paths.genDir);
  const rows = results.map((r) => ({ name: r.tool, ok: r.ok, skipped: r.skipped, detail: r.skipped ? r.output : r.ok ? "" : "ver saída acima" }));
  for (const r of results) {
    if (r.skipped) warn(r.output);
    else if (!r.ok) process.stderr.write(`--- ${r.tool}\n${r.output}\n`);
  }
  table(rows);
  return results.every((r) => r.ok) ? 0 : 1;
}

const commands = { gen: cmdGen, check: cmdCheck, validate: cmdValidate, smoke: cmdSmoke };
const command = commands[process.argv[2]];
if (!command) {
  process.stderr.write(USAGE);
  process.exit(2);
}
try {
  process.exit(command());
} catch (err) {
  fail(err?.stack ?? String(err));
  process.exit(1);
}
