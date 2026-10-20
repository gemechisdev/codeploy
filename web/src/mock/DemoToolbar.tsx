import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { ChevronDown, ChevronUp, FlaskConical } from 'lucide-react';
import { useState } from 'react';
import { setCsrf } from '@/api/session';
import { qk } from '@/api/keys';

/** Mock-mode only (bottom-left, collapsible). Lives under src/mock so it can never ship in a live build. */
export default function DemoToolbar() {
  const [open, setOpen] = useState(false);
  const qc = useQueryClient();
  const nav = useNavigate();
  const m = () => globalThis.__codeploMock;
  const persona = (email: string) => {
    const h = m(); if (!h) return;
    setCsrf(h.loginAs(email));
    qc.clear();
    void qc.invalidateQueries({ queryKey: qk.me });
    void nav({ to: '/' });
  };
  const btn = 'cursor-pointer rounded-md border border-white/15 bg-white/5 px-2 py-1 text-[10px] font-medium text-zinc-100 hover:bg-white/15';
  return (
    <div className="fixed bottom-3 left-3 z-[65] max-w-[calc(100vw-1.5rem)] rounded-xl border border-white/15 bg-zinc-900/95 text-zinc-100 shadow-2xl backdrop-blur lg:left-64">
      <button className="flex w-full cursor-pointer items-center gap-1.5 px-3 py-1.5 text-[11px] font-semibold" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <FlaskConical aria-hidden className="size-3.5 text-brand-accent" />Demo tools{open ? <ChevronDown className="size-3" /> : <ChevronUp className="size-3" />}
      </button>
      {open && (
        <div className="space-y-2 border-t border-white/10 p-3">
          <div><p className="mb-1 text-[10px] uppercase tracking-wide text-zinc-400">Persona</p><div className="flex flex-wrap gap-1">
            {['owner', 'admin', 'deployer', 'viewer'].map((r) => <button key={r} className={btn} onClick={() => persona(`${r}@example.com`)}>{r}</button>)}</div></div>
          <div><p className="mb-1 text-[10px] uppercase tracking-wide text-zinc-400">Timers</p><div className="flex flex-wrap gap-1">
            <button className={btn} onClick={() => m()?.expirePendingChangesNow()}>Expire dead-man timer</button>
            <button className={btn} onClick={() => m()?.setSpeed(0.25)}>Fast-forward (4×)</button>
            <button className={btn} onClick={() => m()?.setSpeed(1)}>Real pace</button></div></div>
          <div><p className="mb-1 text-[10px] uppercase tracking-wide text-zinc-400">Scenarios</p><div className="flex flex-wrap gap-1">
            <button className={btn} onClick={() => m()?.tamperAudit()}>Tamper audit row</button>
            <button className={btn} onClick={() => m()?.simulateDelivery('success')}>Webhook: success</button>
            <button className={btn} onClick={() => m()?.simulateDelivery('rejected')}>Webhook: rejected</button>
            <button className={btn} onClick={() => m()?.setScenario({ noProvenKeyLogin: true })}>No proven key login</button>
            <button className={btn} onClick={() => m()?.setScenario({ lowDisk: true })}>Low disk</button></div></div>
          <div><p className="mb-1 text-[10px] uppercase tracking-wide text-zinc-400">Fault injection</p><div className="flex flex-wrap gap-1">
            {(['none', '500', '429', 'step_up', 'slow'] as const).map((f) => <button key={f} className={btn} onClick={() => m()?.setFault(f)}>{f}</button>)}</div></div>
        </div>
      )}
    </div>
  );
}
