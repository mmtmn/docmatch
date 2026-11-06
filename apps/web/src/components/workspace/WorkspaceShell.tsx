import type { ReactNode } from 'react';
import { useWorkspace } from '../../lib/workspace';
import './workspace.css';
export function WorkspaceShell({ children }: { children: ReactNode }) {
  const workspace = useWorkspace();
  return (
    <div
      className="workspace-shell"
      data-workspace-mode={workspace?.manifest.mode ?? 'system'}
      data-workspace-density={workspace?.manifest.density ?? 'comfortable'}
    >
      {children}
    </div>
  );
}
