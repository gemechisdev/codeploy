import { Link } from '@tanstack/react-router';
import { X } from 'lucide-react';
import { useModules, useSystemStatus } from '@/api/hooks/core';
import { useT } from '@/i18n';
import { cn } from '@/lib/cn';
import { usePermissions } from '@/lib/permissions';
import { NAV } from './nav';

export function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const t = useT();
  const mods = useModules();
  const { can } = usePermissions();
  const status = useSystemStatus();
  const enabled = (k?: string) => !k || !mods.data || mods.data.items.find((m) => m.key === k)?.enabled !== false;
  return (
    <nav aria-label={t('shell.mainNav')} className="flex h-full select-none flex-col bg-surface">
      <div className="flex h-14 shrink-0 items-center justify-between border-b border-subtle px-4">
        <Link to="/" onClick={onNavigate} className="flex items-center gap-2.5 rounded-lg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-primary">
          <span className="flex size-8 items-center justify-center rounded-lg bg-gradient-to-br from-brand-primary to-brand-secondary font-mono text-xs font-bold text-white">c/p</span>
          <span><span className="block text-sm font-bold leading-tight tracking-tight text-primary">codeplo</span><span className="block text-[10px] font-medium text-muted">{status.data?.hostname ?? t('shell.controlPlane')}</span></span>
        </Link>
        {onNavigate && <button aria-label={t('shell.closeNavigation')} onClick={onNavigate} className="cursor-pointer rounded-lg p-1.5 text-secondary hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-primary lg:hidden"><X aria-hidden className="size-4" /></button>}
      </div>
      <div className="flex-1 space-y-5 overflow-y-auto px-3 py-4">
        {NAV.map((g, i) => {
          const items = g.items.filter((n) => enabled(n.module) && (!n.perm || can(n.perm)));
          if (!items.length) return null;
          return (
            <div key={i} className="space-y-0.5">
              {g.labelKey && <div className="px-2.5 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted">{t(g.labelKey)}</div>}
              {items.map((n) => (
                <Link key={n.to} to={n.to} activeOptions={{ exact: !!n.end }} activeProps={{ 'aria-current': 'page' }} onClick={onNavigate}
                  className="group flex items-center justify-between rounded-lg px-2.5 py-2 text-xs font-medium text-secondary transition-colors hover:bg-surface-muted hover:text-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-primary data-[status=active]:bg-brand-primary data-[status=active]:text-white">
                  <span className="flex min-w-0 items-center gap-2.5"><n.icon aria-hidden className="size-4 shrink-0 text-muted group-data-[status=active]:text-white" /><span className="truncate">{t(n.labelKey)}</span></span>
                  {n.to === '/server/security' && status.data && <span className="rounded bg-surface-muted px-1.5 py-0.5 font-mono text-[10px] text-secondary group-data-[status=active]:bg-white/20 group-data-[status=active]:text-white" aria-label={`${t('shell.securityScore')} ${status.data.security_score}`}>{status.data.security_score}</span>}
                </Link>
              ))}
            </div>
          );
        })}
      </div>
      {status.data && (
        <div className="shrink-0 border-t border-subtle bg-surface-subtle/50 p-3 font-mono text-[10px] text-muted">
          <div className="flex justify-between"><span>RAM</span><span className={cn('tabular-nums', status.data.metrics.memory_used_mb / status.data.metrics.memory_total_mb > 0.85 && 'text-amber-500')}>{status.data.metrics.memory_used_mb} / {status.data.metrics.memory_total_mb} MB</span></div>
          <div className="flex justify-between"><span>daemon</span><span className="tabular-nums">{status.data.metrics.daemon_rss_mb} MB</span></div>
        </div>
      )}
    </nav>
  );
}
