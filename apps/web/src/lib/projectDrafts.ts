// Small, encrypted recovery copies of project text edits. Files stay on the server. Each
// editor has its own record, so two tabs cannot erase one another's unsaved work.
import type { ProjectPatch } from './projects';

export interface ProjectDraftScope {
  origin: string;
  userId: string;
  orgId: string;
  projectId: string;
}
export interface ProjectDraft {
  id: string;
  baseVersion: number;
  patch: Pick<ProjectPatch, 'name' | 'settings'>;
  savedAt: number;
}
export interface ProjectDraftStore {
  save(baseVersion: number, patch: ProjectDraft['patch']): Promise<boolean>;
  list(): Promise<ProjectDraft[]>;
  remove(id?: string): Promise<boolean>;
}
export const PROJECT_DRAFT_DB = 'docmatch-project-edits';
export const PROJECT_DRAFT_TTL = 30 * 24 * 60 * 60 * 1000;
const MAX_BYTES = 64 * 1024;
const MAX_RECORDS = 100;
const EPOCH_KEY = 'docmatch.project-edits.epoch';
let localEpoch = 0;

interface RecordData {
  id: string;
  scope: string;
  key: CryptoKey;
  iv: Uint8Array<ArrayBuffer>;
  cipher: ArrayBuffer;
  savedAt: number;
  expiresAt: number;
}
interface Options {
  indexedDB?: IDBFactory;
  now?: () => number;
}
const encode = new TextEncoder();
const decode = new TextDecoder();

function request<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error('IndexedDB failed'));
  });
}
function completed(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(tx.error ?? new Error('IndexedDB aborted'));
    tx.onerror = () => reject(tx.error ?? new Error('IndexedDB failed'));
  });
}
async function open(factory = globalThis.indexedDB): Promise<IDBDatabase> {
  if (!factory) throw new Error('IndexedDB unavailable');
  const req = factory.open(PROJECT_DRAFT_DB, 1);
  req.onupgradeneeded = () => {
    req.result.createObjectStore('drafts', { keyPath: 'id' }).createIndex('scope', 'scope');
    req.result.createObjectStore('meta');
  };
  return request(req);
}
function browserEpoch(): string | null {
  try {
    return globalThis.localStorage?.getItem(EPOCH_KEY) ?? null;
  } catch {
    return null;
  }
}

/** Log out all recovery copies in this browser origin, including writes still encrypting. */
export async function clearProjectDrafts(options: Options = {}): Promise<boolean> {
  localEpoch++;
  const epoch = crypto.randomUUID();
  try {
    globalThis.localStorage?.setItem(EPOCH_KEY, epoch);
  } catch {
    // IndexedDB's epoch still rejects writes already opened by another editor.
  }
  let db: IDBDatabase | undefined;
  try {
    db = await open(options.indexedDB);
    const tx = db.transaction(['drafts', 'meta'], 'readwrite');
    const done = completed(tx);
    tx.objectStore('drafts').clear();
    tx.objectStore('meta').put(epoch, 'epoch');
    await done;
    return true;
  } catch {
    return false;
  } finally {
    db?.close();
  }
}

export function createProjectDraftStore(
  scope: ProjectDraftScope,
  options: Options = {},
): ProjectDraftStore {
  const scopeKey = JSON.stringify([scope.origin, scope.userId, scope.orgId, scope.projectId]);
  const id = `${scopeKey}:${crypto.randomUUID()}`;
  const pageEpoch = localEpoch;
  const pageBrowserEpoch = browserEpoch();
  const now = options.now ?? Date.now;
  const live = () => pageEpoch === localEpoch && pageBrowserEpoch === browserEpoch();
  // Capture the persistent epoch when the editor opens, before encryption or network requests.
  const epoch = (async () => {
    const db = await open(options.indexedDB);
    try {
      const tx = db.transaction('meta', 'readwrite');
      const done = completed(tx);
      const store = tx.objectStore('meta');
      const existing = (await request(store.get('epoch'))) as string | undefined;
      const value = existing ?? crypto.randomUUID();
      if (!existing) store.put(value, 'epoch');
      await done;
      return value;
    } finally {
      db.close();
    }
  })();
  // Missing storage must be reported by save/list, never as an unhandled rejection.
  void epoch.catch(() => undefined);
  let chain: Promise<unknown> = Promise.resolve();
  const serialize = <T>(work: () => Promise<T>) => {
    const next = chain.then(work, work);
    chain = next.catch(() => undefined);
    return next;
  };
  async function transaction<T>(work: (store: IDBObjectStore) => Promise<T>): Promise<T> {
    const expectedEpoch = await epoch;
    if (!live()) throw new Error('Signed out');
    const db = await open(options.indexedDB);
    try {
      const tx = db.transaction(['drafts', 'meta'], 'readwrite');
      const done = completed(tx);
      // Attach the rejection handler before a request can abort this transaction.
      void done.catch(() => undefined);
      const current = (await request(tx.objectStore('meta').get('epoch'))) as string | undefined;
      if (current !== expectedEpoch || !live()) throw new Error('Signed out');
      const value = await work(tx.objectStore('drafts'));
      await done;
      return value;
    } finally {
      db.close();
    }
  }
  return {
    save(baseVersion, patch) {
      // Copy now: later edits cannot mutate a queued write. Never persist archive/share actions.
      const payload = encode.encode(
        JSON.stringify({
          baseVersion,
          patch: {
            ...(patch.name === undefined ? {} : { name: patch.name }),
            ...(patch.settings === undefined ? {} : { settings: patch.settings }),
          },
        }),
      );
      return serialize(async () => {
        try {
          if (
            !Number.isSafeInteger(baseVersion) ||
            baseVersion < 1 ||
            payload.byteLength > MAX_BYTES ||
            !live()
          )
            return false;
          const key = await crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, [
            'encrypt',
            'decrypt',
          ]);
          const iv = crypto.getRandomValues(new Uint8Array(12));
          const cipher = await crypto.subtle.encrypt(
            { name: 'AES-GCM', iv, additionalData: encode.encode(id) },
            key,
            payload,
          );
          return await transaction(async (store) => {
            const records = (await request(store.getAll())) as RecordData[];
            const savedAt = now();
            for (const record of records) if (record.expiresAt <= savedAt) store.delete(record.id);
            if (
              records.filter((record) => record.expiresAt > savedAt && record.id !== id).length >=
              MAX_RECORDS
            )
              return false;
            store.put({
              id,
              scope: scopeKey,
              key,
              iv,
              cipher,
              savedAt,
              expiresAt: savedAt + PROJECT_DRAFT_TTL,
            } satisfies RecordData);
            return true;
          });
        } catch {
          return false;
        }
      });
    },
    list() {
      return serialize(async () => {
        try {
          const records = await transaction(async (store) => {
            const records = (await request(store.index('scope').getAll(scopeKey))) as RecordData[];
            for (const record of records) if (record.expiresAt <= now()) store.delete(record.id);
            return records.filter((record) => record.expiresAt > now());
          });
          const drafts: ProjectDraft[] = [];
          for (const record of records) {
            try {
              const plaintext = await crypto.subtle.decrypt(
                { name: 'AES-GCM', iv: record.iv, additionalData: encode.encode(record.id) },
                record.key,
                record.cipher,
              );
              const payload = JSON.parse(decode.decode(plaintext)) as Pick<
                ProjectDraft,
                'baseVersion' | 'patch'
              >;
              if (
                payload.patch &&
                Number.isSafeInteger(payload.baseVersion) &&
                payload.baseVersion > 0
              )
                drafts.push({ ...payload, id: record.id, savedAt: record.savedAt });
            } catch {
              // A damaged record cannot become an edit or prevent opening the project.
            }
          }
          return live() ? drafts.sort((a, b) => b.savedAt - a.savedAt) : [];
        } catch {
          return [];
        }
      });
    },
    remove(target = id) {
      return serialize(async () => {
        try {
          return await transaction(async (store) => {
            const record = (await request(store.get(target))) as RecordData | undefined;
            if (record && record.scope !== scopeKey) return false;
            if (record) store.delete(target);
            return true;
          });
        } catch {
          return false;
        }
      });
    },
  };
}
