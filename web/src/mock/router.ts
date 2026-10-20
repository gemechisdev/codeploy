import type { Db, MockSession, MockUser, Permission } from './db';
import { ROLE_PERMISSIONS, appendAudit } from './db';
import { HttpError, forbidden } from './util';

export interface Ctx {
  db: Db;
  req: Request;
  url: URL;
  params: Record<string, string>;
  query: URLSearchParams;
  body: any; // request bodies are validated by handlers against the contract
  raw: string; // raw request text (needed for webhook HMAC verification)
  user: MockUser | null;
  session: MockSession | null;
  ip: string;
}
export interface Result { status?: number; body?: unknown; headers?: Record<string, string> }
export type Handler = (c: Ctx) => Promise<Result | void> | Result | void;
export interface Route {
  method: string;
  re: RegExp;
  keys: string[];
  perm: Permission | 'session' | null; // null = public
  module?: string;
  handler: Handler;
}

export const routes: Route[] = [];

export function route(method: string, path: string, perm: Route['perm'], handler: Handler, module?: string) {
  const keys: string[] = [];
  const re = new RegExp('^' + path.replace(/:([a-zA-Z]+)/g, (_m, k: string) => { keys.push(k); return '([^/]+)'; }) + '/?$');
  routes.push({ method, re, keys, perm, module, handler });
}

export const permsOf = (u: MockUser): Permission[] => ROLE_PERMISSIONS[u.role];
export const hasPerm = (c: Ctx, p: Permission) => !!c.user && permsOf(c.user).includes(p);
export function need(c: Ctx, p: Permission) {
  if (!hasPerm(c, p)) throw forbidden(`Missing permission: ${p}`);
}

/** Server-side step-up: requires a TOTP re-check within `step_up_until` (default window 300 s). */
export function requireStepUp(c: Ctx) {
  const until = c.session?.step_up_until ?? 0;
  if (until <= Date.now()) throw new HttpError(403, 'step_up_required', 'Re-enter your 2FA code to continue');
}

export function audit(c: Ctx, action: string, target_type: string, target_id: string, meta: Record<string, unknown> = {}) {
  return appendAudit(c.db, { actor_type: 'user', actor_id: c.user?.id ?? 'anonymous', actor_email: c.user?.email ?? null, ip: c.ip, action, target_type, target_id, meta });
}

export const ok = (body: unknown, status = 200): Result => ({ status, body });
export const noContent = (): Result => ({ status: 204 });
export function paginate<T>(items: T[], q: URLSearchParams): { items: T[]; next_cursor: string | null } {
  const limit = Math.min(Number(q.get('limit')) || 50, 200);
  const start = Number(q.get('cursor')) || 0;
  const slice = items.slice(start, start + limit);
  return { items: slice, next_cursor: start + limit < items.length ? String(start + limit) : null };
}
