import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, call } from '../client';
import { qk } from '../keys';
import { syncServerNow } from '../session';

export const useHardeningChecks = () => useQuery({ queryKey: qk.hardening.checks, queryFn: () => call(api.GET('/hardening/checks')) });
export const useHardeningRuns = () => useQuery({ queryKey: qk.hardening.runs, queryFn: () => call(api.GET('/hardening/runs')) });
export function useHardeningChanges(enabled = true) {
  return useQuery({
    queryKey: qk.hardening.changes,
    enabled,
    refetchInterval: 15_000,
    queryFn: async () => {
      const r = await call(api.GET('/hardening/changes'));
      syncServerNow(r.server_now);
      return r;
    },
  });
}
const invalidate = (qc: ReturnType<typeof useQueryClient>) => () => {
  void qc.invalidateQueries({ queryKey: ['hardening'] });
  void qc.invalidateQueries({ queryKey: qk.system.status });
};
export function useRunAudit() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: () => call(api.POST('/hardening/audit')), onSuccess: invalidate(qc) });
}
export function usePlanHardening() {
  return useMutation({ mutationFn: (check_keys: string[]) => call(api.POST('/hardening/plan', { body: { check_keys } })) });
}
export function usePreflight() {
  return useMutation({ mutationFn: (check_key: string) => call(api.POST('/hardening/preflight', { body: { check_key } })) });
}
export function useApplyHardening() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { check_key: string; console_access?: boolean }) =>
      call(api.POST('/hardening/apply', { body: { check_key: v.check_key, ack: v.console_access === undefined ? null : { console_access: v.console_access } } })),
    onSuccess: invalidate(qc),
  });
}
export function useConfirmChange() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (id: string) => call(api.POST('/hardening/confirm/{changeId}', { params: { path: { changeId: id } } })), onSuccess: invalidate(qc) });
}
export function useRevertChange() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (id: string) => call(api.POST('/hardening/revert/{changeId}', { params: { path: { changeId: id } } })), onSuccess: invalidate(qc) });
}
