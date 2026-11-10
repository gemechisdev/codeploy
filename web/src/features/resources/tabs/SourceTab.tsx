import { KeyRound, RotateCw, Webhook } from 'lucide-react';
import { useEffect, useState, type FormEvent } from 'react';
import { useFlags } from '@/api/hooks/core';
import { useCreateDeployHook, useCreateDeployKey, useCreateWebhook, useDeliveries, useDeployHook, useDeployKey, usePutSource, useSource, useWebhook } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import type { Resource } from '@/api/types';
import { CopyButton } from '@/components/app/CopyButton';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input, Select, Textarea } from '@/components/ui/input';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { useToast } from '@/components/ui/toast';
import { absTime, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';

const origin = () => location.origin;

/** One-time secret reveal. The value exists only in this dialog's props; it is not stored in any cache. */
function OneTimeSecret({ title, rows, onClose, note }: { title: string; rows: { label: string; value: string }[]; onClose: () => void; note: string }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={title} description={note} dismissible={false} footer={<Button onClick={onClose}>I have copied it</Button>}>
      <div className="space-y-3">
        <p role="alert" className="rounded-lg bg-amber-500/10 p-2.5 text-xs text-amber-800 dark:text-amber-300"><strong>This is the only time it is shown.</strong> It is stored hashed and cannot be retrieved later. Lose it and you must rotate.</p>
        {rows.map((r) => <div key={r.label} className="space-y-1"><div className="flex items-center justify-between"><span className="text-[11px] font-semibold text-secondary">{r.label}</span><CopyButton value={r.value} /></div><code className="block break-all rounded-lg bg-code-bg p-2.5 font-mono text-[11px] text-zinc-200">{r.value}</code></div>)}
      </div>
    </Dialog>
  );
}

function SourceForm({ r }: { r: Resource }) {
  const q = useSource(r.id); const put = usePutSource(r.id); const toast = useToast(); const { can } = usePermissions();
  const [f, setF] = useState({ kind: 'registry' as 'registry' | 'public_git' | 'deploy_key_git', repo: '', branch: 'main', build: '.', dockerfile: 'Dockerfile', filters: '' });
  useEffect(() => { const s = q.data; if (s) setF({ kind: s.kind, repo: s.repo_url ?? '', branch: s.branch ?? 'main', build: s.build_path ?? '.', dockerfile: s.dockerfile ?? 'Dockerfile', filters: s.path_filters.join('\n') }); }, [q.data]);
  const err = put.error instanceof ApiError ? put.error : null; const fe = (k: string) => err?.fields[k];
  const ro = !can('git:write'); const git = f.kind !== 'registry';
  const submit = (e: FormEvent) => { e.preventDefault(); put.mutate({ kind: f.kind, repo_url: git ? f.repo : null, branch: git ? f.branch : null, build_path: git ? f.build : null, dockerfile: git ? f.dockerfile : null, path_filters: f.filters.split('\n').map((s) => s.trim()).filter(Boolean), deploy_key_id: null }, { onSuccess: () => toast.success('Source saved.'), onError: (e2) => { if (!(e2 instanceof ApiError && e2.code === 'validation_failed')) toast.error(errorMessage(e2)); } }); };
  return (
    <Card><CardHeader title="Source" description="Where the image comes from." />
      <QueryBoundary query={q} rows={2}>{() => (
        <CardBody><form className="space-y-3" noValidate onSubmit={submit}>
          <Field label="Kind">{(a) => <Select {...a} disabled={ro} value={f.kind} onChange={(e) => setF({ ...f, kind: e.target.value as typeof f.kind })}><option value="registry">Container registry image</option><option value="public_git">Public Git repository (HTTPS)</option><option value="deploy_key_git">Private Git repository (deploy key)</option></Select>}</Field>
          {git && <>
            <Field label="Repository URL" error={fe('repo_url')}>{(a) => <Input {...a} className="font-mono" disabled={ro} placeholder={f.kind === 'deploy_key_git' ? 'ssh://github.com/org/repo.git' : 'https://github.com/org/repo.git'} value={f.repo} onChange={(e) => setF({ ...f, repo: e.target.value })} />}</Field>
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Branch" error={fe('branch')}>{(a) => <Input {...a} className="font-mono" disabled={ro} value={f.branch} onChange={(e) => setF({ ...f, branch: e.target.value })} />}</Field>
              <Field label="Build path" error={fe('build_path')}>{(a) => <Input {...a} className="font-mono" disabled={ro} value={f.build} onChange={(e) => setF({ ...f, build: e.target.value })} />}</Field>
              <Field label="Dockerfile" error={fe('dockerfile')} hint="Optional. If missing, the panel auto-detects Node/Python/Go/… or uses Build tab commands.">{(a) => <Input {...a} className="font-mono" disabled={ro} value={f.dockerfile} onChange={(e) => setF({ ...f, dockerfile: e.target.value })} />}</Field>
            </div>
            <Field label="Path filters (optional)" hint="One per line, e.g. src/**. Pushes touching only other paths are ignored.">{(a) => <Textarea {...a} rows={3} disabled={ro} value={f.filters} onChange={(e) => setF({ ...f, filters: e.target.value })} />}</Field>
            {fe('deploy_key_id') && <p role="alert" className="text-[11px] text-red-600">{fe('deploy_key_id')}</p>}
          </>}
          {!ro && <div className="flex justify-end"><Button type="submit" loading={put.isPending}>Save source</Button></div>}
        </form></CardBody>)}</QueryBoundary></Card>
  );
}

function DeployKeyCard({ r }: { r: Resource }) {
  const q = useDeployKey(r.id); const gen = useCreateDeployKey(r.id); const toast = useToast(); const { can } = usePermissions();
  const missing = q.error instanceof ApiError && q.error.status === 404;
  return (
    <Card><CardHeader title="Deploy key" description="Add this public key to your Git host as a read-only deploy key. The private half never leaves the server." actions={can('git:write') && <Button variant="secondary" size="sm" loading={gen.isPending} onClick={() => gen.mutate(undefined, { onSuccess: () => toast.success('New deploy key generated. Update it on your Git host.'), onError: (e) => toast.error(errorMessage(e)) })}><KeyRound className="size-3.5" />{q.data ? 'Regenerate' : 'Generate'}</Button>} />
      <CardBody>{q.isPending ? <p className="text-xs text-muted">Loading…</p> : missing ? <p className="text-xs text-muted">No deploy key yet. Generate one for private repositories.</p> : q.data ? (
        <div className="space-y-2"><div className="flex justify-end"><CopyButton value={q.data.public_key} label="Copy public key" /></div><code className="block break-all rounded-lg bg-code-bg p-2.5 font-mono text-[11px] text-zinc-200">{q.data.public_key}</code><p className="font-mono text-[11px] text-muted">{q.data.fingerprint}</p></div>) : <p role="alert" className="text-xs text-red-600">{errorMessage(q.error)}</p>}</CardBody></Card>
  );
}

function DeployHookCard({ r }: { r: Resource }) {
  const q = useDeployHook(r.id); const create = useCreateDeployHook(r.id, false); const rotate = useCreateDeployHook(r.id, true);
  const toast = useToast(); const { can } = usePermissions();
  const [secret, setSecret] = useState<{ token: string } | null>(null); const [confirmRotate, setConfirmRotate] = useState(false);
  const missing = q.error instanceof ApiError && q.error.status === 404;
  const hookUrl = (t: string) => `${origin()}/api/v1/hooks/deploy/${t}`;
  const run = (rot: boolean) => (rot ? rotate : create).mutate(undefined, { onSuccess: (d) => { setSecret({ token: d.token }); setConfirmRotate(false); }, onError: (e) => toast.error(errorMessage(e)) });
  return (
    <Card><CardHeader title="Deploy hook" description="Your CI calls this URL after pushing a new image. The image must match this hook's policy." actions={can('hook:manage') && (missing ? <Button size="sm" loading={create.isPending} onClick={() => run(false)}>Create hook</Button> : q.data && <Button variant="secondary" size="sm" onClick={() => setConfirmRotate(true)}><RotateCw className="size-3.5" />Rotate</Button>)} />
      <CardBody>{q.isPending ? <p className="text-xs text-muted">Loading…</p> : missing ? <p className="text-xs text-muted">No deploy hook yet.</p> : q.data ? (
        <dl className="grid gap-2 text-xs sm:grid-cols-3"><div><dt className="text-muted">Token</dt><dd className="font-mono text-primary">{q.data.token_prefix}…</dd></div><div><dt className="text-muted">Image policy</dt><dd className="font-mono text-primary">{q.data.image_policy ?? '—'}</dd></div><div><dt className="text-muted">Last used</dt><dd className="text-primary" title={absTime(q.data.last_used_at)}>{q.data.last_used_at ? relTime(q.data.last_used_at) : 'never'}</dd></div></dl>) : <p role="alert" className="text-xs text-red-600">{errorMessage(q.error)}</p>}</CardBody>
      {secret && <OneTimeSecret title="Deploy hook created" note="Call it from CI with a POST and a JSON body naming the new image." onClose={() => setSecret(null)} rows={[{ label: 'Hook URL', value: hookUrl(secret.token) }, { label: 'Example', value: `curl -X POST -H 'Content-Type: application/json' -d '{"image":"${q.data?.image_policy?.replace(':*', ':TAG') ?? 'ghcr.io/org/app:TAG'}"}' ${hookUrl(secret.token)}` }]} />}
      <Dialog open={confirmRotate} onOpenChange={setConfirmRotate} size="sm" title="Rotate the deploy hook?" description="The old URL stops working immediately. Update your CI with the new one." footer={<><Button variant="secondary" onClick={() => setConfirmRotate(false)}>Cancel</Button><Button loading={rotate.isPending} onClick={() => run(true)}>Rotate</Button></>} />
    </Card>
  );
}

function WebhookCard({ r }: { r: Resource }) {
  const q = useWebhook(r.id); const create = useCreateWebhook(r.id); const deliveries = useDeliveries(r.id);
  const toast = useToast(); const { can } = usePermissions();
  const [secret, setSecret] = useState<string | null>(null);
  return (
    <Card><CardHeader title="GitHub webhook" description="Pushes to the configured branch trigger a build. Signed with HMAC-SHA256; duplicate deliveries are ignored." actions={can('hook:manage') && <Button variant="secondary" size="sm" loading={create.isPending} onClick={() => create.mutate(undefined, { onSuccess: (d) => setSecret(d.secret), onError: (e) => toast.error(errorMessage(e)) })}><Webhook className="size-3.5" />{q.data?.configured ? 'Regenerate secret' : 'Create webhook'}</Button>} />
      <CardBody className="space-y-3">
        {q.data?.configured ? <p className="text-xs text-secondary">Payload URL <code className="font-mono text-primary">{origin()}{q.data.path}</code> · secret <code className="font-mono text-primary">{q.data.secret_prefix}…</code></p> : <p className="text-xs text-muted">No webhook yet.</p>}
        <QueryBoundary query={deliveries} isEmpty={(d) => !d?.items?.length} empty={<p className="text-xs text-muted">No deliveries received yet.</p>} rows={2}>
          {(d) => <Table><THead><tr><TH>Result</TH><TH>Event</TH><TH>Commit</TH><TH>Reason</TH><TH>When</TH></tr></THead><TBody>{d.items.slice(0, 15).map((x) => <TR key={x.id}><TD><StatusBadge status={x.result} /></TD><TD>{x.event}</TD><TD className="font-mono text-[11px]">{x.commit_sha?.slice(0, 7) ?? '—'}</TD><TD className="max-w-60 truncate text-secondary" title={x.reason ?? ''}>{x.reason ?? '—'}</TD><TD title={absTime(x.received_at)}>{relTime(x.received_at)}</TD></TR>)}</TBody></Table>}
        </QueryBoundary>
      </CardBody>
      {secret && <OneTimeSecret title="Webhook secret" note="Paste both values into GitHub → Settings → Webhooks (content type: application/json, event: push)." onClose={() => setSecret(null)} rows={[{ label: 'Payload URL', value: `${origin()}${q.data?.path ?? '/api/v1/webhooks/github'}` }, { label: 'Secret', value: secret }]} />}
    </Card>
  );
}

export function SourceTab({ resource: r }: { resource: Resource }) {
  const flags = useFlags(); const { can } = usePermissions();
  const hookOn = flags.data?.items.find((f) => f.key === 'registry_pull_hook')?.enabled !== false;
  const src = useSource(r.id);
  return (
    <div className="space-y-4">
      <SourceForm r={r} />
      {src.data?.kind === 'deploy_key_git' && <DeployKeyCard r={r} />}
      {src.data && src.data.kind !== 'registry' && can('hook:manage') && <WebhookCard r={r} />}
      {hookOn && can('hook:manage') && <DeployHookCard r={r} />}
    </div>
  );
}
