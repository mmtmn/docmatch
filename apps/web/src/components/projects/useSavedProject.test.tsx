import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../lib/api';
import { projectClient, type ProjectDetail } from '../../lib/projects';
import { useSavedProject } from './useSavedProject';

const initial: ProjectDetail = {
  id: 'prj_1',
  name: 'Novembro',
  visibility: 'private',
  version: 1,
  revision: 1,
  document_count: 0,
  created_at: '',
  updated_at: '',
  archived_at: null,
  documents: [],
  capabilities: {
    manage_account: true,
    create_project: true,
    reconcile: true,
    read_results: true,
    upload: true,
  },
};
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
describe('server-confirmed project saving', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());
  it('keeps a newer edit pending while a slow earlier save finishes, using the new version', async () => {
    const first = deferred<ProjectDetail>();
    const second = deferred<ProjectDetail>();
    const update = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const client = { ...projectClient('org_1'), update };
    const { result } = renderHook(() => useSavedProject(initial, client));
    act(() => result.current.change({ name: 'Primeiro nome' }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(result.current.status).toBe('saving');
    act(() => result.current.change({ name: 'Nome final' }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(update).toHaveBeenCalledTimes(1);
    await act(async () => {
      first.resolve({ ...initial, version: 2, name: 'Primeiro nome' });
      await first.promise;
    });
    expect(update.mock.calls[1]).toEqual([
      { ...initial, version: 2, name: 'Primeiro nome' },
      { name: 'Nome final' },
    ]);
    expect(result.current.status).toBe('saving');
    await act(async () => {
      second.resolve({ ...initial, version: 3, name: 'Nome final' });
      await second.promise;
    });
    expect(result.current.status).toBe('saved');
    expect(result.current.project.name).toBe('Nome final');
  });
  it('retains an offline edit, warns before leaving, and only says saved after a successful retry', async () => {
    const update = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(0, 'network', 'Sem conexão'))
      .mockResolvedValueOnce({ ...initial, version: 2, name: 'Guardado' });
    const client = { ...projectClient('org_1'), update };
    const { result } = renderHook(() => useSavedProject(initial, client));
    act(() => result.current.change({ name: 'Guardado' }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(result.current.status).toBe('offline');
    expect(result.current.project.name).toBe('Novembro');
    const leaving = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(leaving);
    expect(leaving.defaultPrevented).toBe(true);
    await act(async () => {
      await result.current.save();
    });
    expect(result.current.status).toBe('saved');
    expect(update.mock.calls[1]).toEqual([initial, { name: 'Guardado' }]);
    const safe = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(safe);
    expect(safe.defaultPrevented).toBe(false);
  });
  it('does not silently overwrite another device when the server rejects an old version', async () => {
    const update = vi.fn().mockRejectedValue(new ApiError(409, 'project_changed', 'Mudou'));
    const client = { ...projectClient('org_1'), update };
    const { result } = renderHook(() => useSavedProject(initial, client));
    act(() => result.current.change({ name: 'Meu nome' }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(result.current.status).toBe('conflict');
    expect(result.current.project.version).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(update).toHaveBeenCalledTimes(1);
  });
});
