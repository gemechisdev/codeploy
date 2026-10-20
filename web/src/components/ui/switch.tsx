import * as RS from '@radix-ui/react-switch';
import { cn } from '@/lib/cn';

export function Switch({ className, ...p }: RS.SwitchProps) {
  return (
    <RS.Root className={cn('relative h-5 w-9 shrink-0 cursor-pointer rounded-full bg-surface-muted border border-strong transition-colors data-[state=checked]:bg-brand-primary data-[state=checked]:border-brand-primary disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline-2 focus-visible:outline-brand-primary', className)} {...p}>
      <RS.Thumb className="block size-3.5 translate-x-0.5 rounded-full bg-white shadow transition-transform data-[state=checked]:translate-x-[18px] motion-reduce:transition-none" />
    </RS.Root>
  );
}
