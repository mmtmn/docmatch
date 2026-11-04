// Draft preservation: the files (as Blobs), the rules text and the two switches survive a reload,
// encrypted at rest with a non-extractable AES-GCM key that never leaves IndexedDB. Every call is
// wrapped: a private window, a full quota or a missing API just means "no draft", never a crash.
//
// Layout (database `docmatch-draft`, version 1):
//   keys   { id: 'draft', key: CryptoKey }
//   drafts { id: 'draft', iv, cipher, savedAt, expiresAt, fileCount }  cipher = JSON of the text fields
//   files  { id: 'draft:<n>', order, iv, cipher }                      cipher = header + bytes
//
// Each file's plaintext is: u32 big-endian header length, UTF-8 JSON header (name, type,
// lastModified, size), raw bytes. Files above `maxFileBytes` (or past `maxTotalBytes`) are left out
// of the draft and reported in `skipped`.

/** Byte views backed by a plain ArrayBuffer (what WebCrypto and IndexedDB accept). */
export type Bytes = Uint8Array<ArrayBuffer>;

export interface DraftFile {
  name: string;
  type: string;
  lastModified: number;
  blob: Blob;
}

export interface Draft {
  rulesText: string;
  useRules: boolean;
  webSearch: boolean;
  files: DraftFile[];
}

export interface LoadedDraft extends Draft {
  savedAt: number;
}

export interface SaveResult {
  ok: boolean;
  /** Names of files left out because of the size caps. */
  skipped: string[];
}

export interface DraftStoreOptions {
  dbName?: string;
  ttlMs?: number;
  maxFileBytes?: number;
  maxTotalBytes?: number;
  now?: () => number;
  indexedDB?: IDBFactory | undefined;
  subtle?: SubtleCrypto | undefined;
  getRandomValues?: (array: Bytes) => Bytes;
  storage?: StorageManager | undefined;
  onError?: (stage: string, error: unknown) => void;
}

export interface DraftStore {
  save(draft: Draft): Promise<SaveResult>;
  load(): Promise<LoadedDraft | null>;
  clear(): Promise<boolean>;
  available(): Promise<boolean>;
}

export const DRAFT_TTL_MS = 30 * 24 * 60 * 60 * 1000;
export const DRAFT_DB_NAME = 'docmatch-draft';
export const DRAFT_MAX_FILE_BYTES = 64 * 1024 * 1024;
export const DRAFT_MAX_TOTAL_BYTES = 256 * 1024 * 1024;

const DB_VERSION = 1;
const DRAFT_ID = 'draft';
const KEY_ID = 'draft';
const IV_BYTES = 12;

interface KeyRecord {
  id: string;
  key: CryptoKey;
}

interface DraftRecord {
  id: string;
  iv: Bytes;
  cipher: ArrayBuffer;
  savedAt: number;
  expiresAt: number;
  fileCount: number;
}

interface FileRecord {
  id: string;
  order: number;
  iv: Bytes;
  cipher: ArrayBuffer;
}

interface TextPayload {
  rulesText: string;
  useRules: boolean;
  webSearch: boolean;
}

interface FileHeader {
  name: string;
  type: string;
  lastModified: number;
  size: number;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

function requestToPromise<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error('IndexedDB request failed'));
  });
}

function transactionDone(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(tx.error ?? new Error('IndexedDB transaction aborted'));
    tx.onerror = () => reject(tx.error ?? new Error('IndexedDB transaction failed'));
  });
}

function concat(parts: Uint8Array[]): Bytes {
  const total = parts.reduce((sum, part) => sum + part.length, 0);
  const out = new Uint8Array(new ArrayBuffer(total));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

function toArrayBuffer(view: Uint8Array): ArrayBuffer {
  return view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength) as ArrayBuffer;
}

export function encodeFilePayload(header: FileHeader, bytes: Uint8Array): Bytes {
  const headerBytes = encoder.encode(JSON.stringify(header));
  const length = new Uint8Array(new ArrayBuffer(4));
  new DataView(length.buffer).setUint32(0, headerBytes.length, false);
  return concat([length, headerBytes, bytes]);
}

export function decodeFilePayload(payload: Uint8Array): { header: FileHeader; bytes: Uint8Array } {
  if (payload.length < 4) throw new Error('draft: payload truncado');
  const length = new DataView(payload.buffer, payload.byteOffset, 4).getUint32(0, false);
  if (payload.length < 4 + length) throw new Error('draft: cabeçalho truncado');
  const header = JSON.parse(decoder.decode(payload.subarray(4, 4 + length))) as FileHeader;
  return { header, bytes: payload.subarray(4 + length) };
}

export function createDraftStore(options: DraftStoreOptions = {}): DraftStore {
  const dbName = options.dbName ?? DRAFT_DB_NAME;
  const ttlMs = options.ttlMs ?? DRAFT_TTL_MS;
  const maxFileBytes = options.maxFileBytes ?? DRAFT_MAX_FILE_BYTES;
  const maxTotalBytes = options.maxTotalBytes ?? DRAFT_MAX_TOTAL_BYTES;
  const now = options.now ?? (() => Date.now());
  const onError = options.onError ?? (() => {});

  const factory = (): IDBFactory | undefined =>
    'indexedDB' in options ? options.indexedDB : globalThis.indexedDB;
  const subtle = (): SubtleCrypto | undefined =>
    'subtle' in options ? options.subtle : globalThis.crypto?.subtle;
  const randomValues = (array: Bytes): Bytes =>
    options.getRandomValues ? options.getRandomValues(array) : crypto.getRandomValues(array);
  const storageManager = (): StorageManager | undefined =>
    'storage' in options ? options.storage : globalThis.navigator?.storage;

  let dbPromise: Promise<IDBDatabase> | null = null;
  let persistenceRequested = false;

  function openDb(): Promise<IDBDatabase> {
    if (dbPromise) return dbPromise;
    const idb = factory();
    if (!idb) return Promise.reject(new Error('IndexedDB indisponível'));
    dbPromise = new Promise<IDBDatabase>((resolve, reject) => {
      let request: IDBOpenDBRequest;
      try {
        request = idb.open(dbName, DB_VERSION);
      } catch (error) {
        reject(error instanceof Error ? error : new Error(String(error)));
        return;
      }
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains('keys')) db.createObjectStore('keys', { keyPath: 'id' });
        if (!db.objectStoreNames.contains('drafts')) {
          db.createObjectStore('drafts', { keyPath: 'id' });
        }
        if (!db.objectStoreNames.contains('files'))
          db.createObjectStore('files', { keyPath: 'id' });
      };
      request.onblocked = () => reject(new Error('IndexedDB bloqueado'));
      request.onsuccess = () => {
        const db = request.result;
        db.onversionchange = () => {
          db.close();
          dbPromise = null;
        };
        db.onclose = () => {
          dbPromise = null;
        };
        resolve(db);
      };
      request.onerror = () => reject(request.error ?? new Error('IndexedDB open failed'));
    });
    dbPromise.catch(() => {
      dbPromise = null;
    });
    return dbPromise;
  }

  async function getKey(db: IDBDatabase): Promise<CryptoKey> {
    const crypto = subtle();
    if (!crypto) throw new Error('WebCrypto indisponível');
    const existing = await requestToPromise(
      db.transaction('keys', 'readonly').objectStore('keys').get(KEY_ID) as IDBRequest<
        KeyRecord | undefined
      >,
    );
    if (existing?.key) return existing.key;
    const key = await crypto.generateKey({ name: 'AES-GCM', length: 256 }, false, [
      'encrypt',
      'decrypt',
    ]);
    const tx = db.transaction('keys', 'readwrite');
    tx.objectStore('keys').put({ id: KEY_ID, key } satisfies KeyRecord);
    await transactionDone(tx);
    return key;
  }

  async function encrypt(
    key: CryptoKey,
    plain: Uint8Array,
  ): Promise<{ iv: Bytes; cipher: ArrayBuffer }> {
    const crypto = subtle();
    if (!crypto) throw new Error('WebCrypto indisponível');
    const iv = randomValues(new Uint8Array(new ArrayBuffer(IV_BYTES)));
    const cipher = await crypto.encrypt({ name: 'AES-GCM', iv }, key, toArrayBuffer(plain));
    return { iv, cipher };
  }

  async function decrypt(key: CryptoKey, iv: Bytes, cipher: ArrayBuffer): Promise<Uint8Array> {
    const crypto = subtle();
    if (!crypto) throw new Error('WebCrypto indisponível');
    return new Uint8Array(await crypto.decrypt({ name: 'AES-GCM', iv }, key, cipher));
  }

  async function requestPersistence(): Promise<void> {
    if (persistenceRequested) return;
    persistenceRequested = true;
    try {
      const storage = storageManager();
      if (storage && typeof storage.persist === 'function') await storage.persist();
    } catch (error) {
      onError('persist', error);
    }
  }

  async function clearStores(db: IDBDatabase): Promise<void> {
    const tx = db.transaction(['drafts', 'files'], 'readwrite');
    tx.objectStore('drafts').clear();
    tx.objectStore('files').clear();
    await transactionDone(tx);
  }

  async function save(draft: Draft): Promise<SaveResult> {
    const skipped: string[] = [];
    try {
      const db = await openDb();
      const key = await getKey(db);
      const savedAt = now();

      const text: TextPayload = {
        rulesText: draft.rulesText,
        useRules: draft.useRules,
        webSearch: draft.webSearch,
      };
      const textCipher = await encrypt(key, encoder.encode(JSON.stringify(text)));

      const fileRecords: FileRecord[] = [];
      let total = 0;
      for (const file of draft.files) {
        if (file.blob.size > maxFileBytes || total + file.blob.size > maxTotalBytes) {
          skipped.push(file.name);
          continue;
        }
        const bytes = new Uint8Array(await file.blob.arrayBuffer());
        const payload = encodeFilePayload(
          {
            name: file.name,
            type: file.type,
            lastModified: file.lastModified,
            size: file.blob.size,
          },
          bytes,
        );
        const { iv, cipher } = await encrypt(key, payload);
        fileRecords.push({
          id: `${DRAFT_ID}:${fileRecords.length}`,
          order: fileRecords.length,
          iv,
          cipher,
        });
        total += file.blob.size;
      }

      const tx = db.transaction(['drafts', 'files'], 'readwrite');
      tx.objectStore('drafts').clear();
      tx.objectStore('files').clear();
      tx.objectStore('drafts').put({
        id: DRAFT_ID,
        iv: textCipher.iv,
        cipher: textCipher.cipher,
        savedAt,
        expiresAt: savedAt + ttlMs,
        fileCount: fileRecords.length,
      } satisfies DraftRecord);
      for (const record of fileRecords) tx.objectStore('files').put(record);
      await transactionDone(tx);

      void requestPersistence();
      return { ok: true, skipped };
    } catch (error) {
      onError('save', error);
      return { ok: false, skipped };
    }
  }

  async function load(): Promise<LoadedDraft | null> {
    let db: IDBDatabase;
    try {
      db = await openDb();
    } catch (error) {
      onError('open', error);
      return null;
    }
    try {
      const record = await requestToPromise(
        db.transaction('drafts', 'readonly').objectStore('drafts').get(DRAFT_ID) as IDBRequest<
          DraftRecord | undefined
        >,
      );
      if (!record) return null;
      if (record.expiresAt <= now()) {
        await clearStores(db);
        return null;
      }
      const key = await getKey(db);
      const text = JSON.parse(
        decoder.decode(await decrypt(key, record.iv, record.cipher)),
      ) as TextPayload;
      const fileRecords = await requestToPromise(
        db.transaction('files', 'readonly').objectStore('files').getAll() as IDBRequest<
          FileRecord[]
        >,
      );
      fileRecords.sort((a, b) => a.order - b.order);
      const files: DraftFile[] = [];
      for (const fileRecord of fileRecords) {
        const { header, bytes } = decodeFilePayload(
          await decrypt(key, fileRecord.iv, fileRecord.cipher),
        );
        files.push({
          name: header.name,
          type: header.type,
          lastModified: header.lastModified,
          blob: new Blob([toArrayBuffer(bytes)], { type: header.type }),
        });
      }
      return {
        rulesText: typeof text.rulesText === 'string' ? text.rulesText : '',
        useRules: typeof text.useRules === 'boolean' ? text.useRules : true,
        webSearch: typeof text.webSearch === 'boolean' ? text.webSearch : false,
        files,
        savedAt: record.savedAt,
      };
    } catch (error) {
      // A draft that cannot be decrypted (key lost, corrupted record) is useless: drop it so the
      // next save starts clean instead of failing forever.
      onError('load', error);
      try {
        await clearStores(db);
      } catch (clearError) {
        onError('clear', clearError);
      }
      return null;
    }
  }

  async function clear(): Promise<boolean> {
    try {
      const db = await openDb();
      await clearStores(db);
      return true;
    } catch (error) {
      onError('clear', error);
      return false;
    }
  }

  async function available(): Promise<boolean> {
    if (!factory() || !subtle()) return false;
    try {
      await openDb();
      return true;
    } catch (error) {
      onError('available', error);
      return false;
    }
  }

  return { save, load, clear, available };
}

/** Default store used by the app. Created lazily so importing this module is side-effect free. */
let defaultStore: DraftStore | null = null;
export function draftStore(): DraftStore {
  defaultStore ??= createDraftStore();
  return defaultStore;
}
