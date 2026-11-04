// Typed access to packages/contracts/limits.yaml (loaded at build time by vite/limits.ts).
// Nothing here retypes a number: every cap is derived from the YAML.
import limits from 'virtual:limits';

export { limits };

export const KB = 1024;
export const MB = 1024 * KB;
export const GB = 1024 * MB;

export const textLimits = limits.text;

/** Hard caps shared by every plan; a plan can tighten them through `Partial<FileCaps>`. */
export const fileCaps = {
  documentFileBytes: limits.files.document_file_max_mb * MB,
  documentRunBytes: limits.files.document_run_max_mb * MB,
  mediaFileBytes: limits.files.media_file_max_gb * GB,
  mediaRunBytes: limits.files.media_run_max_gb * GB,
  filesPerRun: limits.ids.files_per_run_hard_max,
  mediaFilesPerRun: limits.ids.media_files_per_run_hard_max,
  fileNameBytes: limits.files.uploaded_file_name_bytes,
} as const;

export type FileCaps = { -readonly [K in keyof typeof fileCaps]: number };

export function resolveFileCaps(overrides?: Partial<FileCaps>): FileCaps {
  const caps: FileCaps = { ...fileCaps };
  if (!overrides) return caps;
  for (const key of Object.keys(caps) as Array<keyof FileCaps>) {
    const value = overrides[key];
    // Plans can only tighten the shared hard caps, never exceed them.
    if (typeof value === 'number' && value > 0 && value < caps[key]) caps[key] = value;
  }
  return caps;
}

/** The character counter becomes visible from 80% of the limit. */
export function counterThreshold(max: number): number {
  return Math.ceil(max * 0.8);
}

/** Limits are code points after NFC; JS strings count UTF-16 units, so count properly. */
export function countChars(value: string): number {
  let count = 0;
  for (const _ of value.normalize('NFC')) count += 1;
  return count;
}
