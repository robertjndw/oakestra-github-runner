import { Button, PageHeader, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, EmptyState } from '@oakestra/ui';
import { createDataTableColumnHelper, DataTable } from '@oakestra/ui/data-table';
import { HistoryIcon } from 'lucide-react';
import { useMemo, useState } from 'react';

import { useJobs, usePools } from '../api/hooks';
import type { Job } from '../api/types';
import { JobResultBadge } from '../components/badges';
import { QueryError } from '../components/QueryError';
import { useClusterNames } from '../components/use-cluster-names';
import { formatDuration, formatTimestamp } from '../lib/format';

const ALL = 'all';

// Values GitHub reports in a scale set job completion. "running" is not a result: the backend
// stores an empty result for unfinished jobs, which the result filter cannot select.
const RESULTS = ['succeeded', 'failed', 'canceled', 'abandoned', 'skipped'];

const column = createDataTableColumnHelper<Job>();

function workflowName(ref: string | undefined): string {
  if (!ref) return '-';
  // owner/repo/.github/workflows/ci.yml@refs/heads/main -> ci.yml
  const path = ref.split('@')[0] ?? ref;
  return path.split('/').pop() ?? ref;
}

export default function JobsPage() {
  const [poolId, setPoolId] = useState('');
  const [result, setResult] = useState('');
  const jobs = useJobs(poolId, result);
  const pools = usePools();
  const clusterName = useClusterNames();

  const poolNames = useMemo(
    () => new Map((pools.data ?? []).map((pool) => [pool.id, pool.name])),
    [pools.data],
  );
  const items = useMemo(() => jobs.data?.pages.flatMap((page) => page.items), [jobs.data]);

  const columns = useMemo(
    () =>
      column.columns([
        column.accessor('repository', { header: 'Repository' }),
        column.accessor((job) => workflowName(job.workflow_ref), { id: 'workflow', header: 'Workflow' }),
        column.accessor('job_name', { header: 'Job' }),
        column.accessor((job) => poolNames.get(job.pool_id) ?? job.pool_id, { id: 'pool', header: 'Pool' }),
        column.accessor((job) => job.result ?? '', {
          id: 'result',
          header: 'Result',
          cell: (info) => <JobResultBadge result={info.getValue() || undefined} />,
        }),
        column.accessor((job) => formatTimestamp(job.queued_at), { id: 'queued', header: 'Queued' }),
        column.accessor((job) => formatTimestamp(job.started_at), { id: 'started', header: 'Started' }),
        column.accessor((job) => formatTimestamp(job.finished_at), { id: 'finished', header: 'Finished' }),
        column.accessor((job) => formatDuration(job.started_at, job.finished_at), {
          id: 'duration',
          header: 'Duration',
          meta: { numeric: true },
        }),
        column.accessor((job) => job.runner_name ?? '-', {
          id: 'runner',
          header: 'Runner',
          cell: (info) => <span className="ghr:font-mono ghr:text-xs">{info.getValue()}</span>,
        }),
        column.accessor((job) => (job.cluster_id ? clusterName(job.cluster_id) : '-'), {
          id: 'cluster',
          header: 'Cluster',
        }),
        column.accessor((job) => job.worker_id ?? '-', {
          id: 'worker',
          header: 'Worker',
          cell: (info) => <span className="ghr:font-mono ghr:text-xs">{info.getValue()}</span>,
        }),
      ]),
    [poolNames, clusterName],
  );

  const filtered = poolId !== '' || result !== '';

  const toolbar = (
    <div className="ghr:flex ghr:flex-wrap ghr:gap-2">
      <Select value={poolId || ALL} onValueChange={(value) => setPoolId(value === ALL ? '' : value)}>
        <SelectTrigger aria-label="Filter by pool" className="ghr:w-52">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>All pools</SelectItem>
          {(pools.data ?? []).map((pool) => (
            <SelectItem key={pool.id} value={pool.id}>
              {pool.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={result || ALL} onValueChange={(value) => setResult(value === ALL ? '' : value)}>
        <SelectTrigger aria-label="Filter by result" className="ghr:w-40">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>All results</SelectItem>
          {RESULTS.map((value) => (
            <SelectItem key={value} value={value}>
              {value.charAt(0).toUpperCase() + value.slice(1)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );

  return (
    <div className="ghr:space-y-6">
      <PageHeader title="Jobs" description="Jobs handled by your pools, newest first." />
      {jobs.isError ? (
        <QueryError title="Could not load the jobs" error={jobs.error} onRetry={() => void jobs.refetch()} />
      ) : null}
      <DataTable
        label="Jobs"
        columns={columns}
        data={items}
        isLoading={jobs.isPending}
        getRowId={(job) => `${job.pool_id}:${job.request_id}`}
        toolbar={toolbar}
        initialPageSize={25}
        emptyState={
          <EmptyState
            icon={<HistoryIcon aria-hidden />}
            title={filtered ? 'No jobs match these filters' : 'No jobs yet'}
            description={
              filtered
                ? 'Try another pool or result.'
                : 'Jobs show up here once a workflow runs on one of your pools.'
            }
          />
        }
      />
      {jobs.hasNextPage ? (
        <div className="ghr:flex ghr:justify-center">
          <Button variant="outline" disabled={jobs.isFetchingNextPage} onClick={() => void jobs.fetchNextPage()}>
            {jobs.isFetchingNextPage ? 'Loading...' : 'Load more'}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
