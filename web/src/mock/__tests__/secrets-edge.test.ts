import { afterEach, describe, expect, it } from 'vitest';
import { boot, until, TestClient } from './helpers';
import { routes } from '../router';

let c: TestClient;
afterEach(() => c?.server.dispose());

describe('env and secrets are write-only (plan 3.9)', () => {
  it('never returns secret values; plain values are shown', async () => {
    c = await boot();
    const items = (await c.get(`/resources/${c.resource('storefront').id}/env`)).body.items;
    const secret = items.find((i: any) => i.key === 'SESSION_SECRET');
    expect(secret).toMatchObject({ is_secret: true, value: null });
    expect(items.find((i: any) => i.key === 'NODE_ENV').value).toBe('production');
    expect(JSON.stringify(items)).not.toContain('seeded-secret-value');
  });

  it('has no reveal route or permission anywhere', () => {
    expect(routes.some((r) => /reveal|secret/i.test(r.re.source))).toBe(false);
  });

  it('replacing a secret needs step-up; keeping it does not; deleting needs none', async () => {
    c = await boot();
    const id = c.resource('storefront').id;
    const base = { key: 'SESSION_SECRET', scope: 'runtime', is_secret: true };
    const keep = [{ key: 'NODE_ENV', value: 'production', scope: 'runtime', is_secret: false }, { ...base, keep_existing_secret: true }];
    expect((await c.put(`/resources/${id}/env`, { items: keep })).status).toBe(200);
    const rotate = [keep[0], { ...base, value: 'rotated-value' }];
    const denied = await c.put(`/resources/${id}/env`, { items: rotate });
    expect(denied.status).toBe(403);
    expect(denied.body.code).toBe('step_up_required');
    await c.stepUp();
    const ok = await c.put(`/resources/${id}/env`, { items: rotate });
    expect(ok.status).toBe(200);
    expect(ok.body.items.find((i: any) => i.key === 'SESSION_SECRET').value).toBeNull();
    expect(c.server.db.env.get(id)!.find((e) => e.key === 'SESSION_SECRET')!.value).toBe('rotated-value');
    expect(JSON.stringify(c.server.db.audit.at(-1))).not.toContain('rotated-value'); // audit holds keys, never values
    expect((await c.put(`/resources/${id}/env`, { items: [keep[0]] })).body.items).toHaveLength(1);
  });

  it('validates keys without silently changing case; a secret cannot become plain', async () => {
    c = await boot();
    const id = c.resource('storefront').id;
    const r = await c.put(`/resources/${id}/env`, { items: [
      { key: 'bad-key', value: 'x', scope: 'runtime', is_secret: false }, { key: 'dup', value: '1', scope: 'both', is_secret: false }, { key: 'dup', value: '2', scope: 'both', is_secret: false },
      { key: 'SESSION_SECRET', value: 'now-plain', scope: 'runtime', is_secret: false }, { key: 'NEW_SECRET', scope: 'build', is_secret: true },
    ] });
    expect(r.status).toBe(422);
    const f = r.body.details.fields;
    expect(f['items.0.key']).toBeTruthy(); expect(f['items.2.key']).toBe('Duplicate key'); expect(f['items.3.key']).toMatch(/cannot be converted/); expect(f['items.4.value']).toBeTruthy();
  });

  it('volumes refuse protected mount paths and duplicates', async () => {
    c = await boot();
    const id = c.resource('storefront').id;
    expect((await c.post(`/resources/${id}/volumes`, { name: 'x', mount_path: '/etc' })).status).toBe(422);
    expect((await c.post(`/resources/${id}/volumes`, { name: 'x', mount_path: '/a/../b' })).status).toBe(422);
    const ok = await c.post(`/resources/${id}/volumes`, { name: 'cache', mount_path: '/app/cache' });
    expect(ok.body.host_path).toBe('/var/lib/codeplo/volumes/storefront/cache');
    expect((await c.post(`/resources/${id}/volumes`, { name: 'cache', mount_path: '/app/other' })).status).toBe(409);
  });
});

describe('domains and certificates (plan 7 P2)', () => {
  it('a new domain is issuing, then issued; .invalid hostnames end in error', async () => {
    c = await boot();
    const id = c.resource('job-worker').id;
    const a = await c.post(`/resources/${id}/domains`, { hostname: 'worker.example.com' });
    expect(a.status).toBe(201);
    expect(a.body.cert_status).toBe('issuing');
    await until(() => c.server.db.domains.find((d) => d.hostname === 'worker.example.com')!.cert_status === 'issued');
    const b = await c.post(`/resources/${id}/domains`, { hostname: 'broken.invalid' });
    await until(() => c.server.db.domains.find((d) => d.hostname === 'broken.invalid')!.cert_status === 'error');
    expect((await c.get(`/edge/certificates`)).body.items.find((x: any) => x.hostname === 'broken.invalid').error).toMatch(/NXDOMAIN/);
    expect(b.body.cert_status).toBe('issuing');
  });

  it('enforces hostname validity, uniqueness and the reserved panel domain', async () => {
    c = await boot();
    const id = c.resource('job-worker').id;
    expect((await c.post(`/resources/${id}/domains`, { hostname: 'not a host' })).status).toBe(422);
    const dup = await c.post(`/resources/${id}/domains`, { hostname: 'shop.example.com' });
    expect(dup.status).toBe(409);
    expect(dup.body.code).toBe('conflict');
    const panel = await c.post(`/resources/${id}/domains`, { hostname: 'panel.example.com' });
    expect(panel.status).toBe(422);
    expect(panel.body.details.fields.hostname).toMatch(/reserved for the panel/);
  });

  it('deletes a resource domain and updates the primary domain; the panel row is not deletable here', async () => {
    c = await boot();
    const sf = c.resource('storefront');
    const dom = c.server.db.domains.find((d) => d.hostname === 'shop.example.com')!;
    expect((await c.del(`/resources/${sf.id}/domains/${dom.id}`)).status).toBe(204);
    expect(c.resource('storefront').primary_domain).toBeNull();
    const panel = c.server.db.domains.find((d) => d.is_panel)!;
    expect((await c.del(`/resources/${sf.id}/domains/${panel.id}`)).status).toBe(404);
  });

  it('reconcile updates edge status; permissions are enforced', async () => {
    c = await boot();
    const st = await c.post('/edge/reconcile');
    expect(st.body.drift).toBe(false);
    expect(st.body.route_count).toBe(4);
    await c.login('viewer@example.com');
    expect((await c.post('/edge/reconcile')).status).toBe(403);
    expect((await c.get('/edge/status')).status).toBe(200);
  });
});
