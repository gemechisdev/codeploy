import { Bell, Box, DatabaseBackup, LayoutDashboard, LineChart, Settings, ShieldCheck, Terminal, Globe, LayoutTemplate, Puzzle, GitPullRequest } from 'lucide-react';
import type { ElementType } from 'react';
import type { Permission } from '@/api/types';

export interface NavItem { to: string; label: string; icon: ElementType; module?: string; perm?: Permission; end?: boolean }
export interface NavGroup { label: string | null; items: NavItem[] }

/** Static navigation (plan 5.1). Module-owned items hide when GET /modules says disabled; permission-gated items hide for roles without access. */
export const NAV: NavGroup[] = [
  { label: null, items: [{ to: '/', label: 'Dashboard', icon: LayoutDashboard, end: true }, { to: '/projects', label: 'Projects', icon: Box }] },
  {
    label: 'Server', items: [
      { to: '/server/security', label: 'Security', icon: ShieldCheck, module: 'hardening', perm: 'hardening:read' },
      { to: '/server/terminal', label: 'Terminal', icon: Terminal, module: 'terminal', perm: 'terminal:host' },
      { to: '/server/proxy', label: 'Proxy & Domains', icon: Globe, module: 'edge', perm: 'edge:read' },
      { to: '/server/monitoring', label: 'Monitoring', icon: LineChart, module: 'monitor', perm: 'system:metrics' },
    ],
  },
  { label: null, items: [
    { to: '/templates', label: 'Templates', icon: LayoutTemplate, Puzzle, GitPullRequest, module: 'templates', perm: 'template:deploy' },
    { to: '/notifications', label: 'Notifications', icon: Bell, module: 'notify', perm: 'notify:manage' },
    { to: '/backups', label: 'Backups', icon: DatabaseBackup, module: 'databases', perm: 'backup:manage' },
    { to: '/settings', label: 'Settings', icon: Settings },
  ] },
];
