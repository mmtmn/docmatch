// The run pipeline from the browser: upload each file through its presigned ticket, follow the
// security scan, ask for the estimate, start the run and read its findings.
import { api, ApiError } from './api';
import { selectedAccount } from './accountContext';
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

/** All amounts in this source-backed snapshot are integer centavos. */
export interface ReconciliationDocument {
  version: number;
  title: string;
  currency: string;
  period_start: string;
  period_end: string;
  receivables: ReconciledReceivable[];
  bank_entries: {
    row: number;
    date: string;
    description: string;
    amount: number;
    kind: string;
    proposed: boolean;
    matched_ids: string[];
  }[];
  totals: {
    original_amount: number;
    duplicate_excluded: number;
    received_confirmed: number;
    principal_confirmed: number;
    open_balance: number;
    proposed_amount: number;
    projected_open_balance: number;
    unapplied_cash: number;
    bank_fees: number;
    fines_received: number;
    withholding_pending: number;
  };
}
export interface ReconciledReceivable {
  id: string;
  reference: string;
  customer: string;
  source_id: string;
  source_name: string;
  source_row: number;
  due_date: string;
  paid_on: string;
  original_amount: number;
  received_amount: number;
  applied_principal: number;
  open_amount: number;
  proposed_amount: number;
  status: string;
  duplicate_of: string;
}

export interface RunSummary {
  reconciliation?: ReconciliationDocument;
  findings_total: number;
  amount_at_risk: number;
  lines_total: number;
  lines_matched: number;
  /** Per file, in pt-BR, what did not take part in the reconciliation. */
  source_warnings?: string[];
}

export interface RunView {
  id: string;
  project_id?: string;
  status: 'queued' | 'running' | 'needs_approval' | 'completed' | 'failed' | 'cancelled';
  sources: UploadView[];
  created_at?: string;
  expires_at?: string;
  pending_quote?: QuoteView;
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
  feedback?: { note?: string };
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

async function putObject(
  ticket: Ticket,
  file: File,
  signal?: AbortSignal,
  allowedOrigins?: readonly string[],
): Promise<void> {
  // Presigned requests are authorized by their signature, never by cookies.
  if (allowedOrigins) {
    const target = new URL(ticket.url);
    if (
      !allowedOrigins.includes(target.origin) ||
      target.username ||
      target.password ||
      target.hash
    )
      throw new ApiError(0, 'upload_destination_invalid', t('errors.network'));
  }
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
        redirect: 'error',
        ...(signal ? { signal } : {}),
      });
    } else {
      res = await fetch(ticket.url, {
        method: 'PUT',
        headers: ticket.headers,
        body: file,
        credentials: 'omit',
        redirect: 'error',
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
  destination?: { orgId: string; projectId: string; replacesId?: string },
  transport?: { request: typeof api; uploadOrigins: readonly string[] },
): Promise<UploadView> {
  const request = transport?.request ?? api;
  const context = { orgId: destination?.orgId ?? selectedAccount()?.orgId ?? null };
  const media = file.family === 'media';
  const sha256 = media ? undefined : await sha256Hex(file.file);
  const prefix = destination
    ? `/v1/projects/${encodeURIComponent(destination.projectId)}/uploads`
    : '/v1/uploads';
  const ticket = await request<Ticket>(prefix, {
    ...context,
    body: {
      kind: media ? 'media' : 'document',
      file_name: file.name,
      size_bytes: file.size,
      mime: uploadMime(file.kind, file.name, file.file.type),
      ...(sha256 ? { sha256 } : {}),
      ...(destination?.replacesId ? { replaces_id: destination.replacesId } : {}),
    },
    ...(signal ? { signal } : {}),
  });
  await putObject(ticket, file.file, signal, transport?.uploadOrigins);
  const path = `${prefix}/${encodeURIComponent(ticket.id)}/complete`;
  let view = await request<UploadView>(path, {
    ...context,
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
    view = await request<UploadView>(path, { ...context, body: {}, ...(signal ? { signal } : {}) });
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

export interface RunDestination {
  orgId: string;
  projectId?: string;
}

function runPath(id: string, destination?: RunDestination): string {
  const prefix = destination?.projectId
    ? `/v1/projects/${encodeURIComponent(destination.projectId)}/runs`
    : '/v1/runs';
  return `${prefix}/${encodeURIComponent(id)}`;
}

export function getRun(
  id: string,
  signal?: AbortSignal,
  destination?: RunDestination,
  request: typeof api = api,
): Promise<RunView> {
  return request<RunView>(runPath(id, destination), {
    ...(destination ? { orgId: destination.orgId } : {}),
    ...(signal ? { signal } : {}),
  });
}

export function approveRun(
  id: string,
  quote: QuoteView,
  key: string,
  destination?: RunDestination,
  request: typeof api = api,
): Promise<RunView> {
  return request<RunView>(`${runPath(id, destination)}/approve-quote`, {
    ...(destination ? { orgId: destination.orgId } : {}),
    body: { quote_id: quote.id, quote_signature: quote.signature },
    idempotencyKey: key,
  });
}

export function cancelRun(
  id: string,
  destination?: RunDestination,
  request: typeof api = api,
): Promise<RunView> {
  return request<RunView>(`${runPath(id, destination)}/cancel`, {
    ...(destination ? { orgId: destination.orgId } : {}),
    method: 'POST',
    idempotent: true,
  });
}

/** Every finding of a run (pages of 100). */
export async function listFindings(
  id: string,
  signal?: AbortSignal,
  destination?: RunDestination,
  request: typeof api = api,
): Promise<FindingView[]> {
  const orgId = destination?.orgId ?? selectedAccount()?.orgId ?? null;
  const out: FindingView[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < 50; page++) {
    const q = new URLSearchParams({ limit: '100' });
    if (cursor) q.set('cursor', cursor);
    const res = await request<{ items: FindingView[]; next_cursor?: string }>(
      `${runPath(id, destination)}/findings?${q.toString()}`,
      { orgId, ...(signal ? { signal } : {}) },
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
  destination?: RunDestination,
  request: typeof api = api,
): Promise<unknown> {
  return request(
    `${runPath(runId, destination)}/findings/${encodeURIComponent(findingId)}/feedback`,
    {
      ...(destination ? { orgId: destination.orgId } : {}),
      body: { status, ...(note ? { note } : {}) },
      idempotent: true,
    },
  );
}

export function exportUrl(runId: string, format: 'csv' | 'xlsx' | 'pdf'): string | undefined {
  return apiUrl(`/v1/runs/${encodeURIComponent(runId)}/export?format=${format}`);
}

/** Download through the authenticated client, retaining session refresh and API errors. */
export async function downloadExport(
  runId: string,
  format: 'csv' | 'xlsx' | 'pdf',
  destination?: RunDestination,
  request: typeof api = api,
): Promise<void> {
  const blob = await request<Blob>(`${runPath(runId, destination)}/export?format=${format}`, {
    ...(destination ? { orgId: destination.orgId } : {}),
    responseType: 'blob',
  });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = `docmatch-${runId}.${format}`;
  document.body.append(link);
  link.click();
  link.remove();
  // Give the browser time to consume the download before releasing its backing bytes.
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

export interface RunEvent {
  event: string;
  data: unknown;
}

/** Fetch supports the explicit account header; native EventSource cannot send it. */
export function watchRun(
  runId: string,
  onEvent: (e: RunEvent) => void,
  onError?: () => void,
  destination?: RunDestination,
): () => void {
  const controller = new AbortController();
  const orgId = destination?.orgId ?? selectedAccount()?.orgId ?? null;
  void (async () => {
    const stream = await api<ReadableStream<Uint8Array>>(`${runPath(runId, destination)}/events`, {
      orgId,
      responseType: 'stream',
      signal: controller.signal,
    });
    if (!stream) return;
    const reader = stream.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    try {
      while (!controller.signal.aborted) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer = (buffer + decoder.decode(value, { stream: true })).replace(/\r\n/g, '\n');
        let end = buffer.indexOf('\n\n');
        while (end >= 0) {
          const frame = buffer.slice(0, end);
          buffer = buffer.slice(end + 2);
          let event = '';
          const data: string[] = [];
          for (const line of frame.split('\n')) {
            if (line.startsWith('event:')) event = line.slice(6).trim();
            if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
          }
          if (event && data.length && !controller.signal.aborted) {
            try {
              onEvent({ event, data: JSON.parse(data.join('\n')) as unknown });
            } catch {
              /* Ignore an incomplete or malformed event; polling still recovers state. */
            }
          }
          end = buffer.indexOf('\n\n');
        }
      }
    } finally {
      reader.releaseLock();
    }
  })().catch(() => {
    if (!controller.signal.aborted) onError?.();
  });
  return () => controller.abort();
}
