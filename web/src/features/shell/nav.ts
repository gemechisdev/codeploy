import { Bell, Box, DatabaseBackup, LayoutDashboard, LineChart, Settings, ShieldCheck, Terminal, Globe, LayoutTemplate, Puzzle, GitPullRequest } from 'lucide-react';
import type { ElementType } from 'react';
import type { Permission } from '@/api/types';
import type { TranslationKey } from '@/i18n';

export interface NavItem { to: string; labelKey: TranslationKey; icon: ElementType; module?: string; perm?: Permission; end?: boolean }
export interface NavGroup { labelKey: TranslationKey | null; items: NavItem[] }

/** Static navigation (plan 5.1). Module-owned items hide when GET /modules says disabled; permission-gated items hide for roles without access. */
export const NAV: NavGroup[] = [
  { labelKey: null, items: [{ to: '/', labelKey: 'nav.dashboard', icon: LayoutDashboard, end: true }, { to: '/projects', labelKey: 'nav.projects', icon: Box }] },
  {
    labelKey: 'nav.group.server', items: [
      { to: '/server/security', labelKey: 'nav.security', icon: ShieldCheck, module: 'hardening', perm: 'hardening:read' },
      { to: '/server/terminal', labelKey: 'nav.terminal', icon: Terminal, module: 'terminal', perm: 'terminal:host' },
      { to: '/server/proxy', labelKey: 'nav.proxy', icon: Globe, module: 'edge', perm: 'edge:read' },
      { to: '/server/monitoring', labelKey: 'nav.monitoring', icon: LineChart, module: 'monitor', perm: 'system:metrics' },
    ],
  },
  { labelKey: null, items: [
    { to: '/templates', labelKey: 'nav.templates', icon: LayoutTemplate, module: 'templates', perm: 'template:deploy' },
    { to: '/extensions', labelKey: 'nav.extensions', icon: Puzzle },
    { to: '/previews', labelKey: 'nav.previews', icon: GitPullRequest },
    { to: '/notifications', labelKey: 'nav.notifications', icon: Bell, module: 'notify', perm: 'notify:manage' },
    { to: '/backups', labelKey: 'nav.backups', icon: DatabaseBackup, module: 'databases', perm: 'backup:manage' },
    { to: '/settings', labelKey: 'nav.settings', icon: Settings },
  ] },
];
