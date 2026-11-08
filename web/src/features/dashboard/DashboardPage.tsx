import { Link } from '@tanstack/react-router';
import { Activity, AlertTriangle, Box, ShieldCheck } from 'lucide-react';
import { useLiveMetrics, useSystemStatus, useAudit } from '@/api/hooks/core';
import { useResources } from '@/api/hooks/workloads';
import { Gauge } from '@/components/app/Gauge';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { ScoreRing } from '@/components/app/ScoreRing';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { duration, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { PageHeader } from '@/features/shell/ModuleGate';

export function DashboardPage() {
  const status = useSystemStatus(); const live = useLiveMetrics(); const res = useResources(); const { can } = usePermissions();
  const audit = useAudit({}); const recent = audit.data?.pages[0]?.items.slice(0, 6) ?? [];
  const m = live.data ?? status.data?.metrics;
  return (
    <>
      <PageHeader title="Dashboard" description="Live view of this server and everything running on it." />
      <QueryBoundary query={status} rows={4}>
        {(s) => (
          <div className="space-y-4">
            <div className="grid gap-4 lg:grid-cols-3">
              <Card className="lg:col-span-2"><CardHeader title={s.hostname} description={`${s.os} · ${s.arch} · ${s.cpus} vCPU · ${Math.round(s.ram_total_mb / 1024 * 10) / 10} GB RAM · up ${duration(s.uptime_s)}`} actions={<Badge tone={s.support_tier === 'experimental' || s.support_tier === 'limited' ? 'amber' : 'green'}>{s.support_tier}</Badge>} />
                <CardBody>
                  {m && <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
                    <Gauge label="CPU" value={m.cpu_percent} />
                    <Gauge label="Memory" value={(m.memory_used_mb / m.memory_total_mb) * 100} detail={`${m.memory_used_mb} / ${m.memory_total_mb} MB`} warn={75} crit={90} />
                    <Gauge label="Disk" value={(m.disk_used_gb / m.disk_total_gb) * 100} detail={`${m.disk_used_gb} / ${m.disk_total_gb} GB`} warn={75} crit={90} />
                    <Gauge label="Daemon RSS" value={m.daemon_rss_mb} max={64} unit=" MB" detail="Budget 64 MB" warn={70} crit={90} />
                  </div>}
                  {(s.support_tier === 'experimental' || s.support_tier === 'limited') && <p role="note" className="mt-4 rounded-lg bg-amber-500/10 p-2.5 text-xs text-amber-800 dark:text-amber-300">This server has little RAM. Prefer pulling prebuilt images over building on the host.</p>}
                </CardBody></Card>
              <Card><CardBody className="flex items-center gap-4">
                <ScoreRing score={s.security_score} size={84} />
                <div className="min-w-0 space-y-1.5"><p className="flex items-center gap-1.5 text-sm font-bold text-primary"><ShieldCheck aria-hidden className="size-4" />Security</p>
                  {s.pending_changes > 0 && <Badge tone="amber">{s.pending_changes} awaiting confirmation</Badge>}
                  {can('hardening:read') && <Link to="/server/security" className="block text-xs font-semibold text-brand-primary hover:underline">Review checks</Link>}</div>
              </CardBody></Card>
            </div>
            <div className="grid gap-4 lg:grid-cols-2">
              <Card><CardHeader title="Resources" description={`${m?.container_count ?? 0} containers running`} actions={<Link to="/projects" className="text-xs font-semibold text-brand-primary hover:underline">All projects</Link>} />
                <QueryBoundary query={res} isEmpty={(d) => !d?.items?.length} empty={<p className="p-6 text-center text-xs text-muted">Nothing deployed yet. Create a project to get started.</p>} rows={3}>
                  {(d) => <ul className="divide-y divide-subtle">{d.items.slice(0, 8).map((r) => <li key={r.id} className="flex items-center justify-between gap-3 px-4 py-2.5"><Link to="/resources/$resourceId" params={{ resourceId: r.id }} className="flex min-w-0 items-center gap-2 text-xs font-semibold text-primary hover:text-brand-primary"><Box aria-hidden className="size-3.5 shrink-0 text-muted" /><span className="truncate">{r.name}</span><span className="shrink-0 font-normal text-muted">{r.environment_name}</span></Link><StatusBadge status={r.status} /></li>)}</ul>}
                </QueryBoundary></Card>
              <Card><CardHeader title="Recent activity" actions={can('audit:read') && <Link to="/settings" search={{ tab: 'audit' }} className="text-xs font-semibold text-brand-primary hover:underline">Audit log</Link>} />
                {!can('audit:read') ? <p className="p-6 text-center text-xs text-muted">The audit log is visible to admins and owners.</p> : (
                  <QueryBoundary query={audit} isEmpty={() => recent.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No activity yet.</p>} rows={3}>
                    {() => <ul className="divide-y divide-subtle">{recent.map((e) => <li key={e.id} className="flex items-start gap-2.5 px-4 py-2.5 text-xs"><Activity aria-hidden className="mt-0.5 size-3.5 shrink-0 text-muted" /><div className="min-w-0 flex-1"><p className="truncate font-mono text-[11px] font-semibold text-primary">{e.action}</p><p className="truncate text-muted">{e.actor_email ?? e.actor_type}</p></div><span className="shrink-0 text-[11px] text-muted">{relTime(e.at)}</span></li>)}</ul>}
                  </QueryBoundary>)}
              </Card>
            </div>
            {(s.active_alerts ?? 0) > 0 && <p role="status" className="flex items-center gap-2 rounded-xl border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-primary"><AlertTriangle aria-hidden className="size-4 text-amber-600" />{s.active_alerts} alert{s.active_alerts === 1 ? '' : 's'} fired recently.</p>}
          </div>
        )}
      </QueryBoundary>
    </>
  );
}
