import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useCreateResource } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input, Select } from '@/components/ui/input';
import { useToast } from '@/components/ui/toast';
import { usePermissions } from '@/lib/permissions';
import type { components } from '@/api/schema';

type Req = components['schemas']['CreateResourceRequest'];
type ResType = Req['type'];

const slugify = (s: string) => s.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);

const TYPE_HELP: Record<string, string> = {
  app: 'Container app: Dockerfile, auto-detected Node/Python/Go/Rust/PHP/Ruby, or a registry image.',
  static: 'Static site or SPA: index.html, Vite/CRA/Nuxt export, or any folder of assets — served via nginx in a container.',
  compose: 'Docker Compose stack from compose.yaml / docker-compose.yml (multi-service).',
};

export function NewResourceDialog({ open, onOpenChange, environmentId, environmentName }: { open: boolean; onOpenChange: (o: boolean) => void; environmentId: string; environmentName: string }) {
  const create = useCreateResource();
  const nav = useNavigate();
  const toast = useToast();
  const { can } = usePermissions();
  const [name, setName] = useState('');
  const [touched, setTouched] = useState(false);
  const [type, setType] = useState<ResType>('app');
  const [mode, setMode] = useState<'pull' | 'build'>('build');
  const [ref, setRef] = useState('');
  const [repo, setRepo] = useState('');
  const [branch, setBranch] = useState('main');
  const [dockerfile, setDockerfile] = useState('Dockerfile');
  const [installCmd, setInstallCmd] = useState('');
  const [buildCmd, setBuildCmd] = useState('');
  const [startCmd, setStartCmd] = useState('');
  const [showBuild, setShowBuild] = useState(false);
  const [port, setPort] = useState('3000');
  const [memory, setMemory] = useState('256');
  const [domain, setDomain] = useState('');
  const [deployNow, setDeployNow] = useState(true);
  const err = create.error instanceof ApiError ? create.error : null;
  const f = err?.fields ?? {};
  const canDeploy = can('deploy:create');
  const generic = err && err.code !== 'validation_failed' && err.code !== 'conflict';

  const onType = (t: ResType) => {
    setType(t);
    if (t === 'static') {
      setMode('build');
      setPort('80');
      setDockerfile('Dockerfile');
    } else if (t === 'compose') {
      setMode('build');
      setDockerfile('compose.yaml');
      setPort('3000');
    } else {
      setPort('3000');
      setDockerfile('Dockerfile');
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const imageMode = type === 'compose' ? 'build' : mode;
    const body = {
      environment_id: environmentId,
      type,
      name,
      config: {
        image: {
          mode: imageMode,
          ref: imageMode === 'pull' ? ref : null,
          dockerfile: imageMode === 'build' ? dockerfile : null,
          context: imageMode === 'build' ? '.' : null,
        },
        port: Number(port) || (type === 'static' ? 8080 : 3000),
        healthcheck: { path: '/', interval_s: 10, retries: 12, start_period_s: 15, timeout_s: 3 },
        limits: { memory_mb: Number(memory) || 256, cpus: 0.5, pids_limit: 512 },
        isolation: { cap_add: [], user: null, trusted: false },
        restart: 'unless-stopped',
        retain_images: 5,
        ...(mode === 'build' || type === 'compose' || type === 'static'
          ? {
              build: {
                install_command: installCmd.trim() || null,
                build_command: buildCmd.trim() || null,
                start_command: startCmd.trim() || null,
              },
            }
          : {}),
      },
      source:
        imageMode === 'build'
          ? {
              kind: 'public_git',
              repo_url: repo,
              branch,
              build_path: '.',
              dockerfile,
              path_filters: [],
              deploy_key_id: null,
            }
          : null,
      domain: domain.trim() || null,
      deploy_now: deployNow && canDeploy,
    };
    create.mutate(body as Req, {
      onSuccess: (r) => {
        toast.success(r.deployment ? `${name} created. First deployment started.` : `${name} created.`);
        onOpenChange(false);
        void nav({
          to: '/resources/$resourceId',
          params: { resourceId: r.resource.id },
          search: { tab: r.deployment ? 'deployments' : 'overview' },
        });
      },
    });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="New resource"
      description={`Deploy into ${environmentName}. Apps, static sites, and Compose stacks are supported — with or without a Dockerfile.`}
    >
      <form className="space-y-3" noValidate onSubmit={submit}>
        <div className="rounded-lg border border-subtle bg-surface-subtle px-3 py-2 text-[11px] text-secondary space-y-1">
          <p className="font-medium text-primary">Build strategies (Coolify / Vercel-style)</p>
          <ul className="list-disc space-y-0.5 pl-4">
            <li><strong className="text-primary">Dockerfile</strong> when present — used as-is.</li>
            <li><strong className="text-primary">Auto-detect</strong> Node (Next/Vite/Nuxt), Python, Go, Rust, PHP, Ruby, or static <span className="font-mono">index.html</span>.</li>
            <li><strong className="text-primary">Registry pull</strong> for pre-built images.</li>
            <li><strong className="text-primary">Compose</strong> runs <span className="font-mono">docker compose up</span> for multi-service stacks.</li>
            <li>Host needs Docker. Databases: prefer <strong className="text-primary">Templates</strong> (Postgres, Redis, …).</li>
          </ul>
        </div>

        <Field label="Name" error={f.name} hint={touched ? `Slug: ${slugify(name) || '—'}` : undefined}>
          {(a) => (
            <Input {...a} value={name} onChange={(e) => { setName(e.target.value); setTouched(true); }} placeholder="api" autoFocus />
          )}
        </Field>

        <Field label="Type" error={f.type} hint={TYPE_HELP[type]}>
          {(a) => (
            <Select {...a} value={type} onChange={(e) => onType(e.target.value as ResType)}>
              <option value="app">App (API, worker, full-stack)</option>
              <option value="static">Static site / SPA</option>
              <option value="compose">Docker Compose stack</option>
            </Select>
          )}
        </Field>

        {type !== 'compose' && (
          <Field label="Image source">
            {(a) => (
              <Select {...a} value={mode} onChange={(e) => setMode(e.target.value as 'pull' | 'build')}>
                <option value="build">Build from Git (Dockerfile or auto-detect)</option>
                <option value="pull">Pull image from registry</option>
              </Select>
            )}
          </Field>
        )}

        {type !== 'compose' && mode === 'pull' ? (
          <Field label="Image reference" error={f['config.image.ref']} hint="nginx:alpine · ghcr.io/org/api:main">
            {(a) => (
              <Input {...a} className="font-mono" placeholder="ghcr.io/org/app:latest" value={ref} onChange={(e) => setRef(e.target.value)} />
            )}
          </Field>
        ) : (
          <div className="space-y-3 rounded-lg border border-subtle bg-surface-subtle p-3">
            <Field
              label="Repository URL"
              error={f['source.repo_url'] ?? f.source}
              hint="Public HTTPS, or SSH after adding a deploy key on the Source tab."
            >
              {(a) => (
                <Input
                  {...a}
                  className="font-mono"
                  placeholder="https://github.com/org/repo.git"
                  value={repo}
                  onChange={(e) => setRepo(e.target.value)}
                />
              )}
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Branch" error={f['source.branch']}>
                {(a) => <Input {...a} className="font-mono" value={branch} onChange={(e) => setBranch(e.target.value)} />}
              </Field>
              <Field
                label={type === 'compose' ? 'Compose file' : 'Dockerfile (optional)'}
                error={f['config.image.dockerfile']}
                hint={type === 'compose' ? 'compose.yaml or docker-compose.yml' : 'Leave as Dockerfile — auto-detect runs if missing'}
              >
                {(a) => (
                  <Input {...a} className="font-mono" value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} />
                )}
              </Field>
            </div>
          </div>
        )}

        
            <button type="button" className="text-[11px] font-semibold text-brand-primary" onClick={() => setShowBuild((x) => !x)}>
              {showBuild ? "Hide" : "Show"} install / build / start overrides
            </button>
            {showBuild && (
              <div className="space-y-2 rounded border border-subtle bg-surface p-2">
                <p className="text-[11px] text-muted">Empty = auto-detect (package.json, requirements.txt, go.mod, …) or use Dockerfile if present.</p>
                <Field label="Install command">{(a) => <Input {...a} className="font-mono" placeholder="npm ci · pip install -r requirements.txt" value={installCmd} onChange={(e) => setInstallCmd(e.target.value)} />}</Field>
                <Field label="Build command">{(a) => <Input {...a} className="font-mono" placeholder="npm run build" value={buildCmd} onChange={(e) => setBuildCmd(e.target.value)} />}</Field>
                <Field label="Start command">{(a) => <Input {...a} className="font-mono" placeholder="npm start · uvicorn main:app --host all-interfaces --port 8000" value={startCmd} onChange={(e) => setStartCmd(e.target.value)} />}</Field>
              </div>
            )}

        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Container port" error={f['config.port']} hint="Process listen port">
            {(a) => (
              <Input {...a} inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} />
            )}
          </Field>
          <Field label="Memory (MB)" error={f['config.limits.memory_mb']}>
            {(a) => (
              <Input {...a} inputMode="numeric" value={memory} onChange={(e) => setMemory(e.target.value.replace(/\D/g, ''))} />
            )}
          </Field>
          <Field label="Domain (optional)" error={f.hostname ?? f.domain}>
            {(a) => (
              <Input {...a} placeholder="app.your-domain" value={domain} onChange={(e) => setDomain(e.target.value)} />
            )}
          </Field>
        </div>

        <label className={`flex items-center gap-2 text-xs ${canDeploy ? 'cursor-pointer text-primary' : 'text-muted'}`}>
          <input
            type="checkbox"
            className="size-4 accent-brand-primary"
            checked={deployNow && canDeploy}
            disabled={!canDeploy}
            onChange={(e) => setDeployNow(e.target.checked)}
          />
          Start the first deployment now{!canDeploy && ' (needs deploy permission)'}
        </label>
        {generic && (
          <p role="alert" className="text-xs text-red-600">
            {errorMessage(err)}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            type="submit"
            loading={create.isPending}
            disabled={!name.trim() || (type !== 'compose' && mode === 'pull' ? !ref.trim() : !repo.trim())}
          >
            Create resource
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
