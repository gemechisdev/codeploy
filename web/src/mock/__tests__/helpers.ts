import { createMockServer, type MockServer } from '../server';

export interface Res<T = any> { status: number; body: T; headers: Headers }

/** Drives the mock exactly as the browser would: through its `fetch`, with cookie-less session + CSRF header. */
export class TestClient {
  csrf = '';
  constructor(public server: MockServer) {}
  async req<T = any>(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<Res<T>> {
    const headers: Record<string, string> = { ...extra };
    if (body !== undefined && !headers['content-type']) headers['content-type'] = 'application/json';
    if (method !== 'GET' && this.csrf && headers['X-CSRF-Token'] === undefined) headers['X-CSRF-Token'] = this.csrf;
    const r = await this.server.fetch(`http://localhost/api/v1${path}`, { method, headers, body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body) });
    const text = await r.text();
    return { status: r.status, body: (text ? JSON.parse(text) : null) as T, headers: r.headers };
  }
  get = <T = any>(p: string) => this.req<T>('GET', p);
  post = <T = any>(p: string, b?: unknown) => this.req<T>('POST', p, b ?? {});
  put = <T = any>(p: string, b?: unknown) => this.req<T>('PUT', p, b);
  patch = <T = any>(p: string, b?: unknown) => this.req<T>('PATCH', p, b);
  del = <T = any>(p: string) => this.req<T>('DELETE', p);

  async login(email = 'owner@example.com', password = 'demo-password-change-me') {
    const l = await this.req('POST', '/auth/login', { email, password });
    if (l.status !== 200) return l;
    const v = await this.req('POST', '/auth/totp/verify', { challenge: l.body.challenge, code: '123456' });
    this.csrf = v.body.csrf_token;
    return v;
  }
  async stepUp() { return this.post('/auth/step-up', { code: '123456' }); }
  resource(name: string) { return this.server.db.resources.find((r) => r.name === name)!; }
}

export async function boot(opts: { fresh?: boolean; login?: string | false } = {}) {
  const server = await createMockServer({ speed: 0, latency: [0, 0], metrics: false, fresh: opts.fresh });
  const c = new TestClient(server);
  if (opts.login !== false && !opts.fresh) await c.login(opts.login ?? 'owner@example.com');
  return c;
}

export const tick = (ms = 20) => new Promise((r) => setTimeout(r, ms));
/** Wait until `pred` is true (the mock state machines run on timers). */
export async function until(pred: () => boolean, ms = 2000) {
  const t0 = Date.now();
  while (!pred()) {
    if (Date.now() - t0 > ms) throw new Error('timed out waiting for condition');
    await tick(5);
  }
}
