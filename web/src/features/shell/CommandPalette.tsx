import { useNavigate } from '@tanstack/react-router';
import { Box, CornerDownLeft, Search } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type ElementType } from 'react';
import { useModules } from '@/api/hooks/core';
import { useResources } from '@/api/hooks/workloads';
import { Dialog } from '@/components/ui/dialog';
import { usePermissions } from '@/lib/permissions';
import { NAV } from './nav';

interface Cmd { id: string; label: string; hint: string; icon: ElementType; run: () => void }

/** Built from routes + resources, filtered by modules and permissions. No setup-wizard entry (it is not a post-install action). */
export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const nav = useNavigate();
  const { can } = usePermissions();
  const mods = useModules();
  const res = useResources();
  const [q, setQ] = useState('');
  const [i, setI] = useState(0);
  const list = useRef<HTMLUListElement>(null);
  const cmds = useMemo<Cmd[]>(() => {
    const enabled = (k?: string) => !k || mods.data?.items.find((m) => m.key === k)?.enabled !== false;
    const go = (to: string, hint: string, label: string, icon: ElementType): Cmd => ({ id: to, label, hint, icon, run: () => void nav({ to }) });
    const pages = NAV.flatMap((g) => g.items).filter((n) => enabled(n.module) && (!n.perm || can(n.perm))).map((n) => go(n.to, 'Page', n.label, n.icon));
    const resources = (res.data?.items ?? []).map<Cmd>((r) => ({ id: r.id, label: r.name, hint: `${r.environment_name} · ${r.status}`, icon: Box, run: () => void nav({ to: '/resources/$resourceId', params: { resourceId: r.id } }) }));
    return [...pages, ...resources];
  }, [mods.data, res.data, can, nav]);
  const shown = useMemo(() => { const s = q.trim().toLowerCase(); return (s ? cmds.filter((c) => c.label.toLowerCase().includes(s) || c.hint.toLowerCase().includes(s)) : cmds).slice(0, 30); }, [cmds, q]);
  useEffect(() => { setI(0); }, [q, open]);
  useEffect(() => { if (!open) setQ(''); }, [open]);
  useEffect(() => { list.current?.children[i]?.scrollIntoView?.({ block: 'nearest' }); }, [i]);
  const pick = (c?: Cmd) => { if (c) { onOpenChange(false); c.run(); } };
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Command palette" size="md" description="Jump to a page or resource">
      <div className="space-y-2" onKeyDown={(e) => {
        if (e.key === 'ArrowDown') { e.preventDefault(); setI((n) => Math.min(shown.length - 1, n + 1)); }
        if (e.key === 'ArrowUp') { e.preventDefault(); setI((n) => Math.max(0, n - 1)); }
        if (e.key === 'Enter') { e.preventDefault(); pick(shown[i]); }
      }}>
        <div className="relative"><Search aria-hidden className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted" />
          <input autoFocus role="combobox" aria-expanded aria-controls="palette-list" aria-label="Search pages and resources" placeholder="Search pages and resources…" value={q} onChange={(e) => setQ(e.target.value)}
            className="w-full rounded-lg border border-subtle bg-surface-subtle py-2 pl-8 pr-3 text-xs text-primary placeholder:text-muted focus-visible:outline-2 focus-visible:outline-brand-primary" /></div>
        <ul id="palette-list" ref={list} role="listbox" aria-label="Results" className="max-h-72 space-y-0.5 overflow-y-auto">
          {shown.length === 0 && <li className="px-3 py-6 text-center text-xs text-muted">No matches</li>}
          {shown.map((c, n) => (
            <li key={c.id} role="option" aria-selected={n === i} onMouseMove={() => setI(n)} onClick={() => pick(c)}
              className={`flex cursor-pointer items-center gap-2.5 rounded-lg px-3 py-2 text-xs ${n === i ? 'bg-brand-primary text-white' : 'text-primary'}`}>
              <c.icon aria-hidden className="size-4 shrink-0 opacity-70" /><span className="min-w-0 flex-1 truncate font-medium">{c.label}</span>
              <span className={`shrink-0 text-[10px] ${n === i ? 'text-white/80' : 'text-muted'}`}>{c.hint}</span>{n === i && <CornerDownLeft aria-hidden className="size-3" />}
            </li>
          ))}
        </ul>
      </div>
    </Dialog>
  );
}
