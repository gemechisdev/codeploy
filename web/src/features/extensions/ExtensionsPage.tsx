import { Puzzle } from 'lucide-react';
import { Card, CardBody } from '@/components/ui/card';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';

/** Extensions are managed via CLI (`codeplo extensions`) — no OpenAPI routes (T53). */
export function ExtensionsPage() {
  return (
    <ModuleGate module="extensions">
      <PageHeader title="Extensions" description="Out-of-process integrations receive signed events from the daemon." />
      <Card>
        <CardBody className="space-y-2 text-xs text-secondary">
          <p className="flex items-center gap-2 text-sm font-bold text-primary"><Puzzle className="size-4" />CLI-managed</p>
          <p>Registration, tokens and dead-letter review run on the host:</p>
          <pre className="rounded bg-muted/40 p-2 font-mono text-[11px] text-primary overflow-x-auto">{`codeplo extensions list
codeplo extensions register --name my-ext --endpoint https://… --events deployment.succeeded
codeplo extensions dead
codeplo extensions check`}</pre>
          <p>Enable outbound delivery with feature flag <code className="font-mono">extensions.outbound</code>.</p>
        </CardBody>
      </Card>
    </ModuleGate>
  );
}
