import type { ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { useModules } from '@/api/hooks/core';
import { EmptyState } from '@/components/app/EmptyState';
import { Card } from '@/components/ui/card';
import { usePermissions } from '@/lib/permissions';
import { Puzzle } from 'lucide-react';

/** Module-owned pages render only while GET /modules says the module is enabled. */
export function ModuleGate({ module, children }: { module: string; children: ReactNode }) {
  const { data } = useModules();
  const { can } = usePermissions();
  const m = data?.items.find((x) => x.key === module);
  if (m && !m.enabled) {
    return (
      <Card>
        <EmptyState icon={Puzzle} title={`${m.name} is turned off`} action={can('modules:manage') ? <Link to="/settings" search={{ tab: 'modules' }} className="text-xs font-semibold text-brand-primary hover:underline">Enable it in Settings → Modules</Link> : undefined}>
          {m.description} Ask an owner or admin to enable this module.
        </EmptyState>
      </Card>
    );
  }
  return <>{children}</>;
}

export function PageHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0"><h1 className="text-lg font-bold tracking-tight text-primary">{title}</h1>{description && <p className="mt-0.5 text-xs text-muted">{description}</p>}</div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
