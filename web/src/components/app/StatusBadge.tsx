import { AlertTriangle, Ban, CheckCircle2, Eye, Loader2, RotateCcw, XCircle, CircleDot, ShieldAlert, Clock } from 'lucide-react';
import type { ElementType } from 'react';
import { Badge, type Tone } from '@/components/ui/badge';
import type { DeploymentStatus } from '@/api/types';
import { IN_PROGRESS } from '@/api/types';
import { cn } from '@/lib/cn';

interface Look { tone: Tone; icon: ElementType; label: string; spin?: boolean }

/** Deployment states (plan 3.8). Colour is never the only signal: every state has an icon and a text label. */
export const DEPLOYMENT_LOOK: Record<DeploymentStatus, Look> = {
  queued: { tone: 'brand', icon: Loader2, label: 'Queued', spin: true },
  preparing: { tone: 'brand', icon: Loader2, label: 'Preparing', spin: true },
  building: { tone: 'brand', icon: Loader2, label: 'Building', spin: true },
  starting: { tone: 'brand', icon: Loader2, label: 'Starting', spin: true },
  verifying: { tone: 'brand', icon: Loader2, label: 'Verifying', spin: true },
  switching: { tone: 'brand', icon: Loader2, label: 'Switching', spin: true },
  observing: { tone: 'teal', icon: Eye, label: 'Observing' },
  succeeded: { tone: 'green', icon: CheckCircle2, label: 'Succeeded' },
  failed: { tone: 'red', icon: XCircle, label: 'Failed' },
  cancelled: { tone: 'grey', icon: Ban, label: 'Cancelled' },
  interrupted: { tone: 'amber', icon: AlertTriangle, label: 'Interrupted' },
  rolled_back: { tone: 'amber', icon: RotateCcw, label: 'Rolled back' },
};

const OTHER: Record<string, Look> = {
  running: { tone: 'green', icon: CheckCircle2, label: 'Running' },
  deploying: { tone: 'brand', icon: Loader2, label: 'Deploying', spin: true },
  stopped: { tone: 'grey', icon: Ban, label: 'Stopped' },
  degraded: { tone: 'amber', icon: AlertTriangle, label: 'Degraded' },
  pass: { tone: 'green', icon: CheckCircle2, label: 'Pass' },
  warn: { tone: 'amber', icon: AlertTriangle, label: 'Warning' },
  fail: { tone: 'red', icon: XCircle, label: 'Fail' },
  skipped: { tone: 'grey', icon: CircleDot, label: 'Skipped' },
  issuing: { tone: 'brand', icon: Loader2, label: 'Issuing', spin: true },
  issued: { tone: 'green', icon: CheckCircle2, label: 'Issued' },
  error: { tone: 'red', icon: XCircle, label: 'Error' },
  pending_confirm: { tone: 'amber', icon: Clock, label: 'Awaiting confirm' },
  confirmed: { tone: 'green', icon: ShieldAlert, label: 'Confirmed' },
  reverting: { tone: 'amber', icon: Loader2, label: 'Reverting', spin: true },
  reverted: { tone: 'amber', icon: RotateCcw, label: 'Reverted' },
  applied: { tone: 'green', icon: CheckCircle2, label: 'Applied' },
  success: { tone: 'green', icon: CheckCircle2, label: 'Success' },
  ignored: { tone: 'grey', icon: Ban, label: 'Ignored' },
  rejected: { tone: 'red', icon: XCircle, label: 'Rejected' },
};

export function StatusBadge({ status, className, label }: { status: string; className?: string; label?: string }) {
  const look = (DEPLOYMENT_LOOK as Record<string, Look>)[status] ?? OTHER[status] ?? { tone: 'neutral' as Tone, icon: CircleDot, label: status };
  const Icon = look.icon;
  return (
    <Badge tone={look.tone} className={className} data-status={status}>
      <Icon aria-hidden className={cn('size-3', look.spin && 'animate-spin motion-reduce:animate-none')} />
      {label ?? look.label}
    </Badge>
  );
}
export const isInProgress = (s: DeploymentStatus) => IN_PROGRESS.includes(s);
