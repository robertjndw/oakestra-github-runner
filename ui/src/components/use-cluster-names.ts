import { useClusters } from '../api/hooks';

/** Resolves cluster ids to names; falls back to the id while clusters are unavailable. */
export function useClusterNames(): (id: string | undefined) => string {
  const clusters = useClusters();
  const byId = new Map((clusters.data ?? []).map((cluster) => [cluster.id, cluster.name]));
  return (id) => (id ? (byId.get(id) ?? id) : '-');
}
