import { afterEach, describe, expect, it } from 'vitest';
import { boot, until, tick, TestClient } from './helpers';
import { createMockWebSocket } from '../install';

let c: TestClient;
afterEach(() => c?.server.dispose());

describe('setup wizard (fresh install)', () => {
  it('walks token → owner → TOTP → domain → operator → complete, then locks itself', async () => {
    c = await boot({ fresh: true });
    expect((await c.get('/setup/state')).body).toMatchObject({ setup_required: true, step: 'token', has_operator: false });
    expect((await c.post('/setup/admin', { token: 'wrong', email: 'a@b.co', password: 'a-long-password-1' })).status).toBe(403);
    expect((await c.post('/setup/admin', { token: 'demo-setup-token', email: 'bad', password: 'short' })).body.details.fields).toMatchObject({ email: expect.any(String), password: expect.any(String) });
    const admin = await c.post('/setup/admin', { token: 'demo-setup-token', email: 'boss@corp.test', password: 'a-long-password-1' });
    expect(admin.status).toBe(201);
    expect((await c.post('/setup/admin', { token: 'demo-setup-token', email: 'x@y.co', password: 'a-long-password-1' })).status).toBe(409);
    const enr = (await c.post('/setup/totp/enroll')).body;
    expect(enr.otpauth_uri).toMatch(/^otpauth:\/\/totp\//);
    expect(enr.secret).toMatch(/^[A-Z2-7]+$/);
    expect((await c.post('/setup/totp/verify', { code: '000000' })).status).toBe(422);
    const rc = (await c.post('/setup/totp/verify', { code: '123456' })).body.recovery_codes;
    expect(rc).toHaveLength(8);
    expect(new Set(rc).size).toBe(8);
    expect((await c.post('/setup/verify-domain', { hostname: 'nodns.example.com' })).body.resolves).toBe(false);
    expect((await c.post('/setup/verify-domain', { hostname: 'wrong.example.com' })).body).toMatchObject({ resolves: true, matches_host: false });
    expect((await c.post('/setup/verify-domain', { hostname: 'panel.example.com' })).body.matches_host).toBe(true);
    const pd = await c.post('/setup/panel-domain', { hostname: 'panel.example.com' });
    expect(pd.body.cert_status).toBe('issuing');
    expect(pd.body.url).toBeNull(); // nothing switches until issuance succeeds
    await until(() => c.server.db.setup.panel_domain === 'panel.example.com');
    expect((await c.get('/setup/operator-candidates')).body.items.length).toBeGreaterThan(1);
    expect((await c.post('/setup/operator', { mode: 'existing', username: 'root-ish' })).status).toBe(422);
    expect((await c.post('/setup/operator', { mode: 'existing', username: 'ops-deployer' })).status).toBe(200);
    expect((await c.post('/setup/complete')).status).toBe(204);
    expect((await c.get('/setup/state')).body.setup_required).toBe(false);
    expect((await c.post('/setup/admin', { token: 'demo-setup-token', email: 'z@z.co', password: 'a-long-password-1' })).status).toBe(404);
  });

  it('cannot complete before TOTP is enrolled', async () => {
    c = await boot({ fresh: true });
    await c.post('/setup/admin', { token: 'demo-setup-token', email: 'boss@corp.test', password: 'a-long-password-1' });
    expect((await c.post('/setup/complete')).status).toBe(409);
  });

  it('no credentials are prefilled by the server: fresh state has zero users and zero resources', async () => {
    c = await boot({ fresh: true });
    expect(c.server.db.users).toHaveLength(0);
    expect(c.server.db.resources).toHaveLength(0);
  });
});

describe('modules, flags, settings (plan 8.1)', () => {
  it('seeds only MVP modules enabled and the safe defaults', async () => {
    c = await boot();
    const mods = (await c.get('/modules')).body.items;
    expect(mods.filter((m: any) => m.enabled).map((m: any) => m.key).sort()).toEqual(['deploy', 'edge', 'git', 'hardening', 'terminal']);
    const flags = Object.fromEntries((await c.get('/feature-flags')).body.items.map((f: any) => [f.key, f.enabled]));
    expect(flags).toMatchObject({ 'hardening.apply_tier2': false, 'hardening.apply_tier3': false, 'deploy.compose': false, 'deploy.static': false, registry_pull_hook: true });
    expect((await c.get('/settings')).body.values).toMatchObject({ 'terminal.allow_root': false, retain_images: 5, observation_window_s: 60, step_up_window_s: 300 });
  });

  it('module gating: disabled modules answer 404 module_disabled until enabled', async () => {
    c = await boot();
    for (const p of ['/backups/runs', '/notifications/channels', '/templates', '/system/metrics?range=1h']) {
      const r = await c.get(p);
      expect(r.status, p).toBe(404);
      expect(r.body.code).toBe('module_disabled');
    }
    expect((await c.patch('/modules/monitor', { enabled: true })).status).toBe(200);
    expect((await c.get('/system/metrics?range=1h')).body.points.length).toBeGreaterThan(10);
    expect((await c.patch('/modules/databases', { enabled: true })).status).toBe(200);
    expect((await c.get('/backups/runs')).status).toBe(200);
    expect((await c.patch('/modules/edge', { enabled: false })).status).toBe(409); // core
    await c.patch('/modules/terminal', { enabled: false });
    expect((await c.get('/terminal/sessions')).body.code).toBe('module_disabled');
  });

  it('validates settings and flags; emits change events', async () => {
    c = await boot();
    const seen: string[] = [];
    c.server.db.bus.subscribe((e) => seen.push(e.type));
    expect((await c.patch('/settings', { values: { retain_images: 0 } })).status).toBe(422);
    expect((await c.patch('/settings', { values: { nope: 1 } })).status).toBe(422);
    expect((await c.patch('/settings', { values: { 'terminal.allow_root': 'yes' } })).status).toBe(422);
    expect((await c.patch('/settings', { values: { retain_images: 7 } })).body.values.retain_images).toBe(7);
    expect((await c.patch('/feature-flags', { flags: { missing: true } })).status).toBe(422);
    await c.patch('/feature-flags', { flags: { 'deploy.static': true } });
    expect(seen).toContain('flag.changed');
    await c.patch('/modules/notify', { enabled: true });
    expect(seen).toContain('module.changed');
  });

  it('system status carries the server clock, score and operator state', async () => {
    c = await boot();
    const s = (await c.get('/system/status')).body;
    expect(s).toMatchObject({ operator_configured: true, support_tier: 'primary', ram_total_mb: 2048 });
    expect(Math.abs(Date.parse(s.server_now) - Date.now())).toBeLessThan(5000);
    expect(s.security_score).toBeGreaterThan(0);
    expect((await c.post('/system/cleanup')).body.reclaimed_mb).toBeGreaterThan(0);
    expect((await c.get('/system/version')).body.version).toBe('1.2.0');
  });
});

describe('terminal tickets and WebSocket (plan 7 P3)', () => {
  const open = async (ticket: string, path = '/terminal/host') => {
    const WS = createMockWebSocket(c.server) as unknown as new (u: string) => any;
    const ws = new WS(`ws://localhost/api/v1${path}?ticket=${ticket}`);
    ws.binaryType = 'arraybuffer';
    const out: string[] = []; const ctl: any[] = [];
    let closed: any = null; let opened = false;
    ws.addEventListener('open', () => { opened = true; });
    ws.addEventListener('message', (e: MessageEvent) => (typeof e.data === 'string' ? ctl.push(JSON.parse(e.data)) : out.push(new TextDecoder().decode(e.data))));
    ws.addEventListener('close', (e: any) => { closed = e; });
    return { ws, out, ctl, get closed() { return closed; }, get opened() { return opened; }, text: () => out.join('') };
  };

  it('a ticket is single-use and the PTY (not the client) echoes', async () => {
    c = await boot();
    const t = (await c.post('/terminal/ticket', { target: 'host' })).body;
    expect(t.ticket).toBeTruthy();
    expect(Date.parse(t.expires_at) - Date.now()).toBeLessThanOrEqual(30_000);
    const s = await open(t.ticket);
    await until(() => s.opened);
    s.ws.send(new TextEncoder().encode('whoami\r'));
    await until(() => s.text().includes('ops-deployer\r\n'));
    expect(s.text()).toContain('whoami'); // echoed by the mock PTY
    s.ws.send(JSON.stringify({ type: 'resize', cols: 100, rows: 30 }));
    expect(s.ws.lastResize).toEqual({ cols: 100, rows: 30 });
    const again = await open(t.ticket);
    await until(() => again.closed !== null);
    expect(again.closed.code).toBe(1008);
    expect(again.opened).toBe(false);
  });

  it('expired tickets and tickets for another target are refused', async () => {
    c = await boot();
    const t = (await c.post('/terminal/ticket', { target: 'host' })).body.ticket;
    c.server.db.tickets.get(t)!.expires_at = Date.now() - 1;
    const s = await open(t);
    await until(() => s.closed !== null);
    expect(s.closed.reason).toMatch(/expired/);
    const t2 = (await c.post('/terminal/ticket', { target: 'host' })).body.ticket;
    const wrong = await open(t2, `/terminal/resources/${c.resource('storefront').id}`);
    await until(() => wrong.closed !== null);
    expect(wrong.closed.reason).toMatch(/does not match/);
  });

  it('exit ends the session with an exit frame and records session history', async () => {
    c = await boot();
    const s = await open((await c.post('/terminal/ticket', { target: 'host' })).body.ticket);
    await until(() => s.opened);
    s.ws.send(new TextEncoder().encode('exit\r'));
    await until(() => s.closed !== null);
    expect(s.ctl.at(-1)).toEqual({ type: 'exit', code: 0 });
    const sessions = (await c.get('/terminal/sessions')).body.items;
    expect(sessions[0]).toMatchObject({ user_email: 'owner@example.com', target: 'host', is_root: false });
    expect(sessions[0].ended_at).toBeTruthy();
  });

  it('host shell needs an operator identity (blocking state)', async () => {
    c = await boot();
    c.server.db.operator = null;
    const r = await c.post('/terminal/ticket', { target: 'host' });
    expect(r.status).toBe(409);
    expect(r.body.details.reason).toBe('operator_missing');
  });

  it('root is off by default; once allowed it still needs terminal:root and step-up', async () => {
    c = await boot();
    expect((await c.post('/terminal/ticket', { target: 'host', as_root: true })).status).toBe(403);
    await c.patch('/settings', { values: { 'terminal.allow_root': true } });
    const su = await c.post('/terminal/ticket', { target: 'host', as_root: true });
    expect(su.body.code).toBe('step_up_required');
    await c.stepUp();
    expect((await c.post('/terminal/ticket', { target: 'host', as_root: true })).status).toBe(201);
    await c.login('admin@example.com');
    expect((await c.post('/terminal/ticket', { target: 'host', as_root: true })).body.code).toBe('forbidden');
  });

  it('container shells need terminal:container and a running container; viewers get nothing', async () => {
    c = await boot();
    const id = c.resource('storefront').id;
    expect((await c.post('/terminal/ticket', { target: 'container' })).status).toBe(422);
    expect((await c.post('/terminal/ticket', { target: 'container', resource_id: id })).status).toBe(201);
    c.resource('storefront').status = 'stopped';
    expect((await c.post('/terminal/ticket', { target: 'container', resource_id: id })).status).toBe(409);
    await c.login('viewer@example.com');
    expect((await c.post('/terminal/ticket', { target: 'container', resource_id: id })).status).toBe(403);
  });

  it('idle timeout closes the session from the server side', async () => {
    c = await boot();
    c.server.db.settings['terminal.idle_timeout_s'] = 60;
    const s = await open((await c.post('/terminal/ticket', { target: 'host' })).body.ticket);
    await until(() => s.opened);
    await until(() => s.closed !== null, 3000);
    expect(s.ctl.some((m) => m.type === 'error' && /Idle/.test(m.message))).toBe(true);
    void tick;
  });
});
