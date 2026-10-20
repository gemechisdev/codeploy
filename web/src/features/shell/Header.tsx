import { Link, useRouterState } from '@tanstack/react-router';
import { ChevronRight, LogOut, Menu, Moon, Search, ShieldCheck, Sun, User } from 'lucide-react';
import { useState } from 'react';
import { useLogout, useMe } from '@/api/hooks/auth';
import { useProject, useResource } from '@/api/hooks/workloads';
import { Button } from '@/components/ui/button';
import { useTheme } from '@/lib/theme';
import { useServerNow } from '@/lib/time';

function Crumbs() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const rid = /^\/resources\/([^/]+)/.exec(path)?.[1] ?? '';
  const pid = /^\/projects\/([^/]+)/.exec(path)?.[1] ?? '';
  const res = useResource(rid);
  const proj = useProject(pid || res.data?.project_id || '');
  const Sep = () => <ChevronRight aria-hidden className="size-3 text-muted" />;
  const link = 'text-secondary hover:text-primary';
  if (rid) return <>
    <Link to="/projects" className={link}>Projects</Link><Sep />
    {res.data && <><Link to="/projects/$projectId" params={{ projectId: res.data.project_id }} className={link}>{proj.data?.project.name ?? '…'}</Link><Sep /><span className="text-secondary">{res.data.environment_name}</span><Sep /></>}
    <span className="font-semibold text-primary">{res.data?.name ?? '…'}</span></>;
  if (pid) return <><Link to="/projects" className={link}>Projects</Link><Sep /><span className="font-semibold text-primary">{proj.data?.project.name ?? '…'}</span></>;
  const seg = path.split('/').filter(Boolean);
  const label = seg.length ? seg.map((s) => s[0].toUpperCase() + s.slice(1)).join(' / ') : 'Dashboard';
  return <span className="font-semibold text-primary">{label}</span>;
}

export function Header({ onMenu, onPalette }: { onMenu: () => void; onPalette: () => void }) {
  const { theme, toggle } = useTheme();
  const me = useMe();
  const logout = useLogout();
  const now = useServerNow();
  const [open, setOpen] = useState(false);
  const su = me.data?.step_up_until ? Date.parse(me.data.step_up_until) : 0;
  return (
    <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b border-subtle bg-surface/90 px-3 backdrop-blur sm:px-4">
      <Button variant="ghost" size="icon" className="lg:hidden" aria-label="Open navigation" onClick={onMenu}><Menu className="size-4" /></Button>
      <nav aria-label="Breadcrumb" className="flex min-w-0 flex-1 items-center gap-1.5 truncate text-xs"><Crumbs /></nav>
      {su > now && <span title="Elevated by a recent 2FA check" className="hidden items-center gap-1 rounded-md bg-brand-accent/15 px-2 py-1 text-[10px] font-semibold text-brand-accent-dark dark:text-brand-accent sm:flex"><ShieldCheck aria-hidden className="size-3" />Elevated · {Math.ceil((su - now) / 60000)}m</span>}
      <Button variant="secondary" size="sm" onClick={onPalette} aria-label="Open command palette (Ctrl+K)"><Search className="size-3.5" /><span className="hidden sm:inline">Search</span><kbd className="hidden rounded bg-surface-muted px-1 font-mono text-[10px] text-muted sm:inline">Ctrl K</kbd></Button>
      <Button variant="ghost" size="icon" aria-label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'} onClick={toggle}>{theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}</Button>
      <div className="relative">
        <Button variant="ghost" size="icon" aria-label="User menu" aria-expanded={open} aria-haspopup="menu" onClick={() => setOpen((o) => !o)}><User className="size-4" /></Button>
        {open && (
          <div role="menu" className="absolute right-0 top-full mt-1 w-56 rounded-lg border border-subtle bg-surface-elevated p-1 shadow-lg" onMouseLeave={() => setOpen(false)}>
            <div className="border-b border-subtle px-3 py-2"><p className="truncate text-xs font-semibold text-primary">{me.data?.user.email}</p><p className="text-[10px] uppercase tracking-wide text-muted">{me.data?.role}</p></div>
            <button role="menuitem" className="flex w-full cursor-pointer items-center gap-2 rounded-md px-3 py-2 text-xs text-primary hover:bg-surface-muted" onClick={() => logout.mutate(undefined, { onSettled: () => { window.location.assign('/login'); } })}><LogOut className="size-3.5" />Sign out</button>
          </div>
        )}
      </div>
    </header>
  );
}
