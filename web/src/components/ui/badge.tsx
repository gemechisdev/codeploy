import { cva, type VariantProps } from 'class-variance-authority';
import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

export const badgeVariants = cva('inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide whitespace-nowrap', {
  variants: {
    tone: {
      neutral: 'bg-surface-muted text-secondary',
      brand: 'bg-brand-primary/10 text-brand-primary',
      teal: 'bg-brand-accent/15 text-brand-accent-dark dark:text-brand-accent',
      green: 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
      amber: 'bg-amber-500/15 text-amber-700 dark:text-amber-400',
      red: 'bg-red-500/10 text-red-700 dark:text-red-400',
      grey: 'bg-zinc-500/10 text-zinc-600 dark:text-zinc-400',
    },
  },
  defaultVariants: { tone: 'neutral' },
});
export type Tone = NonNullable<VariantProps<typeof badgeVariants>['tone']>;
export function Badge({ className, tone, ...p }: HTMLAttributes<HTMLSpanElement> & VariantProps<typeof badgeVariants>) {
  return <span className={cn(badgeVariants({ tone }), className)} {...p} />;
}
