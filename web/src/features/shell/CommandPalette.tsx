import { useNavigate } from '@tanstack/react-router';
import { Box, CornerDownLeft, Search } from 'lucide-react';
import { useEffect, useId, useMemo, useRef, useState, type ElementType } from 'react';
import { useModules } from '@/api/hooks/core';
import { useResources } from '@/api/hooks/workloads';
import { Dialog } from '@/components/ui/dialog';
import { useT } from '@/i18n';
import { usePermissions } from '@/lib/permissions';
import { NAV } from './nav';

interface Cmd { id: string; label: string; hint: string; icon: ElementType; run: () => void }

/** Built from routes + resources, filtered by modules and permissions. No setup-wizard entry (it is not a post-install action). */
export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const nav = useNavigate();
  const t = useT();
  const { can } = usePermissions();
  const mods = useModules();
  const res = useResources();
  const [q, setQ] = useState('');
  const [i, setI] = useState(0);
  const list = useRef<HTMLUListElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const baseId = useId();
  const listId = `${baseId}-list`;
  const optId = (n: number) => `${baseId}-opt-${n}`;
  const cmds = useMemo<Cmd[]>(() => {
    const enabled = (k?: string) => !k || mods.data?.items.find((m) => m.key === k)?.enabled !== false;
    const go = (to: string, hint: string, label: string, icon: ElementType): Cmd => ({ id: to, label, hint, icon, run: () => void nav({ to }) });
    const pages = NAV.flatMap((g) => g.items).filter((n) => enabled(n.module) && (!n.perm || can(n.perm))).map((n) => go(n.to, t('palette.page'), t(n.labelKey), n.icon));
    const resources = (res.data?.items ?? []).map<Cmd>((r) => ({ id: r.id, label: r.name, hint: `${r.environment_name} · ${r.status}`, icon: Box, run: () => void nav({ to: '/resources/$resourceId', params: { resourceId: r.id } }) }));
    return [...pages, ...resources];
  }, [mods.data, res.data, can, nav, t]);
  const shown = useMemo(() => { const s = q.trim().toLowerCase(); return (s ? cmds.filter((c) => c.label.toLowerCase().includes(s) || c.hint.toLowerCase().includes(s)) : cmds).slice(0, 30); }, [cmds, q]);
  const active = shown.length ? Math.min(i, shown.length - 1) : -1;
  useEffect(() => { setI(0); }, [q, open]);
  useEffect(() => { if (!open) setQ(''); }, [open]);
  useEffect(() => { if (active >= 0) list.current?.children[active]?.scrollIntoView?.({ block: 'nearest' }); }, [active]);
  const pick = (c?: Cmd) => { if (c) { onOpenChange(false); c.run(); } };
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={t('palette.title')} size="md" description={t('palette.description')} initialFocus={inputRef}>
      <div className="space-y-2" onKeyDown={(e) => {
        if (e.key === 'ArrowDown') { e.preventDefault(); setI((n) => Math.min(shown.length - 1, n + 1)); }
        if (e.key === 'ArrowUp') { e.preventDefault(); setI((n) => Math.max(0, n - 1)); }
        if (e.key === 'Enter') { e.preventDefault(); pick(shown[active]); }
        if (e.key === 'Escape') { e.preventDefault(); onOpenChange(false); }
      }}>
        <div className="relative"><Search aria-hidden className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted" />
          <input ref={inputRef} role="combobox" aria-expanded aria-controls={listId} aria-haspopup="listbox" aria-autocomplete="list" aria-activedescendant={active >= 0 ? optId(active) : undefined} aria-label={t('palette.searchLabel')} placeholder={t('palette.placeholder')} value={q} onChange={(e) => setQ(e.target.value)}
            className="w-full rounded-lg border border-subtle bg-surface-subtle py-2 pl-8 pr-3 text-xs text-primary placeholder:text-muted focus-visible:outline-2 focus-visible:outline-brand-primary" /></div>
        {shown.length === 0 && <div role="status" aria-live="polite" className="px-3 py-6 text-center text-xs text-muted">{t('palette.noMatches')}</div>}
        <ul id={listId} ref={list} role="listbox" aria-label={t('palette.results')} className="max-h-72 space-y-0.5 overflow-y-auto">
          {shown.map((c, n) => (
            <li key={c.id} id={optId(n)} role="option" aria-selected={n === active} onMouseMove={() => setI(n)} onClick={() => pick(c)}
              className={`flex cursor-pointer items-center gap-2.5 rounded-lg px-3 py-2 text-xs ${n === active ? 'bg-brand-primary text-white' : 'text-primary'}`}>
              <c.icon aria-hidden className="size-4 shrink-0 opacity-70" /><span className="min-w-0 flex-1 truncate font-medium">{c.label}</span>
              <span className={`shrink-0 text-[10px] ${n === active ? 'text-white/80' : 'text-muted'}`}>{c.hint}</span>{n === active && <CornerDownLeft aria-hidden className="size-3" />}
            </li>
          ))}
        </ul>
      </div>
    </Dialog>
  );
}
