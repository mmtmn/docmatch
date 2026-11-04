// Identifier conversions shared by every emitter. The snake_case keys in limits.yaml and
// errors.yaml are the canonical names; each language gets its idiomatic casing from here.

// Acronyms that Go (and Swift member names) write in upper case.
const INITIALISMS = Object.freeze({
  id: "ID",
  ids: "IDs",
  api: "API",
  http: "HTTP",
  https: "HTTPS",
  json: "JSON",
  sse: "SSE",
  mb: "MB",
  gb: "GB",
  kb: "KB",
  url: "URL",
  uri: "URI",
  ui: "UI",
  sql: "SQL",
  tls: "TLS",
});

export function words(snake) {
  return String(snake).split("_").filter(Boolean);
}

function capitalize(word) {
  return word.charAt(0).toUpperCase() + word.slice(1);
}

/** UpperCamelCase treating acronyms as words: `invalid_api_key` -> `InvalidApiKey` (Rust, Kotlin). */
export function pascal(snake) {
  return words(snake).map(capitalize).join("");
}

/** UpperCamelCase with Go-style initialisms: `invalid_api_key` -> `InvalidAPIKey`. */
export function pascalGo(snake) {
  return words(snake)
    .map((w) => INITIALISMS[w] ?? capitalize(w))
    .join("");
}

/** lowerCamelCase with initialisms after the first word: `json_body_max_kb` -> `jsonBodyMaxKB` (Swift). */
export function camel(snake) {
  const [first, ...rest] = words(snake);
  return (first ?? "") + rest.map((w) => INITIALISMS[w] ?? capitalize(w)).join("");
}

/** SCREAMING_SNAKE_CASE (Rust and Kotlin constants). */
export function screaming(snake) {
  return String(snake).toUpperCase();
}

/** Escapes a string for a double-quoted literal in Go, Rust, Kotlin, Swift and TypeScript. */
export function quote(text) {
  return `"${String(text).replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\n/g, "\\n")}"`;
}

/** Single-quoted TypeScript literal. */
export function quoteTs(text) {
  return `'${String(text).replace(/\\/g, "\\\\").replace(/'/g, "\\'").replace(/\n/g, "\\n")}'`;
}

/** Pads `name` so that columns line up the way gofmt aligns them (one space before `=`). */
export function padTo(name, width) {
  return name + " ".repeat(Math.max(0, width - name.length));
}
