// Loaders for limits.yaml and errors.yaml. Both files are validated against a meta-schema
// before anything is generated, so a typo in the source fails fast with a precise path.
import { readFileSync } from "node:fs";
import Ajv2020 from "ajv/dist/2020.js";
import { parse } from "yaml";
import { paths, rel } from "./paths.mjs";

const IDENT = "^[a-z][a-z0-9_]*$";

const limitsMetaSchema = {
  type: "object",
  required: ["version"],
  properties: { version: { type: "integer", minimum: 1 } },
  additionalProperties: {
    type: "object",
    minProperties: 1,
    propertyNames: { pattern: IDENT },
    additionalProperties: { type: "integer", minimum: 0 },
  },
};

const errorsMetaSchema = {
  type: "object",
  required: ["version", "categories", "errors"],
  additionalProperties: false,
  properties: {
    version: { type: "integer", minimum: 1 },
    categories: {
      type: "object",
      minProperties: 1,
      propertyNames: { pattern: IDENT },
      additionalProperties: { type: "string", minLength: 1 },
    },
    errors: {
      type: "object",
      minProperties: 1,
      propertyNames: { pattern: IDENT },
      additionalProperties: {
        type: "object",
        required: ["status", "category", "message"],
        additionalProperties: false,
        properties: {
          status: { type: "integer", minimum: 400, maximum: 599 },
          category: { type: "string", pattern: IDENT },
          message: { type: "string", minLength: 1, pattern: "^[^\\p{Cc}]+$" },
          retryable: { type: "boolean" },
          deprecated: { type: "boolean" },
        },
      },
    },
  },
};

const ajv = new Ajv2020({ strict: true, allErrors: true, unicodeRegExp: true });
const validateLimitsShape = ajv.compile(limitsMetaSchema);
const validateErrorsShape = ajv.compile(errorsMetaSchema);

function formatAjvErrors(file, errors) {
  return errors
    .map((e) => `${rel(file)}${e.instancePath || "/"}: ${e.message}${e.params?.additionalProperty ? ` (${e.params.additionalProperty})` : ""}`)
    .join("\n");
}

function readYaml(file) {
  const text = readFileSync(file, "utf8");
  // uniqueKeys is the default: a duplicated code or limit throws instead of silently winning.
  return parse(text, { uniqueKeys: true });
}

/**
 * Loads limits.yaml.
 * @returns {{ version: number, groups: Array<{ name: string, entries: Array<{ key: string, value: number }> }>, byKey: Map<string, number> }}
 */
export function loadLimits(file = paths.limits) {
  const doc = readYaml(file);
  if (!validateLimitsShape(doc)) {
    throw new Error(`limits.yaml inválido:\n${formatAjvErrors(file, validateLimitsShape.errors)}`);
  }
  const groups = [];
  const byKey = new Map();
  for (const [name, entries] of Object.entries(doc)) {
    if (name === "version") continue;
    const group = { name, entries: [] };
    for (const [key, value] of Object.entries(entries)) {
      group.entries.push({ key, value });
      byKey.set(`${name}.${key}`, value);
    }
    groups.push(group);
  }
  return { version: doc.version, groups, byKey };
}

/**
 * Loads errors.yaml.
 * @returns {{ version: number, categories: Array<{ name: string, description: string }>, errors: Array<{ code: string, status: number, category: string, message: string, retryable: boolean, deprecated: boolean }>, byCode: Map<string, object> }}
 */
export function loadErrors(file = paths.errors) {
  const doc = readYaml(file);
  if (!validateErrorsShape(doc)) {
    throw new Error(`errors.yaml inválido:\n${formatAjvErrors(file, validateErrorsShape.errors)}`);
  }
  const categories = Object.entries(doc.categories).map(([name, description]) => ({ name, description }));
  const known = new Set(categories.map((c) => c.name));
  const errors = [];
  const byCode = new Map();
  for (const [code, spec] of Object.entries(doc.errors)) {
    if (!known.has(spec.category)) {
      throw new Error(`errors.yaml: o código ${code} usa a categoria desconhecida "${spec.category}"`);
    }
    const entry = {
      code,
      status: spec.status,
      category: spec.category,
      message: spec.message,
      retryable: spec.retryable === true,
      deprecated: spec.deprecated === true,
    };
    errors.push(entry);
    byCode.set(code, entry);
  }
  return { version: doc.version, categories, errors, byCode };
}
