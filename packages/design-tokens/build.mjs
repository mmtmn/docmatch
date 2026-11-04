#!/usr/bin/env node
// Builds tokens.css from tokens.json and checks the design rules that the tokens promise:
//   - every named text/background pair in `contrast_pairs` reaches WCAG AA 4.5:1 in both modes
//     (contrast is computed here, from the sRGB relative luminance formula);
//   - spacing values are multiples of 8 and the default radius is 8;
//   - every color is a 6-digit hex.
// Zero dependencies. `node build.mjs` writes tokens.css; `node build.mjs --check` fails when
// tokens.css is stale or any rule is broken. Exported functions are unit-tested by tools/codegen.
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
export const TOKENS_FILE = path.join(here, "tokens.json");
export const CSS_FILE = path.join(here, "tokens.css");
export const AA_MIN = 4.5;
export const SPACE_STEP = 8;
export const BASE_RADIUS = 8;

// ---------------------------------------------------------------------------- color math

export function parseHex(hex) {
  const m = /^#([0-9a-fA-F]{6})$/.exec(String(hex).trim());
  if (!m) throw new Error(`cor inválida: ${hex} (use #RRGGBB)`);
  const n = Number.parseInt(m[1], 16);
  return [(n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff];
}

function channel(value) {
  const c = value / 255;
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

/** WCAG 2.x relative luminance of an sRGB hex color (0 = black, 1 = white). */
export function relativeLuminance(hex) {
  const [r, g, b] = parseHex(hex);
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

/** WCAG contrast ratio between two colors (1 to 21), independent of order. */
export function contrastRatio(a, b) {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const [light, dark] = la >= lb ? [la, lb] : [lb, la];
  return (light + 0.05) / (dark + 0.05);
}

// ---------------------------------------------------------------------------- helpers

function lookup(tree, dotted) {
  let node = tree;
  for (const part of dotted.split(".")) {
    if (node === null || typeof node !== "object" || !Object.hasOwn(node, part)) return undefined;
    node = node[part];
  }
  return node;
}

function cssName(prefix, ...parts) {
  return `--${prefix}-${parts.join("-").replace(/_/g, "-")}`;
}

function flattenColors(group, prefix, out = [], trail = []) {
  for (const [key, value] of Object.entries(group)) {
    if (value && typeof value === "object") flattenColors(value, prefix, out, [...trail, key]);
    else out.push([cssName(prefix, "color", ...trail, key), value]);
  }
  return out;
}

// ---------------------------------------------------------------------------- checks

/**
 * Validates the token file. Returns a list of problems (empty when everything holds).
 * @returns {string[]}
 */
export function checkTokens(tokens) {
  const problems = [];
  const modes = Object.keys(tokens.color ?? {});
  if (!modes.includes("light") || !modes.includes("dark")) problems.push("color precisa ter os modos light e dark");

  for (const mode of modes) {
    for (const [name, value] of flattenColors(tokens.color[mode], tokens.meta?.prefix ?? "dm")) {
      try {
        parseHex(value);
      } catch (err) {
        problems.push(`${mode} ${name}: ${err.message}`);
      }
    }
  }

  const lightKeys = flattenColors(tokens.color.light ?? {}, "x").map(([k]) => k).sort();
  const darkKeys = flattenColors(tokens.color.dark ?? {}, "x").map(([k]) => k).sort();
  if (JSON.stringify(lightKeys) !== JSON.stringify(darkKeys)) {
    problems.push("os modos light e dark precisam definir exatamente os mesmos tokens de cor");
  }

  for (const pair of tokens.contrast_pairs ?? []) {
    for (const mode of modes) {
      const fg = lookup(tokens.color[mode], pair.text);
      const bg = lookup(tokens.color[mode], pair.background);
      if (typeof fg !== "string" || typeof bg !== "string") {
        problems.push(`${mode}: par ${pair.text} / ${pair.background} aponta para token inexistente`);
        continue;
      }
      let ratio;
      try {
        ratio = contrastRatio(fg, bg);
      } catch (err) {
        problems.push(`${mode}: ${err.message}`);
        continue;
      }
      const min = pair.min ?? AA_MIN;
      if (ratio < min) {
        problems.push(`${mode}: ${pair.text} (${fg}) sobre ${pair.background} (${bg}) = ${ratio.toFixed(2)}:1, mínimo ${min}:1`);
      }
    }
  }
  if (!Array.isArray(tokens.contrast_pairs) || tokens.contrast_pairs.length === 0) {
    problems.push("contrast_pairs precisa listar os pares texto/fundo usados");
  }

  for (const [key, value] of Object.entries(tokens.space ?? {})) {
    if (!Number.isInteger(value) || value < 0 || value % SPACE_STEP !== 0) {
      problems.push(`space.${key} = ${value}: espaçamento precisa ser múltiplo de ${SPACE_STEP}`);
    }
  }
  if (tokens.radius?.md !== BASE_RADIUS) problems.push(`radius.md precisa ser ${BASE_RADIUS}`);

  for (const [key, value] of Object.entries(tokens.typography?.scale ?? {})) {
    if (!Number.isInteger(value?.size) || !Number.isInteger(value?.line) || value.line < value.size) {
      problems.push(`typography.scale.${key}: size e line inteiros, line >= size`);
    }
  }
  for (const [key, value] of Object.entries(tokens.motion?.duration ?? {})) {
    if (!Number.isInteger(value) || value < 0) problems.push(`motion.duration.${key} precisa ser inteiro em ms`);
  }
  return problems;
}

// ---------------------------------------------------------------------------- css

/** Renders tokens.css: light values on :root, dark values under prefers-color-scheme and data-theme. */
export function buildCss(tokens) {
  const prefix = tokens.meta?.prefix ?? "dm";
  const lines = [];
  lines.push("/* Code generated by packages/design-tokens/build.mjs from tokens.json. DO NOT EDIT. */");
  lines.push("/* Regenerate with `make -C packages/contracts gen`. */");
  lines.push("");
  lines.push(":root {");
  lines.push("  color-scheme: light dark;");
  lines.push("");
  lines.push("  /* typography */");
  for (const [key, value] of Object.entries(tokens.typography.family)) lines.push(`  ${cssName(prefix, "font", key)}: ${value};`);
  for (const [key, value] of Object.entries(tokens.typography.scale)) {
    lines.push(`  ${cssName(prefix, "text", key, "size")}: ${value.size}px;`);
    lines.push(`  ${cssName(prefix, "text", key, "line")}: ${value.line}px;`);
  }
  for (const [key, value] of Object.entries(tokens.typography.weight)) lines.push(`  ${cssName(prefix, "weight", key)}: ${value};`);
  for (const [key, value] of Object.entries(tokens.typography.letter_spacing)) lines.push(`  ${cssName(prefix, "tracking", key)}: ${value};`);
  lines.push("");
  lines.push(`  /* spacing (steps of ${SPACE_STEP}px) */`);
  for (const [key, value] of Object.entries(tokens.space)) lines.push(`  ${cssName(prefix, "space", key)}: ${value}px;`);
  lines.push("");
  lines.push("  /* radius, borders, shadows */");
  for (const [key, value] of Object.entries(tokens.radius)) lines.push(`  ${cssName(prefix, "radius", key)}: ${value}px;`);
  for (const [key, value] of Object.entries(tokens.border_width)) lines.push(`  ${cssName(prefix, "border", key)}: ${value}px;`);
  for (const [key, value] of Object.entries(tokens.shadow)) lines.push(`  ${cssName(prefix, "shadow", key)}: ${value};`);
  lines.push("");
  lines.push("  /* motion */");
  for (const [key, value] of Object.entries(tokens.motion.duration)) lines.push(`  ${cssName(prefix, "duration", key)}: ${value}ms;`);
  for (const [key, value] of Object.entries(tokens.motion.easing)) lines.push(`  ${cssName(prefix, "ease", key)}: ${value};`);
  lines.push("");
  lines.push("  /* layering and breakpoints (breakpoints are informational: media queries cannot read custom properties) */");
  for (const [key, value] of Object.entries(tokens.z_index)) lines.push(`  ${cssName(prefix, "z", key)}: ${value};`);
  for (const [key, value] of Object.entries(tokens.breakpoint)) lines.push(`  ${cssName(prefix, "breakpoint", key)}: ${value}px;`);
  lines.push("");
  lines.push("  /* colors: light */");
  for (const [name, value] of flattenColors(tokens.color.light, prefix)) lines.push(`  ${name}: ${value};`);
  lines.push("}");
  lines.push("");
  const dark = flattenColors(tokens.color.dark, prefix).map(([name, value]) => `    ${name}: ${value};`);
  lines.push("@media (prefers-color-scheme: dark) {");
  lines.push('  :root:not([data-theme="light"]) {');
  lines.push(...dark);
  lines.push("  }");
  lines.push("}");
  lines.push("");
  lines.push(':root[data-theme="dark"] {');
  lines.push(...dark.map((l) => l.slice(2)));
  lines.push("}");
  lines.push("");
  lines.push("@media (prefers-reduced-motion: reduce) {");
  lines.push("  :root {");
  for (const key of Object.keys(tokens.motion.duration)) lines.push(`    ${cssName(prefix, "duration", key)}: 0ms;`);
  lines.push("  }");
  lines.push("}");
  return `${lines.join("\n")}\n`;
}

// ---------------------------------------------------------------------------- cli

export function loadTokens(file = TOKENS_FILE) {
  return JSON.parse(readFileSync(file, "utf8"));
}

function main(argv) {
  const check = argv.includes("--check");
  const tokens = loadTokens();
  const problems = checkTokens(tokens);
  for (const p of problems) process.stderr.write(`[design-tokens] FALHOU: ${p}\n`);
  if (problems.length > 0) return 1;

  const css = buildCss(tokens);
  const pairs = (tokens.contrast_pairs ?? []).length;
  if (check) {
    let current = null;
    try {
      current = readFileSync(CSS_FILE, "utf8");
    } catch {
      current = null;
    }
    if (current !== css) {
      process.stderr.write(`[design-tokens] FALHOU: ${path.basename(CSS_FILE)} está desatualizado; rode \`node packages/design-tokens/build.mjs\`\n`);
      return 1;
    }
    process.stdout.write(`[design-tokens] ok: tokens.css atualizado, ${pairs} pares texto/fundo acima de ${AA_MIN}:1 em claro e escuro\n`);
    return 0;
  }
  writeFileSync(CSS_FILE, css, "utf8");
  process.stdout.write(`[design-tokens] escrito: ${path.relative(process.cwd(), CSS_FILE)} (${pairs} pares verificados)\n`);
  return 0;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(main(process.argv.slice(2)));
}
