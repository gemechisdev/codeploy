import { afterEach, describe, expect, it } from 'vitest';
import { boot, tick, until, TestClient } from './helpers';

let c: TestClient;
afterEach(() => c?.server.dispose());
const check = async (key: string) => (await c.get('/hardening/checks')).body.items.find((x: any) => x.key === key);

describe('hardening tiers (plan 3.10)', () => {
  it('derives score and apply_enabled from the server; tier 2/3 are plan-only by default', async () => {
    c = await boot();
    const r = (await c.get('/hardening/checks')).body;
    expect(r.score).toBeGreaterThan(0);
    expect((await check('fw.default_deny')).apply_enabled).toBe(false);
    expect((await check('ssh.no_password')).apply_enabled).toBe(false);
    expect((await check('upd.unattended')).apply_enabled).toBe(true);
    expect((await check('net.exposed_services')).apply_enabled).toBe(false);
  });

  it('tier 0 cannot be applied', async () => {
    c = await boot();
    await c.stepUp();
    const r = await c.post('/hardening/apply', { check_key: 'net.exposed_services' });
    expect(r.status).toBe(409);
  });

  it('tier 1 apply needs step-up, then completes without a dead-man timer', async () => {
    c = await boot();
    const r1 = await c.post('/hardening/apply', { check_key: 'upd.unattended' });
    expect(r1.body.code).toBe('step_up_required');
    await c.stepUp();
    const r2 = await c.post('/hardening/apply', { check_key: 'upd.unattended' });
    expect(r2.status).toBe(200);
    expect(r2.body).toMatchObject({ state: 'confirmed', confirm_deadline: null });
    expect((await check('upd.unattended')).status).toBe('pass');
  });

  it('plan returns the exact diff and commands; unknown keys are rejected', async () => {
    c = await boot();
    const p = await c.post('/hardening/plan', { check_keys: ['fw.default_deny'] });
    expect(p.body.items[0].plan_diff.after).toContain('table inet codeplo');
    expect(p.body.items[0].commands.join('\n')).toMatch(/only after Confirm/);
    expect((await c.post('/hardening/plan', { check_keys: ['nope'] })).status).toBe(422);
  });

  it('plan-only: applying tier 2 with the flag off is refused even with step-up and ack', async () => {
    c = await boot();
    await c.stepUp();
    const r = await c.post('/hardening/apply', { check_key: 'fw.default_deny', ack: { console_access: true } });
    expect(r.status).toBe(409);
    expect(r.body.message).toMatch(/plan-only/);
  });

  describe('with hardening.apply_tier2 enabled', () => {
    const enable = async () => { c = await boot(); await c.patch('/feature-flags', { flags: { 'hardening.apply_tier2': true, 'hardening.apply_tier3': true } }); await c.stepUp(); };

    it('requires the console-access acknowledgement (sent in the apply request)', async () => {
      await enable();
      expect((await check('fw.default_deny')).apply_enabled).toBe(true);
      const none = await c.post('/hardening/apply', { check_key: 'fw.default_deny' });
      expect(none.status).toBe(422);
      expect(none.body.details.fields.ack).toBeTruthy();
      const no = await c.post('/hardening/apply', { check_key: 'fw.default_deny', ack: { console_access: false } });
      expect(no.status).toBe(422);
    });

    it('preflight failure blocks apply with per-check details and changes nothing', async () => {
      await enable();
      c.server.db.scenario.noProvenKeyLogin = true;
      const pf = await c.post('/hardening/preflight', { check_key: 'ssh.no_password' });
      expect(pf.body.ok).toBe(false);
      expect(pf.body.items.find((i: any) => i.key === 'key_login_proven').ok).toBe(false);
      const r = await c.post('/hardening/apply', { check_key: 'ssh.no_password', ack: { console_access: true } });
      expect(r.status).toBe(409);
      expect(r.body.code).toBe('preflight_failed');
      expect(r.body.details.checks.some((i: any) => !i.ok)).toBe(true);
      expect(c.server.db.changes).toHaveLength(0);
      expect((await check('ssh.no_password')).status).toBe('fail');
    });

    it('apply returns a server-owned confirm_deadline; confirm cancels the dead-man timer', async () => {
      await enable();
      c.server.db.settings.hardening_revert_window_s = 600;
      const before = Date.now();
      const r = await c.post('/hardening/apply', { check_key: 'fw.default_deny', ack: { console_access: true } });
      expect(r.status).toBe(200);
      expect(r.body.state).toBe('pending_confirm');
      expect(Date.parse(r.body.confirm_deadline) - before).toBeGreaterThan(599_000);
      expect(r.body.revert_unit).toMatch(/^codeplo-revert-.*\.timer$/);
      const list = await c.get('/hardening/changes');
      expect(list.body.items[0].id).toBe(r.body.id);
      expect(Date.parse(list.body.server_now)).toBeGreaterThan(0);
      const cf = await c.post(`/hardening/confirm/${r.body.id}`);
      expect(cf.body.state).toBe('confirmed');
      expect((await c.post(`/hardening/confirm/${r.body.id}`)).status).toBe(409);
      await tick(60);
      expect(c.server.db.changes[0].state).toBe('confirmed'); // timer was cleared
    });

    it('dead-man: with no confirmation the SERVER reverts the change and emits hardening.changed', async () => {
      await enable();
      const events: string[] = [];
      c.server.db.bus.subscribe((e) => { if (e.type === 'hardening.changed') events.push(String(e.data.change_id ?? '')); });
      const r = await c.post('/hardening/apply', { check_key: 'fw.default_deny', ack: { console_access: true } });
      await until(() => c.server.db.changes[0].state === 'reverted');
      const ch = (await c.get('/hardening/changes')).body.items[0];
      expect(ch.state).toBe('reverted');
      expect(ch.reverted_at).toBeTruthy();
      expect(events).toContain(r.body.id);
      expect((await check('fw.default_deny')).status).toBe('fail');
      await until(() => c.server.db.audit.some((a) => a.action === 'hardening.reverted'));
      const last = c.server.db.audit.filter((a) => a.action === 'hardening.reverted').at(-1)!;
      expect(last.actor_type).toBe('system');
      expect(String(last.meta.via)).toMatch(/systemd transient timer/);
    });

    it('manual revert works while pending and is refused afterwards', async () => {
      await enable();
      c.server.db.settings.hardening_revert_window_s = 600;
      const r = await c.post('/hardening/apply', { check_key: 'fw.default_deny', ack: { console_access: true } });
      const rv = await c.post(`/hardening/revert/${r.body.id}`);
      expect(rv.status).toBe(200);
      await until(() => c.server.db.changes[0].state === 'reverted');
      expect((await c.post(`/hardening/revert/${r.body.id}`)).status).toBe(409);
    });

    it('only one guarded change may wait for confirmation at a time', async () => {
      await enable();
      c.server.db.settings.hardening_revert_window_s = 600;
      await c.post('/hardening/apply', { check_key: 'fw.default_deny', ack: { console_access: true } });
      const second = await c.post('/hardening/apply', { check_key: 'ssh.limit_auth', ack: { console_access: true } });
      expect(second.status).toBe(409);
    });
  });

  it('audit run is recorded and no SSH port-change check exists (plan 3.10.5)', async () => {
    c = await boot();
    const a = await c.post('/hardening/audit');
    expect(a.status).toBe(200);
    expect((await c.get('/hardening/runs')).body.items[0].mode).toBe('audit');
    const keys: string[] = (await c.get('/hardening/checks')).body.items.map((x: any) => x.key);
    expect(keys.some((k) => /port/i.test(k) && k.startsWith('ssh'))).toBe(false);
  });
});
