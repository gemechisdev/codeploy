import type { QueryClient } from '@tanstack/react-query';
import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet, redirect } from '@tanstack/react-router';
import { meQueryOptions } from '@/api/hooks/auth';
import { setupStateQueryOptions } from '@/api/hooks/setup';
import { ApiError } from '@/api/errors';
import { StepUpDialog } from '@/components/app/StepUpDialog';
import { ErrorState } from '@/components/app/EmptyState';
import { AppShell } from '@/features/shell/AppShell';

export interface RouterContext { qc: QueryClient }

export const RESOURCE_TABS = ['deployments', 'overview', 'source', 'build', 'env', 'domains', 'volumes', 'limits', 'isolation', 'logs', 'shell'] as const;
export type ResourceTab = (typeof RESOURCE_TABS)[number];
export const SETTINGS_TABS = ['users', 'roles', 'tokens', 'git', 'modules', 'audit', 'system'] as const;
export type SettingsTab = (typeof SETTINGS_TABS)[number];
const oneOf = <T extends string>(list: readonly T[], v: unknown): T | undefined => (typeof v === 'string' && (list as readonly string[]).includes(v) ? (v as T) : undefined);
/** Only same-origin absolute paths: `//evil.example` and `https://…` are dropped (open-redirect guard). */
const safeNext = (v: unknown) => (typeof v === 'string' && v.startsWith('/') && !v.startsWith('//') ? v : undefined);

const root = createRootRouteWithContext<RouterContext>()({
  component: () => (<><Outlet /><StepUpDialog /></>),
  errorComponent: ({ error, reset }) => <div className="mx-auto max-w-lg pt-16"><ErrorState error={error} onRetry={reset} title="This page failed to load" /></div>,
});

// ---- pre-auth ----
const login = createRoute({
  getParentRoute: () => root, path: '/login',
  validateSearch: (s: Record<string, unknown>): { next?: string } => ({ next: safeNext(s.next) }),
  beforeLoad: async ({ context: { qc }, search }) => {
    const st = await qc.fetchQuery(setupStateQueryOptions);
    if (st.setup_required) throw redirect({ to: '/setup', search: {} });
    try { await qc.fetchQuery(meQueryOptions); } catch { return; }
    throw redirect({ to: search.next ?? '/' });
  },
  component: lazyRouteComponent(() => import('@/features/auth/LoginPage'), 'LoginPage'),
});
const setup = createRoute({
  getParentRoute: () => root, path: '/setup',
  validateSearch: (s: Record<string, unknown>): { token?: string } => ({ token: typeof s.token === 'string' ? s.token : undefined }),
  beforeLoad: async ({ context: { qc } }) => {
    qc.removeQueries({ queryKey: setupStateQueryOptions.queryKey });
    const st = await qc.fetchQuery({ ...setupStateQueryOptions, staleTime: 0 });
    if (!st.setup_required) throw redirect({ to: '/login', search: {} });
  },
  component: lazyRouteComponent(() => import('@/features/setup/SetupPage'), 'SetupPage'),
});

// ---- authenticated layout ----
const app = createRoute({
  getParentRoute: () => root, id: '_app',
  beforeLoad: async ({ context: { qc }, location }) => {
    const st = await qc.fetchQuery(setupStateQueryOptions);
    if (st.setup_required) throw redirect({ to: '/setup', search: {} });
    try { await qc.ensureQueryData(meQueryOptions); } catch (e) {
      if (e instanceof ApiError && e.status === 401) throw redirect({ to: '/login', search: { next: safeNext(location.href) } });
      throw e;
    }
  },
  component: AppShell,
});
const page = <P extends string>(path: P, load: () => Promise<Record<string, unknown>>, name: string) =>
  createRoute({ getParentRoute: () => app, path, component: lazyRouteComponent(load as never, name) });

const dashboard = page('/', () => import('@/features/dashboard/DashboardPage'), 'DashboardPage');
const projects = page('/projects', () => import('@/features/projects/ProjectsPage'), 'ProjectsPage');
const project = createRoute({
  getParentRoute: () => app, path: '/projects/$projectId',
  validateSearch: (s: Record<string, unknown>): { env?: string } => ({ env: typeof s.env === 'string' ? s.env : undefined }),
  component: lazyRouteComponent(() => import('@/features/projects/ProjectDetailPage'), 'ProjectDetailPage'),
});
const resource = createRoute({
  getParentRoute: () => app, path: '/resources/$resourceId',
  validateSearch: (s: Record<string, unknown>): { tab?: ResourceTab } => ({ tab: oneOf(RESOURCE_TABS, s.tab) }),
  component: lazyRouteComponent(() => import('@/features/resources/ResourcePage'), 'ResourcePage'),
});
const security = page('/server/security', () => import('@/features/security/SecurityPage'), 'SecurityPage');
const terminal = page('/server/terminal', () => import('@/features/terminal/TerminalPage'), 'TerminalPage');
const proxy = page('/server/proxy', () => import('@/features/domains/ProxyPage'), 'ProxyPage');
const monitoring = page('/server/monitoring', () => import('@/features/monitoring/MonitoringPage'), 'MonitoringPage');
const templates = page('/templates', () => import('@/features/templates/TemplatesPage'), 'TemplatesPage');
const extensions = page('/extensions', () => import('@/features/extensions/ExtensionsPage'), 'ExtensionsPage');
const previews = page('/previews', () => import('@/features/previews/PreviewsPage'), 'PreviewsPage');
const notifications = page('/notifications', () => import('@/features/notifications/NotificationsPage'), 'NotificationsPage');
const backups = page('/backups', () => import('@/features/backups/BackupsPage'), 'BackupsPage');
const settings = createRoute({
  getParentRoute: () => app, path: '/settings',
  validateSearch: (s: Record<string, unknown>): { tab?: SettingsTab } => ({ tab: oneOf(SETTINGS_TABS, s.tab) }),
  component: lazyRouteComponent(() => import('@/features/settings/SettingsPage'), 'SettingsPage'),
});

const routeTree = root.addChildren([login, setup, app.addChildren([dashboard, projects, project, resource, security, terminal, proxy, monitoring, templates, extensions, previews, notifications, backups, settings])]);

export function createAppRouter(qc: QueryClient) {
  return createRouter({
    routeTree, context: { qc }, defaultPreload: 'intent', scrollRestoration: true,
    defaultNotFoundComponent: () => <div className="mx-auto max-w-lg pt-16"><ErrorState error={new Error('This page does not exist.')} title="Not found" /></div>,
  });
}
declare module '@tanstack/react-router' {
  interface Register { router: ReturnType<typeof createAppRouter> }
}
