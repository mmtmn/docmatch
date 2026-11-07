import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../../lib/api';
import { type projectClient, type ProjectDetail, type ProjectPatch } from '../../lib/projects';
import type { ProjectDraft, ProjectDraftStore } from '../../lib/projectDrafts';

export function useSavedProject(
  initial: ProjectDetail,
  client: ReturnType<typeof projectClient>,
  recovery?: ProjectDraftStore,
) {
  const current = useRef(initial);
  const [project, setProject] = useState(initial);
  const [status, setStatus] = useState<'saved' | 'saving' | 'offline' | 'conflict'>('saved');
  const [error, setError] = useState<Error | null>(null);
  const pending = useRef<ProjectPatch>({});
  const inFlight = useRef<ProjectPatch>({});
  const recoveredIds = useRef(new Set<string>());
  const [drafts, setDrafts] = useState<ProjectDraft[]>([]);
  const [localSaved, setLocalSaved] = useState(false);
  const [recoveryFailed, setRecoveryFailed] = useState(false);
  const active = useRef(true);
  const writes = useRef(0);
  const chain = useRef<Promise<ProjectDetail>>(Promise.resolve(initial));
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => {
    active.current = true;
    if (recovery)
      void recovery.list().then((items) => {
        if (active.current) setDrafts(items);
      });
    return () => {
      active.current = false;
    };
  }, [recovery]);

  const preserve = useCallback(() => {
    if (!recovery) return;
    const patch = { ...inFlight.current, ...pending.current };
    const hasEdits = Object.keys(patch).length > 0;
    const write = hasEdits ? recovery.save(current.current.version, patch) : recovery.remove();
    void write.then((ok) => {
      if (!active.current) return;
      setLocalSaved(ok && hasEdits);
      setRecoveryFailed(!ok);
    });
  }, [recovery]);

  const replace = useCallback((next: ProjectDetail) => {
    current.current = next;
    setProject(next);
  }, []);

  const save = useCallback((): Promise<ProjectDetail> => {
    if (timer.current) clearTimeout(timer.current);
    chain.current = chain.current
      .catch(() => current.current)
      .then(async () => {
        const patch = pending.current;
        if (!Object.keys(patch).length) return current.current;
        pending.current = {};
        inFlight.current = patch;
        writes.current++;
        setStatus('saving');
        try {
          const next = await client.update(current.current, patch);
          replace(next);
          inFlight.current = {};
          preserve();
          if (recovery) {
            for (const id of recoveredIds.current) {
              void recovery.remove(id).then((ok) => {
                if (ok && active.current)
                  setDrafts((items) => items.filter((item) => item.id !== id));
              });
            }
            recoveredIds.current.clear();
          }
          setError(null);
          setStatus(Object.keys(pending.current).length ? 'saving' : 'saved');
          return next;
        } catch (cause) {
          pending.current = { ...patch, ...pending.current };
          inFlight.current = {};
          preserve();
          const err = cause instanceof Error ? cause : new Error(String(cause));
          setError(err);
          setStatus(cause instanceof ApiError && cause.status === 409 ? 'conflict' : 'offline');
          throw err;
        } finally {
          writes.current--;
        }
      });
    return chain.current;
  }, [client, preserve, recovery, replace]);

  const change = useCallback(
    (patch: ProjectPatch) => {
      pending.current = { ...pending.current, ...patch };
      preserve();
      setStatus('saving');
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        void save().catch(() => undefined);
      }, 600);
    },
    [preserve, save],
  );

  const restore = useCallback(
    (draft: ProjectDraft) => {
      // The caller presents the local/server versions and confirms before reaching this action.
      recoveredIds.current.add(draft.id);
      change(draft.patch);
      return save();
    },
    [change, save],
  );
  const discard = useCallback(
    async (draft: ProjectDraft) => {
      if (await recovery?.remove(draft.id))
        setDrafts((items) => items.filter((item) => item.id !== draft.id));
      else setRecoveryFailed(true);
    },
    [recovery],
  );

  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => {
      if (Object.keys(pending.current).length || writes.current) event.preventDefault();
    };
    window.addEventListener('beforeunload', warn);
    return () => {
      if (timer.current) clearTimeout(timer.current);
      window.removeEventListener('beforeunload', warn);
    };
  }, []);

  return {
    project,
    status,
    error,
    change,
    save,
    replace,
    drafts,
    localSaved,
    recoveryFailed,
    restore,
    discard,
  };
}
