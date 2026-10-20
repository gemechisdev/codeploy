import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, call } from '../client';
import { qk } from '../keys';
import type { Schema } from '../types';

type QC = ReturnType<typeof useQueryClient>;
const refreshResource = (qc: QC, id: string) => () => {
  void qc.invalidateQueries({ queryKey: qk.resource(id) });
  void qc.invalidateQueries({ queryKey: qk.projects });
  void qc.invalidateQueries({ queryKey: ['resources', 'list'] });
};

// projects
export const useProjects = () => useQuery({ queryKey: qk.projects, queryFn: () => call(api.GET('/projects')) });
export const useProject = (id: string) => useQuery({ queryKey: qk.project(id), queryFn: () => call(api.GET('/projects/{id}', { params: { path: { id } } })) });
export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (body: Schema<'CreateProjectRequest'>) => call(api.POST('/projects', { body })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.projects }) });
}
export function useCreateEnvironment(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => call(api.POST('/projects/{id}/environments', { params: { path: { id: projectId } }, body: { name } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.projects }),
  });
}

// resources
export const useResources = (f: { q?: string; environment_id?: string } = {}) =>
  useQuery({ queryKey: qk.resources(f), queryFn: () => call(api.GET('/resources', { params: { query: { ...f, limit: 200 } } })) });
export const useResource = (id: string) => useQuery({ queryKey: qk.resource(id), queryFn: () => call(api.GET('/resources/{id}', { params: { path: { id } } })) });
export function useCreateResource() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schema<'CreateResourceRequest'>) => call(api.POST('/resources', { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.projects });
      void qc.invalidateQueries({ queryKey: ['resources'] });
    },
  });
}
export function usePatchResource(id: string) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (body: Schema<'PatchResourceRequest'>) => call(api.PATCH('/resources/{id}', { params: { path: { id } }, body })), onSuccess: refreshResource(qc, id) });
}
export function useDeleteResource() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => call(api.DELETE('/resources/{id}', { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.projects });
      void qc.invalidateQueries({ queryKey: ['resources'] });
    },
  });
}
export function useControlResource(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (action: 'start' | 'stop' | 'restart') => {
      const path = `/resources/{id}/${action}` as const;
      return call(api.POST(path, { params: { path: { id } } }));
    },
    onSuccess: refreshResource(qc, id),
  });
}
export const useResourcePolicy = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'policy'), queryFn: () => call(api.GET('/resources/{id}/policy', { params: { path: { id } } })) });

// env + volumes
export const useEnv = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'env'), queryFn: () => call(api.GET('/resources/{id}/env', { params: { path: { id } } })) });
export function usePutEnv(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (items: Schema<'EnvVarInput'>[]) => call(api.PUT('/resources/{id}/env', { params: { path: { id } }, body: { items } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'env') }),
  });
}
export const useVolumes = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'volumes'), queryFn: () => call(api.GET('/resources/{id}/volumes', { params: { path: { id } } })) });
export function useCreateVolume(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schema<'CreateVolumeRequest'>) => call(api.POST('/resources/{id}/volumes', { params: { path: { id } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'volumes') }),
  });
}

// domains (resource scoped + global)
export const useResourceDomains = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'domains'), queryFn: () => call(api.GET('/resources/{id}/domains', { params: { path: { id } } })) });
export const useAllDomains = () => useQuery({ queryKey: qk.edge.domains, queryFn: () => call(api.GET('/domains')) });
export function useCreateDomain(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schema<'CreateDomainRequest'>) => call(api.POST('/resources/{id}/domains', { params: { path: { id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'domains') });
      void qc.invalidateQueries({ queryKey: ['edge'] });
    },
  });
}
export function useDeleteDomain(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domainId: string) => call(api.DELETE('/resources/{id}/domains/{domainId}', { params: { path: { id, domainId } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'domains') });
      void qc.invalidateQueries({ queryKey: ['edge'] });
    },
  });
}
export const useEdgeStatus = () => useQuery({ queryKey: qk.edge.status, queryFn: () => call(api.GET('/edge/status')) });
export const useCertificates = () => useQuery({ queryKey: qk.edge.certs, queryFn: () => call(api.GET('/edge/certificates')) });
export function useReconcileEdge() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: () => call(api.POST('/edge/reconcile')), onSuccess: () => void qc.invalidateQueries({ queryKey: ['edge'] }) });
}

// deployments
export const useDeployments = (rid: string) =>
  useQuery({ queryKey: qk.deployments(rid), queryFn: () => call(api.GET('/resources/{id}/deployments', { params: { path: { id: rid }, query: { limit: 50 } } })) });
export const useDeployment = (id: string | undefined) =>
  useQuery({ queryKey: qk.deployment(id ?? ''), enabled: !!id, queryFn: () => call(api.GET('/deployments/{id}', { params: { path: { id: id! } } })) });
export function useCreateDeployment(rid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schema<'CreateDeploymentRequest'> = {}) => call(api.POST('/resources/{id}/deployments', { params: { path: { id: rid } }, body })),
    onSuccess: refreshResource(qc, rid),
  });
}
export function useCancelDeployment(rid: string) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (id: string) => call(api.POST('/deployments/{id}/cancel', { params: { path: { id } } })), onSuccess: refreshResource(qc, rid) });
}
export function useRollback(rid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => call(api.POST('/deployments/{id}/rollback', { params: { path: { id } }, body: { acknowledge_db_unchanged: true } })),
    onSuccess: refreshResource(qc, rid),
  });
}

// git / ci
export const useSource = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'source'), queryFn: () => call(api.GET('/resources/{id}/source', { params: { path: { id } } })) });
export function usePutSource(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schema<'ResourceSource'>) => call(api.PUT('/resources/{id}/source', { params: { path: { id } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'source') }),
  });
}
export const useDeployKey = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'deploy-key'), retry: false, queryFn: () => call(api.GET('/resources/{id}/deploy-key', { params: { path: { id } } })) });
export function useCreateDeployKey(id: string) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: () => call(api.POST('/resources/{id}/deploy-key', { params: { path: { id } } })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'deploy-key') }) });
}
export const useDeployHook = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'deploy-hook'), retry: false, queryFn: () => call(api.GET('/resources/{id}/deploy-hook', { params: { path: { id } } })) });
export function useCreateDeployHook(id: string, rotate: boolean) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => (rotate ? call(api.POST('/resources/{id}/deploy-hook/rotate', { params: { path: { id } } })) : call(api.POST('/resources/{id}/deploy-hook', { params: { path: { id } } }))),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'deploy-hook') }),
  });
}
export const useWebhook = (id: string) => useQuery({ queryKey: qk.resourceSub(id, 'webhook'), queryFn: () => call(api.GET('/resources/{id}/webhook', { params: { path: { id } } })) });
export function useCreateWebhook(id: string) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: () => call(api.POST('/resources/{id}/webhook', { params: { path: { id } } })), onSuccess: () => void qc.invalidateQueries({ queryKey: qk.resourceSub(id, 'webhook') }) });
}
export const useDeliveries = (resourceId?: string) =>
  useQuery({ queryKey: qk.git.deliveries(resourceId), queryFn: () => call(api.GET('/webhooks/deliveries', { params: { query: { resource_id: resourceId, limit: 50 } } })) });
export const useGitConnections = () => useQuery({ queryKey: qk.git.connections, queryFn: () => call(api.GET('/git/connections')) });

// terminal sessions
export const useTerminalSessions = () => useQuery({ queryKey: qk.terminalSessions, queryFn: () => call(api.GET('/terminal/sessions')) });
export function useTerminalTicket() {
  return useMutation({ mutationFn: (body: Schema<'TicketRequest'>) => call(api.POST('/terminal/ticket', { body })) });
}
