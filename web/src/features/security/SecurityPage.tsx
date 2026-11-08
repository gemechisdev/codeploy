import { RefreshCw, Terminal } from 'lucide-react';
import { Link } from '@tanstack/react-router';
import { useHardeningChanges, useHardeningChecks, useRunAudit } from '@/api/hooks/hardening';
import { errorMessage } from '@/api/errors';
import type { HardeningCheck } from '@/api/types';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { ScoreRing } from '@/components/app/ScoreRing';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table';
import { useToast } from '@/components/ui/toast';
import { absTime, relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';
import { CheckCard, TIER_LABEL } from './CheckCard';

function TierSummary({ tier, checks }: { tier: number; checks: HardeningCheck[] }) {
  const scored = checks.filter((c) => c.status !== 'skipped');
  const passing = scored.filter((c) => c.status === 'pass').length;
  return (
    <Card className="p-3.5">
      <div className="flex items-center justify-between"><span className="text-[10px] font-semibold uppercase tracking-wider text-muted">Tier {tier}</span><Badge tone={passing === scored.length ? 'green' : 'amber'}>{passing}/{scored.length}</Badge></div>
      <p className="mt-1 text-sm font-bold text-primary">{TIER_LABEL[tier].name}</p>
      <p className="mt-0.5 text-[11px] text-muted">{TIER_LABEL[tier].blurb}</p>
    </Card>
  );
}

export function SecurityPage() {
  const q = useHardeningChecks();
  const changes = useHardeningChanges();
  const audit = useRunAudit();
  const toast = useToast();
  const { can } = usePermissions();
  return (
    <ModuleGate module="hardening">
      <PageHeader title="Host security" description="Audit this server, then apply fixes in risk tiers. Tiers 2 and 3 can lock you out, so they are guarded."
        actions={can('hardening:read') && <Button variant="secondary" loading={audit.isPending} onClick={() => audit.mutate(undefined, { onSuccess: () => toast.success('Audit complete.'), onError: (e) => toast.error(errorMessage(e)) })}><RefreshCw className="size-3.5" />Run audit</Button>} />
      <QueryBoundary query={q} rows={6}>
        {(d) => {
          const operator = d.items.find((c) => c.key === 'acct.operator');
          return (
            <div className="space-y-5">
              <Card><CardBody className="flex flex-wrap items-center gap-5">
                <ScoreRing score={d.score} />
                <div className="min-w-0 flex-1 space-y-1">
                  <p className="text-sm font-bold text-primary">Security score</p>
                  <p className="text-xs text-muted">Calculated by the server from the checks below. Last audit {d.last_run_at ? `${relTime(d.last_run_at)} (${absTime(d.last_run_at)})` : 'never'}.</p>
                </div>
              </CardBody></Card>
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{[0, 1, 2, 3].map((t) => <TierSummary key={t} tier={t} checks={d.items.filter((c) => c.tier === t)} />)}</div>
              {operator && operator.status !== 'pass' && (
                <div role="note" className="flex flex-wrap items-center gap-3 rounded-xl border border-amber-500/40 bg-amber-500/10 p-3.5 text-xs">
                  <Terminal aria-hidden className="size-4 shrink-0 text-amber-600" />
                  <p className="min-w-0 flex-1 text-primary"><strong>No operator identity yet.</strong> The host terminal stays unavailable until a named, non-root sudo user is configured. Apply "Operator identity" below.</p>
                </div>
              )}
              {[0, 1, 2, 3].map((t) => {
                const items = d.items.filter((c) => c.tier === t);
                return items.length === 0 ? null : (
                  <section key={t} aria-labelledby={`tier-${t}`} className="space-y-2">
                    <div><h2 id={`tier-${t}`} className="text-sm font-bold text-primary">Tier {t}: {TIER_LABEL[t].name}</h2><p className="text-xs text-muted">{TIER_LABEL[t].blurb}</p></div>
                    <div className="space-y-2">{items.map((c) => <CheckCard key={c.key} check={c} />)}</div>
                  </section>
                );
              })}
              <Card>
                <CardHeader title="Change history" description="Every applied change, with its server-owned confirmation deadline." />
                <QueryBoundary query={changes} isEmpty={(c) => !c?.items?.length} empty={<p className="p-6 text-center text-xs text-muted">No hardening changes have been applied yet.</p>} rows={2}>
                  {(c) => (
                    <Table><THead><tr><TH>Check</TH><TH>Tier</TH><TH>State</TH><TH>Applied</TH><TH>Revert unit</TH></tr></THead>
                      <TBody>{c.items.map((x) => <TR key={x.id}><TD className="font-semibold text-primary">{x.check_name}</TD><TD>{x.tier}</TD><TD><StatusBadge status={x.state} /></TD><TD title={absTime(x.applied_at)}>{relTime(x.applied_at)}</TD><TD className="font-mono text-[10px] text-muted">{x.revert_unit ?? '—'}</TD></TR>)}</TBody></Table>
                  )}
                </QueryBoundary>
              </Card>
              <p className="text-[11px] text-muted">SSH port changes are never offered: they are too easy to get wrong remotely. Need to recover? See <Link to="/settings" search={{ tab: 'system' }} className="font-semibold text-brand-primary hover:underline">Settings → System</Link>.</p>
            </div>
          );
        }}
      </QueryBoundary>
    </ModuleGate>
  );
}
