import { Puzzle, Terminal } from 'lucide-react';
import { Card, CardBody } from '@/components/ui/card';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';

/** Extensions are managed via CLI (`codeplo extensions`) — no OpenAPI routes (T53). */
export function ExtensionsPage() {
  return (
    <ModuleGate module="extensions">
      <PageHeader title="Extensions" description="Out-of-process integrations receive signed events from the daemon." />
      <div className="space-y-4">
        <Card>
          <CardBody className="space-y-2 text-xs text-secondary">
            <p className="flex items-center gap-2 text-sm font-bold text-primary"><Puzzle className="size-4" />CLI-managed</p>
            <p>
              Registration, tokens and outbound delivery are host-side only. The panel does not store extension
              secrets in the browser session — use the CLI on the VPS (as the <code className="font-mono">codeplo</code> user or via sudo).
            </p>
            <pre className="rounded bg-muted/40 p-2 font-mono text-[11px] text-primary overflow-x-auto">{`# List registered extensions
sudo -u codeplo codeplo extensions list

# Register an endpoint for one or more events
sudo -u codeplo codeplo extensions register \\
  --name my-ext \\
  --endpoint https://example.com/hooks/codeplo \\
  --events deployment.succeeded

# Health-check registered endpoints
sudo -u codeplo codeplo extensions check

# Review deliveries that exhausted retries (dead-letter queue)
sudo -u codeplo codeplo extensions dead`}</pre>
            <p>Enable outbound delivery with feature flag <code className="font-mono">extensions.outbound</code>.</p>
          </CardBody>
        </Card>
        <Card>
          <CardBody className="space-y-2 text-xs text-secondary">
            <p className="flex items-center gap-2 text-sm font-bold text-primary"><Terminal className="size-4" />Dead-letter review</p>
            <p>
              Failed deliveries land in the dead-letter queue after retries are exhausted. Inspect them with{' '}
              <code className="font-mono">codeplo extensions dead</code>. There is no web UI for this yet — the
              queue lives in the host database and is intentionally CLI-only so operators handle secrets and
              retries on the machine that owns the signing key.
            </p>
          </CardBody>
        </Card>
      </div>
    </ModuleGate>
  );
}
