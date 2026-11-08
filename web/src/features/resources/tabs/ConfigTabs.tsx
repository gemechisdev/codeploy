import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { useCreateVolume, usePatchResource, useResourcePolicy, useVolumes } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import type { Resource, ResourceConfig } from '@/api/types';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Field, Input, Select } from '@/components/ui/input';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { useToast } from '@/components/ui/toast';
import { absTime, relTime, shortDigest } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';

const Fact = ({ k, children }: { k: string; children: ReactNode }) => <div className="rounded-lg border border-subtle bg-surface-subtle p-3"><dt className="text-[10px] font-semibold uppercase tracking-wider text-muted">{k}</dt><dd className="mt-0.5 break-words text-xs text-primary">{children}</dd></div>;

export function OverviewTab({ resource: r }: { resource: Resource }) {
  const c = r.config;
  return (
    <div className="space-y-4">
      <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Fact k="Image source">{c.image.mode === 'pull' ? <span className="font-mono">{c.image.ref}</span> : 'Built on this host from Git'}</Fact>
        <Fact k="Active digest"><span className="font-mono">{shortDigest(r.active_digest)}</span></Fact>
        <Fact k="Container port"><span className="tabular-nums">{c.port}</span></Fact>
        <Fact k="Health check"><span className="font-mono">GET {c.healthcheck.path}</span> every {c.healthcheck.interval_s}s, {c.healthcheck.retries} retries</Fact>
        <Fact k="Restart policy">{c.restart}</Fact>
        <Fact k="Retained images">{c.retain_images}</Fact>
        <Fact k="Running since">{r.started_at ? <span title={absTime(r.started_at)}>{relTime(r.started_at)}</span> : '—'}</Fact>
        <Fact k="Created"><span title={absTime(r.created_at)}>{relTime(r.created_at)}</span></Fact>
        <Fact k="Environment">{r.environment_name}</Fact>
      </dl>
    </div>
  );
}

function usePatch(r: Resource) {
  const patch = usePatchResource(r.id);
  const toast = useToast();
  const err = patch.error instanceof ApiError ? patch.error : null;
  const save = (config: ResourceConfig) => patch.mutate({ config }, { onSuccess: () => toast.success('Saved. Changes apply on the next deployment.'), onError: (e) => { if (!(e instanceof ApiError && e.code === 'validation_failed')) toast.error(errorMessage(e)); } });
  return { patch, err, save, fe: (k: string) => err?.fields[k] };
}
const num = (s: string) => (s === '' ? NaN : Number(s));

export function LimitsTab({ resource: r }: { resource: Resource }) {
  const { can } = usePermissions(); const { patch, save, fe } = usePatch(r);
  const c = r.config;
  const [v, setV] = useState({ mem: String(c.limits.memory_mb), cpus: String(c.limits.cpus), pids: String(c.limits.pids_limit), path: c.healthcheck.path, interval: String(c.healthcheck.interval_s), retries: String(c.healthcheck.retries), start: String(c.healthcheck.start_period_s), timeout: String(c.healthcheck.timeout_s), restart: c.restart, retain: String(c.retain_images), port: String(c.port) });
  useEffect(() => { setV((x) => ({ ...x, mem: String(c.limits.memory_mb), cpus: String(c.limits.cpus), pids: String(c.limits.pids_limit) })); }, [c.limits.memory_mb, c.limits.cpus, c.limits.pids_limit]);
  const ro = !can('resource:write'); const set = (k: keyof typeof v) => (e: { target: { value: string } }) => setV((x) => ({ ...x, [k]: e.target.value }));
  const submit = (e: FormEvent) => { e.preventDefault(); save({ ...c, port: num(v.port), limits: { memory_mb: num(v.mem), cpus: num(v.cpus), pids_limit: num(v.pids) }, healthcheck: { path: v.path, interval_s: num(v.interval), retries: num(v.retries), start_period_s: num(v.start), timeout_s: num(v.timeout) }, restart: v.restart as ResourceConfig['restart'], retain_images: num(v.retain) }); };
  return (
    <Card><CardHeader title="Limits & health" description="Hard resource caps and the health check used to decide whether a deployment is good." />
      <CardBody><form onSubmit={submit} className="space-y-4" noValidate>
        <div className="grid gap-3 sm:grid-cols-4">
          <Field label="Memory (MB)" error={fe('config.limits.memory_mb')}>{(a) => <Input {...a} inputMode="numeric" disabled={ro} value={v.mem} onChange={set('mem')} />}</Field>
          <Field label="CPUs" error={fe('config.limits.cpus')}>{(a) => <Input {...a} inputMode="decimal" disabled={ro} value={v.cpus} onChange={set('cpus')} />}</Field>
          <Field label="PIDs limit" error={fe('config.limits.pids_limit')}>{(a) => <Input {...a} inputMode="numeric" disabled={ro} value={v.pids} onChange={set('pids')} />}</Field>
          <Field label="Container port" error={fe('config.port')}>{(a) => <Input {...a} inputMode="numeric" disabled={ro} value={v.port} onChange={set('port')} />}</Field>
        </div>
        <div className="grid gap-3 sm:grid-cols-5">
          <Field label="Health path" error={fe('config.healthcheck.path')} className="sm:col-span-2">{(a) => <Input {...a} className="font-mono" disabled={ro} value={v.path} onChange={set('path')} />}</Field>
          <Field label="Interval (s)">{(a) => <Input {...a} inputMode="numeric" disabled={ro} value={v.interval} onChange={set('interval')} />}</Field>
          <Field label="Retries" error={fe('config.healthcheck.retries')}>{(a) => <Input {...a} inputMode="numeric" disabled={ro} value={v.retries} onChange={set('retries')} />}</Field>
          <Field label="Start period (s)">{(a) => <Input {...a} inputMode="numeric" disabled={ro} value={v.start} onChange={set('start')} />}</Field>
        </div>
        <div className="grid gap-3 sm:grid-cols-4">
          <Field label="Restart policy">{(a) => <Select {...a} disabled={ro} value={v.restart} onChange={set('restart')}><option value="unless-stopped">unless-stopped</option><option value="always">always</option><option value="on-failure">on-failure</option><option value="no">no</option></Select>}</Field>
          <Field label="Retained images" hint="Kept for rollback." error={fe('config.retain_images')}>{(a) => <Input {...a} inputMode="numeric" disabled={ro} value={v.retain} onChange={set('retain')} />}</Field>
        </div>
        {!ro && <div className="flex justify-end"><Button type="submit" loading={patch.isPending}>Save limits</Button></div>}
      </form></CardBody></Card>
  );
}

// Mirrors the allowlist enforced by the server (contract §isolation). The server is the authority; this only drives the checkboxes.
const CAP_ALLOWLIST = ['NET_BIND_SERVICE', 'CHOWN', 'SETUID', 'SETGID'];

export function IsolationTab({ resource: r }: { resource: Resource }) {
  const { can, role } = usePermissions(); const { patch, save, fe } = usePatch(r);
  const pol = useResourcePolicy(r.id);
  const c = r.config;
  const [caps, setCaps] = useState<string[]>(c.isolation.cap_add); const [user, setUser] = useState(c.isolation.user ?? ''); const [trusted, setTrusted] = useState(c.isolation.trusted);
  const ro = !can('resource:write'); const owner = role === 'owner';
  return (
    <div className="space-y-4">
      <Card><CardHeader title="Effective isolation policy" description="Computed by the server. The defaults are strict; deviations are listed and audited." actions={pol.data && <StatusBadge status={pol.data.ok ? 'pass' : 'fail'} label={pol.data.ok ? 'Compliant' : 'Violations'} />} />
        <QueryBoundary query={pol} rows={3}>{(p) => (
          <Table><THead><tr><TH>Control</TH><TH>Value</TH><TH>Default</TH><TH>State</TH></tr></THead>
            <TBody>{p.controls.map((x) => <TR key={x.key}><TD className="font-semibold text-primary">{x.label}{x.note && <p className="mt-0.5 text-[11px] font-normal text-muted">{x.note}</p>}</TD><TD className="font-mono text-[11px] text-secondary">{x.value}</TD><TD className="font-mono text-[11px] text-muted">{x.default}</TD><TD><StatusBadge status={x.state === 'ok' ? 'pass' : x.state === 'warn' ? 'warn' : 'fail'} /></TD></TR>)}</TBody></Table>)}</QueryBoundary></Card>
      <Card><CardHeader title="Adjust isolation" description="Capabilities start fully dropped. Add back only what the app needs." />
        <CardBody className="space-y-4">
          <fieldset disabled={ro} className="space-y-2"><legend className="text-[11px] font-semibold text-secondary">Extra capabilities</legend>
            <div className="flex flex-wrap gap-3">{CAP_ALLOWLIST.map((cap) => <label key={cap} className="flex cursor-pointer items-center gap-1.5 font-mono text-[11px] text-primary"><input type="checkbox" className="size-3.5 accent-brand-primary" checked={caps.includes(cap)} onChange={(e) => setCaps((x) => (e.target.checked ? [...x, cap] : x.filter((y) => y !== cap)))} />{cap}</label>)}</div>
            {fe('config.isolation.cap_add') && <p role="alert" className="text-[11px] text-red-600">{fe('config.isolation.cap_add')}</p>}</fieldset>
          <Field label="Run as user (optional)" hint="Empty uses the image's USER." className="max-w-xs">{(a) => <Input {...a} className="font-mono" disabled={ro} value={user} placeholder="1000" onChange={(e) => setUser(e.target.value)} />}</Field>
          <div className="rounded-lg border border-red-500/30 bg-red-500/5 p-3">
            <label className="flex cursor-pointer items-start gap-2.5 text-xs text-primary"><input type="checkbox" className="mt-0.5 size-4 accent-red-600" checked={trusted} disabled={ro || !owner} onChange={(e) => setTrusted(e.target.checked)} /><span><strong>Trusted resource</strong>: allows mounting the Docker socket, which is root-equivalent on this host. {owner ? '' : <Badge tone="grey">Owners only</Badge>}</span></label>
            {fe('config.isolation.trusted') && <p role="alert" className="mt-1 text-[11px] text-red-600">{fe('config.isolation.trusted')}</p>}
          </div>
          {!ro && <div className="flex justify-end"><Button loading={patch.isPending} onClick={() => save({ ...c, isolation: { cap_add: caps, user: user.trim() || null, trusted } })}>Save isolation</Button></div>}
        </CardBody></Card>
    </div>
  );
}

export function VolumesTab({ resource: r }: { resource: Resource }) {
  const q = useVolumes(r.id); const create = useCreateVolume(r.id); const toast = useToast(); const { can } = usePermissions();
  const [name, setName] = useState(''); const [mount, setMount] = useState('');
  const err = create.error instanceof ApiError ? create.error : null;
  return (
    <Card><CardHeader title="Volumes" description={`Bind mounts live under /var/lib/codeplo/volumes/${r.slug}/ and nowhere else.`} />
      <QueryBoundary query={q} isEmpty={(d) => !d?.items?.length} empty={<p className="p-6 text-center text-xs text-muted">No volumes. Data written inside the container is lost on redeploy.</p>} rows={2}>
        {(d) => <Table><THead><tr><TH>Name</TH><TH>Mount path</TH><TH>Host path</TH></tr></THead><TBody>{d.items.map((v) => <TR key={v.id}><TD className="font-semibold text-primary">{v.name}</TD><TD className="font-mono text-[11px]">{v.mount_path}</TD><TD className="font-mono text-[11px] text-muted">{v.host_path}</TD></TR>)}</TBody></Table>}
      </QueryBoundary>
      {can('resource:write') && (
        <form className="grid gap-3 border-t border-subtle p-4 sm:grid-cols-[1fr_1.5fr_auto] sm:items-start" noValidate onSubmit={(e) => { e.preventDefault(); create.mutate({ name, mount_path: mount }, { onSuccess: () => { toast.success('Volume added. It is mounted on the next deployment.'); setName(''); setMount(''); } }); }}>
          <Field label="Name" error={err?.fields.name ?? (err?.code === 'conflict' ? err.message : undefined)}>{(a) => <Input {...a} className="font-mono" placeholder="data" value={name} onChange={(e) => setName(e.target.value)} />}</Field>
          <Field label="Mount path" error={err?.fields.mount_path}>{(a) => <Input {...a} className="font-mono" placeholder="/app/data" value={mount} onChange={(e) => setMount(e.target.value)} />}</Field>
          <Button type="submit" className="sm:mt-5" loading={create.isPending} disabled={!name || !mount}>Add volume</Button>
        </form>
      )}
    </Card>
  );
}
