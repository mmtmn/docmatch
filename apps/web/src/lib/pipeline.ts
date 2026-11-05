// The run pipeline from the browser: upload each file through its presigned ticket, follow the
// security scan, ask for the estimate, start the run and read its findings.
import { api, ApiError } from './api';
import { apiUrl } from './env';
import { formatDuration } from './format';
import { t } from './i18n';
import { uploadMime, type AcceptedFile } from './precheck';

/** openapi UploadStatus: `ready` is a file that passed the security scan. */
export type UploadStatus = 'pending' | 'scanning' | 'ready' | 'rejected' | 'expired';

export interface UploadView {
  id: string;
  file_name: string;
  status: UploadStatus;
  rejected_reason?: string;
}

interface DocumentTicket {
  id: string;
  method: 'POST';
  url: string;
  fields: Record<string, string>;
}

interface MediaTicket {
  id: string;
  method: 'PUT';
  url: string;
  headers: Record<string, string>;
}

type Ticket = DocumentTicket | MediaTicket;

export interface QuoteView {
  id: string;
  expected_credits: number;
  cap_credits: number;
  signature: string;
  requires_approval: boolean;
  /** How long the run should take, queue included; absent on older quotes. */
  expected_seconds?: number;
  /** A large order: the confirmation says how long it takes and an e-mail follows. */
  large?: boolean;
}

/** The confirmation of a quote: the tokens, plus the expected duration of a large order. */
export function confirmText(quote: QuoteView): string {
  const lines = [
    t('app.confirm_run', { expected: quote.expected_credits, cap: quote.cap_credits }),
  ];
  if (quote.large && (quote.expected_seconds ?? 0) > 0) {
    lines.push(t('app.confirm_large', { duration: formatDuration(quote.expected_seconds ?? 0) }));
  }
  return lines.join('\n\n');
}

export interface RunOptionsInput {
  rulesText: string;
  useRules: boolean;
  webSearch: boolean;
  ruleIds: string[];
}

export interface RunSummary {
  findings_total: number;
  amount_at_risk: number;
  lines_total: number;
  lines_matched: number;
}

export interface RunView {
  id: string;
  status: 'queued' | 'running' | 'needs_approval' | 'completed' | 'failed' | 'cancelled';
  sources: UploadView[];
  summary?: RunSummary;
  error?: { code: string; message: string };
}

export interface EvidenceItem {
  source_id: string;
  locator?: { type?: string; row?: number } | null;
  excerpt?: string;
  label?: string;
  values?: { date?: string; description?: string; amount?: number; document?: string };
}

export interface FindingView {
  id: string;
  code: string;
  kind: string;
  severity: string;
  amount_at_risk: number;
  currency: string;
  title: string;
  explanation: string;
  confidence: number;
  confidence_label: string;
  evidence: EvidenceItem[];
  status: string;
}

const SCAN_POLL_MS = 1000;
const SCAN_MAX_POLLS = 180;

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener(
      'abort',
      () => {
        clearTimeout(timer);
        reject(new DOMException('aborted', 'AbortError'));
      },
      { once: true },
    );
  });
}

async function sha256Hex(file: File): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer());
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

async function putObject(ticket: Ticket, file: File, signal?: AbortSignal): Promise<void> {
  // Presigned requests are authorized by their signature, never by cookies.
  let res: Response;
  try {
    if (ticket.method === 'POST') {
      const form = new FormData();
      for (const [k, v] of Object.entries(ticket.fields)) form.append(k, v);
      form.append('file', file); // last: S3 ignores fields after the file
      res = await fetch(ticket.url, {
        method: 'POST',
        body: form,
        credentials: 'omit',
        ...(signal ? { signal } : {}),
      });
    } else {
      res = await fetch(ticket.url, {
        method: 'PUT',
        headers: ticket.headers,
        body: file,
        credentials: 'omit',
        ...(signal ? { signal } : {}),
      });
    }
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, 'network', t('errors.network'));
  }
  if (!res.ok)
    throw new ApiError(res.status, 'upload_failed', t('errors.server', { status: res.status }));
}

/** Uploads one file and waits for the scanner's verdict; rejects when the file is refused. */
export async function uploadFile(
  file: AcceptedFile,
  onScanning?: (name: string) => void,
  signal?: AbortSignal,
): Promise<UploadView> {
  const media = file.family === 'media';
  const sha256 = media ? undefined : await sha256Hex(file.file);
  const ticket = await api<Ticket>('/v1/uploads', {
    body: {
      kind: media ? 'media' : 'document',
      file_name: file.name,
      size_bytes: file.size,
      mime: uploadMime(file.kind, file.name, file.file.type),
      ...(sha256 ? { sha256 } : {}),
    },
    ...(signal ? { signal } : {}),
  });
  await putObject(ticket, file.file, signal);
  const path = `/v1/uploads/${encodeURIComponent(ticket.id)}/complete`;
  let view = await api<UploadView>(path, {
    body: sha256 ? { sha256 } : {},
    ...(signal ? { signal } : {}),
  });
  for (
    let i = 0;
    (view.status === 'pending' || view.status === 'scanning') && i < SCAN_MAX_POLLS;
    i++
  ) {
    onScanning?.(file.name);
    await sleep(SCAN_POLL_MS, signal);
    view = await api<UploadView>(path, { body: {}, ...(signal ? { signal } : {}) });
  }
  if (view.status !== 'ready') {
    throw new ApiError(
      422,
      'upload_rejected',
      t('app.rejected', { name: file.name, reason: view.rejected_reason ?? view.status }),
    );
  }
  return view;
}

function runBody(uploadIds: string[], opts: RunOptionsInput) {
  return {
    sources: uploadIds.map((id) => ({ upload_id: id })),
    ...(opts.rulesText.trim() ? { rules_text: opts.rulesText.trim() } : {}),
    options: {
      web_search: opts.webSearch,
      use_rules: opts.useRules,
      ...(opts.ruleIds.length ? { rule_ids: opts.ruleIds } : {}),
    },
  };
}

export function estimate(
  uploadIds: string[],
  opts: RunOptionsInput,
  signal?: AbortSignal,
): Promise<QuoteView> {
  return api<QuoteView>('/v1/runs/estimate', {
    body: runBody(uploadIds, opts),
    ...(signal ? { signal } : {}),
  });
}

export function createRun(
  quote: QuoteView,
  uploadIds: string[],
  opts: RunOptionsInput,
): Promise<RunView> {
  return api<RunView>('/v1/runs', {
    body: { ...runBody(uploadIds, opts), quote_id: quote.id, quote_signature: quote.signature },
    idempotent: true,
  });
}

export function getRun(id: string, signal?: AbortSignal): Promise<RunView> {
  return api<RunView>(`/v1/runs/${encodeURIComponent(id)}`, signal ? { signal } : {});
}

/** Every finding of a run (pages of 100). */
export async function listFindings(id: string, signal?: AbortSignal): Promise<FindingView[]> {
  const out: FindingView[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < 50; page++) {
    const q = new URLSearchParams({ limit: '100' });
    if (cursor) q.set('cursor', cursor);
    const res = await api<{ items: FindingView[]; next_cursor?: string }>(
      `/v1/runs/${encodeURIComponent(id)}/findings?${q.toString()}`,
      signal ? { signal } : {},
    );
    out.push(...res.items);
    if (!res.next_cursor) break;
    cursor = res.next_cursor;
  }
  return out;
}

export function sendFeedback(
  runId: string,
  findingId: string,
  status: string,
  note?: string,
): Promise<unknown> {
  return api(
    `/v1/runs/${encodeURIComponent(runId)}/findings/${encodeURIComponent(findingId)}/feedback`,
    {
      body: { status, ...(note ? { note } : {}) },
      idempotent: true,
    },
  );
}

export function exportUrl(runId: string, format: 'csv' | 'xlsx' | 'pdf'): string | undefined {
  return apiUrl(`/v1/runs/${encodeURIComponent(runId)}/export?format=${format}`);
}

export interface RunEvent {
  event: string;
  data: unknown;
}

/** Follows the run's SSE stream (cookies sent); returns the closer. */
export function watchRun(
  runId: string,
  onEvent: (e: RunEvent) => void,
  onError?: () => void,
): () => void {
  const url = apiUrl(`/v1/runs/${encodeURIComponent(runId)}/events`);
  if (!url || typeof EventSource === 'undefined') return () => {};
  const source = new EventSource(url, { withCredentials: true });
  for (const name of [
    'stage_started',
    'progress',
    'finding',
    'match',
    'needs_approval',
    'completed',
    'failed',
  ]) {
    source.addEventListener(name, (msg) => {
      let data: unknown;
      try {
        data = JSON.parse((msg as MessageEvent<string>).data);
      } catch {
        data = undefined;
      }
      onEvent({ event: name, data });
    });
  }
  source.onerror = () => onError?.();
  return () => source.close();
}
