import type { components } from '@/api/schema';
import { computeScore, type Db } from '../db';
import { runAutoRevert, scheduleRevert } from '../engine';
import { audit, ok, paginate, requireStepUp, route, type Ctx } from '../router';
import { HttpError, invalid, ulid } from '../util';

type S = components['schemas'];
const MOD = 'hardening';

const flagOn = (db: Db, key: string | null) => !key || !!db.flags.find((f) => f.key === key)?.enabled;

/** A check as the API exposes it: `apply_enabled` is derived from tier, status and the release flag. */
function view(db: Db): S['HardeningCheck'][] {
  return db.checks.map(({ apply_flag, ...c }) => ({
    ...c,
    apply_enabled: c.tier === 0 ? false : c.tier === 1 ? c.status !== 'pass' : flagOn(db, apply_flag),
  }));
}
const checksBody = (db: Db) => ({ items: view(db), score: computeScore(db.checks), last_run_at: db.lastAuditRun });

function findCheck(db: Db, key: unknown) {
  const c = db.checks.find((x) => x.key === key);
  if (!c) throw invalid({ check_key: `Unknown check "${String(key)}"` });
  return c;
}

const COMMANDS: Record<string, string[]> = {
  'upd.unattended': ['apt-get install -y unattended-upgrades', 'dpkg-reconfigure -f noninteractive unattended-upgrades'],
  'kern.sysctl': ['install -m 0644 60-codeplo.conf /etc/sysctl.d/60-codeplo.conf', 'sysctl --system'],
  'bf.protection': ['apt-get install -y fail2ban', 'systemctl enable --now fail2ban'],
  'acct.operator': ['adduser --disabled-password ops-deployer', 'usermod -aG sudo ops-deployer'],
  'fw.default_deny': ['nft -f /var/lib/codeplo/revert/<id>/codeplo.nft   # runtime only', '# persisted to /etc/nftables.d/codeplo.nft only after Confirm'],
  'ssh.no_password': ['sshd -t', 'install -m 0644 00-codeplo.conf /etc/ssh/sshd_config.d/00-codeplo.conf', 'sshd -T | grep passwordauthentication', 'systemctl reload ssh'],
  'ssh.no_root_password': ['sshd -t', 'systemctl reload ssh'],
  'ssh.limit_auth': ['sshd -t', 'systemctl reload ssh'],
};

function preflightItems(db: Db, key: string): S['PreflightItem'][] {
  const c = findCheck(db, key);
  if (c.tier <= 1) return [{ key: 'no_lockout_risk', label: 'Cannot cut access', ok: true, detail: `Tier ${c.tier} changes do not touch inbound access.` }];
  if (c.tier === 2) {
    return [
      { key: 'ssh_ports_allowed', label: 'Every sshd listening port is allowed', ok: true, detail: 'sshd listens on :22 (allowed in the plan)' },
      { key: 'edge_ports', label: '80/443 allowed while the edge is enabled', ok: true, detail: 'tcp 80, 443 are in the allow set' },
      { key: 'established', label: 'Established/related and loopback allowed', ok: true, detail: 'ct state established,related and iif lo accept present' },
      { key: 'own_table', label: 'Rules live in their own table', ok: true, detail: 'table inet codeplo; the existing ruleset is not flushed' },
      { key: 'deadman', label: 'Dead-man switch can be armed', ok: true, detail: `systemd-run transient timer, ${String(db.settings.hardening_revert_window_s)} s` },
    ];
  }
  const proven = !db.scenario.noProvenKeyLogin;
  return [
    { key: 'sshd_t', label: 'sshd -t passes', ok: true, detail: 'Configuration test succeeded' },
    { key: 'include_dir', label: 'sshd_config includes sshd_config.d', ok: true, detail: 'Include /etc/ssh/sshd_config.d/*.conf found' },
    { key: 'authorized_keys', label: 'authorized_keys exists with sane permissions', ok: true, detail: `${db.operator ?? 'operator'}: ~/.ssh/authorized_keys 0600` },
    {
      key: 'key_login_proven', label: 'Key-based login is proven', ok: proven,
      detail: proven ? `Accepted publickey for ${db.operator ?? 'operator'} 14 minutes ago` : `No recent "Accepted publickey" entry for ${db.operator ?? 'operator'} in the auth log. Log in once with your key, then retry.`,
    },
    { key: 'dropin_order', label: 'Drop-in is named 00-codeplo.conf (first match wins)', ok: true, detail: '50-cloud-init.conf is read after the drop-in' },
  ];
}

export function registerHardening() {
  route('GET', '/hardening/checks', 'hardening:read', (c) => ok(checksBody(c.db)), MOD);

  route('POST', '/hardening/audit', 'hardening:read', async (c) => {
    const db = c.db;
    const now = new Date().toISOString();
    db.lastAuditRun = now;
    db.runs.unshift({ id: ulid('run_'), mode: 'audit', started_at: now, finished_at: now, actor_id: c.user!.id, score: computeScore(db.checks) });
    await audit(c, 'hardening.audit', 'hardening', 'run', { score: computeScore(db.checks) });
    db.bus.emit('hardening.changed', {});
    return ok(checksBody(db));
  }, MOD);

  route('POST', '/hardening/plan', 'hardening:read', (c) => {
    const keys = c.body?.check_keys;
    if (!Array.isArray(keys) || !keys.length) throw invalid({ check_keys: 'Select at least one check' });
    return ok({ items: keys.map((k: string) => { const ch = findCheck(c.db, k); return { check_key: ch.key, plan_diff: ch.plan_diff, commands: COMMANDS[ch.key] ?? [] }; }) });
  }, MOD);

  route('POST', '/hardening/preflight', 'hardening:read', (c) => {
    const key = String(c.body?.check_key ?? '');
    const items = preflightItems(c.db, key);
    return ok({ check_key: key, ok: items.every((i) => i.ok), items });
  }, MOD);

  route('POST', '/hardening/apply', 'hardening:apply', async (c) => {
    const db = c.db;
    const check = findCheck(db, c.body?.check_key);
    const [shown] = view(db).filter((x) => x.key === check.key);
    // 1. validation that needs no elevation (so a doomed request never opens the step-up dialog)
    if (check.tier === 0) throw new HttpError(409, 'conflict', 'Tier 0 checks are read-only and cannot be applied');
    if (!shown.apply_enabled) {
      throw new HttpError(409, 'conflict', check.tier >= 2 ? `Applying this check is plan-only: feature flag "${check.apply_flag}" is off` : 'Nothing to apply: this check already passes');
    }
    if (check.requires_ack && c.body?.ack?.console_access !== true) throw invalid({ ack: 'Confirm that you have provider console or rescue access before applying' });
    const items = preflightItems(db, check.key);
    if (!items.every((i) => i.ok)) throw new HttpError(409, 'preflight_failed', 'Pre-flight checks failed. Nothing was changed.', { checks: items });
    if (check.tier >= 2 && db.changes.some((x) => x.state === 'pending_confirm')) throw new HttpError(409, 'conflict', 'Another change is waiting for confirmation. Confirm or revert it first.');
    // 2. server-validated TOTP step-up
    requireStepUp(c);

    const now = new Date();
    const id = ulid('chg_');
    const guarded = check.tier >= 2;
    const windowS = Number(db.settings.hardening_revert_window_s ?? 300);
    const change: S['HardeningChange'] = {
      id, check_key: check.key, check_name: check.name, tier: check.tier, snapshot_path: `/var/lib/codeplo/revert/${id}/`,
      revert_unit: guarded ? `codeplo-revert-${id.slice(-8).toLowerCase()}.timer` : null, applied_at: now.toISOString(),
      confirm_deadline: guarded ? new Date(now.getTime() + windowS * 1000).toISOString() : null,
      confirmed_at: guarded ? null : now.toISOString(), reverted_at: null, state: guarded ? 'pending_confirm' : 'confirmed',
    };
    db.changes.unshift(change);
    check.status = 'pass';
    check.detect_detail = guarded ? `${check.name}: applied, waiting for confirmation` : `${check.name}: applied`;
    if (check.key === 'acct.operator') { db.operator = db.operator ?? 'ops-deployer'; check.detect_detail = `Operator user: ${db.operator}`; }
    db.runs.unshift({ id: ulid('run_'), mode: 'apply', started_at: change.applied_at, finished_at: change.applied_at, actor_id: c.user!.id, score: computeScore(db.checks) });
    await audit(c, 'hardening.applied', 'hardening_change', id, { check_key: check.key, tier: check.tier, console_access_ack: c.body?.ack?.console_access === true, guarded });
    if (guarded) scheduleRevert(db, change);
    db.bus.emit('hardening.changed', { change_id: id });
    return ok(change);
  }, MOD);

  route('POST', '/hardening/confirm/:changeId', 'hardening:apply', async (c) => {
    const ch = c.db.changes.find((x) => x.id === c.params.changeId);
    if (!ch) throw new HttpError(404, 'not_found', 'change not found');
    if (ch.state !== 'pending_confirm') throw new HttpError(409, 'conflict', `Change is ${ch.state}; only a pending change can be confirmed`);
    const t = c.db.changeTimers.get(ch.id);
    if (t) clearTimeout(t);
    c.db.changeTimers.delete(ch.id);
    ch.state = 'confirmed';
    ch.confirmed_at = new Date().toISOString();
    await audit(c, 'hardening.confirmed', 'hardening_change', ch.id, { check_key: ch.check_key });
    c.db.bus.emit('hardening.changed', { change_id: ch.id });
    return ok(ch);
  }, MOD);

  route('POST', '/hardening/revert/:changeId', 'hardening:apply', async (c: Ctx) => {
    const ch = c.db.changes.find((x) => x.id === c.params.changeId);
    if (!ch) throw new HttpError(404, 'not_found', 'change not found');
    if (ch.state !== 'pending_confirm') throw new HttpError(409, 'conflict', `Change is ${ch.state}; only a pending change can be reverted`);
    await audit(c, 'hardening.revert_requested', 'hardening_change', ch.id, { check_key: ch.check_key });
    runAutoRevert(c.db, ch.id, `reverted by ${c.user!.email}`);
    return ok(ch);
  }, MOD);

  route('GET', '/hardening/runs', 'hardening:read', (c) => ok(paginate(c.db.runs, c.query)), MOD);
  route('GET', '/hardening/changes', 'hardening:read', (c) => ok({ items: c.db.changes, server_now: new Date().toISOString() }), MOD);
}
