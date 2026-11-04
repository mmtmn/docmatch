import assert from "node:assert/strict";
import { test } from "node:test";
import { checkErrorRefs, checkLimitRefs, checkSpendsCredits, checkSseEvents } from "../src/validate/xref.mjs";

const limits = { byKey: new Map([["text.display_name", 80], ["ids.api_key_scopes_max", 10]]) };
const errors = {
  byCode: new Map([
    ["not_found", { status: 404 }],
    ["insufficient_credits", { status: 402 }],
    ["rate_limited", { status: 429 }],
  ]),
};

test("x-limit must agree with limits.yaml", () => {
  const ok = { components: { schemas: { A: { type: "string", maxLength: 80, "x-limit": "text.display_name" } } } };
  assert.deepEqual(checkLimitRefs(ok, limits, "f.yaml"), []);

  const wrong = { components: { schemas: { A: { type: "string", maxLength: 100, "x-limit": "text.display_name" } } } };
  const issues = checkLimitRefs(wrong, limits, "f.yaml");
  assert.equal(issues.length, 1);
  assert.match(issues[0], /maxLength 100 ≠ limits\.yaml text\.display_name = 80/);
  assert.match(issues[0], /f\.yaml#\/components\/schemas\/A/);
});

test("maxLength without x-limit and unknown keys are reported", () => {
  const noRef = { a: { type: "string", maxLength: 5 } };
  assert.match(checkLimitRefs(noRef, limits, "f")[0], /sem x-limit/);
  const unknown = { a: { type: "string", maxLength: 5, "x-limit": "text.nope" } };
  assert.match(checkLimitRefs(unknown, limits, "f")[0], /não existe em limits\.yaml/);
  const dangling = { a: { type: "string", "x-limit": "text.display_name" } };
  assert.match(checkLimitRefs(dangling, limits, "f")[0], /sem maxLength/);
});

test("x-limit binds to maxItems when there is no maxLength, and example data is skipped", () => {
  const items = { a: { type: "array", maxItems: 10, minItems: 1, "x-limit": "ids.api_key_scopes_max" } };
  assert.deepEqual(checkLimitRefs(items, limits, "f"), []);
  const example = { a: { type: "object", examples: [{ maxLength: 999 }], default: { maxLength: 1 } } };
  assert.deepEqual(checkLimitRefs(example, limits, "f"), []);
});

test("x-error-codes statuses must match the response status", () => {
  const doc = {
    paths: {
      "/x": {
        get: {
          responses: {
            "404": { $ref: "#/components/responses/NotFound" },
            "402": { "x-error-codes": ["insufficient_credits"] },
          },
        },
        post: {
          responses: {
            "409": { $ref: "#/components/responses/NotFound" },
            "429": { "x-error-codes": ["insufficient_credits", "nope"] },
          },
        },
      },
    },
    components: { responses: { NotFound: { "x-status": 404, "x-error-codes": ["not_found"] } } },
  };
  const issues = checkErrorRefs(doc, errors, "f");
  assert.equal(issues.length, 3, issues.join("\n"));
  assert.match(issues[0], /usa NotFound \(x-status 404\) sob o status 409/);
  assert.match(issues[1], /"insufficient_credits" tem status 402 em errors\.yaml, resposta é 429/);
  assert.match(issues[2], /"nope" não existe/);
});

test("named responses need x-status and external refs resolve through externalDocs", () => {
  const noStatus = { components: { responses: { Bad: { "x-error-codes": ["not_found"] } } } };
  assert.match(checkErrorRefs(noStatus, errors, "f")[0], /precisam de x-status/);

  const pub = { components: { responses: { NotFound: { "x-status": 404, "x-error-codes": ["not_found"] } } } };
  const admin = { paths: { "/y": { get: { responses: { "404": { $ref: "./openapi.yaml#/components/responses/NotFound" } } } } } };
  assert.deepEqual(checkErrorRefs(admin, errors, "admin", { "openapi.yaml": pub }), []);
  const bad = { paths: { "/y": { get: { responses: { "404": { $ref: "./other.yaml#/components/responses/NotFound" } } } } } };
  assert.match(checkErrorRefs(bad, errors, "admin", { "openapi.yaml": pub })[0], /arquivo desconhecido/);
});

test("operations that spend credits require Idempotency-Key and a 402", () => {
  const good = {
    paths: {
      "/runs": {
        post: {
          "x-spends-credits": true,
          parameters: [{ $ref: "#/components/parameters/IdempotencyKey" }],
          responses: { "402": {} },
        },
      },
    },
  };
  assert.deepEqual(checkSpendsCredits(good, "f"), []);
  const bad = { paths: { "/runs": { post: { "x-spends-credits": true, responses: { "200": {} } } } } };
  const issues = checkSpendsCredits(bad, "f");
  assert.equal(issues.length, 2);
  assert.match(issues[0], /Idempotency-Key/);
  assert.match(issues[1], /402/);
  const inline = {
    paths: {
      "/runs": {
        parameters: [{ in: "header", name: "Idempotency-Key", required: true }],
        post: { "x-spends-credits": true, responses: { "402": {} } },
      },
    },
  };
  assert.deepEqual(checkSpendsCredits(inline, "f"), []);
});

test("x-sse-events must point at existing schemas", () => {
  const doc = {
    paths: { "/e": { get: { "x-sse-events": { finding: "#/components/schemas/RunEventFinding", Bad: "#/components/schemas/Nope" } } } },
    components: { schemas: { RunEventFinding: {} } },
  };
  const issues = checkSseEvents(doc, "f");
  assert.equal(issues.length, 2);
  assert.match(issues[0], /nome de evento inválido "Bad"/);
  assert.match(issues[1], /não resolve/);
});
