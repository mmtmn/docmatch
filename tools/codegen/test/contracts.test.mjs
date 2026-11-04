// Integration pass over the real repository: the committed contracts must load, cross-check,
// validate their examples and match the committed generated files.
import assert from "node:assert/strict";
import { test } from "node:test";
import { checkGenerated } from "../src/gen.mjs";
import { loadErrors, loadLimits } from "../src/load.mjs";
import { validateSchemas } from "../src/validate/schemas.mjs";
import { xrefContracts } from "../src/validate/xref.mjs";

test("limits.yaml loads with non-empty groups and integer values", () => {
  const limits = loadLimits();
  assert.equal(limits.version, 1);
  assert.ok(limits.groups.length >= 4);
  for (const g of limits.groups) {
    assert.ok(g.entries.length > 0, `grupo ${g.name} vazio`);
    for (const e of g.entries) assert.ok(Number.isInteger(e.value) && e.value >= 0, `${g.name}.${e.key}`);
  }
  assert.ok(limits.byKey.has("text.display_name"));
  assert.ok(limits.byKey.has("ids.idempotency_key_max"));
});

test("errors.yaml honours the contract invariants", () => {
  const errors = loadErrors();
  assert.equal(errors.byCode.get("insufficient_credits").status, 402);
  assert.equal(errors.byCode.get("rate_limited").status, 429);
  assert.equal(errors.byCode.get("rate_limited").retryable, true);
  assert.equal(errors.byCode.get("internal_error").status, 500);
  for (const code of ["upload_rejected_type", "upload_rejected_size", "upload_rejected_malware", "upload_rejected_bomb", "upload_rejected_playlist"]) {
    assert.ok(errors.byCode.has(code), code);
  }
  for (const e of errors.errors) {
    assert.match(e.code, /^[a-z][a-z0-9_]*$/);
    assert.equal(e.message, e.message.trim(), `${e.code}: mensagem com espaço nas pontas`);
    assert.match(e.message, /[.!?]$/, `${e.code}: mensagem sem pontuação final`);
    if (e.retryable) assert.ok([429, 502, 503, 504].includes(e.status), `${e.code}: retryable com status ${e.status}`);
  }
});

test("every schema example validates or is rejected as declared", () => {
  const result = validateSchemas();
  assert.deepEqual(result.issues, []);
  assert.equal(result.schemas, 8);
  assert.ok(result.valid >= 8, "cada esquema precisa de ao menos um exemplo válido");
  assert.ok(result.invalid >= 8, "cada esquema precisa de ao menos um exemplo inválido");
});

test("OpenAPI and schemas agree with limits.yaml and errors.yaml", () => {
  const issues = xrefContracts({ limits: loadLimits(), errors: loadErrors() });
  assert.deepEqual(issues, []);
});

test("committed generated files are fresh", () => {
  const { stale, missing } = checkGenerated();
  assert.deepEqual(missing, []);
  assert.deepEqual(stale, []);
});
