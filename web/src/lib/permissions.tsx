import type { ReactNode } from 'react';
import { useMe } from '@/api/hooks/auth';
import type { Permission } from '@/api/types';

/**
 * UX-only permission helpers. The server is authoritative: these hide or disable controls the
 * session lacks, and never replace handling of 401/403 responses.
 */
export function usePermissions() {
  const { data } = useMe();
  const set = new Set<string>(data?.permissions ?? []);
  return {
    role: data?.role,
    can: (p: Permission) => set.has(p),
    canAny: (...ps: Permission[]) => ps.some((p) => set.has(p)),
  };
}

export function PermissionGate({ perm, children, fallback = null }: { perm: Permission | Permission[]; children: ReactNode; fallback?: ReactNode }) {
  const { can } = usePermissions();
  const list = Array.isArray(perm) ? perm : [perm];
  return <>{list.every((p) => can(p)) ? children : fallback}</>;
}
