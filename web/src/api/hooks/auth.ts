import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, call } from '../client';
import { qk } from '../keys';
import { resetSession, setCsrf, syncServerNow } from '../session';
import { ApiError } from '../errors';
import type { Me } from '../types';

export const meQueryOptions = {
  queryKey: qk.me,
  retry: false,
  staleTime: 30_000,
  queryFn: async (): Promise<Me> => {
    const me = await call(api.GET('/auth/me'));
    setCsrf(me.csrf_token);
    syncServerNow(me.server_now);
    return me;
  },
};

export function useMe(enabled = true) {
  return useQuery<Me, ApiError>({ ...meQueryOptions, enabled });
}

export function useLogin() {
  return useMutation({ mutationFn: (b: { email: string; password: string }) => call(api.POST('/auth/login', { body: b })) });
}

export function useVerifyTotp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { challenge: string; code: string }) => call(api.POST('/auth/totp/verify', { body: b })),
    onSuccess: (me) => {
      setCsrf(me.csrf_token);
      syncServerNow(me.server_now);
      qc.setQueryData(qk.me, me);
    },
  });
}

export function useStepUpMutation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (code: string) => call(api.POST('/auth/step-up', { body: { code } })),
    onSuccess: (r) => {
      qc.setQueryData<Me | undefined>(qk.me, (me) => (me ? { ...me, step_up_until: r.step_up_until } : me));
    },
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => call(api.POST('/auth/logout')),
    onSettled: () => {
      resetSession();
      qc.clear();
    },
  });
}
