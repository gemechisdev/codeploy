import { forwardRef, useId, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

const base = 'w-full rounded-lg border border-subtle bg-surface-subtle px-3 py-1.5 text-xs text-primary placeholder:text-muted focus-visible:outline-2 focus-visible:outline-brand-primary disabled:opacity-60 aria-[invalid=true]:border-red-500';
export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(({ className, ...p }, ref) => <input ref={ref} className={cn(base, className)} {...p} />);
Input.displayName = 'Input';
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(({ className, ...p }, ref) => <textarea ref={ref} className={cn(base, 'font-mono', className)} {...p} />);
Textarea.displayName = 'Textarea';
export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(({ className, ...p }, ref) => <select ref={ref} className={cn(base, 'pr-7', className)} {...p} />);
Select.displayName = 'Select';

/** Label + control + hint/error wiring (ids, aria-describedby, aria-invalid). */
export function Field({ label, hint, error, children, className }: { label: string; hint?: ReactNode; error?: string; className?: string; children: (a: { id: string; 'aria-describedby'?: string; 'aria-invalid'?: boolean }) => ReactNode }) {
  const id = useId();
  const d = error ? `${id}-e` : hint ? `${id}-h` : undefined;
  return (
    <div className={cn('space-y-1', className)}>
      <label htmlFor={id} className="block text-[11px] font-semibold text-secondary">{label}</label>
      {children({ id, 'aria-describedby': d, 'aria-invalid': error ? true : undefined })}
      {error ? <p id={`${id}-e`} role="alert" className="text-[11px] text-red-600 dark:text-red-400">{error}</p> : hint ? <p id={`${id}-h`} className="text-[11px] text-muted">{hint}</p> : null}
    </div>
  );
}
