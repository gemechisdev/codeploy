import type { components } from '@/api/schema';
import { HOST, appendAudit, computeScore, type Db } from './db';
import { fakeDigest, randomHex, ulid } from './util';

type S = components['schemas'];

/** Schedule a step. speed 0 (tests) runs on the next tick; otherwise durations scale with db.speed. */
export function sched(db: Db, ms: number, fn: () => void): ReturnType<typeof setTimeout> {
  const t = setTimeout(() => {
    db.timers.delete(t);
    fn();
  }, db.speed === 0 ? 0 : ms * db.speed);
  db.timers.add(t);
  return t;
}

const sysAudit = (db: Db, action: string, target_type: string, target_id: string, meta: Record<string, unknown> = {}) =>
  appendAudit(db, { actor_type: 'system', actor_id: 'codeplod', ip: '127.0.0.1', action, target_type, target_id, meta });

// ---------------------------------------------------------------- edge
export function applyDomainCreate(db: Db, a: { hostname: string; resource: S['Resource'] | null; force_https: boolean; redirect_www: boolean }): S['DomainRoute'] {
  const d: S['DomainRoute'] = {
    id: ulid('dom_'), resource_id: a.resource?.id ?? null, resource_name: a.resource?.name ?? null, hostname: a.hostname, is_panel: !a.resource,
    force_https: a.force_https, redirect_www: a.redirect_www, cert_status: 'issuing', cert_expires_at: null, cert_error: null, issuer: "Let's Encrypt",
    upstream: a.resource ? `cpl-${a.resource.slug}:${a.resource.config.port}` : 'unix//run/codeplo/codeplo.sock', created_at: new Date().toISOString(),
  };
  db.domains.push(d);
  if (a.resource && !a.resource.primary_domain) a.resource.primary_domain = d.hostname;
  db.bus.emit('edge.changed', { domain_id: d.id });
  sched(db, 3000, () => {
    if (a.hostname.endsWith('.invalid')) {
      d.cert_status = 'error';
      d.cert_error = 'ACME: DNS problem: NXDOMAIN looking up A for ' + a.hostname;
    } else {
      d.cert_status = 'issued';
      d.cert_expires_at = new Date(Date.now() + 90 * 86_400_000).toISOString();
      if (d.is_panel) db.setup.panel_domain = d.hostname;
    }
    db.edge.last_reconciled_at = new Date().toISOString();
    db.bus.emit('edge.changed', { domain_id: d.id });
    void sysAudit(db, d.cert_status === 'issued' ? 'edge.certificate_issued' : 'edge.certificate_failed', 'domain', d.id, { hostname: d.hostname });
  });
  return d;
}

export function refreshOperatorCheck(db: Db) {
  const c = db.checks.find((x) => x.key === 'acct.operator');
  if (!c) return;
  c.status = db.operator ? 'pass' : 'fail';
  c.detect_detail = db.operator ? `Operator user: ${db.operator}` : 'No operator identity configured';
  db.bus.emit('hardening.changed', {});
}

// ---------------------------------------------------------------- hardening
export function scoreOf(db: Db) {
  return computeScore(db.checks);
}

export function scheduleRevert(db: Db, change: S['HardeningChange']) {
  const ms = Math.max(0, Date.parse(change.confirm_deadline!) - Date.now());
  // Real wall-clock timer on purpose: this models the daemon-independent systemd transient timer.
  const t = setTimeout(() => runAutoRevert(db, change.id), db.speed === 0 ? Math.min(ms, 30) : ms);
  db.changeTimers.set(change.id, t);
}

export function runAutoRevert(db: Db, id: string, reason = 'dead-man timer expired without confirmation') {
  const ch = db.changes.find((c) => c.id === id);
  if (!ch || ch.state !== 'pending_confirm') return;
  const t = db.changeTimers.get(id);
  if (t) clearTimeout(t);
  db.changeTimers.delete(id);
  ch.state = 'reverting';
  db.bus.emit('hardening.changed', { change_id: id });
  const finish = () => {
    ch.state = 'reverted';
    ch.reverted_at = new Date().toISOString();
    const check = db.checks.find((c) => c.key === ch.check_key);
    if (check) check.status = 'fail';
    db.bus.emit('hardening.changed', { change_id: id });
    void sysAudit(db, 'hardening.reverted', 'hardening_change', id, { check_key: ch.check_key, reason, via: `systemd transient timer ${ch.revert_unit ?? ''}`.trim() });
  };
  if (db.speed === 0) finish();
  else sched(db, 900, finish);
}

/** Demo/test hook: expire the dead-man timer immediately (the server, not the UI, performs the revert). */
export function expirePendingChangesNow(db: Db) {
  for (const c of db.changes) if (c.state === 'pending_confirm') runAutoRevert(db, c.id);
}

// ---------------------------------------------------------------- deployments
const OBS_CAP_S = 15;
const STEP_MS: Record<string, number> = { queued: 500, preparing: 900, building: 3200, starting: 1100, verifying: 1800, switching: 600 };

function log(db: Db, d: S['Deployment'], stream: S['DeploymentLogLine']['stream'], line: string) {
  const arr = db.logs.get(d.id) ?? [];
  const row = { seq: arr.length + 1, at: new Date().toISOString(), stream, line };
  arr.push(row);
  db.logs.set(d.id, arr);
  db.bus.emit('deployment.log', { deployment_id: d.id, ...row });
}

function touch(db: Db, d: S['Deployment']) {
  d.heartbeat_at = new Date().toISOString();
  d.server_now = d.heartbeat_at;
  db.bus.emit('deployment.updated', { deployment_id: d.id, resource_id: d.resource_id, status: d.status, step: d.step });
}

function resourceAfterFailure(db: Db, r: S['Resource']) {
  r.status = r.active_deployment_id ? 'running' : 'failed';
  db.bus.emit('resource.updated', { resource_id: r.id });
}

function fail(db: Db, r: S['Resource'], d: S['Deployment'], message: string) {
  d.status = 'failed';
  d.error = message;
  d.finished_at = new Date().toISOString();
  d.retained = false;
  log(db, d, 'stderr', message);
  log(db, d, 'system', r.active_deployment_id ? 'deployment failed; previous container untouched, route unchanged' : 'deployment failed; no previous version to keep serving');
  touch(db, d);
  resourceAfterFailure(db, r);
  void sysAudit(db, 'deployment.failed', 'deployment', d.id, { error: message });
}

export function startDeployment(db: Db, r: S['Resource'], o: { mode: 'pull' | 'build'; image_ref: string; trigger: S['Deployment']['trigger']; branch?: string | null; rollback_of?: S['Deployment'] | null }): S['Deployment'] {
  const sha = randomHex(4).slice(0, 7);
  const rb = o.rollback_of ?? null;
  const d: S['Deployment'] = {
    id: ulid('dep_'), resource_id: r.id, status: 'queued', step: 'queued', trigger: o.trigger,
    commit_sha: o.mode === 'build' && !rb ? sha : null, commit_message: o.mode === 'build' && !rb ? `Deploy ${o.branch ?? 'main'} @ ${sha}` : null, branch: o.branch ?? null,
    image_ref: o.image_ref, image_digest: rb ? rb.image_digest : null, started_at: new Date().toISOString(), finished_at: null, heartbeat_at: new Date().toISOString(),
    observe_until: null, rollback_of: rb?.id ?? null, error: null, rollback_reason: null, preflight: null, health_attempts: { done: 0, max: r.config.healthcheck.retries, last_status: null },
    retained: false, server_now: new Date().toISOString(),
  };
  db.deployments.push(d);
  db.logs.set(d.id, []);
  r.status = 'deploying';
  log(db, d, 'system', `deployment ${d.id} queued (trigger=${o.trigger}${rb ? `, rollback_of=${rb.id}` : ''})`);
  db.bus.emit('resource.updated', { resource_id: r.id });
  touch(db, d);

  const steps: string[] = rb ? ['queued', 'preparing', 'starting', 'verifying', 'switching', 'observing'] : o.mode === 'build' ? ['queued', 'preparing', 'building', 'starting', 'verifying', 'switching', 'observing'] : ['queued', 'preparing', 'starting', 'verifying', 'switching', 'observing'];
  const timers: ReturnType<typeof setTimeout>[] = [];
  db.deployTimers.set(d.id, timers);
  const later = (ms: number, fn: () => void) => {
    const t = sched(db, ms, fn);
    timers.push(t);
  };

  const run = (i: number) => {
    if (d.status === 'cancelled') return;
    const step = steps[i];
    if (!step) return;
    d.status = step as S['Deployment']['status'];
    d.step = step;
    const next = () => later(STEP_MS[step] ?? 600, () => run(i + 1));

    switch (step) {
      case 'queued':
        next();
        break;
      case 'preparing': {
        const free = db.scenario.lowDisk ? 1.2 : 25.8;
        const need = Math.max(Number(db.settings.disk_preflight_threshold_gb ?? 2), 2) * 2;
        d.preflight = { ok: free >= need, disk_free_gb: free, disk_required_gb: need, ram_ok: true, detail: `Disk free ${free} GiB (needs ${need} GiB), RAM guard ok` };
        log(db, d, 'system', `pre-flight: ${d.preflight.detail}`);
        if (!d.preflight.ok) {
          touch(db, d);
          later(STEP_MS.preparing, () => fail(db, r, d, `Pre-flight refused: only ${free} GiB free, ${need} GiB required. Free disk space (POST /system/cleanup) and retry.`));
          return;
        }
        log(db, d, 'system', o.mode === 'build' && !rb ? 'cloning source with the per-resource deploy key' : `pulling ${d.image_ref}`);
        touch(db, d);
        if (o.mode === 'pull' && !rb && /invalid|does-not-exist|missing/.test(d.image_ref)) {
          later(STEP_MS.preparing, () => fail(db, r, d, `manifest unknown: ${d.image_ref} not found in registry`));
          return;
        }
        next();
        return;
      }
      case 'building':
        log(db, d, 'system', 'building image with BuildKit (queue concurrency 1, memory limit 50% of RAM)');
        log(db, d, 'stdout', '#5 [build 3/4] RUN npm ci --omit=dev');
        log(db, d, 'stdout', '#6 [build 4/4] RUN npm run build');
        d.image_digest = fakeDigest();
        next();
        break;
      case 'starting':
        d.image_digest ??= fakeDigest();
        log(db, d, 'system', `starting container cpl-${r.slug}-${d.id.slice(-8).toLowerCase()} (policy: cap_drop ALL, no-new-privileges, no published ports)`);
        next();
        break;
      case 'verifying': {
        const max = r.config.healthcheck.retries;
        const bad = /unhealthy/.test(d.image_ref);
        d.health_attempts = { done: 0, max, last_status: null };
        touch(db, d);
        const attempts = bad ? max : 2;
        for (let k = 1; k <= Math.min(attempts, 3); k++) {
          later((STEP_MS.verifying / 3) * k, () => {
            if (d.status !== 'verifying') return;
            d.health_attempts = { done: bad ? Math.round((max / 3) * k) : k, max, last_status: bad ? '503' : k === 2 ? '200 OK' : 'connection refused' };
            log(db, d, 'system', `health: GET ${r.config.healthcheck.path} -> ${d.health_attempts.last_status} (attempt ${d.health_attempts.done}/${max})`);
            touch(db, d);
          });
        }
        later(STEP_MS.verifying + 50, () => (bad ? fail(db, r, d, `Health check failed: ${max}/${max} attempts returned 503`) : run(i + 1)));
        return;
      }
      case 'switching': {
        log(db, d, 'system', 'switching route: single DB transaction sets active_deployment_id, then edge reconcile');
        r.active_deployment_id = d.id;
        r.active_digest = d.image_digest;
        r.started_at = new Date().toISOString();
        r.status = 'running';
        r.desired_state = 'running';
        d.retained = true;
        db.bus.emit('resource.updated', { resource_id: r.id });
        next();
        break;
      }
      case 'observing': {
        const windowS = Math.min(Number(db.settings.observation_window_s ?? 60), OBS_CAP_S);
        const ms = db.speed === 0 ? 0 : windowS * 1000 * db.speed;
        d.observe_until = new Date(Date.now() + ms).toISOString();
        log(db, d, 'system', `observing for ${windowS}s: previous container retained until the window ends`);
        touch(db, d);
        if (/flaky/.test(d.image_ref)) {
          later(Math.max(300, windowS * 1000 * 0.45), () => autoRollback(db, r, d));
          return;
        }
        later(windowS * 1000, () => succeed(db, r, d));
        return;
      }
    }
    touch(db, d);
  };
  later(0, () => run(0));
  return d;
}

function succeed(db: Db, r: S['Resource'], d: S['Deployment']) {
  if (d.status !== 'observing') return;
  d.status = 'succeeded';
  d.step = 'succeeded';
  d.finished_at = new Date().toISOString();
  log(db, d, 'system', 'observation window passed; old container stopped, image retained by digest');
  // retention: keep the last N succeeded digests
  const keep = Number(r.config.retain_images ?? db.settings.retain_images ?? 5);
  const ok = db.deployments.filter((x) => x.resource_id === r.id && (x.status === 'succeeded') && x.image_digest).sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
  ok.forEach((x, idx) => { x.retained = idx < keep; });
  touch(db, d);
  db.bus.emit('resource.updated', { resource_id: r.id });
  void sysAudit(db, 'deployment.succeeded', 'deployment', d.id, { image_digest: d.image_digest });
}

function autoRollback(db: Db, r: S['Resource'], d: S['Deployment']) {
  if (d.status !== 'observing') return;
  const prev = db.deployments
    .filter((x) => x.resource_id === r.id && x.id !== d.id && x.status === 'succeeded' && x.image_digest)
    .sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))[0];
  d.status = 'rolled_back';
  d.step = 'rolled_back';
  d.finished_at = new Date().toISOString();
  d.retained = false;
  d.rollback_reason = 'Health check failed 4 consecutive times during the observation window (HTTP 502)';
  d.error = 'Automatic rollback to the previous digest';
  log(db, d, 'stderr', 'unhealthy after switch: 4 consecutive failed checks');
  log(db, d, 'system', prev ? `automatic rollback: route restored to ${prev.id}` : 'automatic rollback: no previous version retained');
  r.active_deployment_id = prev?.id ?? null;
  r.active_digest = prev?.image_digest ?? null;
  r.status = prev ? 'running' : 'failed';
  touch(db, d);
  db.bus.emit('resource.updated', { resource_id: r.id });
  void sysAudit(db, 'deployment.rolled_back', 'deployment', d.id, { reason: d.rollback_reason });
}

export function cancelDeployment(db: Db, r: S['Resource'], d: S['Deployment']) {
  db.deployTimers.get(d.id)?.forEach(clearTimeout);
  db.deployTimers.delete(d.id);
  d.status = 'cancelled';
  d.step = 'cancelled';
  d.finished_at = new Date().toISOString();
  d.error = 'Cancelled by user';
  log(db, d, 'system', 'cancelled: build containers killed, cleanup is label-driven and idempotent');
  touch(db, d);
  resourceAfterFailure(db, r);
}

// ---------------------------------------------------------------- policy
export function policyFor(db: Db, r: S['Resource']): S['PolicyResult'] {
  const c = r.config;
  const ctl = (key: string, label: string, value: string, def: string, state: 'ok' | 'warn' | 'violation' = 'ok', note: string | null = null): S['PolicyControl'] => ({ key, label, value, default: def, state, note });
  const controls = [
    ctl('cap_drop', 'Linux capabilities', c.isolation.cap_add.length ? `drop ALL, add ${c.isolation.cap_add.join(', ')}` : 'drop ALL', 'drop ALL', c.isolation.cap_add.length ? 'warn' : 'ok', c.isolation.cap_add.length ? 'Additions are audited per resource.' : null),
    ctl('no_new_privileges', 'no-new-privileges', 'enabled', 'enabled'),
    ctl('seccomp', 'Seccomp / AppArmor', 'docker-default', 'docker-default'),
    ctl('user', 'User', c.isolation.user ?? "image's USER", "image's USER", c.isolation.user === 'root' ? 'warn' : 'ok', c.isolation.user === 'root' ? 'Running as root is discouraged.' : null),
    ctl('privileged', 'privileged / host pid / ipc / network / devices', 'rejected', 'rejected'),
    ctl('docker_sock', 'Docker socket mount', c.isolation.trusted ? 'allowed (trusted)' : 'rejected', 'rejected', c.isolation.trusted ? 'warn' : 'ok', c.isolation.trusted ? 'Resource is marked trusted. This is root-equivalent.' : null),
    ctl('bind_mounts', 'Host bind mounts', `only under /var/lib/codeplo/volumes/${r.slug}/`, 'allowlist'),
    ctl('published_ports', 'Published ports', 'none (Caddy reaches the container over the internal network)', 'none'),
    ctl('limits', 'Memory / CPU / PIDs', `${c.limits.memory_mb} MB · ${c.limits.cpus} CPU · ${c.limits.pids_limit} pids`, '256 MB · 0.5 CPU · 512 pids', c.limits.pids_limit > 2048 ? 'warn' : 'ok'),
    ctl('network', 'Network', `per-environment bridge (${r.environment_name})`, 'per-environment bridge'),
  ];
  const violations = controls.filter((x) => x.state === 'violation').map((x) => `${x.label}: ${x.value}`);
  void db;
  return { resource_id: r.id, ok: violations.length === 0, controls, violations };
}

// ---------------------------------------------------------------- metrics + dev hooks
export function startMetrics(db: Db) {
  const iv = setInterval(() => {
    const m = db.metrics;
    const run = db.resources.filter((r) => r.status === 'running').length;
    const next: S['HostMetrics'] = {
      ...m,
      cpu_percent: +Math.max(1, Math.min(95, m.cpu_percent + (Math.random() - 0.5) * 4)).toFixed(1),
      memory_used_mb: Math.round(Math.max(500, Math.min(HOST.ram_total_mb - 100, m.memory_used_mb + (Math.random() - 0.5) * 20))),
      daemon_rss_mb: +Math.max(25, Math.min(48, (m.daemon_rss_mb ?? 31) + (Math.random() - 0.5))).toFixed(1),
      net_rx_kbps: Math.round(Math.max(5, (m.net_rx_kbps ?? 100) + (Math.random() - 0.5) * 40)),
      net_tx_kbps: Math.round(Math.max(5, (m.net_tx_kbps ?? 70) + (Math.random() - 0.5) * 30)),
      container_count: run,
      at: new Date().toISOString(),
    };
    db.metrics = next;
    db.metricsHistory.push(next);
    if (db.metricsHistory.length > 240) db.metricsHistory.shift();
    db.bus.emit('metrics.sample', next as unknown as Record<string, unknown>);
  }, 5000);
  db.intervals.add(iv);
}

/** Dev hook (DemoToolbar): alter a stored row without re-hashing so `POST /audit/verify` detects it. */
export function tamperAudit(db: Db, seq?: number): number | null {
  const row = seq ? db.audit.find((r) => r.seq === seq) : db.audit[Math.max(0, db.audit.length - 3)];
  if (!row) return null;
  row.meta = { ...row.meta, tampered: true };
  return row.seq;
}

export function simulateDelivery(db: Db, result: 'success' | 'ignored' | 'rejected' = 'success') {
  const r = db.resources.find((x) => x.name === 'core-api') ?? db.resources[0];
  if (!r) return null;
  const row: S['WebhookDelivery'] = {
    id: ulid('whd_'), provider: 'github', delivery_id: `${randomHex(4)}-${randomHex(2)}-${randomHex(2)}-${randomHex(6)}`, received_at: new Date().toISOString(), event: 'push', result,
    reason: result === 'rejected' ? 'Invalid HMAC-SHA256 signature' : result === 'ignored' ? 'Branch "dev" does not match configured branch "main"' : null, commit_sha: result === 'rejected' ? null : randomHex(4).slice(0, 7), resource_id: r.id,
  };
  db.deliveries.unshift(row);
  if (result === 'success' && !db.deployments.some((d) => d.resource_id === r.id && ['queued', 'preparing', 'building', 'starting', 'verifying', 'switching', 'observing'].includes(d.status))) {
    startDeployment(db, r, { mode: 'build', image_ref: `cpl-${r.slug}:${row.commit_sha}`, trigger: 'webhook', branch: 'main' });
  }
  return row;
}
