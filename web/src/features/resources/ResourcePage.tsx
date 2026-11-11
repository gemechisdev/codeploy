import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { ExternalLink, Play, RefreshCw, Rocket, Square, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useModules } from '@/api/hooks/core';
import { useControlResource, useCreateDeployment, useDeleteResource, useResource } from '@/api/hooks/workloads';
import { errorMessage } from '@/api/errors';
import { ConfirmDialog } from '@/components/app/ConfirmDialog';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { useToast } from '@/components/ui/toast';
import { usePermissions } from '@/lib/permissions';
import { PageHeader } from '@/features/shell/ModuleGate';
import { RESOURCE_TABS, type ResourceTab } from '@/router';
import { DeploymentsTab } from './tabs/DeploymentsTab';
import { DomainsTab } from './tabs/DomainsTab';
import { EnvTab } from './tabs/EnvTab';
import { LimitsTab, IsolationTab, OverviewTab, VolumesTab, BuildTab, resourceURLs } from './tabs/ConfigTabs';
import { LogsTab } from './tabs/LogsTab';
import { ShellTab } from './tabs/ShellTab';
import { SourceTab } from './tabs/SourceTab';

const LABEL: Record<ResourceTab, string> = { deployments: 'Deployments', overview: 'Overview', source: 'Source & CI', build: 'Build', env: 'Environment', domains: 'Domains', volumes: 'Volumes', limits: 'Limits', isolation: 'Isolation', logs: 'Logs', shell: 'Shell' };

export function ResourcePage() {
  const { resourceId } = useParams({ from: '/_app/resources/$resourceId' });
  const { tab } = useSearch({ from: '/_app/resources/$resourceId' });
  const nav = useNavigate();
  const q = useResource(resourceId);
  const mods = useModules();
  const { can } = usePermissions();
  const toast = useToast();
  const deploy = useCreateDeployment(resourceId);
  const control = useControlResource(resourceId);
  const del = useDeleteResource();
  const [confirmDel, setConfirmDel] = useState(false); const [typed, setTyped] = useState('');
  const modOn = (k: string) => mods.data?.items.find((m) => m.key === k)?.enabled !== false;
  const current: ResourceTab = tab ?? 'deployments';
  const tabs = RESOURCE_TABS.filter((t) => (t !== 'domains' || modOn('edge')) && (t !== 'source' || modOn('git')) && (t !== 'shell' || (modOn('terminal') && can('terminal:container'))) && (t !== 'env' || can('env:read')));

  return (
    <QueryBoundary query={q} rows={5}>
      {(r) => {
        const busy = r.status === 'deploying';
        const run = (a: 'start' | 'stop' | 'restart') => control.mutate(a, { onSuccess: () => toast.success(`${r.name}: ${a} requested.`), onError: (e) => toast.error(errorMessage(e)) });
        return (
          <>
            <PageHeader
              title={r.name}
              description={(() => {
                const u = resourceURLs(r);
                const links = (u.public?.length
                  ? u.public.map((x) => ({ label: x.hostname, url: x.url }))
                  : (u.host_access?.length
                      ? u.host_access.map((x) => ({ label: x.url.replace(/^https?:\/\//, ''), url: x.url }))
                      : (r.primary_domain ? [{ label: r.primary_domain, url: `https://${r.primary_domain}` }] : [])));
                return (
                  <span className="flex flex-wrap items-center gap-2">
                    <StatusBadge status={r.status} />
                    <Badge>{r.type}</Badge>
                    <Badge tone="brand">{r.environment_name}</Badge>
                    {links.map((l) => (
                      <a key={l.url} href={l.url} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 font-mono text-[11px] text-brand-primary hover:underline">
                        {l.label}<ExternalLink aria-hidden className="size-3" />
                      </a>
                    ))}
                    {!links.length && u.internal && (
                      <span className="font-mono text-[11px] text-muted" title="Docker network only">internal {u.internal}</span>
                    )}
                    {!links.length && !u.internal && r.type !== 'database' && (
                      <span className="text-[11px] text-muted">No URL yet — redeploy to publish a host port</span>
                    )}
                  </span>
                );
              })()}
              actions={<>
                {can('deploy:create') && <Button loading={deploy.isPending} disabled={busy} onClick={() => deploy.mutate({}, { onSuccess: () => { toast.success('Deployment started.'); void nav({ to: '/resources/$resourceId', params: { resourceId }, search: { tab: 'deployments' } }); }, onError: (e) => toast.error(errorMessage(e)) })}><Rocket className="size-3.5" />Deploy</Button>}
                {can('resource:control') && r.active_deployment_id && (r.status === 'stopped'
                  ? <Button variant="secondary" loading={control.isPending} onClick={() => run('start')}><Play className="size-3.5" />Start</Button>
                  : <><Button variant="secondary" disabled={busy} loading={control.isPending} onClick={() => run('restart')}><RefreshCw className="size-3.5" />Restart</Button><Button variant="secondary" disabled={busy} onClick={() => run('stop')}><Square className="size-3.5" />Stop</Button></>)}
                {can('resource:delete') && <Button variant="outlineDanger" aria-label="Delete resource" onClick={() => setConfirmDel(true)}><Trash2 className="size-3.5" /></Button>}
              </>} />
            <Tabs value={current} onValueChange={(v) => void nav({ to: '/resources/$resourceId', params: { resourceId }, search: { tab: v as ResourceTab }, replace: true })}>
              <TabsList>{tabs.map((t) => <TabsTrigger key={t} value={t}>{LABEL[t]}</TabsTrigger>)}</TabsList>
              <TabsContent value="deployments"><DeploymentsTab resource={r} /></TabsContent>
              <TabsContent value="overview"><OverviewTab resource={r} /></TabsContent>
              <TabsContent value="source"><SourceTab resource={r} /></TabsContent>
              <TabsContent value="build"><BuildTab resource={r} /></TabsContent>
              <TabsContent value="env"><EnvTab resource={r} /></TabsContent>
              <TabsContent value="domains"><DomainsTab resource={r} /></TabsContent>
              <TabsContent value="volumes"><VolumesTab resource={r} /></TabsContent>
              <TabsContent value="limits"><LimitsTab resource={r} /></TabsContent>
              <TabsContent value="isolation"><IsolationTab resource={r} /></TabsContent>
              <TabsContent value="logs"><LogsTab resource={r} /></TabsContent>
              <TabsContent value="shell"><ShellTab resource={r} /></TabsContent>
            </Tabs>
            <ConfirmDialog open={confirmDel} onOpenChange={(o) => { setConfirmDel(o); setTyped(''); }} destructive title={`Delete ${r.name}?`} confirmLabel="Delete resource" loading={del.isPending} disabled={typed !== r.name}
              description="This removes the container, its domains, environment variables and deployment history. Volumes on disk are kept."
              onConfirm={() => del.mutate(r.id, { onSuccess: () => { toast.success(`${r.name} deleted.`); void nav({ to: '/projects/$projectId', params: { projectId: r.project_id } }); }, onError: (e) => { setConfirmDel(false); toast.error(errorMessage(e)); } })}>
              <label className="block space-y-1 text-xs text-secondary">Type <strong className="font-mono text-primary">{r.name}</strong> to confirm<Input autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} /></label>
            </ConfirmDialog>
          </>
        );
      }}
    </QueryBoundary>
  );
}
