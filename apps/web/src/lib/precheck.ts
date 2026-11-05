// Client-side precheck of dropped files: magic bytes, extension consistency and size caps from
// packages/contracts/limits.yaml. The server repeats every check; this only saves a round trip
// and gives an immediate pt-BR reason. Nothing here trusts `File.type` (browsers guess it from
// the extension).
import { t, type MessageKey } from './i18n';
import { formatBytes } from './format';
import { resolveFileCaps, type FileCaps } from './limits';

export type FileKind =
  'csv' | 'txt' | 'xlsx' | 'pdf' | 'zip' | 'eml' | 'ofx' | 'image' | 'audio' | 'video';

export type FileFamily = 'document' | 'media';

export interface AcceptedFile {
  id: string;
  file: File;
  name: string;
  size: number;
  kind: FileKind;
  family: FileFamily;
}

export interface RejectedFile {
  id: string;
  name: string;
  size: number;
  /** Already localized. */
  reason: string;
}

export interface PrecheckResult {
  accepted: AcceptedFile[];
  rejected: RejectedFile[];
}

export interface PrecheckOptions {
  /** Files already in the list; used for duplicates and per-run totals. */
  existing?: readonly AcceptedFile[];
  /** Plan caps (never looser than the shared hard caps). */
  caps?: Partial<FileCaps>;
  /** Bytes read from the head of each file for sniffing. */
  headBytes?: number;
}

export const KIND_LABEL_KEY: Record<FileKind, MessageKey> = {
  csv: 'dropzone.kind_csv',
  txt: 'dropzone.kind_txt',
  xlsx: 'dropzone.kind_xlsx',
  pdf: 'dropzone.kind_pdf',
  zip: 'dropzone.kind_zip',
  eml: 'dropzone.kind_eml',
  ofx: 'dropzone.kind_ofx',
  image: 'dropzone.kind_image',
  audio: 'dropzone.kind_audio',
  video: 'dropzone.kind_video',
};

export function familyOf(kind: FileKind): FileFamily {
  return kind === 'audio' || kind === 'video' ? 'media' : 'document';
}

/** Extension → kind. CNAB return/remittance files (.ret/.rem) are fixed-width text. */
const EXTENSIONS: Readonly<Record<string, FileKind>> = {
  csv: 'csv',
  tsv: 'csv',
  txt: 'txt',
  text: 'txt',
  md: 'txt',
  log: 'txt',
  ret: 'txt',
  rem: 'txt',
  cnab: 'txt',
  xlsx: 'xlsx',
  xlsm: 'xlsx',
  xls: 'xlsx',
  pdf: 'pdf',
  zip: 'zip',
  eml: 'eml',
  ofx: 'ofx',
  png: 'image',
  jpg: 'image',
  jpeg: 'image',
  gif: 'image',
  webp: 'image',
  heic: 'image',
  heif: 'image',
  tif: 'image',
  tiff: 'image',
  bmp: 'image',
  mp3: 'audio',
  wav: 'audio',
  m4a: 'audio',
  aac: 'audio',
  flac: 'audio',
  ogg: 'audio',
  oga: 'audio',
  opus: 'audio',
  weba: 'audio',
  mp4: 'video',
  m4v: 'video',
  mov: 'video',
  webm: 'video',
  mkv: 'video',
  avi: 'video',
};

const TEXT_KINDS: ReadonlySet<FileKind> = new Set(['csv', 'txt', 'eml', 'ofx']);

/** Value for `<input type="file" accept>`: every known extension plus the media families. */
export const ACCEPT_ATTRIBUTE = [
  ...Object.keys(EXTENSIONS).map((ext) => `.${ext}`),
  'image/*',
  'audio/*',
  'video/*',
].join(',');

const AUDIO_MIME: Readonly<Record<string, string>> = {
  mp3: 'audio/mpeg',
  wav: 'audio/wav',
  m4a: 'audio/mp4',
  aac: 'audio/aac',
  flac: 'audio/flac',
  ogg: 'audio/ogg',
  oga: 'audio/ogg',
  opus: 'audio/opus',
  weba: 'audio/webm',
};

const VIDEO_MIME: Readonly<Record<string, string>> = {
  mp4: 'video/mp4',
  m4v: 'video/mp4',
  mov: 'video/quicktime',
  webm: 'video/webm',
  mkv: 'video/x-matroska',
  avi: 'video/x-msvideo',
};

/**
 * The MIME sent with the upload: one the API's scanner accepts for the kind, never the browser's
 * guess (an .ofx or .ret has none). Images keep the browser's type. testdata/precheck/cases.json
 * holds the same table for the apps.
 */
export function uploadMime(kind: FileKind, name: string, browserType = ''): string {
  const ext = extensionOf(name);
  switch (kind) {
    case 'csv':
      return ext === 'tsv' ? 'text/tab-separated-values' : 'text/csv';
    case 'txt':
      return 'text/plain';
    case 'xlsx':
      return ext === 'xls'
        ? 'application/vnd.ms-excel'
        : 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet';
    case 'pdf':
      return 'application/pdf';
    case 'zip':
      return 'application/zip';
    case 'eml':
      return 'message/rfc822';
    case 'ofx':
      return 'application/x-ofx';
    case 'audio':
      return (ext && AUDIO_MIME[ext]) ?? 'audio/mpeg';
    case 'video':
      return (ext && VIDEO_MIME[ext]) ?? 'video/mp4';
    case 'image':
      return browserType || 'application/octet-stream';
  }
}

export function extensionOf(name: string): string | null {
  const match = /\.([A-Za-z0-9]{1,8})$/.exec(name.trim());
  return match ? match[1]!.toLowerCase() : null;
}

function startsWith(bytes: Uint8Array, signature: readonly number[], offset = 0): boolean {
  if (bytes.length < offset + signature.length) return false;
  return signature.every((byte, i) => bytes[offset + i] === byte);
}

function ascii(bytes: Uint8Array, offset: number, length: number): string {
  let out = '';
  for (let i = offset; i < Math.min(bytes.length, offset + length); i += 1) {
    out += String.fromCharCode(bytes[i]!);
  }
  return out;
}

export function looksLikeText(bytes: Uint8Array): boolean {
  if (bytes.length === 0) return false;
  let control = 0;
  for (const byte of bytes) {
    if (byte === 0) return false;
    if (byte < 32 && byte !== 9 && byte !== 10 && byte !== 13 && byte !== 12 && byte !== 27) {
      control += 1;
    }
  }
  return control / bytes.length < 0.02;
}

const HEADER_LINE = /^[A-Za-z][A-Za-z0-9-]*:[ \t]/;

export function looksLikeEmail(text: string): boolean {
  const body = text.replace(/^\uFEFF/, '');
  const lines = body.split(/\r?\n/, 40);
  const first = lines[0] ?? '';
  if (!(first.startsWith('From ') || HEADER_LINE.test(first))) return false;
  let headers = 0;
  for (const line of lines) {
    if (line === '') break;
    if (HEADER_LINE.test(line)) headers += 1;
  }
  return headers >= 2;
}

export function looksLikeOfx(text: string): boolean {
  const head = text.slice(0, 2048).toUpperCase();
  return head.includes('OFXHEADER') || head.includes('<OFX>') || head.includes('<?OFX');
}

const decoder = new TextDecoder('utf-8', { fatal: false });

/**
 * Identifies the kind from the first bytes. `ext` disambiguates containers (zip → xlsx/zip,
 * mp4 → audio/video). Returns null when the content is not something we accept.
 */
export function sniffKind(bytes: Uint8Array, ext: string | null): FileKind | null {
  const extKind = ext ? (EXTENSIONS[ext] ?? null) : null;

  if (startsWith(bytes, [0x25, 0x50, 0x44, 0x46])) return 'pdf'; // %PDF
  if (
    startsWith(bytes, [0x50, 0x4b, 0x03, 0x04]) ||
    startsWith(bytes, [0x50, 0x4b, 0x05, 0x06]) ||
    startsWith(bytes, [0x50, 0x4b, 0x07, 0x08])
  ) {
    if (extKind === 'xlsx') return 'xlsx';
    if (extKind === 'zip') return 'zip';
    return null; // other OOXML/zip-based formats are not accepted
  }
  if (startsWith(bytes, [0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1])) {
    return extKind === 'xlsx' ? 'xlsx' : null; // legacy Excel (CFB container)
  }
  if (startsWith(bytes, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])) return 'image';
  if (startsWith(bytes, [0xff, 0xd8, 0xff])) return 'image';
  if (startsWith(bytes, [0x47, 0x49, 0x46, 0x38])) return 'image'; // GIF8
  if (startsWith(bytes, [0x49, 0x49, 0x2a, 0x00]) || startsWith(bytes, [0x4d, 0x4d, 0x00, 0x2a])) {
    return 'image'; // TIFF
  }
  if (startsWith(bytes, [0x42, 0x4d]) && extKind === 'image') return 'image'; // BMP needs the ext
  if (startsWith(bytes, [0x52, 0x49, 0x46, 0x46])) {
    const form = ascii(bytes, 8, 4);
    if (form === 'WEBP') return 'image';
    if (form === 'WAVE') return 'audio';
    if (form === 'AVI ') return 'video';
    return null;
  }
  if (startsWith(bytes, [0x49, 0x44, 0x33])) return 'audio'; // ID3
  if (startsWith(bytes, [0x66, 0x4c, 0x61, 0x43])) return 'audio'; // fLaC
  if (startsWith(bytes, [0x4f, 0x67, 0x67, 0x53])) return extKind === 'video' ? 'video' : 'audio'; // OggS
  if (bytes.length >= 2 && bytes[0] === 0xff && (bytes[1]! & 0xe6) === 0xe2) {
    return 'audio'; // MPEG audio frame sync (MP3 / ADTS AAC)
  }
  if (startsWith(bytes, [0x1a, 0x45, 0xdf, 0xa3])) {
    return extKind === 'audio' ? 'audio' : 'video'; // EBML: webm / mkv
  }
  if (ascii(bytes, 4, 4) === 'ftyp') {
    const brand = ascii(bytes, 8, 4);
    if (['heic', 'heix', 'hevc', 'hevx', 'mif1', 'msf1'].includes(brand)) return 'image';
    if (brand === 'M4A ') return 'audio';
    if (extKind === 'audio') return 'audio';
    return 'video';
  }

  if (looksLikeText(bytes)) {
    const text = decoder.decode(bytes);
    if (looksLikeEmail(text)) return 'eml';
    if (looksLikeOfx(text)) return 'ofx';
    if (extKind === 'csv' || extKind === 'txt') return extKind;
    if (extKind === null && ext === null) return 'txt';
    if (extKind === 'eml' || extKind === 'ofx') return 'txt'; // reported as a mismatch below
    return null;
  }
  return null;
}

async function readHead(file: File, length: number): Promise<Uint8Array> {
  const slice = file.slice(0, length);
  if (typeof slice.arrayBuffer === 'function') {
    return new Uint8Array(await slice.arrayBuffer());
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
    reader.onerror = () => reject(reader.error ?? new Error('read'));
    reader.readAsArrayBuffer(slice);
  });
}

let counter = 0;
export function newFileId(): string {
  counter += 1;
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `f-${Date.now().toString(36)}-${counter}`;
}

function isDuplicate(file: File, list: readonly AcceptedFile[]): boolean {
  return list.some(
    (item) =>
      item.name === file.name &&
      item.size === file.size &&
      item.file.lastModified === file.lastModified,
  );
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

/**
 * Decides the kind of a file from its extension and content. Returns the kind or a rejection
 * reason key with the variables to interpolate.
 */
export function classify(
  name: string,
  bytes: Uint8Array,
): { kind: FileKind } | { reason: MessageKey; vars?: Record<string, string> } {
  const ext = extensionOf(name);
  const extKind = ext ? (EXTENSIONS[ext] ?? null) : null;
  const detected = sniffKind(bytes, ext);

  if (detected === null) {
    if (extKind !== null && looksLikeText(bytes) && !TEXT_KINDS.has(extKind)) {
      return { reason: 'errors.file_extension_mismatch', vars: { name, ext: ext ?? '' } };
    }
    if (extKind !== null && !looksLikeText(bytes) && TEXT_KINDS.has(extKind)) {
      return { reason: 'errors.file_extension_mismatch', vars: { name, ext: ext ?? '' } };
    }
    return { reason: 'errors.file_type_unknown', vars: { name } };
  }
  if (extKind !== null && extKind !== detected) {
    return { reason: 'errors.file_extension_mismatch', vars: { name, ext: ext ?? '' } };
  }
  if (extKind === null && ext !== null) {
    // Unknown extension: trust unambiguous content, never containers or plain text.
    if (detected === 'zip' || detected === 'xlsx' || TEXT_KINDS.has(detected)) {
      return { reason: 'errors.file_type_unknown', vars: { name } };
    }
  }
  return { kind: detected };
}

export async function precheckFiles(
  files: readonly File[],
  options: PrecheckOptions = {},
): Promise<PrecheckResult> {
  const caps = resolveFileCaps(options.caps);
  const headBytes = options.headBytes ?? 4096;
  const existing = options.existing ?? [];
  const accepted: AcceptedFile[] = [];
  const rejected: RejectedFile[] = [];

  let documentBytes = 0;
  let mediaBytes = 0;
  let documentCount = 0;
  let mediaCount = 0;
  for (const item of existing) {
    if (item.family === 'media') {
      mediaBytes += item.size;
      mediaCount += 1;
    } else {
      documentBytes += item.size;
      documentCount += 1;
    }
  }

  const reject = (file: File, reason: string): void => {
    rejected.push({ id: newFileId(), name: file.name, size: file.size, reason });
  };

  for (const file of files) {
    const name = file.name || '';
    if (byteLength(name) > caps.fileNameBytes) {
      reject(file, t('errors.file_name_too_long', { name, max: caps.fileNameBytes }));
      continue;
    }
    if (file.size === 0) {
      reject(file, t('errors.file_empty', { name }));
      continue;
    }
    if (isDuplicate(file, existing) || isDuplicate(file, accepted)) {
      reject(file, t('errors.file_duplicate', { name }));
      continue;
    }

    let head: Uint8Array;
    try {
      head = await readHead(file, headBytes);
    } catch {
      reject(file, t('errors.read_failed', { name }));
      continue;
    }

    const verdict = classify(name, head);
    if ('reason' in verdict) {
      reject(file, t(verdict.reason, verdict.vars ?? { name }));
      continue;
    }

    const kind = verdict.kind;
    const family = familyOf(kind);
    if (family === 'media') {
      if (file.size > caps.mediaFileBytes) {
        reject(file, t('errors.file_too_large', { name, max: formatBytes(caps.mediaFileBytes) }));
        continue;
      }
      if (mediaCount + 1 > caps.mediaFilesPerRun) {
        reject(file, t('errors.too_many_files', { max: caps.mediaFilesPerRun }));
        continue;
      }
      if (mediaBytes + file.size > caps.mediaRunBytes) {
        reject(file, t('errors.media_run_too_large', { max: formatBytes(caps.mediaRunBytes) }));
        continue;
      }
      mediaCount += 1;
      mediaBytes += file.size;
    } else {
      if (file.size > caps.documentFileBytes) {
        reject(
          file,
          t('errors.file_too_large', { name, max: formatBytes(caps.documentFileBytes) }),
        );
        continue;
      }
      if (documentCount + 1 > caps.filesPerRun) {
        reject(file, t('errors.too_many_files', { max: caps.filesPerRun }));
        continue;
      }
      if (documentBytes + file.size > caps.documentRunBytes) {
        reject(file, t('errors.run_too_large', { max: formatBytes(caps.documentRunBytes) }));
        continue;
      }
      documentCount += 1;
      documentBytes += file.size;
    }

    accepted.push({ id: newFileId(), file, name, size: file.size, kind, family });
  }

  return { accepted, rejected };
}
