import { useAddonPath } from '@oakestra/dashboard-sdk';
import { Button, EmptyState, PageHeader } from '@oakestra/ui';
import { createDataTableColumnHelper, DataTable } from '@oakestra/ui/data-table';
import { ContainerIcon } from 'lucide-react';
import { useMemo } from 'react';
import { Link } from 'react-router';

import { usePools, useRunners } from '../api/hooks';
import type { Runner } from '../api/types';
import { OakestraStatusBadge, RunnerStateBadge } from '../components/badges';
import { QueryError } from '../components/QueryError';
import { useClusterNames } from '../components/use-cluster-names';
import { formatAge } from '../lib/format';

type RunnerRow = Runner & { pool_name: string; cluster_name: string };

const column = createDataTableColumnHelper<RunnerRow>();

export default function RunnersPage() {
  const runners = useRunners();
  const pools = usePools();
  const clusterName = useClusterNames();
  const addonPath = useAddonPath();

  const poolNames = useMemo(
    () => new Map((pools.data ?? []).map((pool) => [pool.id, pool.name])),
    [pools.data],
  );

  // DataTable caches what an accessor returns per row and only rebuilds its rows when `data`
  // changes. A name looked up inside an accessor would stay stuck on the id when the lookup
  // loads after the list, so the names are resolved into the rows instead.
  const rows = useMemo(
    () =>
      runners.data?.map((runner) => ({
        ...runner,
        pool_name: poolNames.get(runner.pool_id) ?? runner.pool_id,
        cluster_name: clusterName(runner.cluster_id),
      })),
    [runners.data, poolNames, clusterName],
  );

  const columns = useMemo(
    () =>
      column.columns([
        column.accessor('name', {
          header: 'Name',
          cell: (info) => <span className="ghr:font-mono ghr:text-xs">{info.getValue()}</span>,
        }),
        column.accessor('pool_name', { id: 'pool', header: 'Pool' }),
        column.accessor('state', {
          header: 'State',
          cell: (info) => <RunnerStateBadge state={info.getValue()} />,
        }),
        column.accessor((runner) => runner.oakestra_status ?? '', {
          id: 'oakestra_status',
          header: 'Oakestra status',
          cell: (info) => <OakestraStatusBadge status={info.getValue() || undefined} />,
        }),
        column.accessor('cluster_name', { id: 'cluster', header: 'Cluster' }),
        column.accessor((runner) => runner.worker_id ?? '-', {
          id: 'worker',
          header: 'Worker',
          cell: (info) => <span className="ghr:font-mono ghr:text-xs">{info.getValue()}</span>,
        }),
        column.accessor((runner) => runner.job_request_id ?? 0, {
          id: 'job',
          header: 'Job',
          cell: (info) => (info.getValue() ? `#${info.getValue()}` : '-'),
        }),
        column.accessor('created_at', {
          header: 'Age',
          meta: { numeric: true },
          cell: (info) => formatAge(info.getValue()),
        }),
      ]),
    [],
  );

  return (
    <div className="ghr:space-y-6">
      <PageHeader
        title="Runners"
        description="Single-use runner containers that exist right now. The list refreshes every 5 seconds."
      />
      {runners.isError ? (
        <QueryError title="Could not load the runners" error={runners.error} onRetry={() => void runners.refetch()} />
      ) : null}
      <DataTable
        label="Runners"
        columns={columns}
        data={rows}
        isLoading={runners.isPending}
        getRowId={(runner) => runner.name}
        emptyState={
          <EmptyState
            icon={<ContainerIcon aria-hidden />}
            title="No runners right now"
            description="Runners appear when jobs are queued for one of your pools. They are removed after the job ends."
            action={
              <Button asChild variant="outline">
                <Link to={addonPath('pools')}>View pools</Link>
              </Button>
            }
          />
        }
      />
    </div>
  );
}
