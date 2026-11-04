// Tiny logger: user-facing output is pt-BR, one prefix per line so CI logs stay greppable.
const PREFIX = "[codegen]";

export function info(message) {
  process.stdout.write(`${PREFIX} ${message}\n`);
}

export function ok(message) {
  process.stdout.write(`${PREFIX} ok: ${message}\n`);
}

export function warn(message) {
  process.stderr.write(`${PREFIX} aviso: ${message}\n`);
}

export function fail(message) {
  process.stderr.write(`${PREFIX} FALHOU: ${message}\n`);
}

/** Prints a Markdown table of step results. */
export function table(rows) {
  const lines = ["| etapa | resultado | detalhe |", "| --- | --- | --- |"];
  for (const r of rows) {
    const status = r.skipped ? "pulado" : r.ok ? "ok" : "FALHOU";
    lines.push(`| ${r.name} | ${status} | ${(r.detail ?? "").replace(/\|/g, "\\|")} |`);
  }
  process.stdout.write(`${lines.join("\n")}\n`);
}
