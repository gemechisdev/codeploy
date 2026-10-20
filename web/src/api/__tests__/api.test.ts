import { QueryClient } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { apiFetch, api, call, setAuthHandlers } from '../client';
import { ApiError } from '../errors';
import { applyEvent, connectEvents } from '../events';
import { qk } from '../keys';
import { resetSession, serverNowMs, setCsrf, syncServerNow } from '../session';
import { stepUpStore } from '../stepup';

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...headers } });

let realFetch: typeof fetch;
beforeEach(() => { realFetch = globalThis.fetch; resetSession(); });
afterEach(() => { globalThis.fetch = realFetch; vi.useRealTimers(); });

describe('ApiError mapping', () => {
  it('maps the contract error envelope, fields, checks and Retry-After', async () => {
    const r = json(422, { code: 'validation_failed', message: 'Nope', details: { fields: { name: 'bad' } } }, { 'Retry-After': '9' });
    const e = ApiError.fromResponse(await r.clone().json(), r);
    expect(e).toMatchObject({ status: 422, code: 'validation_failed', message: 'Nope', retryAfter: 9 });
    expect(e.fields).toEqual({ name: 'bad' });
    const pf = ApiError.fromResponse({ code: 'preflight_failed', message: 'x', details: { checks: [{ key: 'a', label: 'A', ok: false, detail: 'd' }] } }, json(409, {}));
    expect(pf.checks).toHaveLength(1);
  });
  it('falls back sanely for non-envelope bodies', () => {
    expect(ApiError.fromResponse(undefined, new Response('x', { status: 401 })).code).toBe('unauthenticated');
    expect(ApiError.fromResponse('boom', new Response('x', { status: 502, statusText: 'Bad Gateway' })).code).toBe('internal');
  });
  it('call() throws ApiError for error responses and returns data otherwise', async () => {
    globalThis.fetch = vi.fn(async () => json(404, { code: 'not_found', message: 'gone' })) as unknown as typeof fetch;
    await expect(call(api.GET('/auth/me'))).rejects.toMatchObject({ name: 'ApiError', code: 'not_found', status: 404 });
    globalThis.fetch = vi.fn(async () => json(200, { csrf_token: 't' })) as unknown as typeof fetch;
    await expect(call(api.GET('/auth/me'))).resolves.toMatchObject({ csrf_token: 't' });
  });
  it('turns network failures into a normalized network error', async () => {
    globalThis.fetch = vi.fn(async () => { throw new TypeError('failed'); }) as unknown as typeof fetch;
    await expect(apiFetch(new Request('http://localhost/api/v1/projects'))).rejects.toMatchObject({ code: 'network', status: 0 });
  });
});

describe('CSRF and 401 handling', () => {
  it('sends X-CSRF-Token on non-GET requests only', async () => {
    const seen: Record<string, string | null> = {};
    globalThis.fetch = vi.fn(async (r: Request) => { seen[r.method] = r.headers.get('X-CSRF-Token'); return json(200, {}); }) as unknown as typeof fetch;
    setCsrf('tok-123');
    await api.GET('/projects');
    await api.POST('/projects', { body: { name: 'a', slug: 'a' } });
    expect(seen.GET).toBeNull();
    expect(seen.POST).toBe('tok-123');
  });
  it('routes 401 to the login redirect except on auth endpoints', async () => {
    const onUnauthenticated = vi.fn();
    setAuthHandlers({ onUnauthenticated });
    globalThis.fetch = vi.fn(async () => json(401, { code: 'unauthenticated', message: 'x' })) as unknown as typeof fetch;
    await api.GET('/projects');
    expect(onUnauthenticated).toHaveBeenCalledTimes(1);
    await api.GET('/auth/me');
    await api.POST('/auth/login', { body: { email: 'a@b.co', password: 'x' } });
    expect(onUnauthenticated).toHaveBeenCalledTimes(1);
  });
});

describe('step-up retry (once)', () => {
  const stepUpRequired = () => json(403, { code: 'step_up_required', message: 're-enter code' });
  it('opens the dialog, then retries the original request exactly once', async () => {
    let calls = 0;
    globalThis.fetch = vi.fn(async () => (++calls === 1 ? stepUpRequired() : json(200, { ok: true }))) as unknown as typeof fetch;
    const p = apiFetch(new Request('http://localhost/api/v1/users', { method: 'POST', body: '{"a":1}', headers: { 'content-type': 'application/json' } }));
    await vi.waitFor(() => expect(stepUpStore.getSnapshot()).toBe(true));
    stepUpStore.settle(true);
    const res = await p;
    expect(res.status).toBe(200);
    expect(calls).toBe(2);
    expect(stepUpStore.getSnapshot()).toBe(false);
  });
  it('does not loop: a second step_up_required after the retry is returned to the caller', async () => {
    let calls = 0;
    globalThis.fetch = vi.fn(async () => { calls++; return stepUpRequired(); }) as unknown as typeof fetch;
    const p = apiFetch(new Request('http://localhost/api/v1/users', { method: 'POST', body: '{}', headers: { 'content-type': 'application/json' } }));
    await vi.waitFor(() => expect(stepUpStore.getSnapshot()).toBe(true));
    stepUpStore.settle(true);
    expect((await p).status).toBe(403);
    expect(calls).toBe(2);
  });
  it('rejects (returns the 403) when the user cancels the dialog, without retrying', async () => {
    let calls = 0;
    globalThis.fetch = vi.fn(async () => { calls++; return stepUpRequired(); }) as unknown as typeof fetch;
    const p = apiFetch(new Request('http://localhost/api/v1/users', { method: 'POST', body: '{}', headers: { 'content-type': 'application/json' } }));
    await vi.waitFor(() => expect(stepUpStore.getSnapshot()).toBe(true));
    stepUpStore.settle(false);
    expect((await p).status).toBe(403);
    expect(calls).toBe(1);
  });
  it('concurrent step-up failures share one dialog', async () => {
    let n = 0;
    globalThis.fetch = vi.fn(async () => (++n <= 2 ? stepUpRequired() : json(200, {}))) as unknown as typeof fetch;
    const mk = () => apiFetch(new Request('http://localhost/api/v1/users', { method: 'POST', body: '{}', headers: { 'content-type': 'application/json' } }));
    const a = mk(); const b = mk();
    await vi.waitFor(() => expect(stepUpStore.getSnapshot()).toBe(true));
    stepUpStore.settle(true);
    expect((await a).status).toBe(200);
    expect((await b).status).toBe(200);
  });
});

describe('server clock', () => {
  it('computes countdown time from the server offset, not the client clock', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-10-18T10:00:00Z'));
    syncServerNow('2026-10-18T10:05:00Z'); // server is 5 minutes ahead
    expect(serverNowMs()).toBe(Date.parse('2026-10-18T10:05:00Z'));
    vi.advanceTimersByTime(10_000);
    expect(serverNowMs()).toBe(Date.parse('2026-10-18T10:05:10Z'));
  });
});

class FakeES extends EventTarget {
  static instances: FakeES[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(public url: string) { super(); FakeES.instances.push(this); }
  close() { this.closed = true; }
  emit(type: string, data: object, id = '') { this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data), lastEventId: id })); }
}

describe('SSE connection', () => {
  beforeEach(() => { FakeES.instances = []; vi.useFakeTimers(); });

  it('reconnects with exponential backoff, resumes from the last event id and refreshes everything once', () => {
    const qc = new QueryClient();
    const inv = vi.spyOn(qc, 'invalidateQueries');
    const conn = connectEvents(qc, { eventSource: FakeES as unknown as typeof EventSource, backoffMs: (n) => 100 * 2 ** n });
    const a = FakeES.instances[0];
    a.emit('audit.appended', { seq: 3 }, '41');
    a.onerror?.();
    expect(a.closed).toBe(true);
    vi.advanceTimersByTime(99); expect(FakeES.instances).toHaveLength(1);
    vi.advanceTimersByTime(1); expect(FakeES.instances).toHaveLength(2);
    expect(FakeES.instances[1].url).toContain('last_event_id=41');
    FakeES.instances[1].onerror?.();
    vi.advanceTimersByTime(199); expect(FakeES.instances).toHaveLength(2);
    vi.advanceTimersByTime(1); expect(FakeES.instances).toHaveLength(3);
    inv.mockClear();
    FakeES.instances[2].onopen?.();
    expect(inv).toHaveBeenCalledWith(); // full refresh after a gap
    FakeES.instances[2].onerror?.();
    vi.advanceTimersByTime(100); expect(FakeES.instances).toHaveLength(4); // attempt counter reset on open
    conn.close();
    FakeES.instances[3].onerror?.();
    vi.advanceTimersByTime(10_000); expect(FakeES.instances).toHaveLength(4);
  });

  it('invalidates exactly the query keys each event names (brief 5.4)', () => {
    const qc = new QueryClient();
    const inv = vi.spyOn(qc, 'invalidateQueries');
    const keys = () => inv.mock.calls.map((c) => JSON.stringify((c[0] as { queryKey: unknown }).queryKey));
    applyEvent(qc, 'deployment.updated', { deployment_id: 'd1', resource_id: 'r1', status: 'building', step: 'building' });
    expect(keys()).toEqual(expect.arrayContaining([JSON.stringify(qk.deployments('r1')), JSON.stringify(qk.deployment('d1')), JSON.stringify(qk.resource('r1')), JSON.stringify(qk.projects)]));
    inv.mockClear();
    applyEvent(qc, 'hardening.changed', {});
    expect(keys()).toContain(JSON.stringify(['hardening']));
    inv.mockClear();
    applyEvent(qc, 'edge.changed', {});
    expect(keys()).toContain(JSON.stringify(['edge']));
    inv.mockClear();
    applyEvent(qc, 'audit.appended', { seq: 1 });
    expect(keys()).toEqual([JSON.stringify(['audit'])]);
  });

  it('metrics.sample and session.step_up patch the cache without refetching', () => {
    const qc = new QueryClient();
    const inv = vi.spyOn(qc, 'invalidateQueries');
    qc.setQueryData(qk.me, { step_up_until: null, permissions: [] });
    applyEvent(qc, 'session.step_up', { step_up_until: '2026-10-18T10:05:00Z' });
    expect((qc.getQueryData(qk.me) as { step_up_until: string }).step_up_until).toBe('2026-10-18T10:05:00Z');
    applyEvent(qc, 'metrics.sample', { cpu_percent: 9 });
    expect(qc.getQueryData(['system', 'live-metrics'])).toEqual({ cpu_percent: 9 });
    expect(inv).not.toHaveBeenCalled();
  });

  it('deployment.log goes to open log viewers only, never the query cache', async () => {
    const { logBus } = await import('../logbus');
    const qc = new QueryClient();
    const got: string[] = [];
    const off = logBus.subscribe('d9', (l) => got.push(l.line));
    applyEvent(qc, 'deployment.log', { deployment_id: 'd9', seq: 1, at: 'x', stream: 'system', line: 'hello' });
    applyEvent(qc, 'deployment.log', { deployment_id: 'other', seq: 1, at: 'x', stream: 'system', line: 'nope' });
    off();
    applyEvent(qc, 'deployment.log', { deployment_id: 'd9', seq: 2, at: 'x', stream: 'system', line: 'after-unsub' });
    expect(got).toEqual(['hello']);
    expect(qc.getQueryCache().getAll()).toHaveLength(0);
  });
});
