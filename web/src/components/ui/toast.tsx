import { AlertCircle, CheckCircle2, X } from 'lucide-react';
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react';
import { useT } from '@/i18n';

interface T { id: number; kind: 'success' | 'error'; text: string }
const Ctx = createContext<{ success: (m: string) => void; error: (m: string) => void }>({ success: () => {}, error: () => {} });

export function ToastProvider({ children }: { children: ReactNode }) {
  const t = useT();
  const [items, setItems] = useState<T[]>([]);
  const n = useRef(0);
  const dismiss = useCallback((id: number) => setItems((l) => l.filter((x) => x.id !== id)), []);
  const push = useCallback((kind: T['kind'], text: string) => {
    const id = ++n.current;
    setItems((l) => [...l.slice(-3), { id, kind, text }]);
    setTimeout(() => dismiss(id), kind === 'error' ? 8000 : 4000);
  }, [dismiss]);
  const api = useMemo(() => ({ success: (m: string) => push('success', m), error: (m: string) => push('error', m) }), [push]);
  return (
    <Ctx.Provider value={api}>
      {children}
      <div className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-[calc(100vw-2rem)] max-w-sm flex-col gap-2" role="region" aria-label={t('toast.notifications')} aria-live="polite">
        {items.map((item) => (
          <div key={item.id} role={item.kind === 'error' ? 'alert' : 'status'} className="pointer-events-auto flex items-start gap-2 rounded-lg border border-subtle bg-surface-elevated p-3 text-xs shadow-lg">
            {item.kind === 'error' ? <AlertCircle aria-hidden className="mt-0.5 size-4 shrink-0 text-red-500" /> : <CheckCircle2 aria-hidden className="mt-0.5 size-4 shrink-0 text-emerald-500" />}
            <span className="min-w-0 flex-1 break-words text-primary">{item.text}</span>
            <button aria-label={t('toast.dismiss')} onClick={() => dismiss(item.id)} className="cursor-pointer rounded text-muted hover:text-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-primary"><X aria-hidden className="size-3.5" /></button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}
export const useToast = () => useContext(Ctx);
