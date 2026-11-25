import { useAddonPath } from '@oakestra/dashboard-sdk';
import {
  Alert,
  AlertDescription,
  Button,
  ConfirmDialog,
  DropdownMenuItem,
  EmptyState,
  PageHeader,
  RowMenu,
  StatusBadge,
} from '@oakestra/ui';
import { createDataTableColumnHelper, DataTable } from '@oakestra/ui/data-table';
import { LayersIcon, PlusIcon, TriangleAlertIcon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link } from 'react-router';

import { useConnections, useDeletePool, usePools } from '../api/hooks';
import type { Pool } from '../api/types';
import { QueryError } from '../components/QueryError';
import { READ_ONLY_REASON, ReadOnlyNotice, useCanWrite } from '../components/permissions';
import { formatMemory } from '../lib/format';
import { PoolDialog } from './PoolDialog';

const column = createDataTableColumnHelper<Pool>();

export default function PoolsPage() {
  const pools = usePools();
  const connections = useConnections();
  const deletePool = useDeletePool();
  const canWrite = useCanWrite();
  const addonPath = useAddonPath();

  const [editing, setEditing] = useState<Pool | 'new' | null>(null);
  const [deleting, setDeleting] = useState<Pool | null>(null);

  const connectionNames = useMemo(
    () => new Map((connections.data ?? []).map((connection) => [connection.id, connection.name])),
    [connections.data],
  );

  const columns = useMemo(
    () =>
      column.columns([
        column.accessor('name', {
          header: 'Name',
          cell: (info) => (
            <div className="ghr:min-w-0 ghr:space-y-1">
              <span className="ghr:font-mono ghr:text-sm">{info.getValue()}</span>
              {info.row.original.last_error ? (
                <p className="ghr:flex ghr:max-w-md ghr:items-start ghr:gap-1.5 ghr:text-xs ghr:text-destructive">
                  <TriangleAlertIcon aria-hidden className="ghr:mt-0.5 ghr:size-3.5 ghr:shrink-0" />
                  <span>{info.row.original.last_error}</span>
                </p>
              ) : null}
            </div>
          ),
        }),
        column.accessor((pool) => connectionNames.get(pool.connection_id) ?? pool.connection_id, {
          id: 'connection',
          header: 'Connection',
        }),
        column.accessor('vcpus', { header: 'vCPUs', meta: { numeric: true } }),
        column.accessor((pool) => formatMemory(pool.memory_mb), { id: 'memory', header: 'Memory' }),
        column.accessor((pool) => pool.arch || 'any', { id: 'arch', header: 'Arch' }),
        column.accessor('max_runners', { header: 'Max runners', meta: { numeric: true } }),
        column.accessor((pool) => (pool.cluster ? `${pool.cluster}${pool.node ? ` / ${pool.node}` : ''}` : 'Any'), {
          id: 'placement',
          header: 'Placement',
        }),
        column.accessor('enabled', {
          header: 'Status',
          cell: (info) =>
            info.row.original.last_error ? (
              <StatusBadge tone="destructive">Error</StatusBadge>
            ) : info.getValue() ? (
              <StatusBadge tone="success">Enabled</StatusBadge>
            ) : (
              <StatusBadge tone="neutral">Disabled</StatusBadge>
            ),
        }),
        column.display({
          id: 'actions',
          header: () => <span className="ghr:sr-only">Actions</span>,
          cell: (info) => (
            <RowMenu label={`Actions for ${info.row.original.name}`} size="icon-sm">
              <DropdownMenuItem disabled={!canWrite} onSelect={() => setEditing(info.row.original)}>
                Edit
              </DropdownMenuItem>
              <DropdownMenuItem
                variant="destructive"
                disabled={!canWrite}
                onSelect={() => setDeleting(info.row.original)}
              >
                Delete
              </DropdownMenuItem>
            </RowMenu>
          ),
        }),
      ]),
    [connectionNames, canWrite],
  );

  const createButton = (
    <Button disabled={!canWrite} title={canWrite ? undefined : READ_ONLY_REASON} onClick={() => setEditing('new')}>
      <PlusIcon aria-hidden /> Create pool
    </Button>
  );

  return (
    <div className="ghr:space-y-6">
      <PageHeader
        title="Pools"
        description="Each pool is one runner size. Workflows pick a pool with its name in runs-on."
        actions={createButton}
      />
      {canWrite ? null : <ReadOnlyNotice what="the pools" />}
      <Alert variant="info">
        <AlertDescription>
          {/* AlertDescription is a grid, so bare text and inline elements would each get a row. */}
          <p>
            CPU and memory guide scheduling on Oakestra. They are not enforced as limits. Jobs
            cannot use <code>container:</code>, service containers, Docker actions or{' '}
            <code>docker build</code>.
          </p>
        </AlertDescription>
      </Alert>
      {pools.isError ? (
        <QueryError title="Could not load the pools" error={pools.error} onRetry={() => void pools.refetch()} />
      ) : null}
      <DataTable
        label="Pools"
        columns={columns}
        data={pools.data}
        isLoading={pools.isPending}
        getRowId={(pool) => pool.id}
        emptyState={
          <EmptyState
            icon={<LayersIcon aria-hidden />}
            title="No pools yet"
            description={
              connections.data?.some((connection) => connection.status === 'ready')
                ? 'Create a pool to get a runs-on value for your workflows.'
                : 'Connect GitHub first, then create a pool.'
            }
            action={
              connections.data?.some((connection) => connection.status === 'ready') ? (
                createButton
              ) : (
                <Button asChild variant="outline">
                  <Link to={addonPath('settings')}>Open settings</Link>
                </Button>
              )
            }
          />
        }
      />
      {editing ? (
        <PoolDialog
          // A fresh form state per pool, and per opening of the dialog.
          key={editing === 'new' ? 'new' : editing.id}
          open
          pool={editing === 'new' ? undefined : editing}
          onOpenChange={(open) => {
            if (!open) setEditing(null);
          }}
        />
      ) : null}
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={`Delete ${deleting?.name ?? 'pool'}?`}
        description="Its runners are removed right away, so jobs running on them fail. Workflows that still use the name in runs-on stay queued. To let running jobs finish, disable the pool instead and delete it later."
        confirmLabel="Delete pool"
        destructive
        pending={deletePool.isPending}
        onConfirm={() => {
          if (!deleting) return;
          deletePool.mutate(deleting.id, { onSuccess: () => setDeleting(null) });
        }}
      />
    </div>
  );
}
