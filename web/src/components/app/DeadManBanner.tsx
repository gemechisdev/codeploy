import { Timer, ShieldAlert } from 'lucide-react';
import { useEffect } from 'react';
import { useConfirmChange, useHardeningChanges, useRevertChange } from '@/api/hooks/hardening';
import { errorMessage } from '@/api/errors';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { duration } from '@/lib/format';
import { useCountdown } from '@/lib/time';
import { usePermissions } from '@/lib/permissions';

/**
 * Renders the SERVER-owned dead-man deadline (confirm_deadline - server_now). The UI never reverts anything itself:
 * at zero it only says "Reverting…" and refetches, because a systemd timer on the host does the revert (plan 3.10).
 */
export function DeadManBanner() {
  const { can } = usePermissions();
  const q = useHardeningChanges(can('hardening:read'));
  const toast = useToast();
  const confirm = useConfirmChange();
  const revert = useRevertChange();
  const pending = q.data?.items.find((c) => c.state === 'pending_confirm' || c.state === 'reverting');
  const left = useCountdown(pending?.confirm_deadline);
  const expired = pending && (pending.state === 'reverting' || left === 0);
  const refetch = q.refetch;
  useEffect(() => { if (expired) { const t = setInterval(() => void refetch(), 1500); return () => clearInterval(t); } }, [expired, refetch]);
  if (!pending) return null;
  const act = (kind: 'confirm' | 'revert') => (kind === 'confirm' ? confirm : revert).mutate(pending.id, {
    onSuccess: () => toast.success(kind === 'confirm' ? 'Connectivity confirmed. The change is now permanent.' : 'Revert requested.'),
    onError: (e) => toast.error(errorMessage(e)),
  });
  return (
    <div role="alert" className="border-b border-amber-500/40 bg-amber-500/10 px-4 py-2.5 text-xs">
      <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-x-4 gap-y-2">
        <ShieldAlert aria-hidden className="size-4 shrink-0 text-amber-600 dark:text-amber-400" />
        <div className="min-w-0 flex-1 text-primary">
          <p className="font-bold">{expired ? 'Reverting…' : `Confirm connectivity: “${pending.check_name}” is applied but not yet permanent`}</p>
          <p className="text-secondary">{expired ? 'The server is rolling this change back. This page will refresh by itself.' : 'If you lose access, do nothing: the server reverts this change on its own when the timer ends, even if this page is closed.'}</p>
        </div>
        {!expired && (
          <>
            <span className="flex items-center gap-1.5 rounded-md bg-surface px-2 py-1 font-mono text-sm font-bold tabular-nums text-amber-700 dark:text-amber-300" aria-label={`Time remaining ${duration(left ?? 0)}`}><Timer aria-hidden className="size-3.5" />{duration(left ?? 0)}</span>
            <Button size="sm" loading={confirm.isPending} onClick={() => act('confirm')}>Confirm connectivity</Button>
            <Button size="sm" variant="outlineDanger" loading={revert.isPending} onClick={() => act('revert')}>Revert now</Button>
          </>
        )}
      </div>
    </div>
  );
}
