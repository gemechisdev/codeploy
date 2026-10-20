import type { components } from '@/api/schema';
import { sched } from '../engine';
import { audit, noContent, ok, paginate, requireStepUp, route } from '../router';
import { HttpError, conflict, invalid, notFound, randomHex, ulid } from '../util';
import { createResourceInternal } from './workloads';

type S = components['schemas'];

const ALERT_CATALOG: Record<string, { title: string; condition: string; threshold: string }> = {
  disk_low: { title: 'Disk space low', condition: 'disk_free_percent <', threshold: '15' },
  memory_high: { title: 'Memory pressure', condition: 'memory_used_percent >', threshold: '90' },
  container_down: { title: 'Container down', condition: 'container_state !=', threshold: 'running' },
  deploy_failed: { title: 'Deployment failed', condition: 'deployment.status =', threshold: 'failed' },
  cert_expiring: { title: 'Certificate expiring', condition: 'cert_days_left <', threshold: '14' },
  backup_failed: { title: 'Backup failed', condition: 'backup.status =', threshold: 'failed' },
};
const CRON_RE = /^(\S+\s+){4}\S+$/;

function channelConfig(kind: string, cfg: Record<string, unknown> | undefined): { fields: Record<string, string>; hint: string } {
  const f: Record<string, string> = {}; const c = cfg ?? {};
  let hint = '';
  const str = (k: string) => (typeof c[k] === 'string' ? (c[k] as string).trim() : '');
  if (kind === 'telegram') {
    if (!/^\d+:[\w-]{20,}$/.test(str('bot_token'))) f['config.bot_token'] = 'Enter the bot token from @BotFather';
    if (!/^-?\d+$/.test(str('chat_id'))) f['config.chat_id'] = 'Enter a numeric chat id';
    hint = `chat …${str('chat_id').slice(-4)}`;
  } else if (kind === 'discord') {
    if (!/^https:\/\/discord\.com\/api\/webhooks\//.test(str('webhook_url'))) f['config.webhook_url'] = 'Enter a Discord webhook URL';
    hint = 'discord webhook';
  } else if (kind === 'email') {
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(str('to'))) f['config.to'] = 'Enter a recipient email address';
    hint = str('to').replace(/^(.).*(@.*)$/, '$1…$2');
  } else if (kind === 'webhook') {
    if (!/^https:\/\//.test(str('url'))) f['config.url'] = 'Enter an HTTPS URL';
    try { hint = new URL(str('url')).host; } catch { hint = 'webhook'; }
  }
  return { fields: f, hint };
}

export function registerOps() {
  // ---------- notifications ----------
  route('GET', '/notifications/channels', 'notify:manage', (c) => ok({ items: c.db.channels }), 'notify');
  route('POST', '/notifications/channels', 'notify:manage', async (c) => {
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    if (!['telegram', 'discord', 'email', 'webhook'].includes(b.kind)) f.kind = 'Choose a channel type';
    if (!String(b.name ?? '').trim()) f.name = 'Give the channel a name';
    const cc = channelConfig(b.kind, b.config);
    Object.assign(f, cc.fields);
    if (Object.keys(f).length) throw invalid(f);
    const ch: S['NotificationChannel'] = { id: ulid('chn_'), kind: b.kind, name: String(b.name).trim(), enabled: true, destination_hint: cc.hint, created_at: new Date().toISOString() };
    c.db.channels.push(ch); // credentials are write-only: only the hint is retained
    await audit(c, 'channel.created', 'channel', ch.id, { kind: ch.kind });
    return ok(ch, 201);
  }, 'notify');
  route('PATCH', '/notifications/channels/:id', 'notify:manage', async (c) => {
    const ch = c.db.channels.find((x) => x.id === c.params.id);
    if (!ch) throw notFound('channel');
    if (typeof c.body?.name === 'string') { if (!c.body.name.trim()) throw invalid({ name: 'Name cannot be empty' }); ch.name = c.body.name.trim(); }
    if (typeof c.body?.enabled === 'boolean') ch.enabled = c.body.enabled;
    await audit(c, 'channel.updated', 'channel', ch.id, { enabled: ch.enabled });
    return ok(ch);
  }, 'notify');
  route('DELETE', '/notifications/channels/:id', 'notify:manage', async (c) => {
    const i = c.db.channels.findIndex((x) => x.id === c.params.id);
    if (i < 0) throw notFound('channel');
    c.db.channels.splice(i, 1);
    c.db.rules.forEach((r) => { r.channel_ids = r.channel_ids.filter((x) => x !== c.params.id); });
    await audit(c, 'channel.deleted', 'channel', c.params.id, {});
    return noContent();
  }, 'notify');
  route('POST', '/notifications/channels/:id/test', 'notify:manage', async (c) => {
    const ch = c.db.channels.find((x) => x.id === c.params.id);
    if (!ch) throw notFound('channel');
    const good = !/fail/i.test(ch.name) && ch.enabled;
    await audit(c, 'channel.tested', 'channel', ch.id, { ok: good });
    return ok({ ok: good, detail: good ? `Test message delivered to ${ch.destination_hint}` : ch.enabled ? 'Delivery failed: 401 Unauthorized from the provider' : 'Channel is disabled' });
  }, 'notify');

  route('GET', '/alerts/rules', 'notify:manage', (c) => ok({ items: c.db.rules }), 'notify');
  route('POST', '/alerts/rules', 'notify:manage', async (c) => {
    const b = c.body ?? {};
    const cat = ALERT_CATALOG[String(b.key)];
    if (!cat) throw invalid({ key: `Unknown rule. Available: ${Object.keys(ALERT_CATALOG).join(', ')}` });
    if (c.db.rules.some((r) => r.key === b.key)) throw conflict(`A rule for "${b.key}" already exists`);
    const rule: S['AlertRule'] = { id: ulid('alr_'), key: b.key, ...cat, threshold: b.threshold ?? cat.threshold, cooldown_s: b.cooldown_s ?? 900, enabled: true, channel_ids: b.channel_ids ?? [], last_triggered_at: null };
    c.db.rules.push(rule);
    await audit(c, 'alert_rule.created', 'alert_rule', rule.id, { key: rule.key });
    return ok(rule, 201);
  }, 'notify');
  route('PATCH', '/alerts/rules/:id', 'notify:manage', async (c) => {
    const r = c.db.rules.find((x) => x.id === c.params.id);
    if (!r) throw notFound('alert rule');
    const b = c.body ?? {};
    if (b.cooldown_s !== undefined && (!Number.isInteger(b.cooldown_s) || b.cooldown_s < 60)) throw invalid({ cooldown_s: 'Cooldown must be at least 60 s' });
    if (Array.isArray(b.channel_ids) && b.channel_ids.some((id: string) => !c.db.channels.some((x) => x.id === id))) throw invalid({ channel_ids: 'Unknown channel' });
    if (typeof b.enabled === 'boolean') r.enabled = b.enabled;
    if (typeof b.threshold === 'string') r.threshold = b.threshold;
    if (b.cooldown_s !== undefined) r.cooldown_s = b.cooldown_s;
    if (Array.isArray(b.channel_ids)) r.channel_ids = b.channel_ids;
    await audit(c, 'alert_rule.updated', 'alert_rule', r.id, { enabled: r.enabled });
    return ok(r);
  }, 'notify');

  // ---------- backups ----------
  route('GET', '/backups/schedules', 'backup:manage', (c) => ok({ items: c.db.schedules }), 'databases');
  route('GET', '/backups/runs', 'backup:manage', (c) => ok(paginate([...c.db.backupRuns].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at)), c.query)), 'databases');
  route('GET', '/resources/:id/backups', 'backup:manage', (c) => {
    if (!c.db.resources.some((r) => r.id === c.params.id)) throw notFound('resource');
    return ok({ items: c.db.schedules.filter((s) => s.resource_id === c.params.id) });
  }, 'databases');
  route('POST', '/resources/:id/backups', 'backup:manage', async (c) => {
    const r = c.db.resources.find((x) => x.id === c.params.id);
    if (!r) throw notFound('resource');
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    if (!CRON_RE.test(String(b.cron ?? '').trim())) f.cron = 'Enter a 5-field cron expression such as 0 3 * * *';
    if (!['local', 's3'].includes(b.destination)) f.destination = 'Choose local or s3';
    if (!Number.isInteger(b.retention_days) || b.retention_days < 1 || b.retention_days > 365) f.retention_days = 'Retention must be 1-365 days';
    if (Object.keys(f).length) throw invalid(f);
    const s: S['BackupSchedule'] = { id: ulid('bks_'), resource_id: r.id, resource_name: r.name, cron: b.cron.trim(), destination: b.destination, retention_days: b.retention_days, last_run_at: null, last_run_status: null };
    c.db.schedules.push(s);
    await audit(c, 'backup.schedule_created', 'resource', r.id, { cron: s.cron, destination: s.destination });
    return ok(s, 201);
  }, 'databases');
  /** `{id}` is a schedule id for run, a run id for restore (matches the contract descriptions). */
  route('POST', '/backups/:id/run', 'backup:manage', async (c) => {
    const s = c.db.schedules.find((x) => x.id === c.params.id);
    if (!s) throw notFound('backup schedule');
    const run: S['BackupRun'] = { id: ulid('bkr_'), schedule_id: s.id, resource_name: s.resource_name, status: 'running', size_bytes: 0, checksum_sha256: null, location: `/var/lib/codeplo/backups/${s.resource_name.split(' ')[0]}/${new Date().toISOString().slice(0, 10)}-manual.sql.gz`, started_at: new Date().toISOString(), finished_at: null, error: null };
    c.db.backupRuns.unshift(run);
    s.last_run_status = 'running';
    await audit(c, 'backup.run', 'backup', run.id, { schedule: s.id });
    sched(c.db, 2200, () => {
      run.status = 'success'; run.size_bytes = 48_900_000 + Math.floor(Math.random() * 1_000_000); run.checksum_sha256 = randomHex(32); run.finished_at = new Date().toISOString();
      s.last_run_status = 'success'; s.last_run_at = run.finished_at;
      c.db.bus.emit('resource.updated', { resource_id: s.resource_id });
    });
    return ok(run, 202);
  }, 'databases');
  route('POST', '/backups/:id/restore', 'backup:restore', async (c) => {
    const run = c.db.backupRuns.find((x) => x.id === c.params.id);
    if (!run) throw notFound('backup run');
    if (run.status !== 'success') throw conflict(`A ${run.status} backup cannot be restored`);
    requireStepUp(c);
    const snapshot = `/var/lib/codeplo/backups/pre-restore/${run.id.slice(-8).toLowerCase()}.sql.gz`;
    await audit(c, 'backup.restored', 'backup', run.id, { pre_restore_snapshot: snapshot });
    return ok({ ok: true, pre_restore_snapshot: snapshot });
  }, 'databases');

  // ---------- templates ----------
  route('GET', '/templates', 'template:deploy', (c) => ok({ items: c.db.templates }), 'templates');
  route('POST', '/templates/:key/deploy', 'template:deploy', async (c) => {
    const t = c.db.templates.find((x) => x.key === c.params.key);
    if (!t) throw notFound('template');
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    const project = c.db.projects.find((p) => p.id === b.project_id);
    if (!project) f.project_id = 'Choose a project';
    const env = project ? c.db.environments.find((e) => e.project_id === project.id && e.name === b.environment) : undefined;
    if (project && !env) f.environment = 'Choose an environment of that project';
    if (Object.keys(f).length) throw invalid(f);
    if (t.kind === 'compose' && !c.db.flags.find((x) => x.key === 'deploy.compose')?.enabled) throw new HttpError(409, 'conflict', 'This template uses Docker Compose, which is disabled (feature flag deploy.compose)');
    const slugTpl = t.kind === 'database' ? t.key : t.key;
    const out = await createResourceInternal(c, {
      environment_id: env!.id, type: t.kind === 'compose' ? 'compose' : 'app', name: b.name ?? slugTpl,
      config: { image: { mode: 'pull', ref: `docker.io/library/${t.key}:latest`, dockerfile: null, context: null }, port: t.default_port, healthcheck: { path: '/', interval_s: 10, retries: 12, start_period_s: 10, timeout_s: 3 }, limits: { memory_mb: t.recommended_memory_mb, cpus: 0.5, pids_limit: 512 }, isolation: { cap_add: [], user: null, trusted: false }, restart: 'unless-stopped', retain_images: 5 },
      source: null, domain: null, deploy_now: true,
    });
    if (t.kind === 'database') out.resource.type = 'database';
    await audit(c, 'template.deployed', 'template', t.key, { resource_id: out.resource.id });
    return ok(out, 201);
  }, 'templates');
}
