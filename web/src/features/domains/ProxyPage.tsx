import { RefreshCw } from 'lucide-react';
import { useAllDomains, useCertificates, useEdgeStatus, useReconcileEdge } from '@/api/hooks/workloads';
import { errorMessage } from '@/api/errors';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { useToast } from '@/components/ui/toast';
import { absTime, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';
import { Link } from '@tanstack/react-router';

export function ProxyPage() {
  const edge = useEdgeStatus(); const domains = useAllDomains(); const certs = useCertificates(); const rec = useReconcileEdge();
  const toast = useToast(); const { can } = usePermissions();
  const certOf = (h: string) => certs.data?.items.find((c) => c.hostname === h);
  return (
    <ModuleGate module="edge">
      <PageHeader title="Proxy & domains" description="Caddy routes every domain to its container and manages HTTPS certificates."
        actions={can('edge:reconcile') && <Button variant="secondary" loading={rec.isPending} onClick={() => rec.mutate(undefined, { onSuccess: () => toast.success('Edge reconciled with the stored routes.'), onError: (e) => toast.error(errorMessage(e)) })}><RefreshCw className="size-3.5" />Reconcile</Button>} />
      <div className="space-y-4">
        <QueryBoundary query={edge} rows={1}>
          {(e) => (
            <Card><CardBody className="grid gap-3 text-xs sm:grid-cols-2 lg:grid-cols-4">
              <div><p className="text-muted">Caddy</p><StatusBadge status={e.caddy_running ? 'running' : 'stopped'} /></div>
              <div><p className="text-muted">Routes</p><p className="font-bold tabular-nums text-primary">{e.route_count}</p></div>
              <div><p className="text-muted">Config</p><p className="font-mono text-primary">{e.config_hash} {e.drift ? <Badge tone="amber">drift</Badge> : <Badge tone="green">in sync</Badge>}</p></div>
              <div><p className="text-muted">Last reconciled</p><p className="text-primary" title={absTime(e.last_reconciled_at)}>{relTime(e.last_reconciled_at)}</p></div>
            </CardBody></Card>)}
        </QueryBoundary>
        <Card><CardHeader title="Domains" description="All hostnames routed by this server." />
          <QueryBoundary query={domains} isEmpty={(d) => d.items.length === 0} empty={<p className="p-6 text-center text-xs text-muted">No domains yet. Add one from a resource's Domains tab.</p>} rows={3}>
            {(d) => (
              <Table><THead><tr><TH>Hostname</TH><TH>Target</TH><TH>Certificate</TH><TH>Expires</TH></tr></THead>
                <TBody>{d.items.map((x) => { const c = certOf(x.hostname); return (
                  <TR key={x.id}>
                    <TD><span className="font-mono text-[11px] font-semibold text-primary">{x.hostname}</span>{x.is_panel && <Badge tone="brand" className="ml-2">panel</Badge>}{(c?.error ?? x.cert_error) && <p role="alert" className="mt-0.5 max-w-sm text-[11px] text-red-600">{c?.error ?? x.cert_error}</p>}</TD>
                    <TD>{x.resource_id ? <Link to="/resources/$resourceId" params={{ resourceId: x.resource_id }} search={{ tab: 'domains' }} className="font-semibold text-brand-primary hover:underline">{x.resource_name}</Link> : <span className="text-muted">control plane</span>}</TD>
                    <TD><StatusBadge status={x.cert_status} /></TD>
                    <TD className="text-secondary" title={absTime(x.cert_expires_at)}>{x.cert_expires_at ? relTime(x.cert_expires_at) : '—'}</TD>
                  </TR>); })}</TBody></Table>)}
          </QueryBoundary></Card>
      </div>
    </ModuleGate>
  );
}
