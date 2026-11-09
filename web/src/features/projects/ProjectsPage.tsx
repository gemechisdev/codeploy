import { Link } from '@tanstack/react-router';
import { Box, Plus } from 'lucide-react';
import { useMemo, useState, type FormEvent } from 'react';
import { useCreateProject, useProjects, useResources } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import { EmptyState } from '@/components/app/EmptyState';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { Button } from '@/components/ui/button';
import { Card, CardBody } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input } from '@/components/ui/input';
import { useToast } from '@/components/ui/toast';
import { PermissionGate } from '@/lib/permissions';
import { PageHeader } from '@/features/shell/ModuleGate';
import { StatusBadge } from '@/components/app/StatusBadge';

const slugify = (s: string) => s.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);

function NewProjectDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const create = useCreateProject();
  const toast = useToast();
  const [name, setName] = useState(''); const [slug, setSlug] = useState(''); const [touched, setTouched] = useState(false); const [description, setDescription] = useState('');
  const fields = create.error instanceof ApiError ? create.error.fields : {};
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate({ name, slug, description }, { onSuccess: () => { toast.success(`Project "${name}" created.`); onOpenChange(false); setName(''); setSlug(''); setDescription(''); setTouched(false); } });
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="New project" description="Projects group environments and the resources inside them.">
      <form onSubmit={submit} className="space-y-3" noValidate>
        <Field label="Name" error={fields.name}>{(a) => <Input {...a} autoFocus value={name} onChange={(e) => { setName(e.target.value); if (!touched) setSlug(slugify(e.target.value)); }} />}</Field>
        <Field label="Slug" hint="Lowercase letters, digits and “-”." error={fields.slug ?? (create.error instanceof ApiError && create.error.code === 'conflict' ? create.error.message : undefined)}>{(a) => <Input {...a} className="font-mono" value={slug} onChange={(e) => { setTouched(true); setSlug(e.target.value); }} />}</Field>
        <Field label="Description (optional)">{(a) => <Input {...a} value={description} onChange={(e) => setDescription(e.target.value)} />}</Field>
        {create.isError && !(create.error instanceof ApiError && (create.error.code === 'validation_failed' || create.error.code === 'conflict')) && <p role="alert" className="text-xs text-red-600">{errorMessage(create.error)}</p>}
        <div className="flex justify-end gap-2"><Button variant="secondary" onClick={() => onOpenChange(false)}>Cancel</Button><Button type="submit" loading={create.isPending} disabled={!name || !slug}>Create project</Button></div>
      </form>
    </Dialog>
  );
}

export function ProjectsPage() {
  const q = useProjects();
  const res = useResources();
  const [open, setOpen] = useState(false);
  const byProject = useMemo(() => {
    const m = new Map<string, NonNullable<typeof res.data>['items']>();
    for (const r of res.data?.items ?? []) m.set(r.project_id, [...(m.get(r.project_id) ?? []), r]);
    return m;
  }, [res.data]);
  const add = <PermissionGate perm="project:write"><Button onClick={() => setOpen(true)}><Plus className="size-3.5" />New project</Button></PermissionGate>;
  return (
    <>
      <PageHeader title="Projects" description="Everything you deploy, grouped by project and environment." actions={add} />
      <QueryBoundary query={q} isEmpty={(d) => !d?.items?.length} empty={<Card><EmptyState icon={Box} title="No projects yet" action={add}>Create a project, open it, add an environment, then a resource (Git + Dockerfile, registry image, static site, or Compose). Databases: use Templates (Postgres, Redis, …).</EmptyState></Card>}>
        {(d) => (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {d.items.map((p) => {
              const rs = byProject.get(p.id) ?? [];
              const running = rs.filter((r) => r.status === 'running').length;
              return (
                <Link key={p.id} to="/projects/$projectId" params={{ projectId: p.id }} className="group block rounded-xl focus-visible:outline-2 focus-visible:outline-brand-primary">
                  <Card className="h-full transition-colors group-hover:border-brand-primary/50"><CardBody className="space-y-2">
                    <div className="flex items-start justify-between gap-2"><h2 className="truncate text-sm font-bold text-primary">{p.name}</h2><span className="shrink-0 font-mono text-[10px] text-muted">{p.slug}</span></div>
                    <p className="line-clamp-2 min-h-8 text-xs text-muted">{p.description || 'No description'}</p>
                    <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-secondary">
                      <span className="tabular-nums">{rs.length} resource{rs.length === 1 ? '' : 's'}</span>
                      {rs.length > 0 && <StatusBadge status={running === rs.length ? 'running' : running ? 'degraded' : 'stopped'} label={`${running} running`} />}
                    </div>
                  </CardBody></Card>
                </Link>
              );
            })}
          </div>
        )}
      </QueryBoundary>
      <NewProjectDialog open={open} onOpenChange={setOpen} />
    </>
  );
}
