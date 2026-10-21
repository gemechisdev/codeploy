import { useState } from 'react';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { ShieldCheck } from 'lucide-react';
import { useAudit, useCleanup, useCreateToken, useCreateUser, useFlags, useModules, usePatchFlags, usePatchModule, usePatchSettings, usePatchUser, usePermissionRegistry, useRevokeToken, useRoles, useSettings, useSystemStatus, useSystemVersion, useTokens, useUsers, useVerifyAudit, type AuditFilter } from '@/api/hooks/core';
import { useGitConnections } from '@/api/hooks/workloads';
import { errorMessage } from '@/api/errors';
import type { Permission, Schema } from '@/api/types';
import { CopyButton } from '@/components/app/CopyButton';
import { ConfirmDialog } from '@/components/app/ConfirmDialog';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input, Select } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { useToast } from '@/components/ui/toast';
import { absTime, bytes, duration, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { PageHeader } from '@/features/shell/ModuleGate';
import { SETTINGS_TABS, type SettingsTab } from '@/router';

const LABELS: Record<SettingsTab, string> = { users: 'Users', roles: 'Roles', tokens: 'API tokens', git: 'Git connections', modules: 'Modules & flags', audit: 'Audit log', system: 'System' };

function UsersTab() {
  const q = useUsers(); const create = useCreateUser(); const patch = usePatchUser(); const toast = useToast(); const { can } = usePermissions();
  const [open, setOpen] = useState(false); const [f, setF] = useState<Schema<'CreateUserRequest'>>({ email: '', password: '', role: 'viewer' });
  const err = (e: unknown) => toast.error(errorMessage(e));
  return (
    <Card><CardHeader title="Users" description="Changes here require step-up verification." actions={can('users:manage') && <Button size="sm" onClick={() => setOpen(true)}>Add user</Button>} />
      <QueryBoundary query={q}>{(d) => (
        <Table><THead><tr><TH>Email</TH><TH>Role</TH><TH>2FA</TH><TH>Last login</TH><TH>Status</TH></tr></THead>
          <TBody>{d.items.map((u) => (
            <TR key={u.id}><TD className="font-semibold text-primary">{u.email}</TD>
              <TD>{u.role === 'owner' || !can('users:manage') ? <Badge tone="brand">{u.role}</Badge> :
                <Select aria-label={`Role for ${u.email}`} value={u.role} onChange={(e) => patch.mutate({ id: u.id, body: { role: e.target.value as 'admin' | 'deployer' | 'viewer' } }, { onError: err })}>{['admin', 'deployer', 'viewer'].map((r) => <option key={r}>{r}</option>)}</Select>}</TD>
              <TD>{u.totp_enabled ? <Badge tone="green">on</Badge> : <Badge tone="amber">off</Badge>}</TD>
              <TD title={absTime(u.last_login_at)}>{u.last_login_at ? relTime(u.last_login_at) : 'never'}</TD>
              <TD>{u.role === 'owner' || !can('users:manage') ? (u.disabled ? 'disabled' : 'active') : <Switch aria-label={`${u.email} active`} checked={!u.disabled} onCheckedChange={(on) => patch.mutate({ id: u.id, body: { disabled: !on } }, { onError: err })} />}</TD></TR>))}</TBody></Table>)}</QueryBoundary>
      <Dialog open={open} onOpenChange={setOpen} title="Add user" description="Passwords need at least 12 characters. The user enrolls an authenticator at first login."
        footer={<><Button variant="secondary" onClick={() => setOpen(false)}>Cancel</Button><Button loading={create.isPending} disabled={!f.email || f.password.length < 12}
          onClick={() => create.mutate(f, { onSuccess: () => { toast.success('User created.'); setOpen(false); setF({ email: '', password: '', role: 'viewer' }); }, onError: err })}>Create</Button></>}>
        <div className="space-y-3">
          <Field label="Email">{(a) => <Input {...a} type="email" value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} />}</Field>
          <Field label="Temporary password">{(a) => <Input {...a} type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} />}</Field>
          <Field label="Role">{(a) => <Select {...a} value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as typeof f.role })}>{['admin', 'deployer', 'viewer'].map((r) => <option key={r}>{r}</option>)}</Select>}</Field>
        </div>
      </Dialog>
    </Card>
  );
}

function RolesTab() {
  const roles = useRoles(); const perms = usePermissionRegistry();
  return (
    <Card><CardHeader title="Roles & permissions" description="Read-only: the permission registry is defined by the server." />
      <QueryBoundary query={roles}>{(r) => (
        <div className="overflow-x-auto"><Table><THead><tr><TH>Permission</TH>{r.items.map((x) => <TH key={x.key}>{x.name}</TH>)}</tr></THead>
          <TBody>{(perms.data?.items ?? [...new Set(r.items.flatMap((x) => x.permissions))]).map((p) => {
            const name = (typeof p === 'string' ? p : (p as { key: string }).key) as Permission;
            return <TR key={name}><TD className="font-mono text-[11px]">{name}</TD>{r.items.map((x) => <TD key={x.key}>{x.permissions.includes(name) ? <span aria-label="granted" className="font-bold text-green-600">✓</span> : <span aria-label="not granted" className="text-muted">—</span>}</TD>)}</TR>; })}</TBody></Table></div>)}</QueryBoundary>
    </Card>
  );
}

function TokensTab() {
  const q = useTokens(); const create = useCreateToken(); const revoke = useRevokeToken(); const toast = useToast(); const { can } = usePermissions();
  const [open, setOpen] = useState(false); const [name, setName] = useState(''); const [secret, setSecret] = useState<string | null>(null); const [rev, setRev] = useState<string | null>(null);
  return (
    <Card><CardHeader title="API tokens" description="A token's secret is shown once, at creation." actions={can('tokens:manage') && <Button size="sm" onClick={() => setOpen(true)}>New token</Button>} />
      <QueryBoundary query={q} isEmpty={(d) => d.items.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No tokens.</p>}>{(d) => (
        <Table><THead><tr><TH>Name</TH><TH>Prefix</TH><TH>Scopes</TH><TH>Last used</TH><TH><span className="sr-only">Actions</span></TH></tr></THead>
          <TBody>{d.items.map((t) => (
            <TR key={t.id}><TD className="font-semibold text-primary">{t.name}</TD><TD className="font-mono text-[11px]">{t.prefix}…</TD><TD className="max-w-xs text-[11px] text-secondary">{t.scopes.join(', ')}</TD><TD>{t.last_used_at ? relTime(t.last_used_at) : 'never'}</TD>
              <TD>{can('tokens:manage') && <Button size="sm" variant="secondary" onClick={() => setRev(t.id)}>Revoke</Button>}</TD></TR>))}</TBody></Table>)}</QueryBoundary>
      <Dialog open={open} onOpenChange={setOpen} title="New token" description="Scopes default to read-only access."
        footer={<><Button variant="secondary" onClick={() => setOpen(false)}>Cancel</Button><Button loading={create.isPending} disabled={!name.trim()}
          onClick={() => create.mutate({ name: name.trim(), scopes: ['resource:read'] }, { onSuccess: (r) => { setSecret(r.secret); setOpen(false); setName(''); }, onError: (e) => toast.error(errorMessage(e)) })}>Create</Button></>}>
        <Field label="Name">{(a) => <Input {...a} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      </Dialog>
      <Dialog open={!!secret} onOpenChange={() => setSecret(null)} dismissible={false} title="Copy your token now" description="It will not be shown again." footer={<Button onClick={() => setSecret(null)}>I have copied it</Button>}>
        <div className="flex items-center gap-2 rounded-lg border border-subtle bg-surface-muted p-2"><code className="min-w-0 flex-1 break-all font-mono text-[11px]">{secret}</code>{secret && <CopyButton value={secret} iconOnly />}</div>
      </Dialog>
      <ConfirmDialog open={!!rev} onOpenChange={(o) => !o && setRev(null)} destructive title="Revoke token?" confirmLabel="Revoke" loading={revoke.isPending} description="Anything using this token stops working immediately."
        onConfirm={() => rev && revoke.mutate(rev, { onSuccess: () => { toast.success('Token revoked.'); setRev(null); }, onError: (e) => toast.error(errorMessage(e)) })} />
    </Card>
  );
}

function GitTab() {
  const q = useGitConnections();
  return (
    <Card><CardHeader title="Git connections" description="Per-resource deploy keys are managed in each resource's Source & CI/CD tab." />
      <QueryBoundary query={q} isEmpty={(d) => d.items.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No connections. A GitHub App integration is planned; until then use per-resource deploy keys.</p>}>{(d) => (
        <ul className="divide-y divide-subtle">{d.items.map((c) => <li key={c.id} className="flex items-center justify-between px-4 py-3 text-xs"><span className="font-semibold text-primary">{c.name}</span><span><Badge tone="brand">{c.kind}</Badge> <span className="text-muted">{relTime(c.created_at)}</span></span></li>)}</ul>)}</QueryBoundary></Card>
  );
}

// The two kinds the settings surface actually has. A boolean is a switch and a number is a field, because a
// "true"/"false" text box is how an operator types "1" and gets a silent no.
const SETTING_BOOLEANS: { key: string; label: string; hint: string }[] = [
  { key: 'terminal.allow_root', label: 'Allow root terminal', hint: 'Off by default. Root sessions always need step-up.' },
  { key: 'terminal.record_sessions', label: 'Record terminal sessions', hint: 'Keeps a transcript of every host shell.' },
  { key: 'deploy.stop_old_first', label: 'Stop the old container first', hint: 'Very small hosts: the previous version is stopped before the new one starts, so there is no safe-rollback window.' },
];

const SETTING_FIELDS: { key: string; label: string; hint: string }[] = [
  { key: 'step_up_window_s', label: 'Step-up window (s)', hint: 'How long a verified code keeps sensitive actions unlocked.' },
  { key: 'observation_window_s', label: 'Observation window (s)', hint: 'Time a new deployment is watched before it is final.' },
  { key: 'hardening_revert_window_s', label: 'Hardening revert window (s)', hint: 'Time to confirm connectivity before the server reverts.' },
  { key: 'retain_images', label: 'Images retained', hint: 'Previous images kept per resource for rollback.' },
  { key: 'disk_preflight_threshold_gb', label: 'Disk pre-flight threshold (GB)', hint: 'Minimum free disk to start a build.' },
  { key: 'session_ttl_h', label: 'Session lifetime (h)', hint: 'How long a browser session stays signed in.' },
  { key: 'terminal.idle_timeout_s', label: 'Terminal idle timeout (s)', hint: 'A shell with no input for this long is closed.' },
  { key: 'terminal.max_sessions_per_user', label: 'Terminal sessions per user', hint: 'Open shells one operator may have at a time.' },
  { key: 'deploy.build_timeout_s', label: 'Build timeout (s)', hint: 'Longer than this and the build is stopped and the deployment fails.' },
  { key: 'deploy.release_timeout_s', label: 'Release command timeout (s)', hint: 'The one-off migration container gets this long.' },
  { key: 'deploy.max_log_lines', label: 'Log lines per deployment', hint: 'Older lines of one build are dropped past this count.' },
  { key: 'git.clone_timeout_s', label: 'Clone timeout (s)', hint: 'A repository that cannot be fetched this fast fails the deployment.' },
  { key: 'git.max_clone_mb', label: 'Largest clone (MiB)', hint: 'A working tree bigger than this is not checked out.' },
  { key: 'git.preview_ttl_hours', label: 'Preview lifetime (h)', hint: 'A preview pull request has not pushed to comes down after this.' },
  { key: 'git.preview_max', label: 'Previews kept at once', hint: 'The cap that stops a busy repository building a hundred images here.' },
  { key: 'git.preview_memory_mb', label: 'Memory cap per preview (MiB)', hint: 'A preview never gets more than this, whatever the service it previews is allowed.' },
  { key: 'metrics.sample_interval_s', label: 'Metric sample interval (s)', hint: 'How often the host is measured.' },
  { key: 'metrics.max_samples', label: 'Metric samples kept', hint: 'The chart history the host keeps in memory and on disk.' },
  { key: 'alerts.eval_interval_s', label: 'Alert evaluation interval (s)', hint: 'How often alert rules are checked.' },
  { key: 'notify.timeout_s', label: 'Notification timeout (s)', hint: 'How long a channel may take to answer before the send fails.' },
  { key: 'logs.retention_days', label: 'Deployment log retention (days)', hint: 'Logs older than this are removed, whole deployments at a time.' },
  { key: 'logs.max_rows', label: 'Deployment log rows kept', hint: 'The budget that stops one verbose build filling the disk.' },
  { key: 'cron.history_per_job', label: 'Cron runs kept per job', hint: 'Older runs of a scheduled command are dropped past this count.' },
  { key: 'backup.timeout_s', label: 'Backup timeout (s)', hint: 'A dump that outlives this is failed rather than left running.' },
];

function ModulesTab() {
  const mods = useModules(); const flags = useFlags(); const settings = useSettings(); const pm = usePatchModule(); const pf = usePatchFlags(); const ps = usePatchSettings();
  const toast = useToast(); const { can } = usePermissions(); const [draft, setDraft] = useState<Record<string, string>>({});
  const err = (e: unknown) => toast.error(errorMessage(e));
  return (
    <div className="space-y-4">
      <Card><CardHeader title="Modules" description="Core modules cannot be turned off." />
        <QueryBoundary query={mods}>{(d) => <ul className="divide-y divide-subtle">{d.items.map((m) => (
          <li key={m.key} className="flex items-center gap-3 px-4 py-3 text-xs"><div className="min-w-0 flex-1"><p className="font-semibold text-primary">{m.name} {m.core && <Badge tone="brand">core</Badge>}</p><p className="text-muted">{m.description}</p></div>
            <Switch aria-label={`${m.name} enabled`} checked={m.enabled} disabled={m.core || !can('modules:manage')} onCheckedChange={(enabled) => pm.mutate({ key: m.key, enabled }, { onError: err })} /></li>))}</ul>}</QueryBoundary></Card>
      <Card><CardHeader title="Feature flags" />
        <QueryBoundary query={flags}>{(d) => <ul className="divide-y divide-subtle">{d.items.map((f) => (
          <li key={f.key} className="flex items-center gap-3 px-4 py-3 text-xs"><div className="min-w-0 flex-1"><p className="font-mono text-[11px] font-semibold text-primary">{f.key} {f.release_gated && <Badge tone="amber">release-gated</Badge>}</p><p className="text-muted">{f.description}</p>
            {f.release_gated && <p className="mt-0.5 text-[11px] text-amber-700 dark:text-amber-400">Changes the host's access controls. Enable only after reading the hardening documentation.</p>}</div>
            <Switch aria-label={`${f.key} enabled`} checked={f.enabled} disabled={!can('flags:manage')} onCheckedChange={(enabled) => pf.mutate({ [f.key]: enabled }, { onError: err })} /></li>))}</ul>}</QueryBoundary></Card>
      <Card><CardHeader title="Server settings" description="Owner and admin only." />
        <QueryBoundary query={settings}>{(d) => (
          <CardBody className="space-y-3">
            {SETTING_BOOLEANS.map((s) => (
              <div key={s.key} className="flex items-center gap-3 text-xs"><div className="min-w-0 flex-1"><p className="font-semibold text-primary">{s.label}</p><p className="text-muted">{s.hint}</p></div>
                <Switch aria-label={s.label} checked={d.values[s.key] === true} disabled={!can('flags:manage')} onCheckedChange={(v) => ps.mutate({ [s.key]: v }, { onError: err })} /></div>))}
            <div className="grid gap-3 sm:grid-cols-2">{SETTING_FIELDS.map((s) => (
              <Field key={s.key} label={s.label} hint={s.hint}>{(a) => <Input {...a} type="number" min={0} disabled={!can('flags:manage')} value={draft[s.key] ?? String(d.values[s.key] ?? '')}
                onChange={(e) => setDraft({ ...draft, [s.key]: e.target.value })}
                onBlur={() => { const raw = draft[s.key]; if (raw === undefined || raw === String(d.values[s.key])) return; ps.mutate({ [s.key]: Number(raw) }, { onSuccess: () => setDraft(({ [s.key]: _x, ...rest }) => rest), onError: err }); }} />}</Field>))}</div>
          </CardBody>)}</QueryBoundary></Card>
    </div>
  );
}

function AuditTab() {
  const [filter, setFilter] = useState<AuditFilter>({}); const q = useAudit(filter); const verify = useVerifyAudit(); const toast = useToast(); const { can } = usePermissions();
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];
  if (!can('audit:read')) return <Card><CardBody className="text-xs text-muted">You do not have permission to read the audit log.</CardBody></Card>;
  return (
    <Card><CardHeader title="Audit log" description="Append-only and hash-chained: tampering is detectable (tamper-evident), not impossible."
      actions={<Button size="sm" variant="secondary" loading={verify.isPending} onClick={() => verify.mutate(undefined, { onError: (e) => toast.error(errorMessage(e)) })}><ShieldCheck className="size-3.5" />Verify chain</Button>} />
      <CardBody className="space-y-3">
        {verify.data && <p role="status" className={verify.data.valid ? 'text-xs font-semibold text-green-700 dark:text-green-400' : 'text-xs font-semibold text-red-600'}>
          {verify.data.valid ? `Chain intact: ${verify.data.verified_rows} rows verified.` : `Chain broken at row ${verify.data.first_invalid_seq}. ${verify.data.verified_rows} rows verified before it.`}</p>}
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label="Action contains">{(a) => <Input {...a} value={filter.action ?? ''} onChange={(e) => setFilter({ ...filter, action: e.target.value || undefined })} />}</Field>
          <Field label="Actor">{(a) => <Input {...a} value={filter.actor ?? ''} onChange={(e) => setFilter({ ...filter, actor: e.target.value || undefined })} />}</Field>
        </div>
      </CardBody>
      <QueryBoundary query={{ ...q, data: rows } as never} isEmpty={(d: unknown[]) => d.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No matching events.</p>}>{() => (
        <div className="overflow-x-auto"><Table><THead><tr><TH>#</TH><TH>When</TH><TH>Actor</TH><TH>Action</TH><TH>Target</TH><TH>Hash</TH></tr></THead>
          <TBody>{rows.map((r) => (
            <TR key={r.id}><TD className="tabular-nums">{r.seq}</TD><TD title={absTime(r.at)}>{relTime(r.at)}</TD><TD>{r.actor_email ?? r.actor_type}</TD><TD className="font-mono text-[11px]">{r.action}</TD><TD className="font-mono text-[11px] text-muted">{r.target_type}:{r.target_id}</TD>
              <TD><span className="inline-flex items-center gap-1 font-mono text-[11px]">{r.hash.slice(0, 10)}…<CopyButton value={r.hash} iconOnly /></span></TD></TR>))}</TBody></Table></div>)}</QueryBoundary>
      {q.hasNextPage && <div className="p-3 text-center"><Button size="sm" variant="secondary" loading={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>Load more</Button></div>}
    </Card>
  );
}

function SystemTab() {
  const st = useSystemStatus(); const ver = useSystemVersion(); const cleanup = useCleanup(); const toast = useToast(); const { can } = usePermissions(); const [ask, setAsk] = useState(false);
  return (
    <div className="space-y-4">
      <Card><CardHeader title="System" />
        <QueryBoundary query={st} rows={2}>{(s) => (
          <CardBody className="grid gap-3 text-xs sm:grid-cols-2 lg:grid-cols-4">
            <div><p className="text-muted">Host</p><p className="font-semibold text-primary">{s.hostname}</p></div><div><p className="text-muted">OS</p><p className="text-primary">{s.os} {s.arch}</p></div>
            <div><p className="text-muted">Resources</p><p className="text-primary">{s.cpus} CPU · {bytes(s.ram_total_mb * 1024 * 1024)} RAM</p></div><div><p className="text-muted">Uptime</p><p className="text-primary">{duration(s.uptime_s)}</p></div>
            <div><p className="text-muted">Support tier</p><Badge tone="brand">{s.support_tier}</Badge></div><div><p className="text-muted">Version</p><p className="font-mono text-primary">{ver.data ? `${ver.data.version} (${ver.data.channel})` : '—'}</p></div>
          </CardBody>)}</QueryBoundary></Card>
      <Card><CardHeader title="Cleanup" description="Removes unused images, stopped containers and build cache. Retained rollback images are kept." actions={can('system:cleanup') && <Button size="sm" variant="secondary" onClick={() => setAsk(true)}>Run cleanup</Button>} /></Card>
      <ConfirmDialog open={ask} onOpenChange={setAsk} title="Run cleanup?" confirmLabel="Run cleanup" loading={cleanup.isPending}
        onConfirm={() => cleanup.mutate(undefined, { onSuccess: (r) => { toast.success(`Reclaimed ${r.reclaimed_mb} MB (${r.images_removed} images, ${r.containers_removed} containers).`); setAsk(false); }, onError: (e) => toast.error(errorMessage(e)) })} />
    </div>
  );
}

export function SettingsPage() {
  const { tab = 'users' } = useSearch({ from: '/_app/settings' }); const nav = useNavigate();
  return (
    <>
      <PageHeader title="Settings" description="Users, access, modules and the audit trail." />
      <Tabs value={tab} onValueChange={(t) => void nav({ to: '/settings', search: { tab: t as SettingsTab } })}>
        <TabsList>{SETTINGS_TABS.map((t) => <TabsTrigger key={t} value={t}>{LABELS[t]}</TabsTrigger>)}</TabsList>
        <TabsContent value="users"><UsersTab /></TabsContent><TabsContent value="roles"><RolesTab /></TabsContent><TabsContent value="tokens"><TokensTab /></TabsContent>
        <TabsContent value="git"><GitTab /></TabsContent><TabsContent value="modules"><ModulesTab /></TabsContent><TabsContent value="audit"><AuditTab /></TabsContent><TabsContent value="system"><SystemTab /></TabsContent>
      </Tabs>
    </>
  );
}
