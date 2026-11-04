// JSON Schema 2020-12 validation: every schema in packages/contracts/schemas compiles in
// Ajv strict mode, every example under schemas/examples/<name>/valid validates, every one
// under invalid is rejected, inline `examples` validate, and tool manifests carry an
// `emits.schema` that is itself a compilable schema.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { paths, rel } from "../paths.mjs";

export function newAjv() {
  const ajv = new Ajv2020({ strict: true, allErrors: true, allowUnionTypes: true, unicodeRegExp: true });
  addFormats(ajv);
  // Vendor extension that binds a keyword to a limits.yaml key (checked by xref.mjs).
  ajv.addKeyword({ keyword: "x-limit", schemaType: "string" });
  return ajv;
}

function listJson(dir) {
  if (!existsSync(dir)) return [];
  return readdirSync(dir, { withFileTypes: true })
    .filter((e) => e.isFile() && e.name.endsWith(".json"))
    .map((e) => path.join(dir, e.name))
    .sort();
}

function readJson(file) {
  return JSON.parse(readFileSync(file, "utf8"));
}

function describe(errors) {
  return (errors ?? [])
    .slice(0, 6)
    .map((e) => `${e.instancePath || "/"} ${e.message}`)
    .join("; ");
}

/**
 * @returns {{ issues: string[], schemas: number, valid: number, invalid: number }}
 */
export function validateSchemas({ schemasDir = paths.schemasDir, examplesDir = paths.examplesDir } = {}) {
  const issues = [];
  const ajv = newAjv();
  const schemaFiles = listJson(schemasDir);
  const names = new Set();

  for (const file of schemaFiles) {
    const name = path.basename(file);
    names.add(name.replace(/\.json$/, ""));
    try {
      ajv.addSchema(readJson(file), name);
    } catch (err) {
      issues.push(`${rel(file)}: ${err.message}`);
    }
  }

  let valid = 0;
  let invalid = 0;
  for (const file of schemaFiles) {
    const name = path.basename(file);
    const stem = name.replace(/\.json$/, "");
    let validate;
    try {
      validate = ajv.getSchema(name);
      if (!validate) throw new Error("não compilou");
    } catch (err) {
      issues.push(`${rel(file)}: esquema não compila: ${err.message}`);
      continue;
    }
    const schema = readJson(file);
    for (const [i, example] of (schema.examples ?? []).entries()) {
      if (validate(example)) valid += 1;
      else issues.push(`${rel(file)}#/examples/${i}: exemplo inline inválido: ${describe(validate.errors)}`);
    }

    const validDir = path.join(examplesDir, stem, "valid");
    const invalidDir = path.join(examplesDir, stem, "invalid");
    const validFiles = listJson(validDir);
    const invalidFiles = listJson(invalidDir);
    if (validFiles.length === 0 && !(schema.examples?.length > 0)) {
      issues.push(`${rel(file)}: sem exemplos (crie ${rel(validDir)}/*.json ou \`examples\` no esquema)`);
    }
    for (const ex of validFiles) {
      let instance;
      try {
        instance = readJson(ex);
      } catch (err) {
        issues.push(`${rel(ex)}: JSON inválido: ${err.message}`);
        continue;
      }
      if (validate(instance)) {
        valid += 1;
      } else {
        issues.push(`${rel(ex)}: deveria validar: ${describe(validate.errors)}`);
      }
      if (stem === "tool-manifest" && instance?.emits?.schema) {
        try {
          newAjv().compile(instance.emits.schema);
        } catch (err) {
          issues.push(`${rel(ex)}: emits.schema não é um JSON Schema válido: ${err.message}`);
        }
      }
    }
    for (const ex of invalidFiles) {
      let instance;
      try {
        instance = readJson(ex);
      } catch (err) {
        issues.push(`${rel(ex)}: JSON inválido: ${err.message}`);
        continue;
      }
      if (validate(instance)) issues.push(`${rel(ex)}: deveria ser rejeitado, mas validou`);
      else invalid += 1;
    }
  }

  if (existsSync(examplesDir)) {
    for (const entry of readdirSync(examplesDir, { withFileTypes: true })) {
      if (entry.isDirectory() && !names.has(entry.name)) {
        issues.push(`${rel(path.join(examplesDir, entry.name))}: pasta de exemplos sem esquema correspondente`);
      }
    }
  }

  return { issues, schemas: schemaFiles.length, valid, invalid };
}
