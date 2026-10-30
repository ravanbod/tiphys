import type { ApiErrorBody, DecisionInfo } from './types';

/** Typed error for every non-2xx API response. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: Record<string, unknown>;

  constructor(status: number, code: string, message: string, details?: Record<string, unknown>) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }

  /** `403 denied` carries a DecisionInfo in `details`. */
  get decision(): DecisionInfo | null {
    if (this.code === 'denied' && this.details && 'reason' in this.details) {
      return this.details as unknown as DecisionInfo;
    }
    return null;
  }

  /** `422 invalid` field messages. */
  get fields(): Record<string, string> {
    const f = this.details?.fields;
    return f && typeof f === 'object' ? (f as Record<string, string>) : {};
  }

  /** `422` privilege check violations when adding a cluster. */
  get violations(): string[] {
    const v = this.details?.violations;
    return Array.isArray(v) ? v.map(String) : [];
  }
}

const FALLBACK_CODES: Record<number, string> = {
  401: 'unauthenticated',
  403: 'forbidden_role',
  404: 'not_found',
  409: 'conflict',
  422: 'invalid',
  429: 'rate_limited',
  502: 'cluster_error',
  503: 'recording_unavailable',
};

function isErrorBody(x: unknown): x is ApiErrorBody {
  if (!x || typeof x !== 'object') return false;
  const e = (x as { error?: unknown }).error;
  return !!e && typeof e === 'object' && typeof (e as { code?: unknown }).code === 'string';
}

/** Maps an HTTP status and raw response text to an ApiError. Never throws. */
export function toApiError(status: number, bodyText: string): ApiError {
  let parsed: unknown = null;
  try {
    parsed = bodyText ? JSON.parse(bodyText) : null;
  } catch {
    parsed = null;
  }
  if (isErrorBody(parsed)) {
    const { code, message, details } = parsed.error;
    return new ApiError(status, code, typeof message === 'string' ? message : code, details);
  }
  const code = FALLBACK_CODES[status] ?? (status >= 500 ? 'server_error' : 'http_error');
  const message = bodyText && bodyText.length < 200 && !bodyText.trim().startsWith('<') ? bodyText.trim() : `Request failed (${status})`;
  return new ApiError(status, code, message || `Request failed (${status})`);
}

/** Short human text for an error, used in banners and toasts. */
export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === 'rate_limited') return 'Too many actions in a minute. Wait a moment and try again.';
    if (e.code === 'recording_unavailable') return 'Shells are unavailable: session recording storage is down.';
    return e.message;
  }
  if (e instanceof Error) return e.message;
  return 'Something went wrong';
}
