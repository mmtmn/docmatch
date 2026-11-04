import assert from "node:assert/strict";
import { test } from "node:test";
import { emitErrorsGo, emitErrorsRs, emitErrorsTs } from "../src/emit/errors.mjs";
import { emitLimitsGo, emitLimitsKt, emitLimitsRs, emitLimitsSwift, emitLimitsTs } from "../src/emit/limits.mjs";

const limits = {
  version: 1,
  groups: [
    { name: "text", entries: [{ key: "display_name", value: 80 }, { key: "rules_text", value: 4000 }] },
    { name: "http", entries: [{ key: "json_body_max_kb", value: 256 }] },
  ],
  byKey: new Map([
    ["text.display_name", 80],
    ["text.rules_text", 4000],
    ["http.json_body_max_kb", 256],
  ]),
};

const errorsCatalog = () => {
  const errors = [
    { code: "unauthenticated", status: 401, category: "auth", message: "Faça login para continuar.", retryable: false, deprecated: false },
    { code: "invalid_api_key", status: 401, category: "auth", message: "Chave inválida.", retryable: false, deprecated: false },
    {
      code: "tool_build_failed",
      status: 500,
      category: "internal",
      message: "Não conseguimos construir uma ferramenta para este formato. Nenhum crédito foi cobrado por ela, de verdade.",
      retryable: false,
      deprecated: false,
    },
    { code: "service_unavailable", status: 503, category: "internal", message: "Indisponível.", retryable: true, deprecated: false },
    { code: "internal_error", status: 500, category: "internal", message: "Algo deu errado.", retryable: false, deprecated: false },
  ];
  return {
    version: 1,
    categories: [
      { name: "auth", description: "Autenticação" },
      { name: "internal", description: "Falhas do nosso lado" },
    ],
    errors,
    byCode: new Map(errors.map((e) => [e.code, e])),
  };
};

test("limits emitters are deterministic and end with a newline", () => {
  for (const emit of [emitLimitsTs, emitLimitsGo, emitLimitsRs, emitLimitsKt, emitLimitsSwift]) {
    const a = emit(limits);
    const b = emit(limits);
    assert.equal(a, b);
    assert.ok(a.endsWith("\n"));
    assert.ok(!a.endsWith("\n\n"));
    assert.match(a, /DO NOT EDIT/);
  }
});

test("limits.ts exposes nested constants and the dotted lookup", () => {
  const ts = emitLimitsTs(limits);
  assert.match(ts, /export const LIMITS = \{\n  version: 1,/);
  assert.match(ts, /\n  text: \{\n    display_name: 80,\n    rules_text: 4000,\n  \},/);
  assert.match(ts, /'http\.json_body_max_kb': 256,/);
  assert.match(ts, /\| 'text\.display_name'\n/);
  assert.match(ts, /export function limit\(key: LimitKey\): number/);
});

test("limits.go aligns const blocks and uses initialisms", () => {
  const go = emitLimitsGo(limits);
  assert.match(go, /^package contracts$/m);
  assert.match(go, /\tTextDisplayName = 80\n\tTextRulesText   = 4000\n/);
  assert.match(go, /\tHTTPJSONBodyMaxKB = 256\n/);
  assert.match(go, /"text\.display_name":     TextDisplayName,\n/);
  assert.match(go, /"http\.json_body_max_kb": HTTPJSONBodyMaxKB,\n/);
});

test("limits.rs, Limits.kt and Limits.swift use their idioms", () => {
  const rs = emitLimitsRs(limits);
  assert.match(rs, /pub mod text \{\n    \/\/\/ `text\.display_name`\n    pub const DISPLAY_NAME: u32 = 80;/);
  assert.match(rs, /"http\.json_body_max_kb" => Some\(http::JSON_BODY_MAX_KB\),/);
  const kt = emitLimitsKt(limits);
  assert.match(kt, /^package br\.com\.docmatch\.contracts$/m);
  assert.match(kt, /object Text \{\n        const val DISPLAY_NAME: Int = 80/);
  assert.match(kt, /"text\.rules_text" to Text\.RULES_TEXT,/);
  const swift = emitLimitsSwift(limits);
  assert.match(swift, /public enum HTTP \{\n        public static let jsonBodyMaxKB: Int = 256/);
  assert.match(swift, /"text\.display_name": Text\.displayName,/);
});

test("errors.ts emits the catalog with types and helpers", () => {
  const ts = emitErrorsTs(errorsCatalog());
  assert.match(ts, /export type ErrorCategory =\n  \| 'auth'\n  \| 'internal';/);
  assert.match(ts, /  unauthenticated: \{ status: 401, category: 'auth', message: 'Faça login para continuar\.', retryable: false \},/);
  assert.match(ts, /export type ErrorCode = keyof typeof ERRORS;/);
  assert.match(ts, /return isErrorCode\(code\) \? ERRORS\[code\] : ERRORS\.internal_error;/);
});

test("errors.go aligns the const block and the spec map", () => {
  const go = emitErrorsGo(errorsCatalog());
  assert.match(go, /\tErrUnauthenticated    ErrorCode = "unauthenticated"\n\tErrInvalidAPIKey      ErrorCode = "invalid_api_key"\n/);
  assert.match(go, /\tErrUnauthenticated:    \{Code: ErrUnauthenticated, Status: 401, Category: ErrorCategoryAuth, Message: "Faça login para continuar\.", Retryable: false\},\n/);
  assert.match(go, /func \(c ErrorCode\) Status\(\) int \{/);
  assert.match(go, /return ErrorSpecs\[ErrInternalError\]\.Status/);
});

test("errors.rs wraps long match arms the way rustfmt does", () => {
  const rs = emitErrorsRs(errorsCatalog());
  assert.match(rs, /    InvalidApiKey,\n/);
  assert.match(rs, /            ErrorCode::Unauthenticated => "unauthenticated",\n/);
  assert.match(rs, /            ErrorCode::ToolBuildFailed => \{\n                "Não conseguimos construir[^\n]*"\n            \}\n/);
  assert.match(rs, /        matches!\(\n            self,\n            ErrorCode::ServiceUnavailable\n        \)/);
  assert.match(rs, /pub const fn http_status\(self\) -> u16/);
});

test("errors emitters refuse a catalog without the fallback code", () => {
  const catalog = errorsCatalog();
  catalog.errors = catalog.errors.filter((e) => e.code !== "internal_error");
  catalog.byCode.delete("internal_error");
  assert.throws(() => emitErrorsTs(catalog), /internal_error/);
  assert.throws(() => emitErrorsGo(catalog), /internal_error/);
  assert.throws(() => emitErrorsRs(catalog), /internal_error/);
});
