import type { HTMLAttributes, ReactNode } from 'react';
import { cn } from '@/lib/cn';

export const Card = ({ className, ...p }: HTMLAttributes<HTMLDivElement>) => <div className={cn('rounded-xl border border-subtle bg-surface shadow-xs', className)} {...p} />;
export function CardHeader({ title, description, actions, className }: { title: ReactNode; description?: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex items-start justify-between gap-3 border-b border-subtle px-4 py-3', className)}>
      <div className="min-w-0">
        <h3 className="text-sm font-bold text-primary leading-tight">{title}</h3>
        {description && <p className="mt-0.5 text-xs text-muted">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  );
}
export const CardBody = ({ className, ...p }: HTMLAttributes<HTMLDivElement>) => <div className={cn('p-4', className)} {...p} />;
