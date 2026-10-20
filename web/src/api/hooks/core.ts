import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, call } from '../client';
import { qk } from '../keys';
import type { Schema } from '../types';

const live = { refetchOnWindowFocus: false } as const;

// users, roles, tokens
export const useUsers = () => useQuery({ queryKey: qk.users, queryFn: () => call(api.GET('/users')) });
export const useRoles = () => useQuery({ queryKey: qk.roles, queryFn: () => call(api.GET('/roles')) });
export const usePermissionRegistry = () => useQuery({ queryKey: qk.permissions, queryFn: () => call(api.GET('/permissions')) });
export const useTokens = () => useQuery({ queryKey: qk.tokens, queryFn: () => call(api.GET('/tokens')) });

export function useCreateUser() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (body: Schema<'CreateUserRequest'>) => call(api.POST('/users', { body })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.users }) });
}
export function usePatchUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; body: Schema<'PatchUserRequest'> }) => call(api.PATCH('/users/{id}', { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.users }),
  });
}
export function useCreateToken() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (body: Schema<'CreateTokenRequest'>) => call(api.POST('/tokens', { body })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.tokens }) });
}
export function useRevokeToken() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (id: string) => call(api.DELETE('/tokens/{id}', { params: { path: { id } } })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.tokens }) });
}

// audit
export interface AuditFilter { actor?: string; action?: string; from?: string; to?: string }
export function useAudit(f: AuditFilter) {
  return useInfiniteQuery({
    queryKey: qk.audit(f),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: Schema<'AuditPage'>) => last.next_cursor ?? undefined,
    queryFn: ({ pageParam }): Promise<Schema<'AuditPage'>> => call(api.GET('/audit', { params: { query: { ...f, limit: 25, cursor: pageParam } } })),
  });
}
export function useVerifyAudit() {
  return useMutation({ mutationFn: () => call(api.POST('/audit/verify')) });
}

// system
export const useSystemStatus = () => useQuery({ queryKey: qk.system.status, queryFn: () => call(api.GET('/system/status')), refetchInterval: 30_000, ...live });
export const useSystemVersion = () => useQuery({ queryKey: qk.system.version, queryFn: () => call(api.GET('/system/version')) });
export const useMetrics = (range: string, enabled = true) =>
  useQuery({ queryKey: qk.system.metrics(range), enabled, queryFn: () => call(api.GET('/system/metrics', { params: { query: { range } } })) });
export function useCleanup() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: () => call(api.POST('/system/cleanup')), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.system.status }) });
}

// modules, flags, settings
export const useModules = () => useQuery({ queryKey: qk.modules, queryFn: () => call(api.GET('/modules')), staleTime: 60_000 });
export const useFlags = () => useQuery({ queryKey: qk.flags, queryFn: () => call(api.GET('/feature-flags')), staleTime: 60_000 });
export const useSettings = () => useQuery({ queryKey: qk.settings, queryFn: () => call(api.GET('/settings')), staleTime: 60_000 });
export function usePatchModule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { key: string; enabled: boolean }) => call(api.PATCH('/modules/{key}', { params: { path: { key: v.key } }, body: { enabled: v.enabled } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.modules }),
  });
}
export function usePatchFlags() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (flags: Record<string, boolean>) => call(api.PATCH('/feature-flags', { body: { flags } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.flags });
      void qc.invalidateQueries({ queryKey: qk.hardening.checks });
    },
  });
}
export function usePatchSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (values: Record<string, unknown>) => call(api.PATCH('/settings', { body: { values } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.settings }),
  });
}

/** Latest `metrics.sample` pushed over SSE (patched into the cache by events.ts). Undefined until the first sample. */
export function useLiveMetrics() {
  return useQuery<Schema<'HostMetrics'> | undefined>({ queryKey: ['system', 'live-metrics'], enabled: false, staleTime: Infinity, queryFn: () => undefined });
}
