// Shared request and error types; no browser session or transport dependencies.
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;
  readonly details: unknown;

  constructor(
    status: number,
    code: string,
    message: string,
    requestId?: string,
    details?: unknown,
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.details = details;
  }
}

export interface RequestOptions {
  /** Frozen destination for this operation. null is an identity-only request. */
  orgId?: string | null;
  method?: string;
  responseType?: 'blob' | 'stream';
  body?: unknown;
  signal?: AbortSignal;
  /** Adds an Idempotency-Key, required by the endpoints that spend tokens. */
  idempotent?: boolean;
  /** Reuse this key when retrying the same purchase. */
  idempotencyKey?: string;
  /** Skip the silent refresh on an expired access window (used by the refresh itself). */
  noRefresh?: boolean;
}
