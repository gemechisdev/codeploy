import type { UseQueryResult } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { SkeletonRows } from '@/components/ui/skeleton';
import { EmptyState, ErrorState } from './EmptyState';

/** Every query-driven view gets a loading skeleton, an empty state and an error state with retry (brief 6.10). */
export function QueryBoundary<T>({ query, isEmpty, empty, rows = 4, children }: {
  query: Pick<UseQueryResult<T>, 'data' | 'isPending' | 'isError' | 'error' | 'refetch'>;
  isEmpty?: (d: T) => boolean; empty?: ReactNode; rows?: number; children: (d: T) => ReactNode;
}) {
  if (query.isPending) return <SkeletonRows rows={rows} />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const d = query.data as T;
  if (isEmpty?.(d)) return <>{empty ?? <EmptyState title="Nothing here yet" />}</>;
  return <>{children(d)}</>;
}
