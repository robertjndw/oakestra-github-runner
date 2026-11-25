import { Button, ConfirmDialog, EmptyState, SectionCard, StatusBadge } from '@oakestra/ui';
import { createDataTableColumnHelper, DataTable } from '@oakestra/ui/data-table';
import { LinkIcon, Trash2Icon } from 'lucide-react';
import { useMemo, useState } from 'react';

import { ApiError } from '../api/client';
import { useConnections, useDeleteConnection } from '../api/hooks';
import type { Connection } from '../api/types';
import { ConnectionStatusBadge } from '../components/badges';
import { QueryError } from '../components/QueryError';
import { READ_ONLY_REASON, useCanWrite } from '../components/permissions';

const column = createDataTableColumnHelper<Connection>();

export function ConnectionsSection() {
  const connections = useConnections();
  const remove = useDeleteConnection();
  const canWrite = useCanWrite();
  const [deleting, setDeleting] = useState<Connection | null>(null);
  const [conflict, setConflict] = useState(false);

  const columns = useMemo(
    () =>
      column.columns([
        column.accessor('name', { header: 'Name' }),
        column.accessor('config_url', {
          header: 'Target',
          cell: (info) => (
            <a className="ghr:underline ghr:underline-offset-2" href={info.getValue()} target="_blank" rel="noreferrer">
              {info.getValue().replace(/^https:\/\/github\.com\//, '')}
            </a>
          ),
        }),
        column.accessor('auth_kind', {
          header: 'Type',
          cell: (info) =>
            info.getValue() === 'app' ? (
              <StatusBadge tone="neutral" dot={false}>
                GitHub App
              </StatusBadge>
            ) : (
              <StatusBadge tone="neutral" dot={false}>
                Token
              </StatusBadge>
            ),
        }),
        column.accessor('status', {
          header: 'Status',
          cell: (info) => <ConnectionStatusBadge status={info.getValue()} />,
        }),
        column.display({
          id: 'actions',
          header: () => <span className="ghr:sr-only">Actions</span>,
          cell: (info) => (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`Delete ${info.row.original.name}`}
              disabled={!canWrite}
              title={canWrite ? undefined : READ_ONLY_REASON}
              onClick={() => {
                setConflict(false);
                setDeleting(info.row.original);
              }}
            >
              <Trash2Icon aria-hidden />
            </Button>
          ),
        }),
      ]),
    [canWrite],
  );

  return (
    <SectionCard title="GitHub connections" description="Organizations and repositories your pools can serve.">
      {connections.isError ? (
        <QueryError
          title="Could not load the connections"
          error={connections.error}
          onRetry={() => void connections.refetch()}
        />
      ) : null}
      <DataTable
        label="GitHub connections"
        columns={columns}
        data={connections.data}
        isLoading={connections.isPending}
        getRowId={(connection) => connection.id}
        hideSearch
        emptyState={
          <EmptyState
            icon={<LinkIcon aria-hidden />}
            title="No connections yet"
            description="Connect with a GitHub App or a token below."
          />
        }
      />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={`Delete ${deleting?.name ?? 'connection'}?`}
        description={
          conflict
            ? 'Pools still use this connection. Delete or move those pools first.'
            : 'The stored credential is removed. A GitHub App stays in your GitHub account and has to be removed there.'
        }
        confirmLabel="Delete connection"
        destructive
        pending={remove.isPending}
        onConfirm={() => {
          if (!deleting) return;
          remove.mutate(deleting.id, {
            onSuccess: () => setDeleting(null),
            onError: (error) => {
              // The hook already toasts the message; keep the dialog open with the reason.
              if (error instanceof ApiError && error.isConflict) setConflict(true);
              else setDeleting(null);
            },
          });
        }}
      />
    </SectionCard>
  );
}
