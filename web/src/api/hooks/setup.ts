import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, call } from '../client';
import { qk } from '../keys';
import type { Schema } from '../types';

export const setupStateQueryOptions = { queryKey: qk.setupState, queryFn: () => call(api.GET('/setup/state')), retry: false, staleTime: 15_000 };
export function useSetupState() {
  return useQuery(setupStateQueryOptions);
}
export function useOperatorCandidates(enabled = true) {
  return useQuery({ queryKey: qk.operatorCandidates, enabled, queryFn: () => call(api.GET('/setup/operator-candidates')) });
}
const inv = (qc: ReturnType<typeof useQueryClient>) => () => void qc.invalidateQueries({ queryKey: qk.setupState });

export function useCreateAdmin() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (body: Schema<'SetupAdminRequest'>) => call(api.POST('/setup/admin', { body })), onSuccess: inv(qc) });
}
export function useEnrollTotp() {
  return useMutation({ mutationFn: () => call(api.POST('/setup/totp/enroll')) });
}
export function useVerifySetupTotp() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (code: string) => call(api.POST('/setup/totp/verify', { body: { code } })), onSuccess: inv(qc) });
}
export function useVerifyDomain() {
  return useMutation({ mutationFn: (hostname: string) => call(api.POST('/setup/verify-domain', { body: { hostname } })) });
}
export function useSetPanelDomain() {
  return useMutation({ mutationFn: (hostname: string) => call(api.POST('/setup/panel-domain', { body: { hostname } })) });
}
export function useSetOperator() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (body: Schema<'SetOperatorRequest'>) => call(api.POST('/setup/operator', { body })), onSuccess: inv(qc) });
}
export function useCompleteSetup() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: () => call(api.POST('/setup/complete')), onSuccess: inv(qc) });
}

export function useResumeSetup() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (token: string) => call(api.POST('/setup/resume', { body: { token } })), onSuccess: inv(qc) });
}
