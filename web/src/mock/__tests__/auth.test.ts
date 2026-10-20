import { afterEach, describe, expect, it } from 'vitest';
import { boot, TestClient } from './helpers';
import { ROLE_PERMISSIONS } from '../db';

let c: TestClient;
afterEach(() => c?.server.dispose());

describe('auth, CSRF and step-up', () => {
  it('logs in with password then TOTP; /auth/me exposes permissions, csrf and server time', async () => {
    c = await boot({ login: false });
    expect((await c.get('/auth/me')).status).toBe(401);
    const l = await c.req('POST', '/auth/login', { email: 'owner@example.com', password: 'demo-password-change-me' });
    expect(l.body).toMatchObject({ totp_required: true });
    expect((await c.get('/auth/me')).status).toBe(401); // challenge pending is not a session
    const bad = await c.req('POST', '/auth/totp/verify', { challenge: l.body.challenge, code: '000000' });
    expect(bad.status).toBe(422);
    const ok = await c.req('POST', '/auth/totp/verify', { challenge: l.body.challenge, code: '123456' });
    expect(ok.status).toBe(200);
    expect(ok.body.permissions).toContain('terminal:root');
    expect(ok.body.step_up_until).toBeNull();
    expect(Date.parse(ok.body.server_now)).toBeGreaterThan(0);
  });

  it('rejects wrong passwords and rate-limits repeated failures with Retry-After', async () => {
    c = await boot({ login: false });
    let last = { status: 0, headers: new Headers() } as { status: number; headers: Headers };
    for (let i = 0; i < 6; i++) last = await c.req('POST', '/auth/login', { email: 'ratelimit@example.com', password: 'nope' });
    expect(last.status).toBe(429);
    expect(Number(last.headers.get('Retry-After'))).toBeGreaterThan(0);
  });

  it('requires the CSRF header on every non-GET request', async () => {
    c = await boot();
    const r = await c.req('POST', '/projects', { name: 'X', slug: 'x-proj' }, { 'X-CSRF-Token': '' });
    expect(r.status).toBe(403);
    expect(r.body.message).toMatch(/CSRF/);
    expect((await c.post('/projects', { name: 'X', slug: 'x-proj' })).status).toBe(201);
  });

  it('step-up: user admin is refused until a TOTP re-check, and the window comes from settings', async () => {
    c = await boot();
    const body = { email: 'new.user@example.com', password: 'long-enough-password', role: 'viewer' };
    const r1 = await c.post('/users', body);
    expect(r1.status).toBe(403);
    expect(r1.body.code).toBe('step_up_required');
    expect((await c.req('POST', '/auth/step-up', { code: '111111' })).status).toBe(422); // validated by the server
    const su = await c.stepUp();
    const me = await c.get('/auth/me');
    expect(me.body.step_up_until).toBe(su.body.step_up_until);
    expect(Date.parse(su.body.step_up_until) - Date.now()).toBeGreaterThan(290_000);
    expect(Date.parse(su.body.step_up_until) - Date.now()).toBeLessThanOrEqual(300_000);
    expect((await c.post('/users', body)).status).toBe(201);
  });

  it('logout destroys the session', async () => {
    c = await boot();
    expect((await c.post('/auth/logout')).status).toBe(204);
    expect((await c.get('/auth/me')).status).toBe(401);
  });

  it('serves roles and permissions from the API (no hard-coded matrix in the UI)', async () => {
    c = await boot();
    const roles = (await c.get('/roles')).body.items;
    expect(roles.map((r: any) => r.key)).toEqual(['owner', 'admin', 'deployer', 'viewer']);
    const perms = (await c.get('/permissions')).body.items.map((p: any) => p.key);
    expect(perms).not.toContain('secret:reveal');
    expect(perms).toHaveLength(32);
  });
});

describe('permission matrix', () => {
  // [method, path, body, permission needed]
  const probes: [string, string, unknown, string][] = [
    ['GET', '/users', undefined, 'users:manage'], ['GET', '/tokens', undefined, 'tokens:manage'], ['GET', '/audit', undefined, 'audit:read'],
    ['GET', '/hardening/checks', undefined, 'hardening:read'], ['POST', '/hardening/apply', { check_key: 'upd.unattended' }, 'hardening:apply'],
    ['POST', '/edge/reconcile', {}, 'edge:reconcile'], ['POST', '/system/cleanup', {}, 'system:cleanup'], ['PATCH', '/settings', { values: {} }, 'flags:manage'],
    ['PATCH', '/modules/monitor', { enabled: true }, 'modules:manage'], ['POST', '/projects', { name: 'p', slug: 'p-1' }, 'project:write'],
    ['GET', '/terminal/sessions', undefined, 'terminal:host'], ['GET', '/git/connections', undefined, 'git:write'],
  ];
  for (const role of ['owner', 'admin', 'deployer', 'viewer'] as const) {
    it(`${role}: each probe is allowed iff the role holds the permission`, async () => {
      c = await boot({ login: `${role}@example.com` });
      for (const [m, p, b, perm] of probes) {
        const r = await c.req(m, p, b);
        const allowed = (ROLE_PERMISSIONS[role] as string[]).includes(perm);
        if (allowed) expect(r.body?.code, `${role} ${m} ${p}`).not.toBe('forbidden');
        else {
          expect(r.status, `${role} ${m} ${p}`).toBe(403);
          expect(r.body.code).toBe('forbidden');
        }
      }
    });
  }
  it('viewer cannot hold terminal, deploy or secret-write permissions', () => {
    for (const p of ['terminal:host', 'terminal:container', 'terminal:root', 'deploy:create', 'env:write', 'resource:write']) expect(ROLE_PERMISSIONS.viewer).not.toContain(p);
    expect(ROLE_PERMISSIONS.admin).not.toContain('terminal:root');
  });
  it('deployer can deploy and read env but not manage users', async () => {
    c = await boot({ login: 'deployer@example.com' });
    expect((await c.get(`/resources/${c.resource('storefront').id}/env`)).status).toBe(200);
    expect((await c.get('/users')).status).toBe(403);
  });
});
