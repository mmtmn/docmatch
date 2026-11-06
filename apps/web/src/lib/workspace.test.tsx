import { act, renderHook, waitFor, cleanup } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { api } from './api';
import { SESSION_CHANGED_EVENT } from './session';
import { useWorkspace, defaultManifest } from './workspace';
vi.mock('./api', () => ({ api: vi.fn(), apiConfigured: () => true }));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
it('drops a previous tenant response after the session changes', async () => {
  let finishOld!: (value: unknown) => void;
  vi.mocked(api).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finishOld = resolve;
      }),
  );
  vi.mocked(api).mockResolvedValueOnce({
    manifest: { ...defaultManifest, findings_label: 'New tenant' },
    version: 1,
  });
  const { result } = renderHook(() => useWorkspace());
  act(() => {
    window.dispatchEvent(new Event(SESSION_CHANGED_EVENT));
  });
  await waitFor(() => expect(result.current?.manifest.findings_label).toBe('New tenant'));
  await act(async () => {
    finishOld({ manifest: { ...defaultManifest, findings_label: 'Old tenant' } });
    await Promise.resolve();
  });
  expect(result.current?.manifest.findings_label).toBe('New tenant');
  vi.mocked(api).mockRejectedValueOnce(new Error('signed out'));
  act(() => {
    window.dispatchEvent(new Event(SESSION_CHANGED_EVENT));
  });
  expect(result.current).toBeNull();
  await waitFor(() => expect(api).toHaveBeenCalledTimes(3));
});
