import { IDBFactory } from 'fake-indexeddb';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  clearProjectDrafts,
  createProjectDraftStore,
  PROJECT_DRAFT_DB,
  PROJECT_DRAFT_TTL,
  type ProjectDraftScope,
} from './projectDrafts';

const scope: ProjectDraftScope = {
  origin: 'https://docmatch.test',
  userId: 'usr_1',
  orgId: 'org_1',
  projectId: 'prj_1',
};
afterEach(() => vi.restoreAllMocks());

async function database(factory: IDBFactory) {
  return new Promise<IDBDatabase>((resolve) => {
    const req = factory.open(PROJECT_DRAFT_DB);
    req.onsuccess = () => resolve(req.result);
  });
}
async function records(factory: IDBFactory) {
  const db = await database(factory);
  try {
    return await new Promise<Record<string, unknown>[]>((resolve) => {
      const req = db.transaction('drafts').objectStore('drafts').getAll();
      req.onsuccess = () => resolve(req.result as Record<string, unknown>[]);
    });
  } finally {
    db.close();
  }
}

describe('encrypted project recovery copies', () => {
  it('survives reopening, stores no plaintext and uses a non-extractable key', async () => {
    const indexedDB = new IDBFactory();
    const store = createProjectDraftStore(scope, { indexedDB, now: () => 100 });
    const patch = {
      name: 'PRIVATE PROJECT NAME',
      settings: { rules_text: 'PRIVATE RULE', use_rules: true, web_search: false },
    };
    expect(await store.save(3, patch)).toBe(true);
    const reopened = createProjectDraftStore(scope, { indexedDB, now: () => 101 });
    expect(await reopened.list()).toEqual([
      expect.objectContaining({ baseVersion: 3, patch, savedAt: 100 }),
    ]);
    const stored = await records(indexedDB);
    expect(JSON.stringify(stored)).not.toContain('PRIVATE');
    expect((stored[0]!.key as CryptoKey).extractable).toBe(false);
    await expect(crypto.subtle.exportKey('raw', stored[0]!.key as CryptoKey)).rejects.toThrow();
  });

  it('isolates every identity, account, project and API origin', async () => {
    const indexedDB = new IDBFactory();
    const first = createProjectDraftStore(scope, { indexedDB });
    await first.save(1, { name: 'Only mine' });
    const [draft] = await first.list();
    for (const changed of [
      { userId: 'usr_2' },
      { orgId: 'org_2' },
      { projectId: 'prj_2' },
      { origin: 'https://other.test' },
    ]) {
      const other = createProjectDraftStore({ ...scope, ...changed }, { indexedDB });
      expect(await other.list()).toEqual([]);
      expect(await other.remove(draft!.id)).toBe(false);
    }
    expect(await first.list()).toHaveLength(1);
  });

  it('keeps simultaneous editors independent and removes only a selected recovery copy', async () => {
    const indexedDB = new IDBFactory();
    const a = createProjectDraftStore(scope, { indexedDB, now: () => 100 });
    const b = createProjectDraftStore(scope, { indexedDB, now: () => 200 });
    await Promise.all([a.save(1, { name: 'Tab A' }), b.save(1, { name: 'Tab B' })]);
    expect((await a.list()).map((d) => d.patch.name)).toEqual(['Tab B', 'Tab A']);
    await a.remove();
    expect((await b.list()).map((d) => d.patch.name)).toEqual(['Tab B']);
  });

  it('preserves write order and snapshots changes before asynchronous encryption', async () => {
    const indexedDB = new IDBFactory();
    const store = createProjectDraftStore(scope, { indexedDB });
    const patch = {
      name: 'First',
      settings: { rules_text: 'Original', use_rules: true, web_search: false },
    };
    const first = store.save(1, patch);
    patch.settings.rules_text = 'Later';
    await first;
    expect((await store.list())[0]!.patch.settings!.rules_text).toBe('Original');
    await Promise.all([store.save(1, { name: 'Second' }), store.save(2, { name: 'Newest' })]);
    expect((await store.list())[0]).toMatchObject({ baseVersion: 2, patch: { name: 'Newest' } });
  });

  it('expires text edits after 30 days and does not retain an oversized payload', async () => {
    const indexedDB = new IDBFactory();
    let now = 100;
    const store = createProjectDraftStore(scope, { indexedDB, now: () => now });
    await store.save(1, { name: 'Old' });
    expect(await store.save(1, { name: 'x'.repeat(70_000) })).toBe(false);
    now += PROJECT_DRAFT_TTL;
    expect(await store.list()).toEqual([]);
    expect(await records(indexedDB)).toEqual([]);
  });

  it('logout removes all copies and rejects a write that was already encrypting', async () => {
    const indexedDB = new IDBFactory();
    const store = createProjectDraftStore(scope, { indexedDB });
    await store.save(1, { name: 'Existing' });
    const encrypt = crypto.subtle.encrypt.bind(crypto.subtle);
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const spy = vi.spyOn(crypto.subtle, 'encrypt').mockImplementation(async (...args) => {
      await gate;
      return encrypt(...args);
    });
    const saving = store.save(1, { name: 'Late' });
    await vi.waitFor(() => expect(spy).toHaveBeenCalled());
    expect(await clearProjectDrafts({ indexedDB })).toBe(true);
    release();
    expect(await saving).toBe(false);
    expect(await records(indexedDB)).toEqual([]);
    expect(await store.save(1, { name: 'Signed out' })).toBe(false);
    expect(await store.list()).toEqual([]);
    const nextLogin = createProjectDraftStore(scope, { indexedDB });
    expect(await nextLogin.save(2, { name: 'New session' })).toBe(true);
  });

  it('handles unavailable storage without interrupting the project', async () => {
    const indexedDB = new IDBFactory();
    vi.spyOn(indexedDB, 'open').mockImplementation(() => {
      throw new DOMException('Blocked', 'SecurityError');
    });
    const store = createProjectDraftStore(scope, { indexedDB });
    expect(await store.save(1, { name: 'Unsaved' })).toBe(false);
    expect(await store.list()).toEqual([]);
    expect(await store.remove()).toBe(false);
    expect(await clearProjectDrafts({ indexedDB })).toBe(false);
  });

  it('rejects ciphertext moved to a different editor record', async () => {
    const indexedDB = new IDBFactory();
    const a = createProjectDraftStore(scope, { indexedDB });
    const b = createProjectDraftStore(scope, { indexedDB });
    await a.save(1, { name: 'A' });
    await b.save(1, { name: 'B' });
    const stored = await records(indexedDB);
    const db = await database(indexedDB);
    await new Promise<void>((resolve) => {
      const tx = db.transaction('drafts', 'readwrite');
      tx.objectStore('drafts').put({ ...stored[0], id: stored[1]!.id });
      tx.objectStore('drafts').delete(stored[0]!.id as string);
      tx.oncomplete = () => resolve();
    });
    db.close();
    expect(await b.list()).toEqual([]);
  });
});
