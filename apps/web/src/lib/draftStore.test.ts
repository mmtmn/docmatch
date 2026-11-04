import { IDBFactory, IDBObjectStore } from 'fake-indexeddb';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createDraftStore,
  decodeFilePayload,
  DRAFT_DB_NAME,
  DRAFT_TTL_MS,
  encodeFilePayload,
  type Draft,
} from './draftStore';

const DAY = 24 * 60 * 60 * 1000;

function draft(overrides: Partial<Draft> = {}): Draft {
  return {
    rulesText: 'ignorar centavos',
    useRules: true,
    webSearch: false,
    files: [
      {
        name: 'extrato.ofx',
        type: 'text/plain',
        lastModified: 10,
        blob: new Blob(['OFXHEADER:100']),
      },
      { name: 'nota.pdf', type: 'application/pdf', lastModified: 20, blob: new Blob(['%PDF-1.7']) },
    ],
    ...overrides,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('draftStore', () => {
  it('round-trips text fields and files through encrypted IndexedDB', async () => {
    const factory = new IDBFactory();
    const persist = vi.fn().mockResolvedValue(true);
    const store = createDraftStore({
      indexedDB: factory,
      storage: { persist } as unknown as StorageManager,
      now: () => 1_000,
    });

    expect(await store.available()).toBe(true);
    expect(await store.save(draft())).toEqual({ ok: true, skipped: [] });

    const loaded = await store.load();
    expect(loaded).not.toBeNull();
    expect(loaded?.rulesText).toBe('ignorar centavos');
    expect(loaded?.useRules).toBe(true);
    expect(loaded?.webSearch).toBe(false);
    expect(loaded?.savedAt).toBe(1_000);
    expect(loaded?.files.map((f) => [f.name, f.type, f.lastModified])).toEqual([
      ['extrato.ofx', 'text/plain', 10],
      ['nota.pdf', 'application/pdf', 20],
    ]);
    expect(await loaded!.files[1]!.blob.text()).toBe('%PDF-1.7');
    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(1));
  });

  it('stores only ciphertext: the rules text never appears in the database', async () => {
    const factory = new IDBFactory();
    const store = createDraftStore({ indexedDB: factory });
    await store.save(draft({ rulesText: 'SEGREDO-DO-RASCUNHO' }));

    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = factory.open(DRAFT_DB_NAME);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error ?? new Error('open'));
    });
    const records = await new Promise<unknown[]>((resolve) => {
      const request = db
        .transaction(['drafts', 'files'], 'readonly')
        .objectStore('drafts')
        .getAll();
      request.onsuccess = () => resolve(request.result as unknown[]);
    });
    const dump = JSON.stringify(records, (_key, value: unknown) =>
      value instanceof ArrayBuffer ? new TextDecoder().decode(value) : value,
    );
    expect(dump).not.toContain('SEGREDO-DO-RASCUNHO');
    expect(dump).toContain('expiresAt');
    db.close();
  });

  it('expires drafts after 30 days and clears them', async () => {
    const factory = new IDBFactory();
    let now = 0;
    const store = createDraftStore({ indexedDB: factory, now: () => now });
    await store.save(draft());
    now = DRAFT_TTL_MS - DAY;
    expect(await store.load()).not.toBeNull();
    now = DRAFT_TTL_MS + 1;
    expect(await store.load()).toBeNull();
    now = 0;
    expect(await store.load()).toBeNull();
  });

  it('clear removes the draft but keeps the key', async () => {
    const factory = new IDBFactory();
    const store = createDraftStore({ indexedDB: factory });
    await store.save(draft());
    expect(await store.clear()).toBe(true);
    expect(await store.load()).toBeNull();
    expect(await store.save(draft())).toMatchObject({ ok: true });
    expect((await store.load())?.files).toHaveLength(2);
  });

  it('skips files above the caps and keeps the rest', async () => {
    const factory = new IDBFactory();
    const store = createDraftStore({ indexedDB: factory, maxFileBytes: 10, maxTotalBytes: 12 });
    const result = await store.save(
      draft({
        files: [
          { name: 'ok.txt', type: 'text/plain', lastModified: 1, blob: new Blob(['12345']) },
          {
            name: 'grande.txt',
            type: 'text/plain',
            lastModified: 1,
            blob: new Blob(['x'.repeat(11)]),
          },
          {
            name: 'estoura-total.txt',
            type: 'text/plain',
            lastModified: 1,
            blob: new Blob(['12345678']),
          },
        ],
      }),
    );
    expect(result).toEqual({ ok: true, skipped: ['grande.txt', 'estoura-total.txt'] });
    expect((await store.load())?.files.map((f) => f.name)).toEqual(['ok.txt']);
  });

  it('is a no-op without IndexedDB or WebCrypto (private windows, old browsers)', async () => {
    const onError = vi.fn();
    const noIdb = createDraftStore({ indexedDB: undefined, onError });
    expect(await noIdb.available()).toBe(false);
    expect(await noIdb.save(draft())).toEqual({ ok: false, skipped: [] });
    expect(await noIdb.load()).toBeNull();
    expect(await noIdb.clear()).toBe(false);

    const noCrypto = createDraftStore({ indexedDB: new IDBFactory(), subtle: undefined, onError });
    expect(await noCrypto.available()).toBe(false);
    expect(await noCrypto.save(draft())).toMatchObject({ ok: false });
    expect(onError).toHaveBeenCalled();
  });

  it('survives an open() that throws (Firefox private mode)', async () => {
    const factory = {
      open: () => {
        throw new DOMException('nope', 'InvalidStateError');
      },
    } as unknown as IDBFactory;
    const onError = vi.fn();
    const store = createDraftStore({ indexedDB: factory, onError });
    expect(await store.available()).toBe(false);
    expect(await store.load()).toBeNull();
    expect(await store.save(draft())).toMatchObject({ ok: false });
    expect(onError).toHaveBeenCalledWith('open', expect.anything());
  });

  it('reports a quota error as ok:false and keeps working afterwards', async () => {
    const factory = new IDBFactory();
    const onError = vi.fn();
    const store = createDraftStore({ indexedDB: factory, onError });
    await store.save(draft());
    // eslint-disable-next-line @typescript-eslint/unbound-method -- re-bound with apply below
    const original = IDBObjectStore.prototype.put;
    const spy = vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementation(function (
      this: IDBObjectStore,
      ...args
    ) {
      if (this.name === 'files') throw new DOMException('cheio', 'QuotaExceededError');
      return original.apply(this, args);
    });
    expect(await store.save(draft({ rulesText: 'novo' }))).toMatchObject({ ok: false });
    expect(onError).toHaveBeenCalledWith(
      'save',
      expect.objectContaining({ name: 'QuotaExceededError' }),
    );
    spy.mockRestore();
    expect(await store.save(draft({ rulesText: 'depois' }))).toMatchObject({ ok: true });
    expect((await store.load())?.rulesText).toBe('depois');
  });

  it('drops an undecryptable draft instead of failing forever', async () => {
    const factory = new IDBFactory();
    const onError = vi.fn();
    const store = createDraftStore({ indexedDB: factory, onError });
    await store.save(draft());
    const db = await new Promise<IDBDatabase>((resolve) => {
      const request = factory.open(DRAFT_DB_NAME);
      request.onsuccess = () => resolve(request.result);
    });
    await new Promise<void>((resolve) => {
      const tx = db.transaction('drafts', 'readwrite');
      const os = tx.objectStore('drafts');
      const get = os.get('draft');
      get.onsuccess = () => {
        const record = get.result as { cipher: ArrayBuffer };
        record.cipher = new ArrayBuffer(16);
        os.put(record);
      };
      tx.oncomplete = () => resolve();
    });
    db.close();
    expect(await store.load()).toBeNull();
    expect(onError).toHaveBeenCalledWith('load', expect.anything());
    expect(await store.load()).toBeNull();
    expect(await store.save(draft({ rulesText: 'de novo' }))).toMatchObject({ ok: true });
    expect((await store.load())?.rulesText).toBe('de novo');
  });

  it('encodes and decodes the file payload framing', () => {
    const header = { name: 'ação.csv', type: 'text/csv', lastModified: 5, size: 3 };
    const payload = encodeFilePayload(header, Uint8Array.from([1, 2, 3]));
    const decoded = decodeFilePayload(payload);
    expect(decoded.header).toEqual(header);
    expect(Array.from(decoded.bytes)).toEqual([1, 2, 3]);
    expect(() => decodeFilePayload(Uint8Array.from([0, 0, 0, 9, 1]))).toThrow(/truncado/);
  });
});
