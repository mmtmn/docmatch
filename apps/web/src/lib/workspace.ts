import { useEffect, useState } from 'react';
import { api, apiConfigured } from './api';
import { SESSION_CHANGED_EVENT } from './session';
export type WorkspaceManifest = {
  version: 1;
  mode: 'system' | 'light' | 'dark';
  density: 'comfortable' | 'compact';
  findings_label: string;
  group_by: 'none' | 'severity' | 'kind';
  sort_by: 'severity' | 'amount_at_risk' | 'confidence';
  export_format: 'xlsx' | 'csv' | 'pdf';
};
export type Workspace = {
  manifest: WorkspaceManifest;
  version: number;
  access: { eligible: boolean; can_request: boolean; limit: number; used: number };
  history: { version: number; created_at: string }[];
};
export const defaultManifest: WorkspaceManifest = {
  version: 1,
  mode: 'system',
  density: 'comfortable',
  findings_label: 'Achados',
  group_by: 'none',
  sort_by: 'severity',
  export_format: 'xlsx',
};
// No persistent browser cache: signing out or changing organization cannot reuse a tenant theme.
export function useWorkspace() {
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  useEffect(() => {
    if (!apiConfigured()) return;
    let active = true;
    let generation = 0;
    const refresh = () => {
      const current = ++generation;
      void api<Workspace>('/v1/workspace')
        .then((v) => {
          if (active && current === generation) setWorkspace(v);
        })
        .catch(() => {
          if (active && current === generation) setWorkspace(null);
        });
    };
    const reset = () => {
      setWorkspace(null);
      refresh();
    };
    refresh();
    window.addEventListener(SESSION_CHANGED_EVENT, reset);
    window.addEventListener('pageshow', reset);
    window.addEventListener('docmatch:workspace', refresh);
    return () => {
      active = false;
      window.removeEventListener('docmatch:workspace', refresh);
      window.removeEventListener(SESSION_CHANGED_EVENT, reset);
      window.removeEventListener('pageshow', reset);
    };
  }, []);
  return workspace;
}
export function workspaceChanged() {
  window.dispatchEvent(new Event('docmatch:workspace'));
}
