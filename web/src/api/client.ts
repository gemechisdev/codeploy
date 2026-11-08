import createClient from 'openapi-fetch';
import type { paths } from './schema';
import { ApiError } from './errors';
import { getCsrf, syncServerNow } from './session';
import { stepUpStore } from './stepup';

export const API_MODE = (import.meta.env.VITE_API_MODE ?? 'live') as 'mock' | 'live';
export const API_BASE: string = import.meta.env.VITE_API_BASE ?? '/api/v1';

type AuthHandlers = { onUnauthenticated: () => void };
let handlers: AuthHandlers = { onUnauthenticated: () => {} };
export function setAuthHandlers(h: AuthHandlers) {
  handlers = h;
}

const AUTH_EXEMPT = ['/auth/login', '/auth/totp/verify', '/auth/me', '/setup/'];

/**
 * fetch wrapper: resolves the *current* globalThis.fetch at call time (so the mock transport can
 * replace it), retries once after a successful step-up, and routes 401 to the login redirect.
 */
export async function apiFetch(input: Request): Promise<Response> {
  const retryable = input.clone();
  let res: Response;
  try {
    res = await globalThis.fetch(input);
  } catch {
    throw new ApiError(0, 'network', 'Network error: could not reach the codeplo server');
  }
  if (res.status === 403) {
    const body = (await res.clone().json().catch(() => null)) as { code?: string } | null;
    if (body?.code === 'step_up_required') {
      const ok = await stepUpStore.request();
      if (!ok) return res;
      res = await globalThis.fetch(retryable);
    }
  }
  if (res.status === 401 && !AUTH_EXEMPT.some((p) => input.url.includes(p))) handlers.onUnauthenticated();
  // Keep the client clock offset fresh from any successful response so relative times work even
  // when /auth/me failed once and SSE has not connected yet.
  if (res.ok) {
    const date = res.headers.get('Date');
    if (date) syncServerNow(new Date(date).toISOString());
  }
  return res;
}

export const api = createClient<paths>({
  baseUrl: `${typeof location === 'undefined' ? 'http://localhost' : location.origin}${API_BASE}`,
  fetch: apiFetch,
  credentials: 'include',
});

api.use({
  onRequest({ request }) {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      const t = getCsrf();
      if (t) request.headers.set('X-CSRF-Token', t);
    }
    return request;
  },
});

/** Unwrap an openapi-fetch result: return data or throw a normalized ApiError. */
export async function call<D>(p: Promise<{ data?: D; error?: unknown; response: Response }>): Promise<D> {
  const r = await p;
  if (r.error !== undefined || !r.response.ok) throw ApiError.fromResponse(r.error, r.response);
  return r.data as D;
}
