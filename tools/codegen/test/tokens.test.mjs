// Contrast math and token rules from packages/design-tokens/build.mjs (zero-dependency script).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { buildCss, checkTokens, contrastRatio, CSS_FILE, loadTokens, relativeLuminance } from "../../../packages/design-tokens/build.mjs";

test("relative luminance and contrast follow WCAG 2.x", () => {
  assert.equal(relativeLuminance("#000000"), 0);
  assert.equal(relativeLuminance("#FFFFFF"), 1);
  assert.ok(Math.abs(contrastRatio("#000000", "#FFFFFF") - 21) < 1e-9);
  assert.ok(Math.abs(contrastRatio("#FFFFFF", "#000000") - 21) < 1e-9);
  // #767676 on white is the classic AA boundary (4.54:1); #777777 falls just under it.
  assert.ok(contrastRatio("#767676", "#FFFFFF") >= 4.5);
  assert.ok(contrastRatio("#777777", "#FFFFFF") < 4.5);
  assert.throws(() => contrastRatio("#fff", "#000"), /cor inválida/);
});

test("the committed tokens satisfy every rule and tokens.css is fresh", () => {
  const tokens = loadTokens();
  assert.deepEqual(checkTokens(tokens), []);
  assert.ok(tokens.contrast_pairs.length >= 40);
  assert.equal(readFileSync(CSS_FILE, "utf8"), buildCss(tokens));
});

test("checkTokens catches a failing pair, a bad spacing step and a wrong radius", () => {
  const tokens = loadTokens();
  tokens.color.light.text.muted = "#999999";
  tokens.space["7"] = 52;
  tokens.radius.md = 6;
  const problems = checkTokens(tokens);
  assert.ok(problems.some((p) => /light: text\.muted \(#999999\) sobre bg\.canvas/.test(p)), problems.join("\n"));
  assert.ok(problems.some((p) => /space\.7 = 52/.test(p)));
  assert.ok(problems.some((p) => /radius\.md precisa ser 10/.test(p)));
});

test("checkTokens requires identical color trees in both modes", () => {
  const tokens = loadTokens();
  delete tokens.color.dark.text.link;
  const problems = checkTokens(tokens);
  assert.ok(problems.some((p) => /exatamente os mesmos tokens/.test(p)));
  assert.ok(problems.some((p) => /dark: par text\.link/.test(p)));
});

test("buildCss emits light, dark and reduced-motion blocks with the dm prefix", () => {
  const css = buildCss(loadTokens());
  assert.match(css, /^:root \{\n  color-scheme: light dark;/m);
  assert.match(css, /--dm-color-bg-canvas: #FCFCFD;/);
  assert.match(css, /--dm-space-2: 16px;/);
  assert.match(css, /--dm-radius-md: 10px;/);
  assert.match(css, /@media \(prefers-color-scheme: dark\) \{\n  :root:not\(\[data-theme="light"\]\) \{\n    --dm-color-bg-canvas: #0F1216;/);
  assert.match(css, /:root\[data-theme="dark"\] \{\n  --dm-color-bg-canvas: #0F1216;/);
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)/);
});
