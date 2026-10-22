import { GitPullRequest } from 'lucide-react';
import { Card, CardBody } from '@/components/ui/card';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';

/** Previews have no OpenAPI list route; operators use CLI (T53/T61 density). */
export function PreviewsPage() {
  return (
    <ModuleGate module="deploy">
      <PageHeader title="PR previews" description="Disposable environments for pull requests. Managed on the host." />
      <Card>
        <CardBody className="space-y-2 text-xs text-secondary">
          <p className="flex items-center gap-2 text-sm font-bold text-primary"><GitPullRequest className="size-4" />Host CLI</p>
          <pre className="rounded bg-muted/40 p-2 font-mono text-[11px] text-primary overflow-x-auto">{`codeplo previews check
codeplo previews list`}</pre>
          <p>Requires <code className="font-mono">CODEPLO_PREVIEW_DOMAIN</code> and feature flag <code className="font-mono">git.preview_deploys</code>.</p>
          <p>Lifecycle (open → refresh → close / TTL / parent-deleted) is owned by the daemon sweep — not this page.</p>
        </CardBody>
      </Card>
    </ModuleGate>
  );
}
