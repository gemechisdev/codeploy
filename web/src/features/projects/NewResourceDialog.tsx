import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useFlags } from '@/api/hooks/core';
import { useCreateResource } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input, Select } from '@/components/ui/input';
import { useToast } from '@/components/ui/toast';
import { usePermissions } from '@/lib/permissions';
import type { components } from '@/api/schema';

type Req = components['schemas']['CreateResourceRequest'];
const slugify = (s: string) => s.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);

export function NewResourceDialog({ open, onOpenChange, environmentId, environmentName }: { open: boolean; onOpenChange: (o: boolean) => void; environmentId: string; environmentName: string }) {
  const create = useCreateResource();
  const nav = useNavigate();
  const toast = useToast();
  const flags = useFlags();
  const { can } = usePermissions();
  const on = (k: string) => !!flags.data?.items.find((f) => f.key === k)?.enabled;
  const [name, setName] = useState(''); const [touched, setTouched] = useState(false);
  const [type, setType] = useState<Req['type']>('app');
  const [mode, setMode] = useState<'pull' | 'build'>('pull');
  const [ref, setRef] = useState(''); const [repo, setRepo] = useState(''); const [branch, setBranch] = useState('main'); const [dockerfile, setDockerfile] = useState('Dockerfile');
  const [port, setPort] = useState('3000'); const [memory, setMemory] = useState('256'); const [domain, setDomain] = useState('');
  const [deployNow, setDeployNow] = useState(true);
  const err = create.error instanceof ApiError ? create.error : null;
  const f = err?.fields ?? {};
  const canDeploy = can('deploy:create');

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const body: Req = {
      environment_id: environmentId, type, name,
      config: {
        image: { mode, ref: mode === 'pull' ? ref : null, dockerfile: mode === 'build' ? dockerfile : null, context: mode === 'build' ? '.' : null },
        port: Number(port), healthcheck: { path: '/', interval_s: 10, retries: 12, start_period_s: 10, timeout_s: 3 },
        limits: { memory_mb: Number(memory), cpus: 0.5, pids_limit: 512 }, isolation: { cap_add: [], user: null, trusted: false }, restart: 'unless-stopped', retain_images: 5,
      },
      source: mode === 'build' ? { kind: 'public_git', repo_url: repo, branch, build_path: '.', dockerfile, path_filters: [], deploy_key_id: null } : null,
      domain: domain.trim() || null, deploy_now: deployNow && canDeploy,
    };
    create.mutate(body, {
      onSuccess: (r) => { toast.success(r.deployment ? `${name} created. First deployment started.` : `${name} created.`); onOpenChange(false); void nav({ to: '/resources/$resourceId', params: { resourceId: r.resource.id }, search: { tab: r.deployment ? 'deployments' : 'overview' } }); },
    });
  };
  const generic = err && err.code !== 'validation_failed' && err.code !== 'conflict';
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="lg" title="New resource" description={`Adds a container to the “${environmentName}” environment.`}>
      <form onSubmit={submit} className="space-y-3" noValidate>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Name" error={f.name ?? (err?.code === 'conflict' ? err.message : undefined)} hint="Becomes the container and image name.">{(a) => <Input {...a} autoFocus className="font-mono" value={name} onChange={(e) => { setName(e.target.value); if (!touched) { /* keep raw */ } }} onBlur={() => { if (!touched) setName((n) => slugify(n)); setTouched(true); }} />}</Field>
          <Field label="Type" error={f.type}>{(a) => (
            <Select {...a} value={type} onChange={(e) => setType(e.target.value as Req['type'])}>
              <option value="app">Application (container)</option>
              <option value="static" disabled={!on('deploy.static')}>Static site{on('deploy.static') ? '' : ' (disabled)'}</option>
              <option value="compose" disabled={!on('deploy.compose')}>Compose stack{on('deploy.compose') ? '' : ' (disabled)'}</option>
            </Select>)}</Field>
        </div>
        <Field label="Source">{(a) => <Select {...a} value={mode} onChange={(e) => setMode(e.target.value as 'pull' | 'build')}><option value="pull">Pull a prebuilt image (recommended on small servers)</option><option value="build">Build from a Git repository on this host</option></Select>}</Field>
        {mode === 'pull' ? (
          <Field label="Image" error={f['config.image.ref']} hint="For example ghcr.io/your-org/app:1.0.0">{(a) => <Input {...a} className="font-mono" placeholder="ghcr.io/org/app:tag" value={ref} onChange={(e) => setRef(e.target.value)} />}</Field>
        ) : (
          <div className="space-y-3 rounded-lg border border-subtle bg-surface-subtle p-3">
            <Field label="Repository URL" error={f['source.repo_url'] ?? f.source}>{(a) => <Input {...a} className="font-mono" placeholder="https://github.com/org/repo.git" value={repo} onChange={(e) => setRepo(e.target.value)} />}</Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Branch" error={f['source.branch']}>{(a) => <Input {...a} className="font-mono" value={branch} onChange={(e) => setBranch(e.target.value)} />}</Field>
              <Field label="Dockerfile" error={f['config.image.dockerfile']}>{(a) => <Input {...a} className="font-mono" value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} />}</Field>
            </div>
            <p className="text-[11px] text-muted">Private repositories: create the resource, then add a deploy key under Source. Builds can need over 1 GB of RAM; pulling an image is safer on small servers.</p>
          </div>
        )}
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Container port" error={f['config.port']}>{(a) => <Input {...a} inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} />}</Field>
          <Field label="Memory limit (MB)" error={f['config.limits.memory_mb']}>{(a) => <Input {...a} inputMode="numeric" value={memory} onChange={(e) => setMemory(e.target.value.replace(/\D/g, ''))} />}</Field>
          <Field label="Domain (optional)" error={f.hostname ?? f.domain}>{(a) => <Input {...a} placeholder="app.example.com" value={domain} onChange={(e) => setDomain(e.target.value)} />}</Field>
        </div>
        <label className={`flex items-center gap-2 text-xs ${canDeploy ? 'cursor-pointer text-primary' : 'text-muted'}`}><input type="checkbox" className="size-4 accent-brand-primary" checked={deployNow && canDeploy} disabled={!canDeploy} onChange={(e) => setDeployNow(e.target.checked)} />Start the first deployment now{!canDeploy && ' (needs deploy permission)'}</label>
        {generic && <p role="alert" className="text-xs text-red-600">{errorMessage(err)}</p>}
        <div className="flex justify-end gap-2"><Button variant="secondary" onClick={() => onOpenChange(false)}>Cancel</Button><Button type="submit" loading={create.isPending} disabled={!name}>Create resource</Button></div>
      </form>
    </Dialog>
  );
}
