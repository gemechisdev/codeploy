import { AlertCircle, CheckCircle2, X } from 'lucide-react';
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react';

interface T { id: number; kind: 'success' | 'error'; text: string }
const Ctx = createContext<{ success: (m: string) => void; error: (m: string) => void }>({ success: () => {}, error: () => {} });

export function ToastProvider({ children }: { children: ReactNode }) {
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
      <div className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-[calc(100vw-2rem)] max-w-sm flex-col gap-2" role="region" aria-label="Notifications">
        {items.map((t) => (
          <div key={t.id} role={t.kind === 'error' ? 'alert' : 'status'} className="pointer-events-auto flex items-start gap-2 rounded-lg border border-subtle bg-surface-elevated p-3 text-xs shadow-lg">
            {t.kind === 'error' ? <AlertCircle className="mt-0.5 size-4 shrink-0 text-red-500" /> : <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-500" />}
            <span className="min-w-0 flex-1 break-words text-primary">{t.text}</span>
            <button aria-label="Dismiss notification" onClick={() => dismiss(t.id)} className="cursor-pointer text-muted hover:text-primary"><X className="size-3.5" /></button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}
export const useToast = () => useContext(Ctx);
