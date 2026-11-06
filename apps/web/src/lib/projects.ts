import { api } from './api';
import type { Capabilities } from './accounts';
import type { QuoteView, RunView, UploadStatus } from './pipeline';

export interface ProjectSettings {
  rules_text?: string;
  use_rules: boolean;
  web_search: boolean;
  rule_ids?: string[];
  llm_mode?: string;
}
export interface Project {
  id: string;
  name: string;
  visibility: 'private' | 'team';
  version: number;
  revision: number;
  settings?: ProjectSettings;
  document_count: number;
  created_at: string;
  updated_at: string;
  archived_at: string | null;
  capabilities: Capabilities;
}
export interface ProjectDocument {
  id: string;
  upload_id: string;
  replaces_id?: string;
  pending_replacement_id?: string;
  file_name: string;
  role: string;
  status: UploadStatus;
  size_bytes: number;
  created_at: string;
  expires_at: string | null;
  removed_at: string | null;
}
export interface ProjectDetail extends Project {
  documents: ProjectDocument[];
  shared_with?: string[];
}
export interface ProjectEvent {
  id: number;
  action: string;
  created_at: string;
}
export interface ProjectRule {
  id: string;
  title: string;
  body: string;
  version: number;
  status: 'draft' | 'candidate' | 'verified' | 'trusted' | 'retired';
  provenance: { origin: string };
}

export interface Page<T> {
  items: T[];
  next_cursor?: string | null;
}
export interface ProjectAnalysis {
  state: 'ready' | 'unchanged' | 'queued' | 'running' | 'needs_approval' | 'expired';
  revision: number;
  quote?: QuoteView;
  run?: RunView;
}
export interface ProjectPatch {
  name?: string;
  settings?: ProjectSettings;
  archived?: boolean;
}

export function projectClient(orgId: string) {
  const path = (id: string) => `/v1/projects/${encodeURIComponent(id)}`;
  return {
    list: (archived = false, cursor?: string, signal?: AbortSignal) =>
      api<Page<Project>>(
        `/v1/projects?archived=${archived}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
        { orgId, ...(signal ? { signal } : {}) },
      ),
    create: (name: string) => api<Project>('/v1/projects', { orgId, body: { name } }),
    get: (id: string, signal?: AbortSignal) =>
      api<ProjectDetail>(path(id), { orgId, ...(signal ? { signal } : {}) }),
    update: (project: Project, patch: ProjectPatch) =>
      api<ProjectDetail>(path(project.id), {
        orgId,
        method: 'PATCH',
        body: { version: project.version, ...patch },
      }),
    remove: (project: Project) =>
      api<void>(`${path(project.id)}?version=${project.version}`, { orgId, method: 'DELETE' }),
    removeDocument: (project: Project, doc: ProjectDocument) =>
      api<void>(
        `${path(project.id)}/documents/${encodeURIComponent(doc.id)}?version=${project.version}`,
        { orgId, method: 'DELETE' },
      ),
    replaceDocument: (project: Project, previous: ProjectDocument, replacement: ProjectDocument) =>
      api<ProjectDetail>(
        `${path(project.id)}/documents/${encodeURIComponent(previous.id)}/replace`,
        {
          orgId,
          body: { version: project.version, replacement_id: replacement.id },
        },
      ),
    rules: (id: string, cursor?: string, signal?: AbortSignal) =>
      api<Page<ProjectRule>>(
        `${path(id)}/rules${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`,
        { orgId, ...(signal ? { signal } : {}) },
      ),
    createRule: (id: string, title: string, body: string) =>
      api<ProjectRule>(`${path(id)}/rules`, { orgId, body: { title, body }, idempotent: true }),
    versionRule: (id: string, ruleId: string, title: string, body: string) =>
      api<ProjectRule>(`${path(id)}/rules/${encodeURIComponent(ruleId)}/versions`, {
        orgId,
        body: { title, body },
        idempotent: true,
      }),
    promoteRule: (id: string, ruleId: string, to: 'verified' | 'trusted' | 'retired') =>
      api<ProjectRule>(`${path(id)}/rules/${encodeURIComponent(ruleId)}/promote`, {
        orgId,
        body: { to },
        idempotent: true,
      }),
    share: (project: Project, visibility: Project['visibility'], userIds: string[]) =>
      api<ProjectDetail>(`${path(project.id)}/sharing`, {
        orgId,
        method: 'PUT',
        body: { version: project.version, visibility, user_ids: userIds },
      }),
    activity: (id: string, signal?: AbortSignal) =>
      api<Page<ProjectEvent>>(`${path(id)}/activity`, { orgId, ...(signal ? { signal } : {}) }),
    runs: (id: string, signal?: AbortSignal) =>
      api<Page<RunView>>(`${path(id)}/runs`, { orgId, ...(signal ? { signal } : {}) }),
    estimate: (project: Project, reprocessReason?: string) =>
      api<ProjectAnalysis>(`${path(project.id)}/runs/estimate`, {
        orgId,
        body: {
          revision: project.revision,
          ...(reprocessReason ? { reprocess_reason: reprocessReason } : {}),
        },
      }),
    reconcile: (project: Project, quote: QuoteView, key: string, reprocessReason?: string) =>
      api<ProjectAnalysis>(`${path(project.id)}/runs`, {
        orgId,
        body: {
          revision: project.revision,
          quote_id: quote.id,
          quote_signature: quote.signature,
          ...(reprocessReason ? { reprocess_reason: reprocessReason } : {}),
        },
        idempotencyKey: key,
      }),
  };
}

export function projectResultUrl(orgId: string, projectId: string, runId: string): string {
  return `/resultados?${new URLSearchParams({ account: orgId, project: projectId, run: runId }).toString()}`;
}
