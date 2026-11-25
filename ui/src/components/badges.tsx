import { StatusBadge, type StatusTone } from '@oakestra/ui';

import type { ConnectionStatus, RunnerState } from '../api/types';

const RUNNER_TONES: Record<RunnerState, { tone: StatusTone; label: string; pulse?: boolean }> = {
  pending: { tone: 'info', label: 'Pending', pulse: true },
  idle: { tone: 'neutral', label: 'Idle' },
  busy: { tone: 'success', label: 'Busy' },
  removed: { tone: 'neutral', label: 'Removed' },
};

export function RunnerStateBadge({ state }: { state: RunnerState }) {
  const entry = RUNNER_TONES[state] ?? { tone: 'neutral' as const, label: state };
  return (
    <StatusBadge tone={entry.tone} pulse={'pulse' in entry ? entry.pulse : false}>
      {entry.label}
    </StatusBadge>
  );
}

const RESULT_TONES: Record<string, StatusTone> = {
  succeeded: 'success',
  failed: 'destructive',
  abandoned: 'destructive',
  canceled: 'neutral',
  cancelled: 'neutral',
  skipped: 'neutral',
};

/** An empty result means the job has not finished. */
export function JobResultBadge({ result }: { result: string | undefined }) {
  if (!result) {
    return (
      <StatusBadge tone="info" pulse>
        Running
      </StatusBadge>
    );
  }
  const label = result.charAt(0).toUpperCase() + result.slice(1);
  return <StatusBadge tone={RESULT_TONES[result.toLowerCase()] ?? 'neutral'}>{label}</StatusBadge>;
}

export function ConnectionStatusBadge({ status }: { status: ConnectionStatus }) {
  return status === 'ready' ? (
    <StatusBadge tone="success">Ready</StatusBadge>
  ) : (
    <StatusBadge tone="warning">Needs installation</StatusBadge>
  );
}

const OAKESTRA_TONES: Record<string, StatusTone> = {
  RUNNING: 'success',
  COMPLETED: 'neutral',
  FAILED: 'destructive',
  DEAD: 'destructive',
  NO_WORKER_CAPACITY: 'warning',
  CLUSTER_NO_RESOURCES: 'warning',
};

export function OakestraStatusBadge({ status }: { status: string | undefined }) {
  if (!status) return <span className="ghr:text-muted-foreground">-</span>;
  return (
    <StatusBadge tone={OAKESTRA_TONES[status] ?? 'info'} dot={false}>
      {status}
    </StatusBadge>
  );
}
