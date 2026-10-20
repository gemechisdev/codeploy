import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Globe, Plus } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useCreateEnvironment, useProject } from '@/api/hooks/workloads';
import { ApiError } from '@/api/errors';
import { EmptyState } from '@/components/app/EmptyState';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input } from '@/components/ui/input';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { PermissionGate } from '@/lib/permissions';
import { PageHeader } from '@/features/shell/ModuleGate';
import { NewResourceDialog } from './NewResourceDialog';

function NewEnvDialog({ projectId, open, onOpenChange }: { projectId: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const m = useCreateEnvironment(projectId);
  const [name, setName] = useState('');
  const err = m.error instanceof ApiError ? m.error : null;
  const submit = (e: FormEvent) => { e.preventDefault(); m.mutate(name, { onSuccess: () => { setName(''); onOpenChange(false); } }); };
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="sm" title="New environment">
      <form onSubmit={submit} className="space-y-3" noValidate>
        <Field label="Name" error={err?.fields.name ?? (err?.code === 'conflict' ? err.message : undefined)}>{(a) => <Input {...a} autoFocus className="font-mono" placeholder="staging" value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <div className="flex justify-end gap-2"><Button variant="secondary" onClick={() => onOpenChange(false)}>Cancel</Button><Button type="submit" loading={m.isPending} disabled={!name}>Create</Button></div>
      </form>
    </Dialog>
  );
}

export function ProjectDetailPage() {
  const { projectId } = useParams({ from: '/_app/projects/$projectId' });
  const { env } = useSearch({ from: '/_app/projects/$projectId' });
  const nav = useNavigate();
  const q = useProject(projectId);
  const [newRes, setNewRes] = useState(false); const [newEnv, setNewEnv] = useState(false);
  return (
    <QueryBoundary query={q} rows={5}>
      {(d) => {
        const active = d.environments.find((e) => e.environment.name === env) ?? d.environments[0];
        return (
          <>
            <PageHeader title={d.project.name} description={d.project.description || `Project ${d.project.slug}`}
              actions={<PermissionGate perm="resource:write"><Button disabled={!active} onClick={() => setNewRes(true)}><Plus className="size-3.5" />New resource</Button></PermissionGate>} />
            <div className="mb-3 flex items-end justify-between gap-2">
              <Tabs value={active?.environment.name} onValueChange={(v) => void nav({ to: '/projects/$projectId', params: { projectId }, search: { env: v } })} className="min-w-0 flex-1">
                <TabsList>{d.environments.map((e) => <TabsTrigger key={e.environment.id} value={e.environment.name}>{e.environment.name}<Badge className="ml-1.5">{e.resources.length}</Badge></TabsTrigger>)}</TabsList>
              </Tabs>
              <PermissionGate perm="project:write"><Button variant="ghost" size="sm" onClick={() => setNewEnv(true)}><Plus className="size-3" />Environment</Button></PermissionGate>
            </div>
            <Card>
              {!active || active.resources.length === 0 ? (
                <EmptyState title="No resources in this environment" action={<PermissionGate perm="resource:write"><Button onClick={() => setNewRes(true)}><Plus className="size-3.5" />New resource</Button></PermissionGate>}>Add a container to deploy it behind a domain with automatic HTTPS.</EmptyState>
              ) : (
                <Table>
                  <THead><tr><TH>Resource</TH><TH>Status</TH><TH>Type</TH><TH>Domain</TH><TH>Image</TH></tr></THead>
                  <TBody>{active.resources.map((r) => (
                    <TR key={r.id}>
                      <TD><Link to="/resources/$resourceId" params={{ resourceId: r.id }} className="font-semibold text-brand-primary hover:underline">{r.name}</Link></TD>
                      <TD><StatusBadge status={r.status} /></TD>
                      <TD className="text-secondary">{r.type}</TD>
                      <TD>{r.primary_domain ? <span className="inline-flex items-center gap-1 font-mono text-[11px] text-secondary"><Globe aria-hidden className="size-3" />{r.primary_domain}</span> : <span className="text-muted">—</span>}</TD>
                      <TD className="max-w-48 truncate font-mono text-[11px] text-muted">{r.config.image.mode === 'pull' ? r.config.image.ref : 'built on host'}</TD>
                    </TR>))}</TBody>
                </Table>
              )}
            </Card>
            {active && <NewResourceDialog open={newRes} onOpenChange={setNewRes} environmentId={active.environment.id} environmentName={active.environment.name} />}
            <NewEnvDialog projectId={projectId} open={newEnv} onOpenChange={setNewEnv} />
          </>
        );
      }}
    </QueryBoundary>
  );
}
