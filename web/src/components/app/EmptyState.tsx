import type { ElementType, ReactNode } from 'react';
import { AlertTriangle, Inbox } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { ApiError } from '@/api/errors';

export function EmptyState({ icon: Icon = Inbox, title, children, action }: { icon?: ElementType; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 px-6 py-10 text-center">
      <div className="rounded-xl bg-surface-muted p-3 text-muted"><Icon aria-hidden className="size-5" /></div>
      <p className="text-sm font-bold text-primary">{title}</p>
      {children && <p className="max-w-md text-xs text-muted">{children}</p>}
      {action}
    </div>
  );
}

export function ErrorState({ error, onRetry, title = 'Something went wrong' }: { error: unknown; onRetry?: () => void; title?: string }) {
  const e = error instanceof ApiError ? error : null;
  const msg = e ? e.message : error instanceof Error ? error.message : 'Unexpected error';
  const forbidden = e?.code === 'forbidden';
  const disabled = e?.code === 'module_disabled';
  return (
    <div role="alert" className="flex flex-col items-center gap-2 px-6 py-10 text-center">
      <div className="rounded-xl bg-red-500/10 p-3 text-red-500"><AlertTriangle aria-hidden className="size-5" /></div>
      <p className="text-sm font-bold text-primary">{forbidden ? 'You do not have access to this' : disabled ? 'This module is turned off' : title}</p>
      <p className="max-w-md text-xs text-muted">{msg}{e?.retryAfter ? ` Retry in ${e.retryAfter}s.` : ''}</p>
      {onRetry && !forbidden && !disabled && <Button variant="secondary" size="sm" onClick={onRetry}>Try again</Button>}
    </div>
  );
}
