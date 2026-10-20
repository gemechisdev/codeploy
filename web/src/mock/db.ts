import type { components } from '@/api/schema';
import { Bus } from './bus';
import { canonicalJson, fakeDigest, randomHex, sha256Hex, ulid } from './util';

type S = components['schemas'];
export type Permission = S['Permission'];
export type RoleKey = S['Role']['key'];

export const ALL_PERMISSIONS: Permission[] = (
  'resource:read resource:write resource:delete resource:control deploy:create deploy:cancel deploy:rollback env:read env:write ' +
  'domain:write project:write terminal:host terminal:root terminal:container hardening:read hardening:apply edge:read edge:reconcile ' +
  'git:write hook:manage audit:read users:manage tokens:manage modules:manage flags:manage system:read system:metrics system:cleanup ' +
  'notify:manage backup:manage backup:restore template:deploy'
).split(' ') as Permission[];

const P = (s: string) => s.split(' ') as Permission[];
export const ROLE_PERMISSIONS: Record<RoleKey, Permission[]> = {
  owner: ALL_PERMISSIONS,
  admin: ALL_PERMISSIONS.filter((p) => p !== 'terminal:root'),
  deployer: P('resource:read resource:control deploy:create deploy:cancel deploy:rollback env:read env:write system:read'),
  viewer: P('resource:read env:read hardening:read edge:read system:read system:metrics'),
};

export const PERMISSION_DOCS: Record<string, string> = {
  'resource:read': 'View projects, resources and deployments', 'resource:write': 'Create and edit resources',
  'resource:delete': 'Delete resources', 'resource:control': 'Start, stop and restart resources',
  'deploy:create': 'Trigger deployments', 'deploy:cancel': 'Cancel running deployments', 'deploy:rollback': 'Roll back to a retained image',
  'env:read': 'List environment variables (secret values are never returned)', 'env:write': 'Add, replace and delete variables',
  'domain:write': 'Add and remove domains', 'project:write': 'Create projects and environments',
  'terminal:host': 'Open the host shell as the operator user', 'terminal:root': 'Open a root shell (step-up required)',
  'terminal:container': 'Open a container shell', 'hardening:read': 'View audit, plans and preflight', 'hardening:apply': 'Apply, confirm and revert hardening changes',
  'edge:read': 'View proxy status and certificates', 'edge:reconcile': 'Force a Caddy reconcile', 'git:write': 'Manage Git sources and keys',
  'hook:manage': 'Manage webhooks and deploy hooks', 'audit:read': 'Read and verify the audit log', 'users:manage': 'Create and edit users',
  'tokens:manage': 'Create and revoke API tokens', 'modules:manage': 'Enable or disable modules', 'flags:manage': 'Change feature flags and settings',
  'system:read': 'View host status', 'system:metrics': 'View metric history', 'system:cleanup': 'Run Docker cleanup',
  'notify:manage': 'Manage notification channels and alerts', 'backup:manage': 'Manage backup schedules and runs', 'backup:restore': 'Restore backups (step-up)',
  'template:deploy': 'Deploy catalog templates',
};

export interface MockUser {
  id: string; email: string; password: string; role: RoleKey; totp_enabled: boolean; disabled: boolean;
  created_at: string; last_login_at: string | null;
}
export interface MockSession {
  user_id: string; csrf: string; step_up_until: number | null; bootstrap: boolean; pending_challenge?: string;
}
export interface EnvRow { id: string; key: string; value: string | null; scope: 'build' | 'runtime' | 'both'; is_secret: boolean; updated_at: string }
export interface Ticket { ticket: string; expires_at: number; used: boolean; target: 'host' | 'container'; resource_id: string | null; as_root: boolean; user_id: string }

export interface Db {
  bus: Bus;
  speed: number; // 1 = realistic demo pacing, 0 = run steps immediately (tests)
  latency: [number, number];
  fault: 'none' | '500' | '429' | 'step_up' | 'slow';
  scenario: { noProvenKeyLogin: boolean; lowDisk: boolean; bootstrap: boolean };
  timers: Set<ReturnType<typeof setTimeout>>;
  intervals: Set<ReturnType<typeof setInterval>>;
  users: MockUser[];
  session: MockSession | null;
  setup: { required: boolean; token: string; step: S['SetupState']['step']; admin_id: string | null; totp_secret: string | null; totp_verified: boolean; recovery_issued: boolean; panel_domain: string | null };
  settings: Record<string, unknown>;
  flags: S['FeatureFlag'][];
  modules: S['Module'][];
  operator: string | null;
  checks: (S['HardeningCheck'] & { apply_flag: string | null })[];
  changes: S['HardeningChange'][];
  changeTimers: Map<string, ReturnType<typeof setTimeout>>;
  runs: S['HardeningRun'][];
  lastAuditRun: string | null;
  projects: S['Project'][];
  environments: S['Environment'][];
  resources: S['Resource'][];
  deployments: S['Deployment'][];
  logs: Map<string, S['DeploymentLogLine'][]>;
  deployTimers: Map<string, ReturnType<typeof setTimeout>[]>;
  env: Map<string, EnvRow[]>;
  volumes: Map<string, S['Volume'][]>;
  domains: S['DomainRoute'][];
  sources: Map<string, S['ResourceSource']>;
  deployKeys: Map<string, S['DeployKey']>;
  hooks: Map<string, { hook: S['DeployHook']; token: string }>;
  webhooks: Map<string, { info: S['WebhookInfo']; secret: string }>;
  deliveries: S['WebhookDelivery'][];
  audit: S['AuditEvent'][];
  auditQueue: Promise<unknown>;
  tokens: S['ApiToken'][];
  channels: S['NotificationChannel'][];
  rules: S['AlertRule'][];
  schedules: S['BackupSchedule'][];
  backupRuns: S['BackupRun'][];
  templates: S['Template'][];
  termSessions: S['TerminalSession'][];
  gitConnections: S['GitConnection'][];
  tickets: Map<string, Ticket>;
  edge: { last_reconciled_at: string | null; drift: boolean };
  metrics: S['HostMetrics'];
  metricsHistory: S['HostMetrics'][];
  startedAt: number;
  dispose(): void;
}

export const GENESIS = '0'.repeat(64);
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString();
const MIN = 60_000, HOUR = 3_600_000, DAY = 86_400_000;

export function defaultResourceConfig(over: Partial<S['ResourceConfig']> = {}): S['ResourceConfig'] {
  return {
    image: { mode: 'pull', ref: 'ghcr.io/acme/app:latest', dockerfile: null, context: null },
    port: 3000,
    healthcheck: { path: '/healthz', interval_s: 5, retries: 12, start_period_s: 5, timeout_s: 3 },
    limits: { memory_mb: 256, cpus: 0.5, pids_limit: 512 },
    isolation: { cap_add: [], user: null, trusted: false },
    restart: 'unless-stopped',
    retain_images: 5,
    ...over,
  };
}

export const HOST = { hostname: 'codeplo-vps', os: 'Debian 12 (bookworm)', arch: 'amd64', cpus: 1, ram_total_mb: 2048, ips: ['203.0.113.10', '2001:db8::10'] };

export async function appendAudit(db: Db, a: { actor_type: 'user' | 'system' | 'token'; actor_id: string; actor_email?: string | null; ip?: string; action: string; target_type: string; target_id: string; meta?: Record<string, unknown> }, at?: string): Promise<S['AuditEvent']> {
  const run = async () => {
    const prev = db.audit.length ? db.audit[db.audit.length - 1] : null;
    const seq = (prev?.seq ?? 0) + 1;
    const prev_hash = prev?.hash ?? GENESIS;
    const when = at ?? new Date().toISOString();
    const meta = a.meta ?? {};
    const hash = await sha256Hex(`${prev_hash}${seq}${when}${a.actor_id}${a.action}${a.target_type}:${a.target_id}${canonicalJson(meta)}`);
    const row: S['AuditEvent'] = {
      id: ulid('aud_'), seq, at: when, actor_type: a.actor_type, actor_id: a.actor_id, actor_email: a.actor_email ?? null,
      ip: a.ip ?? '127.0.0.1', action: a.action, target_type: a.target_type, target_id: a.target_id, meta, prev_hash, hash,
    };
    db.audit.push(row);
    db.bus.emit('audit.appended', { seq });
    return row;
  };
  const p = db.auditQueue.then(run, run);
  db.auditQueue = p.catch(() => undefined);
  return p as Promise<S['AuditEvent']>;
}

export async function recomputeHash(row: S['AuditEvent'], prevHash: string): Promise<string> {
  return sha256Hex(`${prevHash}${row.seq}${row.at}${row.actor_id}${row.action}${row.target_type}:${row.target_id}${canonicalJson(row.meta)}`);
}

export interface CreateDbOptions { fresh?: boolean; speed?: number; latency?: [number, number] }

export async function createDb(opts: CreateDbOptions = {}): Promise<Db> {
  const fresh = !!opts.fresh;
  const db: Db = {
    bus: new Bus(), speed: opts.speed ?? 1, latency: opts.latency ?? [60, 200], fault: 'none',
    scenario: { noProvenKeyLogin: false, lowDisk: false, bootstrap: fresh },
    timers: new Set(), intervals: new Set(), users: [], session: null,
    setup: { required: fresh, token: fresh ? 'demo-setup-token' : '', step: fresh ? 'token' : 'done', admin_id: null, totp_secret: null, totp_verified: false, recovery_issued: false, panel_domain: null },
    settings: {
      'terminal.allow_root': false, 'terminal.idle_timeout_s': 900, retain_images: 5, observation_window_s: 60, step_up_window_s: 300,
      hardening_revert_window_s: 300, disk_preflight_threshold_gb: 2, session_ttl_h: 12,
    },
    flags: [
      { key: 'hardening.apply_tier2', description: 'Allow applying the reversible default-deny firewall (Tier 2). Release-gated: enable only after the VM fault-injection matrix passes.', enabled: false, release_gated: true },
      { key: 'hardening.apply_tier3', description: 'Allow applying guarded SSH hardening (Tier 3). Release-gated; plan-only by default.', enabled: false, release_gated: true },
      { key: 'deploy.compose', description: 'Docker Compose resources (checked against the isolation policy).', enabled: false, release_gated: false },
      { key: 'deploy.static', description: 'Static site hosting served directly by Caddy.', enabled: false, release_gated: false },
      { key: 'registry_pull_hook', description: 'Registry-pull deploy hook for GitHub Actions.', enabled: true, release_gated: false },
    ],
    modules: [
      mod('hardening', 'Host hardening', 'Audit, safe fixes, reversible firewall, guarded SSH.', true, true, 'P2'),
      mod('edge', 'Edge (Caddy)', 'Managed Caddy, automatic HTTPS, domains.', true, true, 'P2'),
      mod('terminal', 'Terminal', 'Host and container shells in the browser.', true, false, 'P3'),
      mod('deploy', 'Deployments', 'Deployment engine, rollout, rollback.', true, true, 'P4'),
      mod('git', 'Git & CI/CD', 'Deploy keys, webhooks, registry-pull hook.', true, false, 'P5'),
      mod('monitor', 'Monitoring', 'Metrics history and alert evaluation.', false, false, 'P6'),
      mod('notify', 'Notifications', 'Telegram, Discord, email and webhook channels.', false, false, 'P6'),
      mod('databases', 'Databases & backups', 'One-click databases, scheduled backups, restore.', false, false, 'P6'),
      mod('templates', 'Templates', 'One-click catalog of compose templates.', false, false, 'P7'),
      mod('extensions', 'Extensions', 'Out-of-process extension API.', false, false, 'P7'),
      mod('mcp', 'MCP server', 'MCP server for AI agents using the same permissions as the API.', false, false, 'P7'),
    ],
    operator: fresh ? null : 'ops-deployer',
    checks: [], changes: [], changeTimers: new Map(), runs: [], lastAuditRun: null,
    projects: [], environments: [], resources: [], deployments: [], logs: new Map(), deployTimers: new Map(),
    env: new Map(), volumes: new Map(), domains: [], sources: new Map(), deployKeys: new Map(), hooks: new Map(), webhooks: new Map(), deliveries: [],
    audit: [], auditQueue: Promise.resolve(), tokens: [], channels: [], rules: [], schedules: [], backupRuns: [], templates: [],
    termSessions: [], gitConnections: [], tickets: new Map(), edge: { last_reconciled_at: fresh ? null : iso(2 * MIN), drift: false },
    metrics: { cpu_percent: 7.4, memory_used_mb: 812, memory_total_mb: HOST.ram_total_mb, daemon_rss_mb: 31.6, disk_used_gb: 14.2, disk_total_gb: 40, net_rx_kbps: 120, net_tx_kbps: 85, container_count: 4, at: new Date().toISOString() },
    metricsHistory: [], startedAt: Date.now() - 6 * DAY,
    dispose() {
      db.timers.forEach(clearTimeout);
      db.intervals.forEach(clearInterval);
      db.changeTimers.forEach(clearTimeout);
      db.deployTimers.forEach((l) => l.forEach(clearTimeout));
      db.timers.clear(); db.intervals.clear(); db.changeTimers.clear(); db.deployTimers.clear();
    },
  };

  seedChecks(db);
  seedTemplates(db);
  if (!fresh) await seedDemo(db);
  else {
    // fresh install: no users, no resources; the wizard creates the owner
    await appendAudit(db, { actor_type: 'system', actor_id: 'codeplod', action: 'system.start', target_type: 'system', target_id: 'codeplod', meta: { version: '1.2.0' } });
  }
  return db;
}

function mod(key: string, name: string, description: string, enabled: boolean, core: boolean, phase: string): S['Module'] {
  return { key, name, description, enabled, core, version: '1.2.0', phase };
}

export function computeScore(checks: Db['checks']): number {
  const scored = checks.filter((c) => c.status !== 'skipped');
  if (!scored.length) return 0;
  const pts = scored.reduce((a, c) => a + (c.status === 'pass' ? 1 : c.status === 'warn' ? 0.5 : 0), 0);
  return Math.round((pts / scored.length) * 100);
}

function seedChecks(db: Db) {
  const t = (c: Partial<Db['checks'][number]> & Pick<Db['checks'][number], 'key' | 'tier' | 'name' | 'description' | 'status' | 'driver' | 'detect_detail'>): Db['checks'][number] => ({
    plan_diff: null, apply_enabled: false, requires_ack: false, experimental: false, apply_flag: null, ...c,
  });
  db.checks = [
    t({ key: 'upd.unattended', tier: 1, name: 'Unattended security upgrades', description: 'Automatically install security updates.', status: 'fail', driver: 'apt',
      detect_detail: 'unattended-upgrades is not installed', apply_enabled: true,
      plan_diff: { target: '/etc/apt/apt.conf.d/20auto-upgrades', before: '(file does not exist)', after: 'APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";' } }),
    t({ key: 'kern.sysctl', tier: 1, name: 'Kernel network hardening', description: 'rp_filter, syncookies and related sysctl baseline.', status: 'fail', driver: 'sysctl',
      detect_detail: 'net.ipv4.tcp_syncookies=0, net.ipv4.conf.all.rp_filter=0', apply_enabled: true,
      plan_diff: { target: '/etc/sysctl.d/60-codeplo.conf', before: '(file does not exist)', after: 'net.ipv4.tcp_syncookies = 1\nnet.ipv4.conf.all.rp_filter = 1\nnet.ipv4.conf.default.rp_filter = 1\nnet.ipv4.icmp_echo_ignore_broadcasts = 1' } }),
    t({ key: 'bf.protection', tier: 1, name: 'Brute-force protection', description: 'fail2ban with the current admin IPs on the ignore list.', status: 'fail', driver: 'fail2ban',
      detect_detail: 'fail2ban is not active', apply_enabled: true,
      plan_diff: { target: '/etc/fail2ban/jail.d/codeplo.local', before: '(file does not exist)', after: '[DEFAULT]\nignoreip = 127.0.0.1/8 ::1 198.51.100.24\n\n[sshd]\nenabled = true\nmaxretry = 5\nbantime = 1h' } }),
    t({ key: 'acct.operator', tier: 1, name: 'Operator identity', description: 'A named non-root sudo user for the host terminal.', status: db.operator ? 'pass' : 'fail', driver: 'users',
      detect_detail: db.operator ? `Operator user: ${db.operator}` : 'No operator identity configured', apply_enabled: true }),
    t({ key: 'net.exposed_services', tier: 0, name: 'Exposed services', description: 'Listeners on public interfaces and their owning process.', status: 'pass', driver: 'network',
      detect_detail: 'sshd :22, caddy :80/:443 (expected)' }),
    t({ key: 'net.docker_published', tier: 0, name: 'Docker published ports', description: 'Containers publishing ports that bypass the host firewall.', status: 'warn', driver: 'docker',
      detect_detail: 'container "legacy-redis" (not managed by codeplo) publishes 0.0.0.0:6379' }),
    t({ key: 'fw.default_deny', tier: 2, name: 'Default-deny inbound firewall', description: 'Dedicated nftables table, applied runtime-first and persisted only on confirm.', status: 'fail', driver: 'nftables',
      detect_detail: 'Inbound policy is accept; no table inet codeplo', requires_ack: true, apply_flag: 'hardening.apply_tier2',
      plan_diff: { target: 'nft table inet codeplo', before: '(no table inet codeplo)', after: 'table inet codeplo {\n  chain input {\n    type filter hook input priority 0; policy drop;\n    ct state established,related accept\n    iif "lo" accept\n    ip protocol icmp accept\n    ip6 nexthdr icmpv6 accept\n    tcp dport { 22, 80, 443 } accept\n  }\n}' } }),
    t({ key: 'ssh.no_password', tier: 3, name: 'SSH password login disabled', description: 'Effective PasswordAuthentication must be no (drop-in 00-codeplo.conf, first match wins).', status: 'fail', driver: 'sshd',
      detect_detail: 'sshd -T: passwordauthentication yes (from 50-cloud-init.conf)', requires_ack: true, apply_flag: 'hardening.apply_tier3',
      plan_diff: { target: '/etc/ssh/sshd_config.d/00-codeplo.conf', before: '(file does not exist)', after: 'PasswordAuthentication no\nKbdInteractiveAuthentication no' } }),
    t({ key: 'ssh.no_root_password', tier: 3, name: 'Root password login disabled', description: 'PermitRootLogin prohibit-password.', status: 'warn', driver: 'sshd',
      detect_detail: 'sshd -T: permitrootlogin without-password (acceptable) but not explicit', requires_ack: true, apply_flag: 'hardening.apply_tier3',
      plan_diff: { target: '/etc/ssh/sshd_config.d/00-codeplo.conf', before: '(no explicit PermitRootLogin)', after: 'PermitRootLogin prohibit-password' } }),
    t({ key: 'ssh.limit_auth', tier: 3, name: 'SSH auth limits', description: 'Conservative MaxAuthTries and no empty passwords.', status: 'fail', driver: 'sshd',
      detect_detail: 'sshd -T: maxauthtries 6, permitemptypasswords no', requires_ack: true, apply_flag: 'hardening.apply_tier3',
      plan_diff: { target: '/etc/ssh/sshd_config.d/00-codeplo.conf', before: 'MaxAuthTries 6', after: 'MaxAuthTries 3\nPermitEmptyPasswords no' } }),
  ];
}

function seedTemplates(db: Db) {
  const tp = (key: string, name: string, category: string, description: string, icon: string, default_port: number, recommended_memory_mb: number, kind: S['Template']['kind'] = 'app'): S['Template'] => ({ key, name, category, description, icon, default_port, recommended_memory_mb, kind });
  db.templates = [
    tp('postgres', 'PostgreSQL', 'Databases', 'Relational database; binds to the internal network only.', 'database', 5432, 256, 'database'),
    tp('mysql', 'MySQL', 'Databases', 'MySQL 8 with a generated root secret.', 'database', 3306, 384, 'database'),
    tp('redis', 'Redis', 'Databases', 'In-memory cache and queue.', 'database', 6379, 64, 'database'),
    tp('uptime-kuma', 'Uptime Kuma', 'Monitoring', 'Self-hosted uptime monitor.', 'activity', 3001, 128, 'compose'),
    tp('plausible', 'Plausible', 'Analytics', 'Privacy-friendly web analytics.', 'bar-chart', 8000, 512, 'compose'),
  ];
}

async function seedDemo(db: Db) {
  const user = (email: string, role: RoleKey, totp: boolean, ago: number, last: number | null): MockUser => ({
    id: ulid('usr_'), email, password: 'demo-password-change-me', role, totp_enabled: totp, disabled: false, created_at: iso(ago), last_login_at: last === null ? null : iso(last),
  });
  db.users = [
    user('owner@example.com', 'owner', true, 12 * DAY, 3 * HOUR),
    user('admin@example.com', 'admin', true, 9 * DAY, 20 * HOUR),
    user('deployer@example.com', 'deployer', true, 8 * DAY, 2 * DAY),
    user('viewer@example.com', 'viewer', true, 5 * DAY, 4 * DAY),
  ];
  db.setup.panel_domain = 'panel.example.com';

  const proj = { id: ulid('prj_'), name: 'Acme platform', slug: 'acme-platform', description: 'Customer-facing storefront, API and background workers.', created_at: iso(10 * DAY) };
  db.projects = [proj];
  const prod = { id: ulid('env_'), project_id: proj.id, name: 'production' };
  const stg = { id: ulid('env_'), project_id: proj.id, name: 'staging' };
  db.environments = [prod, stg];

  const mk = (env: typeof prod, name: string, over: Partial<S['Resource']>, cfg: Partial<S['ResourceConfig']>): S['Resource'] => ({
    id: ulid('res_'), environment_id: env.id, project_id: proj.id, environment_name: env.name, type: 'app', name, slug: name,
    config: defaultResourceConfig(cfg), status: 'running', desired_state: 'running', active_deployment_id: null, active_digest: null,
    primary_domain: null, started_at: iso(2 * DAY), created_at: iso(9 * DAY), ...over,
  });
  const storefront = mk(prod, 'storefront', {}, { image: { mode: 'pull', ref: 'ghcr.io/acme/storefront:1.14.2', dockerfile: null, context: null }, port: 3000, limits: { memory_mb: 256, cpus: 0.5, pids_limit: 512 } });
  const api = mk(prod, 'core-api', {}, { image: { mode: 'build', ref: null, dockerfile: 'Dockerfile', context: '.' }, port: 8080, healthcheck: { path: '/api/health', interval_s: 5, retries: 12, start_period_s: 5, timeout_s: 3 }, limits: { memory_mb: 512, cpus: 1, pids_limit: 512 } });
  const worker = mk(prod, 'job-worker', {}, { image: { mode: 'pull', ref: 'ghcr.io/acme/worker:2.3.0', dockerfile: null, context: null }, port: 9000, limits: { memory_mb: 128, cpus: 0.25, pids_limit: 256 } });
  const stagingApp = mk(stg, 'storefront-staging', { status: 'degraded' }, { image: { mode: 'pull', ref: 'ghcr.io/acme/storefront:next', dockerfile: null, context: null }, port: 3000 });
  db.resources = [storefront, api, worker, stagingApp];

  const dep = (res: S['Resource'], status: S['Deployment']['status'], ago: number, over: Partial<S['Deployment']>): S['Deployment'] => ({
    id: ulid('dep_'), resource_id: res.id, status, step: status, trigger: 'manual', commit_sha: null, commit_message: null, branch: null,
    image_ref: res.config.image.ref ?? `cpl-${res.slug}:local`, image_digest: fakeDigest(), started_at: iso(ago), finished_at: iso(ago - 2 * MIN), heartbeat_at: iso(ago - 2 * MIN),
    observe_until: null, rollback_of: null, error: null, rollback_reason: null, preflight: { ok: true, disk_free_gb: 25.8, disk_required_gb: 4, ram_ok: true, detail: 'Disk and RAM guard passed' },
    health_attempts: { done: 2, max: 12, last_status: '200 OK' }, retained: true, ...over,
  });
  const s1old = dep(storefront, 'succeeded', 5 * DAY, { image_ref: 'ghcr.io/acme/storefront:1.14.0', trigger: 'webhook', commit_sha: '9f31c0a', commit_message: 'chore: bump deps' });
  const s1mid = dep(storefront, 'rolled_back', 3 * DAY, { image_ref: 'ghcr.io/acme/storefront:1.14.1', trigger: 'deploy_hook', commit_sha: 'b7a22d1', commit_message: 'feat: new checkout', rollback_reason: 'Health check failed 4 consecutive times during the observation window (HTTP 502)', error: 'Automatic rollback to the previous digest', retained: false });
  const s1 = dep(storefront, 'succeeded', 2 * DAY, { image_ref: 'ghcr.io/acme/storefront:1.14.2', trigger: 'deploy_hook', commit_sha: '4c8e9b2', commit_message: 'fix: cart rounding' });
  storefront.active_deployment_id = s1.id; storefront.active_digest = s1.image_digest; storefront.primary_domain = 'shop.example.com';
  const a1 = dep(api, 'succeeded', 3 * DAY, { image_ref: 'cpl-core-api:3d2f9ab', trigger: 'webhook', branch: 'main', commit_sha: '3d2f9ab', commit_message: 'perf: cache product list' });
  const a2 = dep(api, 'interrupted', 1 * DAY, { image_ref: 'cpl-core-api:77ac0e4', trigger: 'webhook', branch: 'main', commit_sha: '77ac0e4', commit_message: 'feat: orders export', step: 'building', error: 'Interrupted by daemon restart; recovered at boot (orphan container removed, traffic unchanged)', finished_at: iso(1 * DAY - 90_000), retained: false, image_digest: null });
  const a3 = dep(api, 'succeeded', 6 * HOUR, { image_ref: 'cpl-core-api:e19b6c3', trigger: 'webhook', branch: 'main', commit_sha: 'e19b6c3', commit_message: 'fix: order totals' });
  api.active_deployment_id = a3.id; api.active_digest = a3.image_digest; api.primary_domain = 'api.example.com';
  const w1 = dep(worker, 'succeeded', 4 * DAY, { image_ref: 'ghcr.io/acme/worker:2.3.0' });
  worker.active_deployment_id = w1.id; worker.active_digest = w1.image_digest;
  const g1 = dep(stagingApp, 'failed', 4 * HOUR, { image_ref: 'ghcr.io/acme/storefront:next', error: 'Health check failed: 12/12 attempts returned 503', retained: false, image_digest: null, health_attempts: { done: 12, max: 12, last_status: '503' } });
  const g0 = dep(stagingApp, 'succeeded', 2 * DAY, { image_ref: 'ghcr.io/acme/storefront:next' });
  stagingApp.active_deployment_id = g0.id; stagingApp.active_digest = g0.image_digest;
  db.deployments = [s1old, s1mid, s1, a1, a2, a3, w1, g0, g1];
  let seq = 0;
  for (const d of db.deployments) {
    const base = Date.parse(d.started_at);
    const lines: [S['DeploymentLogLine']['stream'], string][] = [
      ['system', `deployment ${d.id} queued (trigger=${d.trigger})`],
      ['system', 'pre-flight: disk free 25.8 GiB (>= 4 GiB), RAM guard ok'],
      ['system', d.image_ref.startsWith('cpl-') ? 'building image with BuildKit (concurrency 1)' : `pulling ${d.image_ref}`],
      ['stdout', d.image_ref.startsWith('cpl-') ? '#8 [build 4/4] RUN npm run build ... done' : 'Status: Downloaded newer image'],
      ['system', 'starting container cpl-' + d.id.slice(-8).toLowerCase()],
      ['system', 'health: GET ' + (d.status === 'failed' ? '503 (attempt 12/12)' : '200 OK (attempt 2/12)')],
      ['system', d.status === 'failed' ? 'deployment failed; previous container untouched' : d.status === 'interrupted' ? 'daemon restarted during build; marked interrupted' : 'route switched in one transaction'],
    ];
    db.logs.set(d.id, lines.map(([stream, line], i) => ({ seq: ++seq, at: new Date(base + i * 4000).toISOString(), stream, line })));
  }

  const env = (key: string, value: string | null, scope: EnvRow['scope'], secret: boolean): EnvRow => ({ id: ulid('env_'), key, value, scope, is_secret: secret, updated_at: iso(2 * DAY) });
  db.env.set(storefront.id, [env('NODE_ENV', 'production', 'runtime', false), env('API_BASE_URL', 'https://api.example.com', 'runtime', false), env('SESSION_SECRET', 'seeded-secret-value', 'runtime', true)]);
  db.env.set(api.id, [env('PORT', '8080', 'runtime', false), env('DATABASE_URL', 'postgres://seeded', 'runtime', true), env('NPM_TOKEN', 'seeded-build-token', 'build', true)]);
  db.env.set(worker.id, [env('QUEUE_CONCURRENCY', '4', 'runtime', false)]);
  db.env.set(stagingApp.id, []);
  const vol = (r: S['Resource'], name: string, mount: string): S['Volume'] => ({ id: ulid('vol_'), name, mount_path: mount, host_path: `/var/lib/codeplo/volumes/${r.slug}/${name}`, kind: 'bind', size_mb: 120 });
  db.volumes.set(storefront.id, [vol(storefront, 'uploads', '/app/uploads')]);
  db.volumes.set(api.id, [vol(api, 'data', '/var/lib/api')]);
  db.volumes.set(worker.id, []);
  db.volumes.set(stagingApp.id, []);

  const dom = (r: S['Resource'] | null, hostname: string, over: Partial<S['DomainRoute']> = {}): S['DomainRoute'] => ({
    id: ulid('dom_'), resource_id: r?.id ?? null, resource_name: r?.name ?? null, hostname, is_panel: !r, force_https: true, redirect_www: false,
    cert_status: 'issued', cert_expires_at: new Date(Date.now() + 61 * DAY).toISOString(), cert_error: null, issuer: "Let's Encrypt", upstream: r ? `cpl-${r.slug}:${r.config.port}` : 'unix//run/codeplo/codeplo.sock', created_at: iso(10 * DAY), ...over,
  });
  db.domains = [dom(null, 'panel.example.com'), dom(storefront, 'shop.example.com', { redirect_www: true }), dom(api, 'api.example.com'), dom(stagingApp, 'staging.example.com', { cert_expires_at: new Date(Date.now() + 9 * DAY).toISOString() })];

  db.sources.set(storefront.id, { kind: 'registry', repo_url: null, branch: null, build_path: null, dockerfile: null, path_filters: [], deploy_key_id: null });
  const key: S['DeployKey'] = { id: ulid('key_'), title: 'core-api deploy key', public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJkq9m0oQz3nExampleDemoKeyOnly codeplo:core-api', fingerprint: 'SHA256:q8P2xk0DemoFingerprintNotARealKeyAbCdEfGhIjK', created_at: iso(9 * DAY) };
  db.deployKeys.set(api.id, key);
  db.sources.set(api.id, { kind: 'deploy_key_git', repo_url: 'git@github.com:acme/core-api.git', branch: 'main', build_path: '.', dockerfile: 'Dockerfile', path_filters: ['src/**', 'Dockerfile'], deploy_key_id: key.id });
  db.hooks.set(storefront.id, { hook: { id: ulid('hok_'), token_prefix: 'cplh_7Qd2', last_used_at: iso(2 * DAY), created_at: iso(9 * DAY), image_policy: 'ghcr.io/acme/storefront:*' }, token: 'cplh_7Qd2seededtokenvalueneverreturned' });
  db.webhooks.set(api.id, { info: { configured: true, secret_prefix: 'whsec_4kP', created_at: iso(9 * DAY), path: `/api/v1/webhooks/github` }, secret: 'whsec_4kPseeded' });
  const dl = (result: S['WebhookDelivery']['result'], ago: number, event: string, reason: string | null, sha: string | null, rid: string | null): S['WebhookDelivery'] => ({
    id: ulid('whd_'), provider: 'github', delivery_id: `${randomHex(4)}-${randomHex(2)}-${randomHex(2)}-${randomHex(6)}`, received_at: iso(ago), event, result, reason, commit_sha: sha, resource_id: rid,
  });
  db.deliveries = [
    dl('success', 6 * HOUR, 'push', null, 'e19b6c3', api.id),
    dl('ignored', 8 * HOUR, 'push', 'Branch "feature/export" does not match configured branch "main"', '1ab45cd', api.id),
    dl('rejected', 1 * DAY, 'push', 'Invalid HMAC-SHA256 signature', null, api.id),
    dl('rejected', 1 * DAY + 30_000, 'push', 'Delivery ID already seen (replay)', null, api.id),
    dl('success', 3 * DAY, 'push', null, '3d2f9ab', api.id),
  ];

  db.tokens = [
    { id: ulid('tok_'), name: 'github-actions-deploy', prefix: 'cpl_8fT2', scopes: ['deploy:create', 'resource:read'], last_used_at: iso(6 * HOUR), expires_at: null, created_at: iso(9 * DAY) },
  ];
  db.channels = [{ id: ulid('chn_'), kind: 'telegram', name: 'Ops chat', enabled: true, destination_hint: 'chat …4821', created_at: iso(5 * DAY) }];
  const rule = (key: string, title: string, condition: string, threshold: string, enabled: boolean, last: string | null): S['AlertRule'] => ({ id: ulid('alr_'), key, title, condition, threshold, cooldown_s: 900, enabled, channel_ids: [], last_triggered_at: last });
  db.rules = [
    rule('disk_low', 'Disk space low', 'disk_free_percent <', '15', true, null), rule('container_down', 'Container down', 'container_state !=', 'running', true, iso(2 * DAY)),
    rule('deploy_failed', 'Deployment failed', 'deployment.status =', 'failed', true, iso(4 * HOUR)), rule('cert_expiring', 'Certificate expiring', 'cert_days_left <', '14', false, null),
  ];
  const sch: S['BackupSchedule'] = { id: ulid('bks_'), resource_id: worker.id, resource_name: 'job-worker (postgres)', cron: '0 3 * * *', destination: 'local', retention_days: 14, last_run_at: iso(9 * HOUR), last_run_status: 'success' };
  db.schedules = [sch];
  db.backupRuns = [
    { id: ulid('bkr_'), schedule_id: sch.id, resource_name: sch.resource_name, status: 'success', size_bytes: 48_234_496, checksum_sha256: randomHex(32), location: '/var/lib/codeplo/backups/job-worker/2026-10-18.sql.gz', started_at: iso(9 * HOUR), finished_at: iso(9 * HOUR - 40_000), error: null },
    { id: ulid('bkr_'), schedule_id: sch.id, resource_name: sch.resource_name, status: 'failed', size_bytes: 0, checksum_sha256: null, location: '/var/lib/codeplo/backups/job-worker/2026-10-16.sql.gz', started_at: iso(2 * DAY + 9 * HOUR), finished_at: iso(2 * DAY + 9 * HOUR - 5000), error: 'pg_dump: connection refused' },
  ];
  db.termSessions = [
    { id: ulid('trm_'), user_email: 'owner@example.com', ip: '198.51.100.24', target: 'host', target_name: 'codeplo-vps', as_user: 'ops-deployer', is_root: false, started_at: iso(1 * DAY), ended_at: iso(1 * DAY - 12 * MIN), recorded: false },
    { id: ulid('trm_'), user_email: 'admin@example.com', ip: '198.51.100.31', target: 'container', target_name: 'core-api', as_user: 'app', is_root: false, started_at: iso(3 * DAY), ended_at: iso(3 * DAY - 4 * MIN), recorded: false },
  ];

  // metrics history: deterministic wave so charts look alive
  const now = Date.now();
  for (let i = 59; i >= 0; i--) {
    const w = Math.sin(i / 7);
    db.metricsHistory.push({ ...db.metrics, cpu_percent: +(8 + 5 * w + (i % 5)).toFixed(1), memory_used_mb: Math.round(800 + 40 * Math.cos(i / 9)), net_rx_kbps: Math.round(110 + 50 * w), net_tx_kbps: Math.round(80 + 30 * w), at: new Date(now - i * 60_000).toISOString() });
  }

  // audit history through the real hash chain
  const hist: [string, string, string, string, string, string][] = [
    ['system', 'codeplod', 'system.start', 'system', 'codeplod', 'system'], ['user', db.users[0].id, 'setup.admin_created', 'user', db.users[0].id, db.users[0].email],
    ['user', db.users[0].id, 'auth.login', 'user', db.users[0].id, db.users[0].email], ['user', db.users[0].id, 'domain.created', 'domain', db.domains[1].id, db.users[0].email],
    ['user', db.users[0].id, 'resource.created', 'resource', storefront.id, db.users[0].email], ['token', db.tokens[0].id, 'deployment.created', 'deployment', s1.id, ''],
    ['user', db.users[1].id, 'hardening.audit', 'hardening', 'run', db.users[1].email], ['system', 'codeplod', 'deployment.interrupted', 'deployment', a2.id, 'system'],
  ];
  for (const [t, actor, action, tt, tid, email] of hist) {
    await appendAudit(db, { actor_type: t as 'user', actor_id: actor, actor_email: email || null, ip: t === 'system' ? '127.0.0.1' : '198.51.100.24', action, target_type: tt, target_id: tid, meta: {} }, new Date(Date.now() - (hist.length - hist.findIndex((h) => h[2] === action && h[4] === tid)) * HOUR).toISOString());
  }
  db.runs = [{ id: ulid('run_'), mode: 'audit', started_at: iso(3 * HOUR), finished_at: iso(3 * HOUR - 4000), actor_id: db.users[1].id, score: computeScore(db.checks) }];
  db.lastAuditRun = db.runs[0].started_at;
}
