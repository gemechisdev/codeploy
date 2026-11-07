import type { ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { useT } from '@/i18n';

export function ConfirmDialog({ open, onOpenChange, title, description, children, confirmLabel, destructive, loading, disabled, onConfirm }: {
  open: boolean; onOpenChange: (o: boolean) => void; title: string; description?: ReactNode; children?: ReactNode; confirmLabel?: string; destructive?: boolean; loading?: boolean; disabled?: boolean; onConfirm: () => void;
}) {
  const t = useT();
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={title} description={description} size="sm"
      footer={<>
        <Button variant="secondary" onClick={() => onOpenChange(false)}>{t('confirm.cancel')}</Button>
        <Button variant={destructive ? 'danger' : 'primary'} loading={loading} disabled={disabled} onClick={onConfirm}>{confirmLabel ?? t('confirm.confirm')}</Button>
      </>}>
      {children}
    </Dialog>
  );
}
