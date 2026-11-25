import type { ClusterContext } from '@oakestra/dashboard-sdk';
import { useAddonPath } from '@oakestra/dashboard-sdk';
import { Skeleton } from '@oakestra/ui';
import { Link } from 'react-router';

import { useRunners } from '../api/hooks';
import { RunnerStateBadge } from '../components/badges';
import { formatAge } from '../lib/format';

export function ClusterRunnersWidget({ clusterId }: ClusterContext) {
  const runners = useRunners();
  const addonPath = useAddonPath();

  if (runners.isPending) return <Skeleton className="ghr:h-12 ghr:w-full" />;
  if (runners.isError) {
    return <p className="ghr:text-sm ghr:text-destructive">Could not load the runners.</p>;
  }

  const here = runners.data.filter((runner) => runner.cluster_id === clusterId);
  if (here.length === 0) {
    return <p className="ghr:text-sm ghr:text-muted-foreground">No runners on this cluster right now.</p>;
  }

  return (
    <div className="ghr:space-y-2">
      <ul className="ghr:divide-y ghr:divide-border">
        {here.map((runner) => (
          <li key={runner.name} className="ghr:flex ghr:items-center ghr:gap-3 ghr:py-2 ghr:text-sm">
            <span className="ghr:min-w-0 ghr:flex-1 ghr:truncate ghr:font-mono ghr:text-xs">{runner.name}</span>
            <RunnerStateBadge state={runner.state} />
            <span className="ghr:w-10 ghr:text-right ghr:tabular-nums ghr:text-muted-foreground">
              {formatAge(runner.created_at)}
            </span>
          </li>
        ))}
      </ul>
      <Link className="ghr:text-sm ghr:underline ghr:underline-offset-2" to={addonPath('runners')}>
        All runners
      </Link>
    </div>
  );
}
