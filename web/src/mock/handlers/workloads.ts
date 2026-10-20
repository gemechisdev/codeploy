import type { components } from '@/api/schema';
import { CANCELLABLE, IN_PROGRESS } from '@/api/types';
import { HOST, defaultResourceConfig, type Db, type EnvRow } from '../db';
import { applyDomainCreate, cancelDeployment, policyFor, startDeployment } from '../engine';
import { audit, hasPerm, noContent, ok, paginate, requireStepUp, route, type Ctx } from '../router';
import { HttpError, conflict, envKeyRe, forbidden, hostRe, invalid, notFound, slugRe, ulid } from '../util';

type S = components['schemas'];

const IMAGE_RE = /^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]+)?(@sha256:[a-f0-9]{64})?$/;
const CAP_ALLOW = ['NET_BIND_SERVICE', 'CHOWN', 'SETUID', 'SETGID'];
const FORBIDDEN_MOUNTS = ['/', '/etc', '/proc', '/sys', '/dev', '/var/run', '/var/lib/docker'];
const GIT_URL_RE = /^(git@[\w.-]+:[\w./-]+\.git|https:\/\/[\w.-]+\/[\w./-]+(\.git)?)$/;

const flagOn = (db: Db, key: string) => !!db.flags.find((f) => f.key === key)?.enabled;

function getResource(db: Db, id: string): S['Resource'] {
  const r = db.resources.find((x) => x.id === id);
  if (!r) throw notFound('resource');
  return r;
}

export function validateConfig(cfg: S['ResourceConfig'] | undefined, db: Db, owner: boolean): Record<string, string> {
  const f: Record<string, string> = {};
  if (!cfg) return { config: 'config is required' };
  if (cfg.image.mode === 'pull') {
    if (!cfg.image.ref || !IMAGE_RE.test(cfg.image.ref)) f['config.image.ref'] = 'Enter an image reference such as ghcr.io/org/app:tag';
  } else if (cfg.image.dockerfile && /^\/|\.\./.test(cfg.image.dockerfile)) f['config.image.dockerfile'] = 'Dockerfile path must stay inside the build context';
  if (!Number.isInteger(cfg.port) || cfg.port < 1 || cfg.port > 65535) f['config.port'] = 'Port must be 1-65535';
  if (!cfg.healthcheck.path.startsWith('/')) f['config.healthcheck.path'] = 'Health path must start with "/"';
  if (cfg.healthcheck.retries < 1 || cfg.healthcheck.retries > 60) f['config.healthcheck.retries'] = 'Retries must be 1-60';
  if (!Number.isInteger(cfg.limits.memory_mb) || cfg.limits.memory_mb < 16 || cfg.limits.memory_mb > HOST.ram_total_mb) f['config.limits.memory_mb'] = `Memory must be 16-${HOST.ram_total_mb} MB`;
  if (!(cfg.limits.cpus >= 0.1 && cfg.limits.cpus <= 4)) f['config.limits.cpus'] = 'CPU must be 0.1-4';
  if (!Number.isInteger(cfg.limits.pids_limit) || cfg.limits.pids_limit < 16 || cfg.limits.pids_limit > 4096) f['config.limits.pids_limit'] = 'PIDs limit must be 16-4096';
  const bad = cfg.isolation.cap_add.filter((c) => !CAP_ALLOW.includes(c));
  if (bad.length) f['config.isolation.cap_add'] = `Not allowed: ${bad.join(', ')}. Allowed additions: ${CAP_ALLOW.join(', ')}`;
  if (cfg.isolation.trusted && !owner) f['config.isolation.trusted'] = 'Only an owner can mark a resource trusted';
  if (cfg.retain_images < 1 || cfg.retain_images > 20) f['config.retain_images'] = 'Retain 1-20 images';
  void db;
  return f;
}

function checkDomain(db: Db, hostname: unknown): string {
  const h = String(hostname ?? '').trim().toLowerCase();
  if (!hostRe.test(h)) throw invalid({ hostname: 'Enter a valid hostname such as app.example.com' });
  if (db.domains.some((d) => d.hostname === h && d.is_panel) || db.setup.panel_domain === h) throw invalid({ hostname: 'This hostname is reserved for the panel' });
  if (db.domains.some((d) => d.hostname === h)) throw conflict(`${h} is already routed to another resource`, { hostname: h });
  return h;
}

export async function createResourceInternal(c: Ctx, input: S['CreateResourceRequest']): Promise<S['CreateResourceResponse']> {
  const db = c.db;
  const f: Record<string, string> = {};
  const env = db.environments.find((e) => e.id === input.environment_id);
  if (!env) f.environment_id = 'Choose an environment';
  if (!slugRe.test(String(input.name ?? ''))) f.name = 'Use lowercase letters, digits and "-" (1-40 chars, no leading or trailing "-")';
  if (input.type === 'compose' && !flagOn(db, 'deploy.compose')) f.type = 'Compose resources are disabled (feature flag deploy.compose)';
  if (input.type === 'static' && !flagOn(db, 'deploy.static')) f.type = 'Static sites are disabled (feature flag deploy.static)';
  if (!['app', 'compose', 'static'].includes(input.type)) f.type = 'Unsupported resource type';
  Object.assign(f, validateConfig(input.config, db, c.user?.role === 'owner'));
  if (input.config?.image.mode === 'build') {
    const s = input.source;
    if (!s || s.kind === 'registry') f.source = 'A Git source is required to build on the host';
    else if (!s.repo_url || !GIT_URL_RE.test(s.repo_url)) f['source.repo_url'] = 'Enter an SSH (git@host:org/repo.git) or HTTPS repository URL';
    else if (!s.branch) f['source.branch'] = 'Branch is required';
  }
  if (Object.keys(f).length) throw invalid(f);
  if (db.resources.some((r) => r.slug === input.name)) throw conflict(`A resource named "${input.name}" already exists`, { name: input.name });
  const domain = input.domain ? checkDomain(db, input.domain) : null;
  if (input.deploy_now && !hasPerm(c, 'deploy:create')) throw forbidden('Missing permission: deploy:create');

  const r: S['Resource'] = {
    id: ulid('res_'), environment_id: env!.id, project_id: env!.project_id, environment_name: env!.name, type: input.type, name: input.name, slug: input.name,
    config: { ...defaultResourceConfig(), ...input.config }, status: 'stopped', desired_state: 'stopped', active_deployment_id: null, active_digest: null,
    primary_domain: null, started_at: null, created_at: new Date().toISOString(),
  };
  db.resources.push(r);
  db.env.set(r.id, []);
  db.volumes.set(r.id, []);
  db.sources.set(r.id, input.source ?? { kind: 'registry', repo_url: null, branch: null, build_path: null, dockerfile: null, path_filters: [], deploy_key_id: null });
  if (domain) applyDomainCreate(db, { hostname: domain, resource: r, force_https: true, redirect_www: false });
  await audit(c, 'resource.created', 'resource', r.id, { name: r.name, type: r.type, mode: r.config.image.mode });
  db.bus.emit('resource.updated', { resource_id: r.id });
  let deployment: S['Deployment'] | null = null;
  let snapshot: S['Deployment'] | null = null;
  if (input.deploy_now) {
    const cfg = r.config;
    deployment = startDeployment(db, r, { mode: cfg.image.mode, image_ref: cfg.image.mode === 'pull' ? cfg.image.ref! : `cpl-${r.slug}:pending`, trigger: 'manual', branch: input.source?.branch ?? null });
    snapshot = { ...deployment };
    await audit(c, 'deployment.created', 'deployment', deployment.id, { resource: r.name, trigger: 'manual' });
  }
  return { resource: { ...r, status: deployment ? 'deploying' : r.status, active_deployment_id: null, active_digest: null }, deployment: snapshot };
}

function purge(db: Db, r: S['Resource']) {
  db.resources = db.resources.filter((x) => x.id !== r.id);
  db.domains = db.domains.filter((d) => d.resource_id !== r.id);
  for (const d of db.deployments.filter((x) => x.resource_id === r.id)) { db.logs.delete(d.id); db.deployTimers.get(d.id)?.forEach(clearTimeout); db.deployTimers.delete(d.id); }
  db.deployments = db.deployments.filter((d) => d.resource_id !== r.id);
  db.env.delete(r.id); db.volumes.delete(r.id); db.sources.delete(r.id); db.deployKeys.delete(r.id); db.hooks.delete(r.id); db.webhooks.delete(r.id);
  db.schedules = db.schedules.filter((s) => s.resource_id !== r.id);
}

const secretFree = (e: EnvRow): S['EnvVar'] => ({ id: e.id, key: e.key, value: e.is_secret ? null : e.value, scope: e.scope, is_secret: e.is_secret, updated_at: e.updated_at });

export function registerWorkloads() {
  // ---------- projects & environments ----------
  route('GET', '/projects', 'resource:read', (c) => ok(paginate(c.db.projects, c.query)));
  route('POST', '/projects', 'project:write', async (c) => {
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    if (!String(b.name ?? '').trim()) f.name = 'Give the project a name';
    if (!slugRe.test(String(b.slug ?? ''))) f.slug = 'Use lowercase letters, digits and "-"';
    if (Object.keys(f).length) throw invalid(f);
    if (c.db.projects.some((p) => p.slug === b.slug)) throw conflict(`A project with slug "${b.slug}" already exists`);
    const p: S['Project'] = { id: ulid('prj_'), name: String(b.name).trim(), slug: b.slug, description: String(b.description ?? ''), created_at: new Date().toISOString() };
    c.db.projects.push(p);
    c.db.environments.push({ id: ulid('env_'), project_id: p.id, name: 'production' });
    await audit(c, 'project.created', 'project', p.id, { slug: p.slug });
    return ok(p, 201);
  });
  route('GET', '/projects/:id', 'resource:read', (c) => {
    const p = c.db.projects.find((x) => x.id === c.params.id);
    if (!p) throw notFound('project');
    return ok({ project: p, environments: c.db.environments.filter((e) => e.project_id === p.id).map((environment) => ({ environment, resources: c.db.resources.filter((r) => r.environment_id === environment.id) })) });
  });
  route('GET', '/projects/:id/environments', 'resource:read', (c) => {
    if (!c.db.projects.some((x) => x.id === c.params.id)) throw notFound('project');
    return ok({ items: c.db.environments.filter((e) => e.project_id === c.params.id) });
  });
  route('POST', '/projects/:id/environments', 'project:write', async (c) => {
    if (!c.db.projects.some((x) => x.id === c.params.id)) throw notFound('project');
    const name = String(c.body?.name ?? '');
    if (!slugRe.test(name)) throw invalid({ name: 'Use lowercase letters, digits and "-"' });
    if (c.db.environments.some((e) => e.project_id === c.params.id && e.name === name)) throw conflict(`Environment "${name}" already exists`);
    const e: S['Environment'] = { id: ulid('env_'), project_id: c.params.id, name };
    c.db.environments.push(e);
    await audit(c, 'environment.created', 'environment', e.id, { name });
    return ok(e, 201);
  });

  // ---------- resources ----------
  route('GET', '/resources', 'resource:read', (c) => {
    const q = c.query.get('q')?.toLowerCase(); const env = c.query.get('environment_id');
    return ok(paginate(c.db.resources.filter((r) => (!q || r.name.includes(q)) && (!env || r.environment_id === env)), c.query));
  });
  route('POST', '/resources', 'resource:write', async (c) => ok(await createResourceInternal(c, c.body ?? {}), 201));
  route('GET', '/resources/:id', 'resource:read', (c) => ok(getResource(c.db, c.params.id)));
  route('PATCH', '/resources/:id', 'resource:write', async (c) => {
    const r = getResource(c.db, c.params.id);
    const b = c.body ?? {};
    if (b.name !== undefined && b.name !== r.name) throw invalid({ name: 'Resources cannot be renamed: container and image names embed the slug' });
    if (b.config) {
      const next = { ...r.config, ...b.config } as S['ResourceConfig'];
      const f = validateConfig(next, c.db, c.user?.role === 'owner');
      if (Object.keys(f).length) throw invalid(f);
      r.config = next;
    }
    await audit(c, 'resource.updated', 'resource', r.id, { fields: Object.keys(b) });
    c.db.bus.emit('resource.updated', { resource_id: r.id });
    return ok(r);
  });
  route('DELETE', '/resources/:id', 'resource:delete', async (c) => {
    const r = getResource(c.db, c.params.id);
    if (r.status === 'deploying') throw conflict('Cancel the running deployment before deleting this resource');
    purge(c.db, r);
    await audit(c, 'resource.deleted', 'resource', r.id, { name: r.name });
    c.db.bus.emit('resource.updated', { resource_id: r.id });
    c.db.bus.emit('edge.changed', {});
    return noContent();
  });
  for (const action of ['start', 'stop', 'restart'] as const) {
    route('POST', `/resources/:id/${action}`, 'resource:control', async (c) => {
      const r = getResource(c.db, c.params.id);
      if (r.status === 'deploying') throw conflict('A deployment is in progress');
      if (action !== 'stop' && !r.active_deployment_id) throw conflict('This resource has no successful deployment yet. Deploy it first.');
      if (action === 'stop') { r.status = 'stopped'; r.desired_state = 'stopped'; }
      else { r.status = 'running'; r.desired_state = 'running'; r.started_at = new Date().toISOString(); }
      await audit(c, `resource.${action}`, 'resource', r.id, {});
      c.db.bus.emit('resource.updated', { resource_id: r.id });
      return ok(r);
    });
  }
  route('GET', '/resources/:id/policy', 'resource:read', (c) => ok(policyFor(c.db, getResource(c.db, c.params.id))));

  // ---------- env / secrets (write-only) ----------
  route('GET', '/resources/:id/env', 'env:read', (c) => {
    getResource(c.db, c.params.id);
    return ok({ items: (c.db.env.get(c.params.id) ?? []).map(secretFree) });
  });
  route('PUT', '/resources/:id/env', 'env:write', async (c) => {
    const r = getResource(c.db, c.params.id);
    const items: S['EnvVarInput'][] = c.body?.items;
    if (!Array.isArray(items)) throw invalid({ items: 'items is required' });
    const existing = c.db.env.get(r.id) ?? [];
    const f: Record<string, string> = {};
    const seen = new Set<string>();
    let replacesSecret = false;
    items.forEach((it, i) => {
      const k = `items.${i}.key`;
      if (!envKeyRe.test(it.key ?? '')) f[k] = 'Keys must match [A-Za-z_][A-Za-z0-9_]*';
      else if (seen.has(it.key)) f[k] = 'Duplicate key';
      seen.add(it.key);
      if (!['build', 'runtime', 'both'].includes(it.scope)) f[`items.${i}.scope`] = 'Scope must be build, runtime or both';
      const prev = existing.find((e) => e.key === it.key);
      if (prev?.is_secret && !it.is_secret) f[k] = 'A secret cannot be converted to a plain variable. Delete it and add a new one.';
      const hasValue = typeof it.value === 'string' && it.value.length > 0;
      if (it.is_secret) {
        if (!hasValue && !(prev?.is_secret && it.keep_existing_secret)) f[`items.${i}.value`] = prev ? 'Enter a new value, or keep the existing secret' : 'A secret needs a value';
        if (hasValue && prev?.is_secret) replacesSecret = true;
      } else if (it.value === undefined || it.value === null) f[`items.${i}.value`] = 'Value is required';
    });
    if (Object.keys(f).length) throw invalid(f);
    if (replacesSecret) requireStepUp(c);
    const now = new Date().toISOString();
    const changed: string[] = [];
    const next: EnvRow[] = items.map((it) => {
      const prev = existing.find((e) => e.key === it.key);
      const keep = it.is_secret && prev?.is_secret && (it.keep_existing_secret || !it.value);
      const value = keep ? prev!.value : (it.value ?? '');
      const same = prev && prev.value === value && prev.scope === it.scope && prev.is_secret === it.is_secret;
      if (!same) changed.push(it.key);
      return { id: prev?.id ?? ulid('env_'), key: it.key, value, scope: it.scope, is_secret: it.is_secret, updated_at: same ? prev!.updated_at : now };
    });
    const removed = existing.filter((e) => !seen.has(e.key)).map((e) => e.key);
    c.db.env.set(r.id, next);
    await audit(c, 'env.updated', 'resource', r.id, { changed, removed, secrets_replaced: replacesSecret }); // keys only, never values
    return ok({ items: next.map(secretFree) });
  });

  // ---------- volumes ----------
  route('GET', '/resources/:id/volumes', 'resource:read', (c) => { getResource(c.db, c.params.id); return ok({ items: c.db.volumes.get(c.params.id) ?? [] }); });
  route('POST', '/resources/:id/volumes', 'resource:write', async (c) => {
    const r = getResource(c.db, c.params.id);
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    if (!/^[a-z0-9][a-z0-9_-]{0,38}$/.test(String(b.name ?? ''))) f.name = 'Use lowercase letters, digits, "-" and "_"';
    const mp = String(b.mount_path ?? '');
    if (!mp.startsWith('/') || mp.includes('..')) f.mount_path = 'Mount path must be absolute';
    else if (FORBIDDEN_MOUNTS.includes(mp.replace(/\/+$/, '') || '/')) f.mount_path = `${mp} is a protected path and cannot be mounted`;
    if (Object.keys(f).length) throw invalid(f);
    const list = c.db.volumes.get(r.id) ?? [];
    if (list.some((v) => v.name === b.name)) throw conflict(`Volume "${b.name}" already exists`);
    if (list.some((v) => v.mount_path === mp)) throw conflict(`Something is already mounted at ${mp}`);
    const v: S['Volume'] = { id: ulid('vol_'), name: b.name, mount_path: mp, host_path: `/var/lib/codeplo/volumes/${r.slug}/${b.name}`, kind: 'bind', size_mb: 0 };
    list.push(v);
    c.db.volumes.set(r.id, list);
    await audit(c, 'volume.created', 'resource', r.id, { name: v.name, mount_path: mp });
    return ok(v, 201);
  });

  // ---------- domains ----------
  route('GET', '/domains', 'edge:read', (c) => ok(paginate(c.db.domains, c.query)), 'edge');
  route('GET', '/resources/:id/domains', 'resource:read', (c) => { getResource(c.db, c.params.id); return ok(paginate(c.db.domains.filter((d) => d.resource_id === c.params.id), c.query)); }, 'edge');
  route('POST', '/resources/:id/domains', 'domain:write', async (c) => {
    const r = getResource(c.db, c.params.id);
    const h = checkDomain(c.db, c.body?.hostname);
    const d = applyDomainCreate(c.db, { hostname: h, resource: r, force_https: c.body?.force_https ?? true, redirect_www: c.body?.redirect_www ?? false });
    const created = { ...d }; // state at creation: the cert timer may advance `d` while the audit row is written
    await audit(c, 'domain.created', 'domain', d.id, { hostname: h, resource: r.name });
    return ok(created, 201);
  }, 'edge');
  route('DELETE', '/resources/:id/domains/:domainId', 'domain:write', async (c) => {
    const r = getResource(c.db, c.params.id);
    const d = c.db.domains.find((x) => x.id === c.params.domainId && x.resource_id === r.id);
    if (!d) throw notFound('domain');
    if (d.is_panel) throw new HttpError(409, 'conflict', 'The panel domain cannot be removed here');
    c.db.domains = c.db.domains.filter((x) => x.id !== d.id);
    if (r.primary_domain === d.hostname) r.primary_domain = c.db.domains.find((x) => x.resource_id === r.id)?.hostname ?? null;
    await audit(c, 'domain.deleted', 'domain', d.id, { hostname: d.hostname });
    c.db.bus.emit('edge.changed', { domain_id: d.id });
    c.db.bus.emit('resource.updated', { resource_id: r.id });
    return noContent();
  }, 'edge');

  // ---------- deployments ----------
  route('GET', '/resources/:id/deployments', 'resource:read', (c) => {
    getResource(c.db, c.params.id);
    const rows = c.db.deployments.filter((d) => d.resource_id === c.params.id).sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
    return ok(paginate(rows, c.query));
  }, 'deploy');
  route('GET', '/deployments/:id', 'resource:read', (c) => {
    const d = c.db.deployments.find((x) => x.id === c.params.id);
    if (!d) throw notFound('deployment');
    return ok({ ...d, server_now: new Date().toISOString() });
  }, 'deploy');
  route('POST', '/resources/:id/deployments', 'deploy:create', async (c) => {
    const r = getResource(c.db, c.params.id);
    const b = c.body ?? {};
    if (c.db.deployments.some((d) => d.resource_id === r.id && IN_PROGRESS.includes(d.status))) throw conflict('A deployment is already in progress for this resource');
    const mode: 'pull' | 'build' = b.mode ?? r.config.image.mode;
    const f: Record<string, string> = {};
    let ref: string;
    if (mode === 'pull') {
      ref = b.image_ref ?? r.config.image.ref ?? '';
      if (!IMAGE_RE.test(ref)) f.image_ref = 'Enter an image reference such as ghcr.io/org/app:tag';
    } else {
      const src = c.db.sources.get(r.id);
      if (!src || src.kind === 'registry') f.mode = 'Configure a Git source before building on the host';
      ref = `cpl-${r.slug}:${Math.random().toString(16).slice(2, 9)}`;
    }
    if (Object.keys(f).length) throw invalid(f);
    const pol = policyFor(c.db, r);
    if (!pol.ok) throw conflict('Isolation policy rejected this resource', { violations: pol.violations });
    const branch = b.branch ?? c.db.sources.get(r.id)?.branch ?? null;
    const d = startDeployment(c.db, r, { mode, image_ref: ref!, trigger: 'manual', branch });
    const created = { ...d }; // response shows the state at creation, not whatever the timers did during the audit write
    await audit(c, 'deployment.created', 'deployment', d.id, { resource: r.name, mode, image_ref: ref! });
    return ok(created, 202);
  }, 'deploy');
  route('POST', '/deployments/:id/cancel', 'deploy:cancel', async (c) => {
    const d = c.db.deployments.find((x) => x.id === c.params.id);
    if (!d) throw notFound('deployment');
    if (!CANCELLABLE.includes(d.status)) throw conflict(`A deployment that is ${d.status} cannot be cancelled`);
    cancelDeployment(c.db, getResource(c.db, d.resource_id), d);
    await audit(c, 'deployment.cancelled', 'deployment', d.id, {});
    return ok(d);
  }, 'deploy');
  route('POST', '/deployments/:id/rollback', 'deploy:rollback', async (c) => {
    // plan 3.5: rollback is NOT a step-up action
    const target = c.db.deployments.find((x) => x.id === c.params.id);
    if (!target) throw notFound('deployment');
    const r = getResource(c.db, target.resource_id);
    if (c.body?.acknowledge_db_unchanged !== true) throw invalid({ acknowledge_db_unchanged: 'Acknowledge that application rollback does not roll back database changes' });
    if (target.status !== 'succeeded' || !target.image_digest || !target.retained) throw conflict('Only a succeeded deployment that still retains its image can be rolled back to');
    if (r.active_deployment_id === target.id) throw conflict('This deployment is already the active one');
    if (c.db.deployments.some((d) => d.resource_id === r.id && IN_PROGRESS.includes(d.status))) throw conflict('A deployment is already in progress for this resource');
    const d = startDeployment(c.db, r, { mode: 'pull', image_ref: target.image_ref, trigger: 'rollback', rollback_of: target });
    const created = { ...d };
    await audit(c, 'deployment.rollback', 'deployment', d.id, { to: target.id, digest: target.image_digest });
    return ok(created, 202);
  }, 'deploy');
}
