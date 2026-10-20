import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

export const Skeleton = ({ className, ...p }: HTMLAttributes<HTMLDivElement>) => <div aria-hidden className={cn('animate-pulse motion-reduce:animate-none rounded-md bg-surface-muted', className)} {...p} />;
export const SkeletonRows = ({ rows = 4 }: { rows?: number }) => (
  <div role="status" aria-label="Loading" className="space-y-2 p-4">
    {Array.from({ length: rows }, (_, i) => <Skeleton key={i} className="h-9 w-full" />)}
    <span className="sr-only">Loading…</span>
  </div>
);
