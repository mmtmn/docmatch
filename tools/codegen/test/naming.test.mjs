import assert from "node:assert/strict";
import { test } from "node:test";
import { camel, padTo, pascal, pascalGo, quote, quoteTs, screaming } from "../src/naming.mjs";

test("pascalGo uses Go initialisms", () => {
  assert.equal(pascalGo("invalid_api_key"), "InvalidAPIKey");
  assert.equal(pascalGo("json_body_max_kb"), "JSONBodyMaxKB");
  assert.equal(pascalGo("ids"), "IDs");
  assert.equal(pascalGo("sse_idle_timeout_s"), "SSEIdleTimeoutS");
  assert.equal(pascalGo("document_file_max_mb"), "DocumentFileMaxMB");
});

test("pascal treats acronyms as plain words (Rust, Kotlin)", () => {
  assert.equal(pascal("invalid_api_key"), "InvalidApiKey");
  assert.equal(pascal("invalid_json"), "InvalidJson");
  assert.equal(pascal("http"), "Http");
});

test("camel keeps the first word lower-case (Swift members)", () => {
  assert.equal(camel("json_body_max_kb"), "jsonBodyMaxKB");
  assert.equal(camel("idempotency_key_max"), "idempotencyKeyMax");
  assert.equal(camel("api_key_scopes_max"), "apiKeyScopesMax");
  assert.equal(camel("email"), "email");
});

test("screaming and padding", () => {
  assert.equal(screaming("rules_text"), "RULES_TEXT");
  assert.equal(padTo("ab", 5), "ab   ");
  assert.equal(padTo("abcdef", 5), "abcdef");
});

test("quote escapes for double- and single-quoted literals", () => {
  assert.equal(quote('a "b" \\ c'), '"a \\"b\\" \\\\ c"');
  assert.equal(quoteTs("it's"), "'it\\'s'");
  assert.equal(quote("Faça login."), '"Faça login."');
});
