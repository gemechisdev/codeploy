import type { components } from './schema';

export type ErrorCode = components['schemas']['Error']['code'];

export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode | 'network';
  readonly details: Record<string, unknown>;
  readonly retryAfter: number | null;

  constructor(status: number, code: ErrorCode | 'network', message: string, details: Record<string, unknown> = {}, retryAfter: number | null = null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
    this.retryAfter = retryAfter;
  }

  /** Field-level messages from `validation_failed` (details.fields). */
  get fields(): Record<string, string> {
    const f = this.details.fields;
    return f && typeof f === 'object' ? (f as Record<string, string>) : {};
  }

  /** Per-check results from `preflight_failed` (details.checks). */
  get checks(): { key: string; label: string; ok: boolean; detail: string }[] {
    const c = this.details.checks;
    return Array.isArray(c) ? (c as { key: string; label: string; ok: boolean; detail: string }[]) : [];
  }

  static fromResponse(body: unknown, response: Response): ApiError {
    const retry = Number(response.headers.get('Retry-After'));
    const retryAfter = Number.isFinite(retry) && retry > 0 ? retry : null;
    if (body && typeof body === 'object' && 'code' in body && 'message' in body) {
      const b = body as components['schemas']['Error'];
      return new ApiError(response.status, b.code, b.message, (b.details as Record<string, unknown>) ?? {}, retryAfter);
    }
    return new ApiError(response.status, response.status === 401 ? 'unauthenticated' : 'internal', response.statusText || 'Request failed', {}, retryAfter);
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return 'Unexpected error';
}
