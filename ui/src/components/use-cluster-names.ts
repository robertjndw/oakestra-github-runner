import { useMemo } from 'react';

import { useClusters } from '../api/hooks';

/**
 * Resolves cluster ids to names; falls back to the id while clusters are unavailable. The
 * function only changes when the clusters do, so tables can list it as a dependency of their rows.
 */
export function useClusterNames(): (id: string | undefined) => string {
  const clusters = useClusters();
  return useMemo(() => {
    const byId = new Map((clusters.data ?? []).map((cluster) => [cluster.id, cluster.name]));
    return (id) => (id ? (byId.get(id) ?? id) : '-');
  }, [clusters.data]);
}
