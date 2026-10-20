import { useState } from 'react';
import { Play, RotateCcw } from 'lucide-react';
import { useBackupRuns, useBackupSchedules, useRestoreBackup, useRunBackup } from '@/api/hooks/ops';
import { errorMessage } from '@/api/errors';
import { ConfirmDialog } from '@/components/app/ConfirmDialog';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Button } from '@/components/ui/button';
import { Card, CardHeader } from '@/components/ui/card';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { useToast } from '@/components/ui/toast';
import { absTime, bytes, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';

export function BackupsPage() {
  const sched = useBackupSchedules(); const runs = useBackupRuns(); const run = useRunBackup(); const restore = useRestoreBackup();
  const toast = useToast(); const { can } = usePermissions(); const [restoreId, setRestoreId] = useState<string | null>(null);
  return (
    <ModuleGate module="databases">
      <PageHeader title="Backups" description="Scheduled database backups. Restoring overwrites current data and needs step-up verification." />
      <div className="space-y-4">
        <Card><CardHeader title="Schedules" />
          <QueryBoundary query={sched} isEmpty={(d) => d.items.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No backup schedules. Add one from a database resource.</p>} rows={3}>
            {(d) => (
              <Table><THead><tr><TH>Resource</TH><TH>Cron</TH><TH>Destination</TH><TH>Retention</TH><TH>Last run</TH><TH><span className="sr-only">Actions</span></TH></tr></THead>
                <TBody>{d.items.map((s) => (
                  <TR key={s.id}><TD className="font-semibold text-primary">{s.resource_name}</TD><TD className="font-mono text-[11px]">{s.cron}</TD><TD>{s.destination}</TD><TD>{s.retention_days} d</TD>
                    <TD>{s.last_run_status ? <StatusBadge status={s.last_run_status} /> : '—'} <span className="text-muted" title={absTime(s.last_run_at)}>{relTime(s.last_run_at)}</span></TD>
                    <TD>{can('backup:manage') && <Button size="sm" variant="secondary" loading={run.isPending && run.variables === s.id} onClick={() => run.mutate(s.id, { onSuccess: () => toast.success('Backup started.'), onError: (e) => toast.error(errorMessage(e)) })}><Play className="size-3" />Run now</Button>}</TD></TR>))}</TBody></Table>)}
          </QueryBoundary></Card>
        <Card><CardHeader title="Recent runs" />
          <QueryBoundary query={runs} isEmpty={(d) => d.items.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No backup runs yet.</p>} rows={3}>
            {(d) => (
              <Table><THead><tr><TH>Resource</TH><TH>Status</TH><TH>Size</TH><TH>Started</TH><TH>Location</TH><TH><span className="sr-only">Actions</span></TH></tr></THead>
                <TBody>{d.items.map((r) => (
                  <TR key={r.id}><TD className="font-semibold text-primary">{r.resource_name}</TD>
                    <TD><StatusBadge status={r.status} />{r.error && <p role="alert" className="mt-0.5 text-[11px] text-red-600">{r.error}</p>}</TD>
                    <TD className="tabular-nums">{bytes(r.size_bytes)}</TD><TD title={absTime(r.started_at)}>{relTime(r.started_at)}</TD><TD className="font-mono text-[11px] text-muted">{r.location}</TD>
                    <TD>{can('backup:restore') && r.status === 'success' && <Button size="sm" variant="secondary" onClick={() => setRestoreId(r.id)}><RotateCcw className="size-3" />Restore</Button>}</TD></TR>))}</TBody></Table>)}
          </QueryBoundary></Card>
      </div>
      <ConfirmDialog open={!!restoreId} onOpenChange={(o) => !o && setRestoreId(null)} destructive title="Restore this backup?" confirmLabel="Restore" loading={restore.isPending}
        description="Current data in the database will be replaced. You will be asked for a fresh authenticator code."
        onConfirm={() => restoreId && restore.mutate(restoreId, { onSuccess: () => { toast.success('Restore started.'); setRestoreId(null); }, onError: (e) => toast.error(errorMessage(e)) })} />
    </ModuleGate>
  );
}
