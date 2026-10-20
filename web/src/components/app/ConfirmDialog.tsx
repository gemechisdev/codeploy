import type { ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';

export function ConfirmDialog({ open, onOpenChange, title, description, children, confirmLabel = 'Confirm', destructive, loading, disabled, onConfirm }: {
  open: boolean; onOpenChange: (o: boolean) => void; title: string; description?: ReactNode; children?: ReactNode; confirmLabel?: string; destructive?: boolean; loading?: boolean; disabled?: boolean; onConfirm: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={title} description={description} size="sm"
      footer={<>
        <Button variant="secondary" onClick={() => onOpenChange(false)}>Cancel</Button>
        <Button variant={destructive ? 'danger' : 'primary'} loading={loading} disabled={disabled} onClick={onConfirm}>{confirmLabel}</Button>
      </>}>
      {children}
    </Dialog>
  );
}
