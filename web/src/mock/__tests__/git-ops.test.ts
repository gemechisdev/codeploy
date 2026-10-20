import { afterEach, describe, expect, it } from 'vitest';
import { boot, until, TestClient } from './helpers';

let c: TestClient;
afterEach(() => c?.server.dispose());

async function hmac(secret: string, text: string) {
  const key = await crypto.subtle.importKey('raw', new TextEncoder().encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const sig = await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(text));
  return `sha256=${[...new Uint8Array(sig)].map((b) => b.toString(16).padStart(2, '0')).join('')}`;
}

describe('git, deploy keys, hooks and webhooks', () => {
  it('deploy key: public key + fingerprint only; regenerating replaces it', async () => {
    c = await boot();
    const id = c.resource('storefront').id;
    expect((await c.get(`/resources/${id}/deploy-key`)).status).toBe(404);
    const k = (await c.post(`/resources/${id}/deploy-key`)).body;
    expect(k.public_key).toMatch(/^ssh-ed25519 /);
    expect(k.fingerprint).toMatch(/^SHA256:/);
    expect(JSON.stringify(k)).not.toMatch(/PRIVATE/);
    const k2 = (await c.post(`/resources/${id}/deploy-key`)).body;
    expect(k2.id).not.toBe(k.id);
    expect((await c.get(`/resources/${id}/deploy-key`)).body.id).toBe(k2.id);
  });

  it('deploy hook token is returned once; later reads expose only the prefix; rotate issues a new one', async () => {
    c = await boot();
    const id = c.resource('job-worker').id;
    const made = await c.post(`/resources/${id}/deploy-hook`);
    expect(made.status).toBe(201);
    expect(made.body.token).toMatch(/^cplh_[0-9a-f]{48}$/);
    expect(made.body.hook.token_prefix).toBe(made.body.token.slice(0, 9));
    const read = (await c.get(`/resources/${id}/deploy-hook`)).body;
    expect(read.token).toBeUndefined();
    expect(JSON.stringify(read)).not.toContain(made.body.token);
    expect((await c.post(`/resources/${id}/deploy-hook`)).status).toBe(409);
    const rot = await c.post(`/resources/${id}/deploy-hook/rotate`);
    expect(rot.body.token).not.toBe(made.body.token);
    expect((await c.req('POST', `/hooks/deploy/${made.body.token}`, { image: 'ghcr.io/acme/worker:9' })).status).toBe(404); // old token dead
  });

  it('the deploy hook starts a pull deployment within its image policy and rejects anything else', async () => {
    c = await boot();
    const id = c.resource('job-worker').id;
    const { token } = (await c.post(`/resources/${id}/deploy-hook`)).body;
    const bad = await c.req('POST', `/hooks/deploy/${token}`, { image: 'evil.example/pwn:1' });
    expect(bad.status).toBe(422);
    expect(c.server.db.deliveries[0]).toMatchObject({ provider: 'deploy_hook', result: 'rejected' });
    const ok = await c.req('POST', `/hooks/deploy/${token}`, { image: 'ghcr.io/acme/worker:2.4.0' });
    expect(ok.status).toBe(202);
    const d = c.server.db.deployments.find((x) => x.id === ok.body.deployment_id)!;
    expect(d).toMatchObject({ trigger: 'deploy_hook', image_ref: 'ghcr.io/acme/worker:2.4.0' });
    await until(() => d.status === 'succeeded');
    expect((await c.get(`/resources/${id}/deploy-hook`)).body.last_used_at).toBeTruthy();
  });

  it('GitHub webhook: HMAC-SHA256 verified, delivery-ID dedupe, branch and skip filters', async () => {
    c = await boot();
    const api = c.resource('core-api');
    const sec = (await c.post(`/resources/${api.id}/webhook`)).body.secret as string;
    expect(sec).toMatch(/^whsec_/);
    expect((await c.get(`/resources/${api.id}/webhook`)).body.secret).toBeUndefined();
    const payload = (over: object = {}) => JSON.stringify({ ref: 'refs/heads/main', after: 'abcdef1234', repository: { ssh_url: 'git@github.com:acme/core-api.git' }, head_commit: { message: 'feat: x' }, ...over });
    const send = async (body: string, delivery: string, signature?: string, event = 'push') =>
      c.req('POST', '/webhooks/github', body, { 'X-GitHub-Delivery': delivery, 'X-GitHub-Event': event, 'X-Hub-Signature-256': signature ?? (await hmac(sec, body)) });

    const forged = await send(payload(), 'd-1', 'sha256=deadbeef');
    expect(forged.status).toBe(401);
    expect(c.server.db.deliveries[0]).toMatchObject({ result: 'rejected', reason: 'Invalid HMAC-SHA256 signature' });

    const ok = await send(payload(), 'd-2');
    expect(ok.status).toBe(202);
    const dep = c.server.db.deployments.filter((d) => d.resource_id === api.id).at(-1)!;
    expect(dep).toMatchObject({ trigger: 'webhook', branch: 'main' });
    await until(() => dep.status === 'succeeded');

    const replay = await send(payload(), 'd-2');
    expect(replay.status).toBe(409);
    expect(c.server.db.deliveries.some((d) => d.reason === 'Delivery ID already seen (replay)')).toBe(true);

    const wrongBranch = await send(payload({ ref: 'refs/heads/dev' }), 'd-3');
    expect(wrongBranch.body.result).toBe('ignored');
    expect(c.server.db.deliveries[0].reason).toMatch(/does not match configured branch/);
    const skip = await send(payload({ head_commit: { message: 'docs [skip deploy]' } }), 'd-4');
    expect(skip.body.result).toBe('ignored');
    const before = c.server.db.deployments.length;
    await send(payload({ ref: 'refs/heads/dev' }), 'd-5');
    expect(c.server.db.deployments.length).toBe(before);

    const list = (await c.get(`/webhooks/deliveries?resource_id=${api.id}`)).body.items;
    expect(new Set(list.map((d: any) => d.result))).toEqual(new Set(['success', 'ignored', 'rejected']));
  });

  it('source form validates repo URL and requires a deploy key for key-based sources', async () => {
    c = await boot();
    const id = c.resource('storefront').id;
    const bad = await c.put(`/resources/${id}/source`, { kind: 'deploy_key_git', repo_url: 'ftp://nope', branch: '', path_filters: [] });
    expect(bad.status).toBe(422);
    expect(Object.keys(bad.body.details.fields)).toEqual(expect.arrayContaining(['repo_url', 'branch', 'deploy_key_id']));
    await c.post(`/resources/${id}/deploy-key`);
    const ok = await c.put(`/resources/${id}/source`, { kind: 'deploy_key_git', repo_url: 'git@github.com:acme/storefront.git', branch: 'main', build_path: '.', dockerfile: 'Dockerfile', path_filters: ['src/**'] });
    expect(ok.status).toBe(200);
    expect(c.resource('storefront').config.image.mode).toBe('build');
  });

  it('git connections start empty (GitHub App arrives later)', async () => {
    c = await boot();
    expect((await c.get('/git/connections')).body.items).toEqual([]);
  });
});

describe('notifications, backups and templates (module-gated)', () => {
  const enable = async (...m: string[]) => { c = await boot(); for (const k of m) await c.patch(`/modules/${k}`, { enabled: true }); };

  it('channels: credentials are write-only, validated per kind, testable', async () => {
    await enable('notify');
    expect((await c.post('/notifications/channels', { kind: 'telegram', name: 'x', config: {} })).status).toBe(422);
    const ch = await c.post('/notifications/channels', { kind: 'webhook', name: 'ops hook', config: { url: 'https://hooks.example.com/abc?secret=zzz' } });
    expect(ch.status).toBe(201);
    expect(JSON.stringify(ch.body)).not.toContain('zzz');
    expect(ch.body.destination_hint).toBe('hooks.example.com');
    expect((await c.post(`/notifications/channels/${ch.body.id}/test`)).body.ok).toBe(true);
    await c.patch(`/notifications/channels/${ch.body.id}`, { enabled: false });
    expect((await c.post(`/notifications/channels/${ch.body.id}/test`)).body.ok).toBe(false);
    expect((await c.del(`/notifications/channels/${ch.body.id}`)).status).toBe(204);
  });

  it('alert rules: catalog, uniqueness and patching', async () => {
    await enable('notify');
    expect((await c.post('/alerts/rules', { key: 'bogus' })).status).toBe(422);
    expect((await c.post('/alerts/rules', { key: 'disk_low' })).status).toBe(409);
    const r = await c.post('/alerts/rules', { key: 'memory_high', threshold: '85' });
    expect(r.body.threshold).toBe('85');
    expect((await c.patch(`/alerts/rules/${r.body.id}`, { cooldown_s: 5 })).status).toBe(422);
    expect((await c.patch(`/alerts/rules/${r.body.id}`, { enabled: false })).body.enabled).toBe(false);
  });

  it('backups: schedule validation, manual run, restore needs step-up and reports a pre-restore snapshot', async () => {
    await enable('databases');
    const id = c.resource('job-worker').id;
    expect((await c.post(`/resources/${id}/backups`, { cron: 'daily', destination: 'ftp', retention_days: 0 })).body.details.fields).toHaveProperty('cron');
    const s = await c.post(`/resources/${id}/backups`, { cron: '30 2 * * *', destination: 'local', retention_days: 7 });
    expect(s.status).toBe(201);
    const run = await c.post(`/backups/${s.body.id}/run`);
    expect(run.status).toBe(202);
    await until(() => c.server.db.backupRuns.find((r) => r.id === run.body.id)!.status === 'success');
    const denied = await c.post(`/backups/${run.body.id}/restore`);
    expect(denied.body.code).toBe('step_up_required');
    await c.stepUp();
    const rs = await c.post(`/backups/${run.body.id}/restore`);
    expect(rs.body).toMatchObject({ ok: true });
    expect(rs.body.pre_restore_snapshot).toMatch(/pre-restore/);
    const failed = c.server.db.backupRuns.find((r) => r.status === 'failed')!;
    expect((await c.post(`/backups/${failed.id}/restore`)).status).toBe(409);
  });

  it('templates: compose templates are flag-gated; plain templates deploy through the normal engine', async () => {
    await enable('templates');
    const proj = c.server.db.projects[0].id;
    const body = { project_id: proj, environment: 'production', name: 'kuma' };
    const gated = await c.post('/templates/uptime-kuma/deploy', body);
    expect(gated.status).toBe(409);
    expect(gated.body.message).toMatch(/deploy\.compose/);
    const ok = await c.post('/templates/redis/deploy', { ...body, name: 'cache' });
    expect(ok.status).toBe(201);
    expect(ok.body.resource.type).toBe('database');
    await until(() => c.server.db.deployments.find((d) => d.id === ok.body.deployment.id)!.status === 'succeeded');
    expect((await c.post('/templates/redis/deploy', { ...body, environment: 'nope' })).status).toBe(422);
  });
});
