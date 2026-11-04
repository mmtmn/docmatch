// Cross-reference checks between the contracts and the two source-of-truth files:
//   - every `maxLength` (and any keyword tagged with `x-limit`) agrees with limits.yaml;
//   - every `x-error-codes` list names codes from errors.yaml whose status matches the response;
//   - every operation tagged `x-spends-credits` requires the Idempotency-Key header and
//     documents 402;
//   - every `x-sse-events` entry points at an existing schema.
// Pure functions over parsed documents; `xrefContracts` wires them to the files on disk.
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { parse } from "yaml";
import { paths, rel } from "../paths.mjs";

const BOUND_KEYWORDS = Object.freeze(["maxLength", "maxItems", "maximum", "minLength", "minItems", "minimum"]);
// Values under these keys are data, not schema: never walked.
const DATA_KEYS = new Set(["example", "examples", "default", "enum", "const", "x-sse-events", "x-error-codes"]);
const HTTP_METHODS = Object.freeze(["get", "put", "post", "delete", "options", "head", "patch", "trace"]);

function escapePointer(token) {
  return String(token).replace(/~/g, "~0").replace(/\//g, "~1");
}

/** Depth-first visit of every object node with its JSON pointer. */
export function walk(node, visit, pointer = "") {
  if (Array.isArray(node)) {
    node.forEach((item, i) => walk(item, visit, `${pointer}/${i}`));
    return;
  }
  if (node === null || typeof node !== "object") return;
  visit(node, pointer);
  for (const [key, value] of Object.entries(node)) {
    if (DATA_KEYS.has(key)) continue;
    walk(value, visit, `${pointer}/${escapePointer(key)}`);
  }
}

/** `x-limit` agreement with limits.yaml, and no `maxLength` without `x-limit`. */
export function checkLimitRefs(doc, limits, file) {
  const issues = [];
  walk(doc, (node, ptr) => {
    const where = `${file}#${ptr || "/"}`;
    const bound = BOUND_KEYWORDS.find((k) => Object.hasOwn(node, k));
    if (Object.hasOwn(node, "x-limit")) {
      const key = node["x-limit"];
      if (typeof key !== "string") {
        issues.push(`${where}: x-limit precisa ser uma string (ex.: text.display_name)`);
      } else if (!limits.byKey.has(key)) {
        issues.push(`${where}: x-limit "${key}" não existe em limits.yaml`);
      } else if (!bound) {
        issues.push(`${where}: x-limit "${key}" sem ${BOUND_KEYWORDS.join("/")} ao lado`);
      } else if (node[bound] !== limits.byKey.get(key)) {
        issues.push(`${where}: ${bound} ${node[bound]} ≠ limits.yaml ${key} = ${limits.byKey.get(key)}`);
      }
    } else if (Object.hasOwn(node, "maxLength")) {
      issues.push(`${where}: maxLength ${node.maxLength} sem x-limit (todo maxLength vem de limits.yaml)`);
    }
  });
  return issues;
}

function resolveResponseRef(ref, doc, externalDocs) {
  const m = /^(?:\.\/)?([^#]*)#\/components\/responses\/([^/]+)$/.exec(ref);
  if (!m) return { error: `$ref de resposta fora do padrão: ${ref}` };
  const [, fileName, name] = m;
  const target = fileName === "" ? doc : externalDocs[fileName];
  if (!target) return { error: `$ref aponta para um arquivo desconhecido: ${ref}` };
  const resp = target.components?.responses?.[name];
  if (!resp) return { error: `$ref de resposta não resolve: ${ref}` };
  return { response: resp, name };
}

function* operations(doc) {
  for (const [route, item] of Object.entries(doc.paths ?? {})) {
    if (!item || typeof item !== "object") continue;
    for (const method of HTTP_METHODS) {
      const op = item[method];
      if (op && typeof op === "object") yield { route, method, item, op, pointer: `/paths/${escapePointer(route)}/${method}` };
    }
  }
}

/** `x-error-codes` and `x-status` agreement with errors.yaml. */
export function checkErrorRefs(doc, errors, file, externalDocs = {}) {
  const issues = [];
  const codesOf = (resp, where, expectedStatus) => {
    const codes = resp["x-error-codes"];
    if (codes === undefined) return;
    if (!Array.isArray(codes) || codes.length === 0 || codes.some((c) => typeof c !== "string")) {
      issues.push(`${where}: x-error-codes precisa ser uma lista de códigos`);
      return;
    }
    for (const code of codes) {
      const spec = errors.byCode.get(code);
      if (!spec) {
        issues.push(`${where}: código "${code}" não existe em errors.yaml`);
      } else if (expectedStatus !== undefined && spec.status !== expectedStatus) {
        issues.push(`${where}: código "${code}" tem status ${spec.status} em errors.yaml, resposta é ${expectedStatus}`);
      }
    }
  };

  for (const [name, resp] of Object.entries(doc.components?.responses ?? {})) {
    const where = `${file}#/components/responses/${name}`;
    const status = resp["x-status"];
    if (resp["x-error-codes"] !== undefined && !Number.isInteger(status)) {
      issues.push(`${where}: respostas nomeadas com x-error-codes precisam de x-status`);
      continue;
    }
    codesOf(resp, where, status);
  }

  for (const { op, pointer } of operations(doc)) {
    for (const [status, resp] of Object.entries(op.responses ?? {})) {
      const where = `${file}#${pointer}/responses/${status}`;
      const n = status === "default" ? undefined : Number(status);
      if (resp && typeof resp === "object" && typeof resp.$ref === "string") {
        const r = resolveResponseRef(resp.$ref, doc, externalDocs);
        if (r.error) {
          issues.push(`${where}: ${r.error}`);
        } else if (n !== undefined && Number.isInteger(r.response["x-status"]) && r.response["x-status"] !== n) {
          issues.push(`${where}: usa ${r.name} (x-status ${r.response["x-status"]}) sob o status ${n}`);
        }
        continue;
      }
      if (resp && typeof resp === "object") codesOf(resp, where, n);
    }
  }
  return issues;
}

function hasIdempotencyParam(params) {
  return params.some(
    (p) =>
      (typeof p?.$ref === "string" && /\/parameters\/IdempotencyKey$/.test(p.$ref)) ||
      (p?.in === "header" && p?.name === "Idempotency-Key" && p?.required === true),
  );
}

/** Operations tagged `x-spends-credits: true` require Idempotency-Key and document 402. */
export function checkSpendsCredits(doc, file) {
  const issues = [];
  for (const { item, op, pointer } of operations(doc)) {
    if (op["x-spends-credits"] !== true) continue;
    const where = `${file}#${pointer}`;
    const params = [...(item.parameters ?? []), ...(op.parameters ?? [])];
    if (!hasIdempotencyParam(params)) {
      issues.push(`${where}: gasta créditos mas não exige o header Idempotency-Key`);
    }
    if (!op.responses?.["402"]) {
      issues.push(`${where}: gasta créditos mas não documenta a resposta 402 (insufficient_credits)`);
    }
  }
  return issues;
}

/** `x-sse-events` maps event names to local schema refs that must resolve. */
export function checkSseEvents(doc, file) {
  const issues = [];
  for (const { op, pointer } of operations(doc)) {
    const events = op["x-sse-events"];
    if (events === undefined) continue;
    const where = `${file}#${pointer}/x-sse-events`;
    if (!events || typeof events !== "object" || Array.isArray(events)) {
      issues.push(`${where}: precisa ser um objeto nome -> $ref`);
      continue;
    }
    for (const [name, ref] of Object.entries(events)) {
      if (!/^[a-z][a-z0-9_]*$/.test(name)) issues.push(`${where}: nome de evento inválido "${name}"`);
      const m = /^#\/components\/schemas\/([^/]+)$/.exec(String(ref));
      if (!m || !doc.components?.schemas?.[m[1]]) {
        issues.push(`${where}/${name}: $ref "${ref}" não resolve em components.schemas`);
      }
    }
  }
  return issues;
}

function readDoc(file) {
  const text = readFileSync(file, "utf8");
  return file.endsWith(".json") ? JSON.parse(text) : parse(text, { uniqueKeys: true });
}

/** Runs every cross-reference check against the files on disk. */
export function xrefContracts({ limits, errors }) {
  const issues = [];
  const openapi = readDoc(paths.openapi);
  const admin = readDoc(paths.openapiAdmin);
  const external = { "openapi.yaml": openapi };

  const specs = [
    [paths.openapi, openapi, {}],
    [paths.openapiAdmin, admin, external],
  ];
  for (const [file, doc, ext] of specs) {
    const name = rel(file);
    issues.push(...checkLimitRefs(doc, limits, name));
    issues.push(...checkErrorRefs(doc, errors, name, ext));
    issues.push(...checkSpendsCredits(doc, name));
    issues.push(...checkSseEvents(doc, name));
  }

  for (const entry of readdirSync(paths.schemasDir, { withFileTypes: true })) {
    if (!entry.isFile() || !entry.name.endsWith(".json")) continue;
    const file = path.join(paths.schemasDir, entry.name);
    issues.push(...checkLimitRefs(readDoc(file), limits, rel(file)));
  }
  return issues;
}
