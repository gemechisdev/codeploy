import * as RD from '@radix-ui/react-dialog';
import { X } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

/** Radix Dialog: focus trap, ESC to close, aria-modal and focus return come for free. */
export function Dialog({ open, onOpenChange, title, description, children, footer, size = 'md', dismissible = true }: {
  open: boolean; onOpenChange: (o: boolean) => void; title: string; description?: ReactNode; children?: ReactNode; footer?: ReactNode; size?: 'sm' | 'md' | 'lg' | 'xl'; dismissible?: boolean;
}) {
  return (
    <RD.Root open={open} onOpenChange={(o) => (dismissible || o) && onOpenChange(o)}>
      <RD.Portal>
        <RD.Overlay className="fixed inset-0 z-50 bg-black/55 backdrop-blur-[2px]" />
        <RD.Content
          onInteractOutside={(e) => !dismissible && e.preventDefault()}
          onEscapeKeyDown={(e) => !dismissible && e.preventDefault()}
          className={cn('fixed left-1/2 top-1/2 z-50 flex max-h-[90vh] w-[calc(100vw-1.5rem)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-xl border border-subtle bg-surface-elevated shadow-2xl focus:outline-none',
            { sm: 'max-w-sm', md: 'max-w-lg', lg: 'max-w-2xl', xl: 'max-w-4xl' }[size])}
        >
          <div className="flex items-start justify-between gap-3 border-b border-subtle px-4 py-3">
            <div className="min-w-0">
              <RD.Title className="text-sm font-bold text-primary">{title}</RD.Title>
              {description ? <RD.Description className="mt-0.5 text-xs text-muted">{description}</RD.Description> : <RD.Description className="sr-only">{title}</RD.Description>}
            </div>
            {dismissible && <RD.Close aria-label="Close dialog" className="cursor-pointer rounded-lg p-1 text-secondary hover:bg-surface-muted hover:text-primary"><X className="size-4" /></RD.Close>}
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-4">{children}</div>
          {footer && <div className="flex flex-wrap justify-end gap-2 border-t border-subtle px-4 py-3">{footer}</div>}
        </RD.Content>
      </RD.Portal>
    </RD.Root>
  );
}
