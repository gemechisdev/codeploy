import type { Resource } from '@/api/types';
import { TerminalPane } from '@/features/terminal/TerminalPane';

export function ShellTab({ resource }: { resource: Resource }) {
  if (!resource.active_deployment_id || resource.status === 'stopped') return <p className="rounded-xl border border-subtle bg-surface p-6 text-center text-xs text-muted">The container is not running, so a shell is not available.</p>;
  return <TerminalPane target="container" resourceId={resource.id} label={resource.name} />;
}
