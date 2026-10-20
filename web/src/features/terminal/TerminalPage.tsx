import { Link } from '@tanstack/react-router';
import { ShieldAlert, Terminal } from 'lucide-react';
import { useState } from 'react';
import { useSettings, useSystemStatus } from '@/api/hooks/core';
import { useTerminalSessions } from '@/api/hooks/workloads';
import { EmptyState } from '@/components/app/EmptyState';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { Badge } from '@/components/ui/badge';
import { Card, CardHeader } from '@/components/ui/card';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { absTime, duration, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';
import { TerminalPane } from './TerminalPane';

export function TerminalPage() {
  const status = useSystemStatus();
  const settings = useSettings();
  const sessions = useTerminalSessions();
  const { can } = usePermissions();
  const [root, setRoot] = useState(false);
  const rootAllowed = settings.data?.values['terminal.allow_root'] === true && can('terminal:root');
  const operator = status.data?.operator_user;
  return (
    <ModuleGate module="terminal">
      <PageHeader title="Host terminal" description="A real shell on this server, as a named non-root operator. Every session is audited." />
      {status.data && !status.data.operator_configured ? (
        <Card><EmptyState icon={ShieldAlert} title="Operator identity required" action={<Link to="/server/security" className="text-xs font-semibold text-brand-primary hover:underline">Open Security</Link>}>The host terminal never starts as root by default, and no named operator user is configured yet. Apply the “Operator identity” check first.</EmptyState></Card>
      ) : (
        <div className="space-y-4">
          {rootAllowed && (
            <label className="flex cursor-pointer items-start gap-2.5 rounded-xl border border-red-500/30 bg-red-500/5 p-3 text-xs text-primary">
              <input type="checkbox" className="mt-0.5 size-4 accent-red-600" checked={root} onChange={(e) => setRoot(e.target.checked)} />
              <span><strong>Open as root.</strong> Needs a fresh 2FA check and is flagged in the audit log. Prefer the operator account and use sudo.</span>
            </label>
          )}
          <TerminalPane key={String(root)} target="host" asRoot={root} label={root ? 'root@host' : `${operator ?? 'operator'}@host`} />
        </div>
      )}
      <Card className="mt-6">
        <CardHeader title="Session history" description="Who opened a shell, on what, and for how long." />
        <QueryBoundary query={sessions} isEmpty={(s) => s.items.length === 0} empty={<EmptyState icon={Terminal} title="No sessions yet" />} rows={3}>
          {(s) => (
            <Table><THead><tr><TH>Who</TH><TH>Target</TH><TH>As</TH><TH>Started</TH><TH>Duration</TH></tr></THead>
              <TBody>{s.items.map((x) => (
                <TR key={x.id}>
                  <TD className="font-semibold text-primary">{x.user_email}</TD><TD>{x.target === 'host' ? 'Host' : x.target_name}</TD>
                  <TD>{x.is_root ? <Badge tone="red">root</Badge> : <span className="font-mono text-[11px] text-secondary">{x.as_user}</span>}</TD>
                  <TD title={absTime(x.started_at)}>{relTime(x.started_at)}</TD>
                  <TD className="tabular-nums">{x.ended_at ? duration((Date.parse(x.ended_at) - Date.parse(x.started_at)) / 1000) : <Badge tone="green">live</Badge>}</TD>
                </TR>))}</TBody></Table>
          )}
        </QueryBoundary>
      </Card>
    </ModuleGate>
  );
}
