import type { components } from '@/api/schema';
import { HOST, computeScore, type Db } from '../db';
import { policyFor, startDeployment } from '../engine';
import { audit, hasPerm, noContent, ok, paginate, requireStepUp, route } from '../router';
import { HttpError, conflict, forbidden, invalid, notFound, randomHex, ulid } from '../util';
import { IN_PROGRESS } from '@/api/types';

type S = components['schemas'];

const resourceOf = (db: Db, id: string) => {
  const r = db.resources.find((x) => x.id === id);
  if (!r) throw notFound('resource');
  return r;
};

function fnv(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 0x01000193) >>> 0; }
  return h.toString(16).padStart(8, '0');
}

const SETTING_RULES: Record<string, { type: 'boolean' | 'int' | 'number'; min?: number; max?: number; label: string }> = {
  'terminal.allow_root': { type: 'boolean', label: 'Allow root terminal' },
  'terminal.idle_timeout_s': { type: 'int', min: 60, max: 3600, label: 'Terminal idle timeout (s)' },
  retain_images: { type: 'int', min: 1, max: 20, label: 'Retained images per resource' },
  observation_window_s: { type: 'int', min: 10, max: 600, label: 'Observation window (s)' },
  step_up_window_s: { type: 'int', min: 60, max: 900, label: 'Step-up window (s)' },
  hardening_revert_window_s: { type: 'int', min: 30, max: 900, label: 'Dead-man revert window (s)' },
  disk_preflight_threshold_gb: { type: 'number', min: 1, max: 100, label: 'Disk pre-flight threshold (GiB)' },
  session_ttl_h: { type: 'int', min: 1, max: 72, label: 'Session lifetime (h)' },
};

const tier = (mb: number): S['SystemStatus']['support_tier'] => (mb < 1024 ? 'experimental' : mb < 2048 ? 'limited' : mb < 4096 ? 'primary' : mb < 8192 ? 'comfortable' : 'headroom');

const GIT_URL_RE = /^(git@[\w.-]+:[\w./-]+\.git|https:\/\/[\w.-]+\/[\w./-]+(\.git)?)$/;

async function hmacHex(secret: string, text: string): Promise<string> {
  const key = await crypto.subtle.importKey('raw', new TextEncoder().encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const sig = await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(text));
  return [...new Uint8Array(sig)].map((b) => b.toString(16).padStart(2, '0')).join('');
}
const safeEqual = (a: string, b: string) => { let d = a.length ^ b.length; for (let i = 0; i < Math.max(a.length, b.length); i++) d |= (a.charCodeAt(i) || 0) ^ (b.charCodeAt(i) || 0); return d === 0; };

function delivery(db: Db, d: Partial<S['WebhookDelivery']> & Pick<S['WebhookDelivery'], 'result' | 'event'>): S['WebhookDelivery'] {
  const row: S['WebhookDelivery'] = { id: ulid('whd_'), provider: 'github', delivery_id: randomHex(8), received_at: new Date().toISOString(), reason: null, commit_sha: null, resource_id: null, ...d };
  db.deliveries.unshift(row);
  return row;
}

export function registerPlatform() {
  // ---------- edge ----------
  const edgeStatus = (db: Db): S['EdgeStatus'] => ({
    caddy_running: true, admin_socket: '/run/codeplo/caddy-admin.sock', config_hash: fnv(db.domains.map((d) => d.hostname + d.cert_status).sort().join('|')),
    last_reconciled_at: db.edge.last_reconciled_at, drift: db.edge.drift, route_count: db.domains.length, server_now: new Date().toISOString(),
  });
  route('GET', '/edge/status', 'edge:read', (c) => ok(edgeStatus(c.db)), 'edge');
  route('POST', '/edge/reconcile', 'edge:reconcile', async (c) => {
    c.db.edge.last_reconciled_at = new Date().toISOString();
    c.db.edge.drift = false;
    await audit(c, 'edge.reconcile', 'edge', 'caddy', {});
    c.db.bus.emit('edge.changed', {});
    return ok(edgeStatus(c.db));
  }, 'edge');
  route('GET', '/edge/certificates', 'edge:read', (c) => ok({ items: c.db.domains.map((d) => ({ hostname: d.hostname, issuer: d.issuer, status: d.cert_status, not_after: d.cert_expires_at, error: d.cert_error ?? null })) }), 'edge');

  // ---------- system ----------
  route('GET', '/system/status', 'system:read', (c) => {
    const db = c.db;
    const status: S['SystemStatus'] = {
      ...HOST, ram_total_mb: HOST.ram_total_mb, support_tier: tier(HOST.ram_total_mb), uptime_s: Math.floor((Date.now() - db.startedAt) / 1000), metrics: db.metrics,
      operator_configured: !!db.operator, operator_user: db.operator, security_score: computeScore(db.checks), pending_changes: db.changes.filter((x) => x.state === 'pending_confirm').length,
      active_alerts: db.rules.filter((r) => r.enabled && r.last_triggered_at && Date.now() - Date.parse(r.last_triggered_at) < 6 * 3_600_000).length, server_now: new Date().toISOString(),
    };
    return ok(status);
  });
  route('GET', '/system/metrics', 'system:metrics', (c) => {
    const range = c.query.get('range') ?? '1h';
    const n: Record<string, number> = { '15m': 15, '1h': 60, '6h': 240, '24h': 240 };
    if (!(range in n)) throw invalid({ range: 'Use 15m, 1h, 6h or 24h' });
    return ok({ range, points: c.db.metricsHistory.slice(-n[range]) });
  }, 'monitor');
  route('POST', '/system/cleanup', 'system:cleanup', async (c) => {
    const images = c.db.deployments.filter((d) => !d.retained && d.image_digest).length + 3;
    const reclaimed = 180 + images * 40;
    c.db.metrics = { ...c.db.metrics, disk_used_gb: +Math.max(2, c.db.metrics.disk_used_gb - reclaimed / 1024).toFixed(1) };
    c.db.scenario.lowDisk = false;
    await audit(c, 'system.cleanup', 'system', 'docker', { reclaimed_mb: reclaimed });
    return ok({ reclaimed_mb: reclaimed, images_removed: images, containers_removed: 1, build_cache_mb: 96 });
  });
  route('GET', '/system/version', 'system:read', () => ok({ version: '1.2.0', channel: 'stable', commit: '8f3c1d2', built_at: '2026-10-01T09:00:00Z', go_version: 'go1.23', db_schema: 14 }));

  // ---------- modules / flags / settings ----------
  route('GET', '/modules', 'session', (c) => ok({ items: c.db.modules }));
  route('PATCH', '/modules/:key', 'modules:manage', async (c) => {
    const m = c.db.modules.find((x) => x.key === c.params.key);
    if (!m) throw notFound('module');
    if (typeof c.body?.enabled !== 'boolean') throw invalid({ enabled: 'enabled must be true or false' });
    if (m.core && !c.body.enabled) throw conflict(`"${m.name}" is a core module and cannot be disabled`);
    m.enabled = c.body.enabled;
    await audit(c, m.enabled ? 'module.enabled' : 'module.disabled', 'module', m.key, {});
    c.db.bus.emit('module.changed', { key: m.key });
    return ok(m);
  });
  route('GET', '/feature-flags', 'session', (c) => ok({ items: c.db.flags }));
  route('PATCH', '/feature-flags', 'flags:manage', async (c) => {
    const flags = c.body?.flags;
    if (!flags || typeof flags !== 'object') throw invalid({ flags: 'flags is required' });
    const f: Record<string, string> = {};
    for (const [k, v] of Object.entries(flags)) {
      if (!c.db.flags.some((x) => x.key === k)) f[k] = 'Unknown flag';
      else if (typeof v !== 'boolean') f[k] = 'Must be true or false';
    }
    if (Object.keys(f).length) throw invalid(f);
    for (const [k, v] of Object.entries(flags) as [string, boolean][]) {
      const fl = c.db.flags.find((x) => x.key === k)!;
      if (fl.enabled !== v) { fl.enabled = v; await audit(c, 'flag.changed', 'flag', k, { enabled: v }); c.db.bus.emit('flag.changed', { key: k }); }
    }
    return ok({ items: c.db.flags });
  });
  route('GET', '/settings', 'session', (c) => ok({ values: c.db.settings }));
  route('PATCH', '/settings', 'flags:manage', async (c) => {
    const values = c.body?.values;
    if (!values || typeof values !== 'object') throw invalid({ values: 'values is required' });
    const f: Record<string, string> = {};
    for (const [k, v] of Object.entries(values)) {
      const rule = SETTING_RULES[k];
      if (!rule) { f[k] = 'Unknown setting'; continue; }
      if (rule.type === 'boolean') { if (typeof v !== 'boolean') f[k] = 'Must be true or false'; continue; }
      if (typeof v !== 'number' || !Number.isFinite(v) || (rule.type === 'int' && !Number.isInteger(v))) f[k] = 'Must be a number';
      else if (v < (rule.min ?? -Infinity) || v > (rule.max ?? Infinity)) f[k] = `Must be between ${rule.min} and ${rule.max}`;
    }
    if (Object.keys(f).length) throw invalid(f);
    Object.assign(c.db.settings, values);
    await audit(c, 'settings.updated', 'settings', 'server', { keys: Object.keys(values), values });
    c.db.bus.emit('flag.changed', { key: 'settings' });
    return ok({ values: c.db.settings });
  });

  // ---------- terminal (WebSocket itself lives in install.ts) ----------
  route('POST', '/terminal/ticket', 'terminal:container', async (c) => {
    const db = c.db;
    const b = c.body ?? {};
    if (b.target !== 'host' && b.target !== 'container') throw invalid({ target: 'target must be host or container' });
    let resource_id: string | null = null;
    if (b.target === 'host') {
      if (!hasPerm(c, 'terminal:host')) throw forbidden('Missing permission: terminal:host');
      if (!db.operator) throw new HttpError(409, 'conflict', 'The host shell needs an operator identity. Configure one under Server → Security first.', { reason: 'operator_missing' });
    } else {
      if (!b.resource_id) throw invalid({ resource_id: 'resource_id is required for a container shell' });
      const r = resourceOf(db, b.resource_id);
      if (!r.active_deployment_id || r.status === 'stopped') throw conflict('The container is not running');
      resource_id = r.id;
    }
    if (b.as_root) {
      if (b.target !== 'host') throw invalid({ as_root: 'Root applies to the host shell only' });
      if (db.settings['terminal.allow_root'] !== true) throw forbidden('The root shell is disabled in server settings');
      if (!hasPerm(c, 'terminal:root')) throw forbidden('Missing permission: terminal:root');
      requireStepUp(c);
    }
    const ticket = `tk_${randomHex(16)}`;
    const expires = Date.now() + 30_000;
    db.tickets.set(ticket, { ticket, expires_at: expires, used: false, target: b.target, resource_id, as_root: !!b.as_root, user_id: c.user!.id });
    await audit(c, 'terminal.ticket_issued', 'terminal', b.target, { resource_id, as_root: !!b.as_root });
    return ok({ ticket, expires_at: new Date(expires).toISOString(), idle_timeout_s: Number(db.settings['terminal.idle_timeout_s'] ?? 900) }, 201);
  }, 'terminal');
  route('GET', '/terminal/sessions', 'terminal:host', (c) => ok(paginate([...c.db.termSessions].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at)), c.query)), 'terminal');

  // ---------- git / ci ----------
  route('GET', '/git/connections', 'git:write', (c) => ok({ items: c.db.gitConnections }), 'git');
  route('POST', '/git/connections', 'git:write', async (c) => {
    const name = String(c.body?.name ?? '').trim();
    if (!name) throw invalid({ name: 'Give the connection a name' });
    const g: S['GitConnection'] = { id: ulid('gcn_'), kind: 'deploy_key', name, created_at: new Date().toISOString() };
    c.db.gitConnections.push(g);
    await audit(c, 'git.connection_created', 'git_connection', g.id, { name });
    return ok(g, 201);
  }, 'git');
  route('GET', '/resources/:id/source', 'resource:read', (c) => {
    resourceOf(c.db, c.params.id);
    return ok(c.db.sources.get(c.params.id) ?? { kind: 'registry', repo_url: null, branch: null, build_path: null, dockerfile: null, path_filters: [], deploy_key_id: null });
  }, 'git');
  route('PUT', '/resources/:id/source', 'git:write', async (c) => {
    const r = resourceOf(c.db, c.params.id);
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    if (!['public_git', 'deploy_key_git', 'registry'].includes(b.kind)) f.kind = 'Choose a source kind';
    if (b.kind !== 'registry') {
      if (!b.repo_url || !GIT_URL_RE.test(b.repo_url)) f.repo_url = 'Enter an SSH (git@host:org/repo.git) or HTTPS repository URL';
      if (!b.branch) f.branch = 'Branch is required';
      if (b.dockerfile && /^\/|\.\./.test(b.dockerfile)) f.dockerfile = 'Dockerfile must stay inside the build context';
      if (b.build_path && /^\/|\.\./.test(b.build_path)) f.build_path = 'Build path must stay inside the clone';
      if (b.kind === 'deploy_key_git' && !c.db.deployKeys.has(r.id)) f.deploy_key_id = 'Generate a deploy key first';
    }
    if (Object.keys(f).length) throw invalid(f);
    const src: S['ResourceSource'] = { kind: b.kind, repo_url: b.repo_url ?? null, branch: b.branch ?? null, build_path: b.build_path ?? null, dockerfile: b.dockerfile ?? null, path_filters: Array.isArray(b.path_filters) ? b.path_filters : [], deploy_key_id: b.kind === 'deploy_key_git' ? c.db.deployKeys.get(r.id)!.id : null };
    c.db.sources.set(r.id, src);
    r.config = { ...r.config, image: { ...r.config.image, mode: b.kind === 'registry' ? 'pull' : 'build', dockerfile: src.dockerfile, context: src.build_path } };
    await audit(c, 'source.updated', 'resource', r.id, { kind: src.kind, repo_url: src.repo_url, branch: src.branch });
    c.db.bus.emit('resource.updated', { resource_id: r.id });
    return ok(src);
  }, 'git');
  route('POST', '/resources/:id/deploy-key', 'git:write', async (c) => {
    const r = resourceOf(c.db, c.params.id);
    const key: S['DeployKey'] = {
      id: ulid('key_'), title: `${r.name} deploy key`, public_key: `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI${randomHex(16)}DemoKeyOnly codeplo:${r.name}`,
      fingerprint: `SHA256:${btoa(randomHex(16)).replace(/=/g, '')}`, created_at: new Date().toISOString(),
    };
    c.db.deployKeys.set(r.id, key); // the private half is never stored in, or returned by, the API
    await audit(c, 'deploy_key.created', 'resource', r.id, { fingerprint: key.fingerprint });
    return ok(key, 201);
  }, 'git');
  route('GET', '/resources/:id/deploy-key', 'resource:read', (c) => {
    resourceOf(c.db, c.params.id);
    const k = c.db.deployKeys.get(c.params.id);
    if (!k) throw notFound('deploy key');
    return ok(k);
  }, 'git');
  const hookFor = (db: Db, id: string) => { resourceOf(db, id); return db.hooks.get(id); };
  const newHook = (db: Db, r: S['Resource'], prev?: S['DeployHook']) => {
    const token = `cplh_${randomHex(24)}`;
    const base = (r.config.image.ref ?? `ghcr.io/org/${r.slug}`).replace(/[:@].*$/, '');
    const hook: S['DeployHook'] = { id: prev?.id ?? ulid('hok_'), token_prefix: token.slice(0, 9), last_used_at: null, created_at: new Date().toISOString(), image_policy: `${base}:*` };
    db.hooks.set(r.id, { hook, token });
    return { hook, token };
  };
  route('GET', '/resources/:id/deploy-hook', 'hook:manage', (c) => {
    const h = hookFor(c.db, c.params.id);
    if (!h) throw notFound('deploy hook');
    return ok(h.hook); // prefix only: the token is hashed at rest
  }, 'git');
  route('POST', '/resources/:id/deploy-hook', 'hook:manage', async (c) => {
    if (hookFor(c.db, c.params.id)) throw conflict('A deploy hook already exists. Rotate it to get a new token.');
    const out = newHook(c.db, resourceOf(c.db, c.params.id));
    await audit(c, 'deploy_hook.created', 'resource', c.params.id, { token_prefix: out.hook.token_prefix });
    return ok(out, 201);
  }, 'git');
  route('POST', '/resources/:id/deploy-hook/rotate', 'hook:manage', async (c) => {
    const prev = hookFor(c.db, c.params.id);
    if (!prev) throw notFound('deploy hook');
    const out = newHook(c.db, resourceOf(c.db, c.params.id), prev.hook);
    await audit(c, 'deploy_hook.rotated', 'resource', c.params.id, { token_prefix: out.hook.token_prefix });
    return ok(out);
  }, 'git');
  route('GET', '/resources/:id/webhook', 'hook:manage', (c) => {
    resourceOf(c.db, c.params.id);
    return ok(c.db.webhooks.get(c.params.id)?.info ?? { configured: false, secret_prefix: null, created_at: null, path: '/api/v1/webhooks/github' });
  }, 'git');
  route('POST', '/resources/:id/webhook', 'hook:manage', async (c) => {
    resourceOf(c.db, c.params.id);
    const secret = `whsec_${randomHex(20)}`;
    const info: S['WebhookInfo'] = { configured: true, secret_prefix: secret.slice(0, 9), created_at: new Date().toISOString(), path: '/api/v1/webhooks/github' };
    c.db.webhooks.set(c.params.id, { info, secret });
    await audit(c, 'webhook.created', 'resource', c.params.id, { secret_prefix: info.secret_prefix });
    return ok({ webhook: info, secret }, 201);
  }, 'git');
  route('GET', '/webhooks/deliveries', 'hook:manage', (c) => {
    const rid = c.query.get('resource_id');
    return ok(paginate(c.db.deliveries.filter((d) => !rid || d.resource_id === rid), c.query));
  }, 'git');

  // Server-side receivers (public routes, authenticated by HMAC / unguessable token). The UI never calls these.
  route('POST', '/webhooks/github', null, async (c) => {
    const db = c.db;
    const sig = c.req.headers.get('X-Hub-Signature-256') ?? '';
    const did = c.req.headers.get('X-GitHub-Delivery') ?? '';
    const event = c.req.headers.get('X-GitHub-Event') ?? 'push';
    const repo = String(c.body?.repository?.ssh_url ?? c.body?.repository?.clone_url ?? '');
    const target = db.resources.find((r) => { const s = db.sources.get(r.id); return s?.repo_url && s.repo_url === repo; });
    const secret = target ? db.webhooks.get(target.id)?.secret : undefined;
    const expected = secret ? `sha256=${await hmacHex(secret, c.raw)}` : '';
    if (!secret || !safeEqual(sig, expected)) {
      delivery(db, { event, result: 'rejected', reason: 'Invalid HMAC-SHA256 signature', resource_id: target?.id ?? null });
      throw new HttpError(401, 'unauthenticated', 'Invalid signature');
    }
    if (!did) throw invalid({ 'X-GitHub-Delivery': 'Missing delivery id' });
    if (db.deliveries.some((d) => d.delivery_id === did)) {
      delivery(db, { event, result: 'rejected', reason: 'Delivery ID already seen (replay)', resource_id: target!.id, delivery_id: `${did}#replay${randomHex(2)}` });
      throw conflict('Delivery already processed');
    }
    const src = db.sources.get(target!.id)!;
    const branch = String(c.body?.ref ?? '').replace('refs/heads/', '');
    const sha = String(c.body?.after ?? '').slice(0, 7) || null;
    const msg = String(c.body?.head_commit?.message ?? '');
    let reason: string | null = null;
    if (event !== 'push') reason = `Event "${event}" is not handled`;
    else if (branch !== src.branch) reason = `Branch "${branch}" does not match configured branch "${src.branch}"`;
    else if (/\[skip deploy\]/i.test(msg)) reason = 'Commit message contains [skip deploy]';
    else if (src.path_filters.length) {
      const changed: string[] = (c.body?.commits ?? []).flatMap((k: { added?: string[]; modified?: string[]; removed?: string[] }) => [...(k.added ?? []), ...(k.modified ?? []), ...(k.removed ?? [])]);
      const hit = changed.some((p) => src.path_filters.some((f) => (f.endsWith('/**') ? p.startsWith(f.slice(0, -2)) : p === f)));
      if (changed.length && !hit) reason = 'No changed path matches the configured path filters';
    }
    if (!reason && db.deployments.some((d) => d.resource_id === target!.id && IN_PROGRESS.includes(d.status))) reason = 'A deployment is already in progress';
    const row = delivery(db, { event, delivery_id: did, result: reason ? 'ignored' : 'success', reason, commit_sha: sha, resource_id: target!.id });
    if (!reason) startDeployment(db, target!, { mode: 'build', image_ref: `cpl-${target!.slug}:${sha ?? 'head'}`, trigger: 'webhook', branch });
    void policyFor;
    return { status: 202, body: { delivery_id: row.delivery_id, result: row.result } };
  });
  route('POST', '/hooks/deploy/:token', null, async (c) => {
    const db = c.db;
    const entry = [...db.hooks.entries()].find(([, h]) => safeEqual(h.token, c.params.token));
    if (!entry) throw new HttpError(404, 'not_found', 'Unknown deploy hook');
    const r = resourceOf(db, entry[0]);
    const image = String(c.body?.image ?? '');
    const policy = entry[1].hook.image_policy ?? '';
    if (!image || !policy.endsWith(':*') || !image.startsWith(policy.slice(0, -1))) {
      delivery(db, { provider: 'deploy_hook', event: 'deploy', result: 'rejected', reason: `Image "${image || '(none)'}" is outside the hook's image policy ${policy}`, resource_id: r.id });
      throw invalid({ image: `Image must match ${policy}` });
    }
    if (db.deployments.some((d) => d.resource_id === r.id && IN_PROGRESS.includes(d.status))) throw conflict('A deployment is already in progress');
    entry[1].hook.last_used_at = new Date().toISOString();
    delivery(db, { provider: 'deploy_hook', event: 'deploy', result: 'success', commit_sha: null, resource_id: r.id });
    const d = startDeployment(db, r, { mode: 'pull', image_ref: image, trigger: 'deploy_hook' });
    return { status: 202, body: { deployment_id: d.id } };
  });
  void noContent;
}
