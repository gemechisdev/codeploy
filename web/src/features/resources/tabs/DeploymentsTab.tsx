import { Ban, Check, RotateCcw } from 'lucide-react';
import { useState } from 'react';
import { useDeploymentLogs } from '@/api/hooks/logs';
import { useCancelDeployment, useDeployment, useDeployments, useRollback } from '@/api/hooks/workloads';
import { errorMessage } from '@/api/errors';
import { CANCELLABLE, type Deployment, type Resource } from '@/api/types';
import { EmptyState } from '@/components/app/EmptyState';
import { LogViewer } from '@/components/app/LogViewer';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { useToast } from '@/components/ui/toast';
import { absTime, duration, relTime, shortDigest } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { useCountdown, useServerNow } from '@/lib/time';
import { cn } from '@/lib/cn';

const STEPS = ['preparing', 'building', 'starting', 'verifying', 'switching', 'observing', 'succeeded'] as const;

function Timeline({ d }: { d: Deployment }) {
  const steps = STEPS.filter((s) => s !== 'building' || d.image_ref.startsWith('cpl-') || d.step === 'building' || !!d.preflight && d.trigger !== 'rollback' && d.image_ref.startsWith('cpl-'));
  const failed = d.status === 'failed' || d.status === 'cancelled' || d.status === 'interrupted' || d.status === 'rolled_back';
  const idx = steps.indexOf(d.status as (typeof STEPS)[number]);
  const stepIdx = steps.indexOf(d.step as (typeof STEPS)[number]);
  const at = failed ? stepIdx : idx;
  const left = useCountdown(d.status === 'observing' ? d.observe_until : null);
  return (
    <ol className="flex flex-wrap gap-x-1 gap-y-2" aria-label="Deployment progress">
      {steps.map((s, i) => {
        const done = !failed && i < at || d.status === 'succeeded';
        const cur = i === at && !done;
        const bad = failed && i === at;
        return (
          <li key={s} className={cn('flex items-center gap-1.5 rounded-md px-2 py-1 text-[11px] font-semibold capitalize', done && 'text-emerald-600 dark:text-emerald-400', cur && !bad && 'bg-brand-primary/10 text-brand-primary', bad && 'bg-red-500/10 text-red-600', !done && !cur && 'text-muted')} aria-current={cur ? 'step' : undefined}>
            {done ? <Check aria-hidden className="size-3" /> : <span aria-hidden className="size-1.5 rounded-full bg-current" />}{s}{s === 'observing' && left !== null && cur ? ` · ${duration(left)}` : ''}
          </li>
        );
      })}
    </ol>
  );
}

function DeploymentDrawer({ id, resource, onClose }: { id: string; resource: Resource; onClose: () => void }) {
  const q = useDeployment(id);
  const logs = useDeploymentLogs(id);
  const cancel = useCancelDeployment(resource.id);
  const rollback = useRollback(resource.id);
  const toast = useToast();
  const { can } = usePermissions();
  const [rb, setRb] = useState(false); const [ack, setAck] = useState(false);
  const d = q.data;
  const canRollback = !!d && can('deploy:rollback') && d.status === 'succeeded' && d.retained && !!d.image_digest && resource.active_deployment_id !== d.id;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="xl" title={d ? `Deployment ${d.id.slice(-8)}` : 'Deployment'} description={d ? `${d.trigger.replace('_', ' ')} · ${absTime(d.started_at)}` : undefined}
      footer={d && <>
        {can('deploy:cancel') && CANCELLABLE.includes(d.status) && <Button variant="outlineDanger" loading={cancel.isPending} onClick={() => cancel.mutate(d.id, { onSuccess: () => toast.success('Deployment cancelled.'), onError: (e) => toast.error(errorMessage(e)) })}><Ban className="size-3.5" />Cancel deployment</Button>}
        {canRollback && <Button variant="secondary" onClick={() => setRb(true)}><RotateCcw className="size-3.5" />Roll back to this</Button>}
        <Button variant="secondary" onClick={onClose}>Close</Button>
      </>}>
      <QueryBoundary query={q} rows={3}>
        {(x) => (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2"><StatusBadge status={x.status} />{x.retained && <Badge tone="teal">Image retained</Badge>}{resource.active_deployment_id === x.id && <Badge tone="green">Active</Badge>}</div>
            <Timeline d={x} />
            <dl className="grid gap-x-6 gap-y-1.5 text-xs sm:grid-cols-2">
              <div><dt className="inline text-muted">Image </dt><dd className="inline break-all font-mono text-primary">{x.image_ref}</dd></div>
              <div><dt className="inline text-muted">Digest </dt><dd className="inline font-mono text-primary">{shortDigest(x.image_digest)}</dd></div>
              {x.branch && <div><dt className="inline text-muted">Branch </dt><dd className="inline font-mono text-primary">{x.branch}{x.commit_sha ? ` @ ${x.commit_sha.slice(0, 7)}` : ''}</dd></div>}
              {x.commit_message && <div><dt className="inline text-muted">Commit </dt><dd className="inline text-primary">{x.commit_message}</dd></div>}
              {x.preflight && <div className="sm:col-span-2"><dt className="inline text-muted">Pre-flight </dt><dd className="inline text-primary">{x.preflight.detail}</dd></div>}
            </dl>
            {x.error && <p role="alert" className="rounded-lg bg-red-500/10 p-3 text-xs text-red-700 dark:text-red-400"><strong>Failed:</strong> {x.error}</p>}
            {x.rollback_reason && <p role="status" className="rounded-lg bg-amber-500/10 p-3 text-xs text-amber-800 dark:text-amber-300"><strong>Automatically rolled back:</strong> {x.rollback_reason}. The previous version is serving traffic.</p>}
            <LogViewer rows={logs.rows} live={logs.live} height={300} emptyText={logs.ended ? 'No log output was recorded.' : 'Waiting for output…'} />
          </div>
        )}
      </QueryBoundary>
      <Dialog open={rb} onOpenChange={setRb} size="sm" title="Roll back to this deployment?" description="A new deployment starts from the retained image. There is no rebuild."
        footer={<><Button variant="secondary" onClick={() => setRb(false)}>Cancel</Button><Button disabled={!ack} loading={rollback.isPending} onClick={() => d && rollback.mutate(d.id, { onSuccess: () => { setRb(false); toast.success('Rollback started.'); onClose(); }, onError: (e) => toast.error(errorMessage(e)) })}>Roll back</Button></>}>
        <label className="flex cursor-pointer items-start gap-2.5 text-xs text-primary"><input type="checkbox" className="mt-0.5 size-4 accent-brand-primary" checked={ack} onChange={(e) => setAck(e.target.checked)} /><span><strong>Rolling back the application does not roll back database changes.</strong> If a migration ran, you may need to restore a backup separately.</span></label>
      </Dialog>
    </Dialog>
  );
}

export function DeploymentsTab({ resource }: { resource: Resource }) {
  const q = useDeployments(resource.id);
  const [open, setOpen] = useState<string | null>(null);
  const now = useServerNow(5000);
  return (
    <>
      <Card>
        <QueryBoundary query={q} isEmpty={(d) => d.items.length === 0} empty={<EmptyState title="No deployments yet">Press Deploy to build or pull the configured image and start this resource.</EmptyState>} rows={4}>
          {(d) => (
            <Table>
              <THead><tr><TH>Status</TH><TH>Trigger</TH><TH>Image</TH><TH>Digest</TH><TH>Started</TH><TH>Took</TH><TH><span className="sr-only">Open</span></TH></tr></THead>
              <TBody>{d.items.map((x) => (
                <TR key={x.id}>
                  <TD><div className="flex items-center gap-1.5"><StatusBadge status={x.status} />{resource.active_deployment_id === x.id && <Badge tone="green">Active</Badge>}</div></TD>
                  <TD className="text-secondary">{x.trigger.replace('_', ' ')}</TD>
                  <TD className="max-w-52 truncate font-mono text-[11px] text-secondary" title={x.image_ref}>{x.image_ref}</TD>
                  <TD className="font-mono text-[11px] text-muted">{shortDigest(x.image_digest)}</TD>
                  <TD title={absTime(x.started_at)} className="whitespace-nowrap text-secondary">{relTime(x.started_at, now)}</TD>
                  <TD className="tabular-nums text-secondary">{x.finished_at ? duration((Date.parse(x.finished_at) - Date.parse(x.started_at)) / 1000) : '…'}</TD>
                  <TD><Button size="sm" variant="secondary" onClick={() => setOpen(x.id)}>Details</Button></TD>
                </TR>))}</TBody>
            </Table>
          )}
        </QueryBoundary>
      </Card>
      {open && <DeploymentDrawer id={open} resource={resource} onClose={() => setOpen(null)} />}
    </>
  );
}
