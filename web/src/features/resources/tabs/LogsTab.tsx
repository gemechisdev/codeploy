import { useContainerLogs } from '@/api/hooks/logs';
import type { Resource } from '@/api/types';
import { LogViewer } from '@/components/app/LogViewer';

export function LogsTab({ resource }: { resource: Resource }) {
  const logs = useContainerLogs(resource.id, !!resource.active_deployment_id);
  if (!resource.active_deployment_id) return <p className="rounded-xl border border-subtle bg-surface p-6 text-center text-xs text-muted">No container has been started yet, so there are no logs. Deploy this resource first.</p>;
  return <LogViewer rows={logs.rows} live={logs.live} height={420} emptyText={resource.status === 'stopped' ? 'The container is stopped.' : 'Waiting for output…'} />;
}
