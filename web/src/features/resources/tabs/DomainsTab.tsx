import { Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useCreateDomain, useDeleteDomain, useResourceDomains } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import type { Resource } from '@/api/types';
import { ConfirmDialog } from '@/components/app/ConfirmDialog';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Button } from '@/components/ui/button';
import { Card, CardHeader } from '@/components/ui/card';
import { Field, Input } from '@/components/ui/input';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { useToast } from '@/components/ui/toast';
import { absTime, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';

export function DomainsTab({ resource: r }: { resource: Resource }) {
  const q = useResourceDomains(r.id); const create = useCreateDomain(r.id); const del = useDeleteDomain(r.id);
  const toast = useToast(); const { can } = usePermissions();
  const [host, setHost] = useState(''); const [rm, setRm] = useState<{ id: string; hostname: string } | null>(null);
  const err = create.error instanceof ApiError ? create.error : null;
  return (
    <Card>
      <CardHeader title="Domains" description="Caddy issues and renews HTTPS certificates automatically once DNS points at this server." />
      <QueryBoundary query={q} isEmpty={(d) => !d?.items?.length} empty={<p className="p-6 text-center text-xs text-muted">No domains yet. Add one below.</p>} rows={2}>
        {(d) => (
          <Table><THead><tr><TH>Hostname</TH><TH>Certificate</TH><TH>Expires</TH><TH><span className="sr-only">Actions</span></TH></tr></THead>
            <TBody>{d.items.map((x) => (
              <TR key={x.id}>
                <TD><span className="font-mono text-[11px] font-semibold text-primary">{x.hostname}</span>{x.cert_error && <p role="alert" className="mt-0.5 max-w-sm text-[11px] text-red-600">{x.cert_error}</p>}</TD>
                <TD><StatusBadge status={x.cert_status} /></TD>
                <TD title={absTime(x.cert_expires_at)} className="text-secondary">{x.cert_expires_at ? relTime(x.cert_expires_at) : '—'}</TD>
                <TD>{can('domain:write') && <Button variant="ghost" size="icon" aria-label={`Remove ${x.hostname}`} onClick={() => setRm({ id: x.id, hostname: x.hostname })}><Trash2 className="size-3.5 text-red-500" /></Button>}</TD>
              </TR>))}</TBody></Table>
        )}
      </QueryBoundary>
      {can('domain:write') && (
        <form className="grid gap-3 border-t border-subtle p-4 sm:grid-cols-[1fr_auto] sm:items-start" noValidate onSubmit={(e) => { e.preventDefault(); create.mutate({ hostname: host }, { onSuccess: () => { toast.success(`${host} added. Issuing a certificate…`); setHost(''); } }); }}>
          <Field label="Add a domain" hint="Create a DNS A/AAAA record pointing at this server first, or issuance will fail." error={err?.fields.hostname ?? (err && err.code !== 'validation_failed' ? err.message : undefined)}>{(a) => <Input {...a} className="font-mono" placeholder="app.example.com" value={host} onChange={(e) => setHost(e.target.value)} />}</Field>
          <Button type="submit" className="sm:mt-5" loading={create.isPending} disabled={!host}>Add domain</Button>
        </form>
      )}
      <ConfirmDialog open={!!rm} onOpenChange={(o) => !o && setRm(null)} destructive title={`Remove ${rm?.hostname}?`} confirmLabel="Remove domain" loading={del.isPending} description="The route is removed from the edge. The certificate is no longer renewed."
        onConfirm={() => rm && del.mutate(rm.id, { onSuccess: () => { toast.success('Domain removed.'); setRm(null); }, onError: (e) => { setRm(null); toast.error(errorMessage(e)); } })} />
    </Card>
  );
}
