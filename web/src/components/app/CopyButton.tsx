import { Check, Copy } from 'lucide-react';
import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { copyText } from '@/lib/clipboard';

export function CopyButton({ value, label = 'Copy', iconOnly = false }: { value: string; label?: string; iconOnly?: boolean }) {
  const [done, setDone] = useState(false);
  const toast = useToast();
  return (
    <Button variant="secondary" size={iconOnly ? 'icon' : 'sm'} aria-label={iconOnly ? label : undefined}
      onClick={async () => {
        if (await copyText(value)) { setDone(true); setTimeout(() => setDone(false), 1500); }
        else toast.error('Could not access the clipboard. Select the text and copy it manually.');
      }}>
      {done ? <Check aria-hidden className="size-3.5 text-emerald-500" /> : <Copy aria-hidden className="size-3.5" />}
      {!iconOnly && (done ? 'Copied' : label)}
    </Button>
  );
}
