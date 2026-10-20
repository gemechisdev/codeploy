import { afterEach, describe, expect, it } from 'vitest';
import { boot, until, TestClient } from './helpers';
import { DEPLOYMENT_STATES } from '@/api/types';

let c: TestClient;
afterEach(() => c?.server.dispose());

const pullCfg = (ref = 'ghcr.io/acme/new-app:1.0.0') => ({
  image: { mode: 'pull', ref, dockerfile: null, context: null }, port: 3000, healthcheck: { path: '/healthz', interval_s: 5, retries: 12, start_period_s: 5, timeout_s: 3 },
  limits: { memory_mb: 256, cpus: 0.5, pids_limit: 512 }, isolation: { cap_add: [], user: null, trusted: false }, restart: 'unless-stopped', retain_images: 5,
});
const envId = (name = 'production') => c.server.db.environments.find((e) => e.name === name)!.id;
const dep = (id: string) => c.server.db.deployments.find((d) => d.id === id)!;
const settled = (id: string) => until(() => ['succeeded', 'failed', 'cancelled', 'rolled_back', 'interrupted'].includes(dep(id).status));

describe('resources', () => {
  it('a new resource is stopped with no active deployment until its first deployment (C8)', async () => {
    c = await boot();
    const r = await c.post('/resources', { environment_id: envId(), type: 'app', name: 'fresh-app', config: pullCfg(), deploy_now: false });
    expect(r.status).toBe(201);
    expect(r.body.resource).toMatchObject({ status: 'stopped', active_deployment_id: null, active_digest: null });
    expect(r.body.deployment).toBeNull();
  });

  it('create + deploy_now starts the first deployment and only sets active_deployment_id at switching', async () => {
    c = await boot();
    const statuses: string[] = [];
    c.server.db.bus.subscribe((e) => { if (e.type === 'deployment.updated') statuses.push(String(e.data.status)); });
    const r = await c.post('/resources', { environment_id: envId(), type: 'app', name: 'auto-app', config: pullCfg(), deploy_now: true, domain: 'auto.example.com' });
    expect(r.body.deployment.status).toBe('queued');
    expect(r.body.resource.active_deployment_id).toBeNull();
    await settled(r.body.deployment.id);
    expect(dep(r.body.deployment.id).status).toBe('succeeded');
    expect(c.resource('auto-app')).toMatchObject({ status: 'running', active_deployment_id: r.body.deployment.id });
    expect(statuses).toEqual(expect.arrayContaining(['queued', 'preparing', 'starting', 'verifying', 'switching', 'observing', 'succeeded']));
    expect(statuses).not.toContain('building'); // image mode skips build
    expect(dep(r.body.deployment.id).retained).toBe(true);
  });

  it('validates the slug, config, flags and uniqueness', async () => {
    c = await boot();
    const bad = await c.post('/resources', { environment_id: envId(), type: 'app', name: 'Bad_Name', config: { ...pullCfg(), port: 70000, limits: { memory_mb: 99999, cpus: 9, pids_limit: 4 } } });
    expect(bad.status).toBe(422);
    expect(Object.keys(bad.body.details.fields)).toEqual(expect.arrayContaining(['name', 'config.port', 'config.limits.memory_mb', 'config.limits.cpus', 'config.limits.pids_limit']));
    const compose = await c.post('/resources', { environment_id: envId(), type: 'compose', name: 'stack', config: pullCfg() });
    expect(compose.body.details.fields.type).toMatch(/deploy\.compose/);
    expect((await c.post('/resources', { environment_id: envId(), type: 'app', name: 'storefront', config: pullCfg() })).status).toBe(409);
    const dockerless = await c.post('/resources', { environment_id: envId(), type: 'app', name: 'git-app', config: { ...pullCfg(), image: { mode: 'build', ref: null, dockerfile: 'Dockerfile', context: '.' } } });
    expect(dockerless.status).toBe(422);
    expect(dockerless.body.details.fields.source).toBeTruthy();
  });

  it('start/stop/restart need a successful deployment; delete is blocked while deploying', async () => {
    c = await boot();
    const r = await c.post('/resources', { environment_id: envId(), type: 'app', name: 'ctl-app', config: pullCfg(), deploy_now: false });
    expect((await c.post(`/resources/${r.body.resource.id}/start`)).status).toBe(409);
    const d = await c.post(`/resources/${r.body.resource.id}/deployments`, {});
    expect((await c.del(`/resources/${r.body.resource.id}`)).status).toBe(409);
    await settled(d.body.id);
    expect((await c.post(`/resources/${r.body.resource.id}/stop`)).body.status).toBe('stopped');
    expect((await c.post(`/resources/${r.body.resource.id}/start`)).body.status).toBe('running');
    expect((await c.del(`/resources/${r.body.resource.id}`)).status).toBe(204);
    expect((await c.get(`/resources/${r.body.resource.id}`)).status).toBe(404);
  });

  it('viewer sees read-only API: no write, deploy or control', async () => {
    c = await boot({ login: 'viewer@example.com' });
    const id = c.resource('storefront').id;
    expect((await c.get(`/resources/${id}`)).status).toBe(200);
    for (const [m, p] of [['POST', `/resources/${id}/deployments`], ['POST', `/resources/${id}/restart`], ['DELETE', `/resources/${id}`], ['PUT', `/resources/${id}/env`]] as const) {
      expect((await c.req(m, p, {})).status, p).toBe(403);
    }
  });
});

describe('deployment state machine (plan 3.8)', () => {
  it('reaches all 12 states', async () => {
    c = await boot();
    const seen = new Set<string>();
    c.server.db.bus.subscribe((e) => { if (e.type === 'deployment.updated') seen.add(String(e.data.status)); });
    const sf = c.resource('storefront').id;

    // building + succeeded (git/build mode)
    const api = c.resource('core-api').id;
    const built = await c.post(`/resources/${api}/deployments`, {});
    expect(built.status).toBe(202);
    await settled(built.body.id);
    expect(dep(built.body.id).status).toBe('succeeded');

    // failed: bad image reference; previous container untouched
    const before = c.resource('storefront').active_deployment_id;
    const bad = await c.post(`/resources/${sf}/deployments`, { image_ref: 'ghcr.io/acme/does-not-exist:1' });
    await settled(bad.body.id);
    expect(dep(bad.body.id).status).toBe('failed');
    expect(dep(bad.body.id).error).toMatch(/manifest unknown/);
    expect(c.resource('storefront').active_deployment_id).toBe(before);
    expect(c.resource('storefront').status).toBe('running');

    // rolled_back: unhealthy during the observation window
    const flaky = await c.post(`/resources/${sf}/deployments`, { image_ref: 'ghcr.io/acme/storefront:flaky' });
    await settled(flaky.body.id);
    expect(dep(flaky.body.id)).toMatchObject({ status: 'rolled_back' });
    expect(dep(flaky.body.id).rollback_reason).toMatch(/Health check failed/);
    expect(c.resource('storefront').active_deployment_id).toBe(before);

    // cancelled
    const q = await c.post(`/resources/${sf}/deployments`, { image_ref: 'ghcr.io/acme/storefront:1.15.0' });
    const cancel = await c.post(`/deployments/${q.body.id}/cancel`);
    expect(cancel.body.status).toBe('cancelled');
    seen.add('cancelled');

    // interrupted: seeded "recovered after restart" example
    const list = (await c.get(`/resources/${api}/deployments`)).body.items;
    for (const d of list) seen.add(d.status);

    expect([...seen].sort()).toEqual([...DEPLOYMENT_STATES].sort());
  });

  it('cancel is refused after switching; only one deployment at a time', async () => {
    c = await boot();
    const id = c.resource('job-worker').id;
    const d = await c.post(`/resources/${id}/deployments`, {});
    expect((await c.post(`/resources/${id}/deployments`, {})).status).toBe(409);
    await settled(d.body.id);
    expect((await c.post(`/deployments/${d.body.id}/cancel`)).status).toBe(409);
  });

  it('pre-flight blocks the deployment when disk is below the threshold and names the fix', async () => {
    c = await boot();
    c.server.db.scenario.lowDisk = true;
    const d = await c.post(`/resources/${c.resource('job-worker').id}/deployments`, {});
    await settled(d.body.id);
    expect(dep(d.body.id).status).toBe('failed');
    expect(dep(d.body.id).error).toMatch(/Pre-flight refused/);
    expect(dep(d.body.id).preflight).toMatchObject({ ok: false });
  });

  it('rollback creates a new rollback deployment on a retained digest and needs no step-up (C6)', async () => {
    c = await boot();
    const sf = c.resource('storefront');
    const target = c.server.db.deployments.find((d) => d.resource_id === sf.id && d.status === 'succeeded' && d.id !== sf.active_deployment_id)!;
    expect((await c.post(`/deployments/${target.id}/rollback`, {})).status).toBe(422); // must acknowledge
    expect((await c.post(`/deployments/${sf.active_deployment_id}/rollback`, { acknowledge_db_unchanged: true })).status).toBe(409); // already active
    const r = await c.post(`/deployments/${target.id}/rollback`, { acknowledge_db_unchanged: true });
    expect(r.status).toBe(202);
    expect(r.body).toMatchObject({ trigger: 'rollback', rollback_of: target.id, image_digest: target.image_digest });
    await settled(r.body.id);
    expect(dep(r.body.id).status).toBe('succeeded');
    expect(c.resource('storefront').active_deployment_id).toBe(r.body.id);
  });

  it('rollback target must retain its image', async () => {
    c = await boot();
    const t = c.server.db.deployments.find((d) => d.status === 'rolled_back')!;
    expect((await c.post(`/deployments/${t.id}/rollback`, { acknowledge_db_unchanged: true })).status).toBe(409);
  });

  it('streams logs through the bus in order with increasing seq', async () => {
    c = await boot();
    const d = await c.post(`/resources/${c.resource('job-worker').id}/deployments`, {});
    await settled(d.body.id);
    const lines = c.server.db.logs.get(d.body.id)!;
    expect(lines.length).toBeGreaterThan(5);
    expect(lines.map((l) => l.seq)).toEqual(lines.map((_, i) => i + 1));
    expect(lines.some((l) => l.line.includes('pre-flight'))).toBe(true);
  });
});
