import { ListChecks, ShieldAlert, Wrench } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useApplyHardening, usePlanHardening, usePreflight } from '@/api/hooks/hardening';
import { ApiError, errorMessage } from '@/api/errors';
import type { HardeningCheck } from '@/api/types';
import type { components } from '@/api/schema';
import { CopyButton } from '@/components/app/CopyButton';
import { DiffView } from '@/components/app/DiffView';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Skeleton } from '@/components/ui/skeleton';
import { useToast } from '@/components/ui/toast';
import { usePermissions } from '@/lib/permissions';

type PreflightItem = components['schemas']['PreflightItem'];
export const TIER_LABEL: Record<number, { name: string; blurb: string }> = {
  0: { name: 'Audit', blurb: 'Read-only detection. Nothing here changes the host.' },
  1: { name: 'Safe fixes', blurb: 'Cannot cut your access. Plan, review the diff, then apply.' },
  2: { name: 'Reversible firewall', blurb: 'Can cut inbound access. Guarded by preflight, a console-access acknowledgement and a server-side dead-man timer.' },
  3: { name: 'Guarded SSH', blurb: 'Can cut SSH access. Same guards as the firewall; plan-only until released.' },
};

function PreflightList({ items }: { items: PreflightItem[] }) {
  return (
    <ul className="space-y-1.5" aria-label="Preflight results">
      {items.map((i) => (
        <li key={i.key} className="flex items-start gap-2 rounded-lg border border-subtle bg-surface-subtle p-2.5 text-xs">
          <StatusBadge status={i.ok ? 'pass' : 'fail'} />
          <div className="min-w-0"><p className="font-semibold text-primary">{i.label}</p><p className="text-muted">{i.detail}</p></div>
        </li>
      ))}
    </ul>
  );
}

/** Plan → diff → (preflight → console-access ack for Tier 2/3) → apply. Step-up is automatic via the API client. */
export function PlanDialog({ check, open, onOpenChange }: { check: HardeningCheck; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { can } = usePermissions();
  const toast = useToast();
  const plan = usePlanHardening();
  const pre = usePreflight();
  const apply = useApplyHardening();
  const [ack, setAck] = useState(false); // unchecked by default, required, sent in the apply request
  const guarded = check.tier >= 2;
  const { mutate: planMutate, reset: planReset } = plan;
  useEffect(() => {
    if (open) planMutate([check.key]);
    else { planReset(); pre.reset(); apply.reset(); setAck(false); }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, check.key]);

  const item = plan.data?.items[0];
  const apiErr = apply.error instanceof ApiError ? apply.error : null;
  const preflightItems: PreflightItem[] | null = pre.data?.items ?? (apiErr?.code === 'preflight_failed' ? apiErr.checks : null);
  const preflightOk = guarded ? !!pre.data?.ok : true;
  const planOnly = !check.apply_enabled;
  const mayApply = can('hardening:apply') && check.tier >= 1;
  const canApply = !planOnly && mayApply && preflightOk && (!check.requires_ack || ack) && !apply.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="lg" title={`Plan: ${check.name}`} description={`Tier ${check.tier} · ${TIER_LABEL[check.tier].name}`}
      footer={<>
        <Button variant="secondary" onClick={() => onOpenChange(false)}>Close</Button>
        {guarded && !planOnly && mayApply && <Button variant="secondary" loading={pre.isPending} onClick={() => pre.mutate(check.key)}><ListChecks className="size-3.5" />Run preflight</Button>}
        {!planOnly && mayApply && <Button disabled={!canApply} loading={apply.isPending}
          onClick={() => apply.mutate({ check_key: check.key, console_access: check.requires_ack ? ack : undefined }, {
            onSuccess: (c) => { toast.success(c.state === 'pending_confirm' ? 'Applied. Confirm connectivity before the timer ends.' : `${check.name}: applied.`); onOpenChange(false); },
            onError: (e) => { if (!(e instanceof ApiError && e.code === 'preflight_failed')) toast.error(errorMessage(e)); },
          })}><Wrench className="size-3.5" />Apply{guarded ? ' with dead-man timer' : ''}</Button>}
      </>}>
      <div className="space-y-4">
        <p className="text-xs text-secondary">{check.detect_detail}</p>
        {plan.isPending && <Skeleton className="h-32 w-full" />}
        {plan.isError && <p role="alert" className="text-xs text-red-600">{errorMessage(plan.error)}</p>}
        {item?.plan_diff ? <DiffView diff={item.plan_diff} /> : item && <p className="text-xs text-muted">This check has no file diff; the commands below show what would run.</p>}
        {item && item.commands.length > 0 && (
          <div>
            <div className="mb-1 flex items-center justify-between"><h4 className="text-[11px] font-semibold uppercase tracking-wide text-muted">Command set</h4><CopyButton value={item.commands.join('\n')} label="Copy commands" /></div>
            <pre className="overflow-x-auto rounded-lg bg-code-bg p-3 font-mono text-[11px] text-zinc-300">{item.commands.join('\n')}</pre>
          </div>
        )}
        {planOnly && check.tier >= 2 && (
          <div role="note" className="flex gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-primary">
            <ShieldAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-amber-600" />
            <div><p className="font-bold">Plan-only</p><p className="text-secondary">Applying this check is switched off{check.apply_flag ? <> (feature flag <code className="font-mono">{check.apply_flag}</code>)</> : ''} until its VM fault-injection matrix passes. Review the exact diff above, or run the command set yourself with provider console access ready.</p></div>
          </div>
        )}
        {guarded && !planOnly && (
          <>
            <div role="note" className="rounded-lg border border-subtle bg-surface-subtle p-3 text-xs text-secondary">
              After applying, you have a limited time to press <strong className="text-primary">Confirm connectivity</strong>. <strong className="text-primary">If you lose connectivity, the server reverts this change by itself</strong>, even if this tab is closed.
            </div>
            {preflightItems ? <PreflightList items={preflightItems} /> : <p className="text-xs text-muted">Run preflight to verify the safety preconditions. Apply stays disabled until every item passes.</p>}
          </>
        )}
        {check.requires_ack && !planOnly && mayApply && (
          <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border border-subtle bg-surface-subtle p-3 text-xs text-primary">
            <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} className="mt-0.5 size-4 accent-brand-primary" />
            <span>I have access to my provider's web console or rescue mode, so I can recover this server if SSH or inbound access is lost.</span>
          </label>
        )}
        {apiErr && apiErr.code !== 'preflight_failed' && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{apiErr.fields.ack ?? apiErr.message}</p>}
      </div>
    </Dialog>
  );
}

export function CheckCard({ check }: { check: HardeningCheck }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-xl border border-subtle bg-surface p-3.5">
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2"><h4 className="text-xs font-bold text-primary">{check.name}</h4><StatusBadge status={check.status} />{check.tier >= 2 && !check.apply_enabled && <Badge tone="amber">Plan-only</Badge>}</div>
        <p className="text-[11px] text-muted">{check.description}</p>
        <p className="break-words font-mono text-[11px] text-secondary">{check.detect_detail}</p>
      </div>
      {check.tier >= 1 && check.plan_diff && <Button variant="secondary" size="sm" onClick={() => setOpen(true)}>{check.apply_enabled ? 'Plan & apply' : 'View plan'}</Button>}
      {check.tier >= 1 && <PlanDialog check={check} open={open} onOpenChange={setOpen} />}
    </div>
  );
}
