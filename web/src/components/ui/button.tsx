import { cva, type VariantProps } from 'class-variance-authority';
import { forwardRef, type ButtonHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

export const buttonVariants = cva(
  'inline-flex items-center justify-center gap-1.5 rounded-lg text-xs font-semibold transition-colors cursor-pointer select-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-primary disabled:opacity-50 disabled:cursor-not-allowed',
  {
    variants: {
      variant: {
        primary: 'bg-brand-primary text-white hover:bg-brand-primary-hover shadow-xs',
        secondary: 'border border-subtle bg-surface-subtle text-primary hover:bg-surface-muted font-medium',
        ghost: 'text-secondary hover:text-primary hover:bg-surface-muted font-medium',
        danger: 'bg-red-600 text-white hover:bg-red-700 shadow-xs',
        outlineDanger: 'border border-red-500/40 text-red-600 dark:text-red-400 hover:bg-red-500/10 font-medium',
      },
      size: { sm: 'px-2.5 py-1', md: 'px-3 py-1.5', lg: 'px-4 py-2 text-sm', icon: 'p-1.5' },
    },
    defaultVariants: { variant: 'primary', size: 'md' },
  },
);

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {
  loading?: boolean;
}
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(({ className, variant, size, loading, disabled, children, type = 'button', ...p }, ref) => (
  <button ref={ref} type={type} disabled={disabled || loading} aria-busy={loading || undefined} className={cn(buttonVariants({ variant, size }), className)} {...p}>
    {loading && <span aria-hidden className="size-3 rounded-full border-2 border-current border-t-transparent animate-spin motion-reduce:animate-none" />}
    {children}
  </button>
));
Button.displayName = 'Button';
