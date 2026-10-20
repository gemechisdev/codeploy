import { createDb, type CreateDbOptions, type Db } from './db';
import { HttpError, sleep, type ErrorBody } from './util';
import { routes, permsOf, type Ctx, type Route } from './router';
import { registerAuth } from './handlers/auth';
import { registerHardening } from './handlers/hardening';
import { registerWorkloads } from './handlers/workloads';
import { registerPlatform } from './handlers/platform';
import { registerOps } from './handlers/ops';
import { startMetrics } from './engine';

const API = '/api/v1';
let registered = false;

export interface MockServer {
  db: Db;
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
  dispose(): void;
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...headers } });
const err = (e: HttpError) => json(e.status, { code: e.code, message: e.message, details: e.details } satisfies ErrorBody, e.headers);

export async function createMockServer(opts: CreateDbOptions & { metrics?: boolean } = {}): Promise<MockServer> {
  if (!registered) {
    registerAuth(); registerHardening(); registerWorkloads(); registerPlatform(); registerOps();
    registered = true;
  }
  const db = await createDb(opts);
  if (opts.metrics !== false) startMetrics(db);

  async function handle(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const req = input instanceof Request ? input : new Request(typeof input === 'string' || input instanceof URL ? new URL(String(input), 'http://localhost') : input, init);
    const url = new URL(req.url, 'http://localhost');
    if (!url.pathname.startsWith(API)) return json(404, { code: 'not_found', message: 'not an API route' });
    const path = url.pathname.slice(API.length) || '/';

    const [lo, hi] = db.latency;
    if (hi > 0) await sleep(lo + Math.random() * (hi - lo));
    if (db.fault === 'slow') await sleep(2500);

    let match: { route: Route; params: Record<string, string> } | null = null;
    let pathMatched = false;
    for (const r of routes) {
      const m = r.re.exec(path);
      if (!m) continue;
      pathMatched = true;
      if (r.method !== req.method) continue;
      match = { route: r, params: Object.fromEntries(r.keys.map((k, i) => [k, decodeURIComponent(m[i + 1])])) };
      break;
    }
    if (!match) return pathMatched ? json(405, { code: 'not_found', message: 'method not allowed' }) : json(404, { code: 'not_found', message: `no route for ${req.method} ${path}` });
    const { route: r, params } = match;

    try {
      const session = db.session;
      const user = session ? db.users.find((u) => u.id === session.user_id && !u.disabled) ?? null : null;
      let body: unknown = undefined;
      let raw = '';
      if (req.method !== 'GET' && req.method !== 'HEAD') {
        const text = await req.text();
        raw = text;
        if (text) {
          try { body = JSON.parse(text); } catch { throw new HttpError(422, 'validation_failed', 'Body is not valid JSON'); }
        }
      }
      const c: Ctx = { db, req, url, params, query: url.searchParams, body, raw, user, session: user ? session : null, ip: '198.51.100.24' };

      if (r.perm !== null) {
        if (!user || !session) throw new HttpError(401, 'unauthenticated', 'Sign in to continue');
        if (req.method !== 'GET' && req.headers.get('X-CSRF-Token') !== session.csrf) throw new HttpError(403, 'forbidden', 'Missing or invalid CSRF token');
        if (r.perm !== 'session' && !permsOf(user).includes(r.perm)) throw new HttpError(403, 'forbidden', `Missing permission: ${r.perm}`);
      }
      if (r.module) {
        const m = db.modules.find((x) => x.key === r.module);
        if (m && !m.enabled) throw new HttpError(404, 'module_disabled', `Module "${r.module}" is disabled`);
      }
      // fault injection for UI resilience tests (never on /auth/* so the app stays reachable)
      if (db.fault !== 'none' && db.fault !== 'slow' && r.perm !== null && !path.startsWith('/auth/')) {
        if (db.fault === '500') throw new HttpError(500, 'internal', 'Injected server error');
        if (db.fault === '429') throw new HttpError(429, 'rate_limited', 'Too many requests', undefined, { 'Retry-After': '7' });
        if (db.fault === 'step_up' && req.method !== 'GET' && (session?.step_up_until ?? 0) <= Date.now()) {
          throw new HttpError(403, 'step_up_required', 'Injected step-up requirement');
        }
      }
      const res = (await r.handler(c)) ?? { status: 204 };
      return json(res.status ?? 200, res.body, res.headers);
    } catch (e) {
      if (e instanceof HttpError) return err(e);
      return json(500, { code: 'internal', message: e instanceof Error ? e.message : 'Internal error' });
    }
  }

  return { db, fetch: handle, dispose: () => db.dispose() };
}
