import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../../lib/api';
import { type projectClient, type ProjectDetail, type ProjectPatch } from '../../lib/projects';

export function useSavedProject(initial: ProjectDetail, client: ReturnType<typeof projectClient>) {
  const current = useRef(initial);
  const [project, setProject] = useState(initial);
  const [status, setStatus] = useState<'saved' | 'saving' | 'offline' | 'conflict'>('saved');
  const [error, setError] = useState<Error | null>(null);
  const pending = useRef<ProjectPatch>({});
  const writes = useRef(0);
  const chain = useRef<Promise<ProjectDetail>>(Promise.resolve(initial));
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

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
        writes.current++;
        setStatus('saving');
        try {
          const next = await client.update(current.current, patch);
          replace(next);
          setError(null);
          setStatus(Object.keys(pending.current).length ? 'saving' : 'saved');
          return next;
        } catch (cause) {
          pending.current = { ...patch, ...pending.current };
          const err = cause instanceof Error ? cause : new Error(String(cause));
          setError(err);
          setStatus(cause instanceof ApiError && cause.status === 409 ? 'conflict' : 'offline');
          throw err;
        } finally {
          writes.current--;
        }
      });
    return chain.current;
  }, [client, replace]);

  const change = useCallback(
    (patch: ProjectPatch) => {
      pending.current = { ...pending.current, ...patch };
      setStatus('saving');
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        void save().catch(() => undefined);
      }, 600);
    },
    [save],
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

  return { project, status, error, change, save, replace };
}
