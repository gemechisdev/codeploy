import { useState } from 'react';
import { Layers } from 'lucide-react';
import { useProjects } from '@/api/hooks/workloads';
import { useDeployTemplate, useTemplates } from '@/api/hooks/ops';
import { errorMessage } from '@/api/errors';
import type { Schema } from '@/api/types';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input, Select } from '@/components/ui/input';
import { useToast } from '@/components/ui/toast';
import { usePermissions } from '@/lib/permissions';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';

const SLUG = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/;

function DeployDialog({ tpl, onClose }: { tpl: Schema<'Template'>; onClose: () => void }) {
  const projects = useProjects(); const deploy = useDeployTemplate(); const toast = useToast();
  const [projectId, setProjectId] = useState(''); const [environment, setEnvironment] = useState('production'); const [name, setName] = useState(tpl.key);
  const nameErr = name && !SLUG.test(name) ? 'Lowercase letters, digits and hyphens only (max 40).' : undefined;
  const pid = projectId || projects.data?.items[0]?.id || '';
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`Deploy ${tpl.name}`} description={`Creates a ${tpl.kind} resource on port ${tpl.default_port}; recommended memory ${tpl.recommended_memory_mb} MB.`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button loading={deploy.isPending} disabled={!pid || !name || !!nameErr || !environment}
          onClick={() => deploy.mutate({ key: tpl.key, body: { project_id: pid, environment, name } }, { onSuccess: () => { toast.success(`${tpl.name} is being deployed.`); onClose(); }, onError: (e) => toast.error(errorMessage(e)) })}>Deploy</Button></>}>
      <div className="space-y-3">
        <Field label="Project">{(a) => <Select {...a} value={pid} onChange={(e) => setProjectId(e.target.value)}>{projects.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</Select>}</Field>
        <Field label="Environment">{(a) => <Input {...a} value={environment} onChange={(e) => setEnvironment(e.target.value)} />}</Field>
        <Field label="Resource name" error={nameErr}>{(a) => <Input {...a} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      </div>
    </Dialog>
  );
}

export function TemplatesPage() {
  const q = useTemplates(); const { can } = usePermissions(); const [sel, setSel] = useState<Schema<'Template'> | null>(null);
  return (
    <ModuleGate module="templates">
      <PageHeader title="Templates" description="One-click starting points. Each template becomes a normal resource you can edit." />
      <QueryBoundary query={q} isEmpty={(d) => d.items.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No templates available.</p>}>
        {(d) => (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {d.items.map((t) => (
              <Card key={t.key}><CardBody className="flex h-full flex-col gap-2">
                <div className="flex items-center justify-between gap-2"><span className="flex items-center gap-2 text-sm font-bold text-primary"><Layers className="size-4 text-brand-primary" />{t.name}</span><Badge tone="brand">{t.category}</Badge></div>
                <p className="flex-1 text-xs text-secondary">{t.description}</p>
                <div className="flex items-center justify-between text-[11px] text-muted"><span>{t.kind} · :{t.default_port} · {t.recommended_memory_mb} MB</span>
                  <Button size="sm" disabled={!can('template:deploy')} title={can('template:deploy') ? undefined : 'Requires template:deploy'} onClick={() => setSel(t)}>Deploy</Button></div>
              </CardBody></Card>))}
          </div>)}
      </QueryBoundary>
      {sel && <DeployDialog tpl={sel} onClose={() => setSel(null)} />}
    </ModuleGate>
  );
}
