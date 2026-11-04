// CSV for Excel in pt-BR: semicolon separator, decimal comma, UTF-8 BOM, CRLF.
export const CSV_SEPARATOR = ';';

export function csvCell(value: string | number | null | undefined): string {
  if (value === null || value === undefined) return '';
  const text =
    typeof value === 'number'
      ? value.toLocaleString('pt-BR', { useGrouping: false, maximumFractionDigits: 2 })
      : value;
  // Prevent formula injection in free text (numbers are already safe) and quote when needed.
  const safe = typeof value === 'string' && /^[=+\-@\t\r]/.test(text) ? `'${text}` : text;
  return /[";\n\r]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe;
}

export function toCsv(
  header: readonly string[],
  rows: ReadonlyArray<ReadonlyArray<string | number>>,
): string {
  const lines = [header, ...rows].map((row) => row.map(csvCell).join(CSV_SEPARATOR));
  return `\uFEFF${lines.join('\r\n')}\r\n`;
}

/** Triggers a download in the browser; returns false outside one. */
export function downloadText(name: string, text: string, type = 'text/csv;charset=utf-8'): boolean {
  if (typeof document === 'undefined' || typeof URL.createObjectURL !== 'function') return false;
  const url = URL.createObjectURL(new Blob([text], { type }));
  const link = document.createElement('a');
  link.href = url;
  link.download = name;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  return true;
}
