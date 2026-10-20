import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, call } from '../client';
import { qk } from '../keys';
import type { Schema } from '../types';

export const useChannels = () => useQuery({ queryKey: qk.channels, queryFn: () => call(api.GET('/notifications/channels')) });
export const useAlertRules = () => useQuery({ queryKey: qk.alertRules, queryFn: () => call(api.GET('/alerts/rules')) });
export function useCreateChannel() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (body: Schema<'CreateChannelRequest'>) => call(api.POST('/notifications/channels', { body })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.channels }) });
}
export function usePatchChannel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; body: Schema<'PatchChannelRequest'> }) => call(api.PATCH('/notifications/channels/{id}', { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.channels }),
  });
}
export function useDeleteChannel() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (id: string) => call(api.DELETE('/notifications/channels/{id}', { params: { path: { id } } })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.channels }) });
}
export function useTestChannel() {
  return useMutation({ mutationFn: (id: string) => call(api.POST('/notifications/channels/{id}/test', { params: { path: { id } } })) });
}
export function usePatchAlertRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; body: Schema<'PatchAlertRuleRequest'> }) => call(api.PATCH('/alerts/rules/{id}', { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.alertRules }),
  });
}

export const useBackupSchedules = () => useQuery({ queryKey: qk.backups.schedules, queryFn: () => call(api.GET('/backups/schedules')) });
export const useBackupRuns = () => useQuery({ queryKey: qk.backups.runs, queryFn: () => call(api.GET('/backups/runs', { params: { query: { limit: 50 } } })) });
export function useRunBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => call(api.POST('/backups/{id}/run', { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['backups'] });
    },
  });
}
export function useRestoreBackup() {
  return useMutation({ mutationFn: (id: string) => call(api.POST('/backups/{id}/restore', { params: { path: { id } } })) });
}
export const useTemplates = (enabled = true) => useQuery({ queryKey: qk.templates, enabled, queryFn: () => call(api.GET('/templates')) });
export function useDeployTemplate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { key: string; body: Schema<'DeployTemplateRequest'> }) => call(api.POST('/templates/{key}/deploy', { params: { path: { key: v.key } }, body: v.body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.projects });
      void qc.invalidateQueries({ queryKey: ['resources'] });
    },
  });
}
