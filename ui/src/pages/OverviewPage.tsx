import { useAddonPath } from '@oakestra/dashboard-sdk';
import {
  Button,
  CopyButton,
  PageHeader,
  SectionCard,
  Skeleton,
  StatCard,
  StatGroup,
  StatusBadge,
} from '@oakestra/ui';
import { CheckCircle2Icon, CircleDashedIcon, TriangleAlertIcon } from 'lucide-react';
import { Link } from 'react-router';

import { usePools, useStatus } from '../api/hooks';
import type { Status } from '../api/types';
import { QueryError } from '../components/QueryError';

type StepState = 'done' | 'todo' | 'error';

interface Step {
  id: string;
  state: StepState;
  title: string;
  detail: string;
  /** Page below the addon that fixes the step. */
  page: 'settings' | 'pools';
  action: string;
}

export function buildSteps(status: Status): Step[] {
  const { oakestra } = status;
  const oakestraState: StepState = !oakestra.configured ? 'todo' : oakestra.ok ? 'done' : 'error';
  return [
    {
      id: 'oakestra',
      state: oakestraState,
      title: 'Oakestra service user',
      detail:
        oakestraState === 'done'
          ? `Logged in as ${oakestra.username ?? 'the service user'}.`
          : oakestraState === 'error'
            ? `Login failed: ${oakestra.error ?? 'unknown error'}`
            : 'Runners are created as this user. Add its credentials in the settings.',
      page: 'settings',
      action: oakestraState === 'todo' ? 'Set up' : 'Open settings',
    },
    {
      id: 'connection',
      state: status.connections.ready > 0 ? 'done' : 'todo',
      title: 'GitHub connection',
      detail:
        status.connections.ready > 0
          ? `${status.connections.ready} of ${status.connections.total} connections ready.`
          : status.connections.total > 0
            ? 'A connection exists but is not ready yet. Finish installing the GitHub App.'
            : 'Connect an organization or repository with a GitHub App or a token.',
      page: 'settings',
      action: status.connections.total > 0 ? 'Open settings' : 'Connect GitHub',
    },
    {
      id: 'pool',
      state: status.pools.enabled > 0 ? 'done' : 'todo',
      title: 'Runner pool',
      detail:
        status.pools.enabled > 0
          ? `${status.pools.enabled} of ${status.pools.total} pools enabled.`
          : status.pools.total > 0
            ? 'All pools are disabled. Enable one to receive jobs.'
            : 'A pool is one runner size. Its name is what workflows put in runs-on.',
      page: 'pools',
      action: status.pools.total > 0 ? 'Open pools' : 'Create a pool',
    },
  ];
}

function StepIcon({ state }: { state: StepState }) {
  if (state === 'done') return <CheckCircle2Icon aria-hidden className="ghr:size-5 ghr:text-success" />;
  if (state === 'error') return <TriangleAlertIcon aria-hidden className="ghr:size-5 ghr:text-destructive" />;
  return <CircleDashedIcon aria-hidden className="ghr:size-5 ghr:text-muted-foreground" />;
}

function SetupChecklist({ status }: { status: Status }) {
  const addonPath = useAddonPath();
  const steps = buildSteps(status);
  const complete = steps.every((step) => step.state === 'done');
  return (
    <SectionCard
      title="Setup"
      description={complete ? 'Everything is in place.' : 'Finish these steps to run jobs on Oakestra.'}
    >
      <ol className="ghr:divide-y ghr:divide-border" aria-label="Setup steps">
        {steps.map((step) => (
          <li key={step.id} data-state={step.state} className="ghr:flex ghr:items-start ghr:gap-3 ghr:py-3 ghr:first:pt-0 ghr:last:pb-0">
            <StepIcon state={step.state} />
            <div className="ghr:min-w-0 ghr:flex-1">
              <p className="ghr:text-sm ghr:font-medium">
                {step.title}
                <span className="ghr:sr-only">
                  {step.state === 'done' ? ' (done)' : step.state === 'error' ? ' (failed)' : ' (to do)'}
                </span>
              </p>
              <p className="ghr:text-sm ghr:text-muted-foreground">{step.detail}</p>
            </div>
            {step.state === 'done' ? null : (
              <Button asChild size="sm" variant={step.state === 'error' ? 'destructive' : 'default'}>
                <Link to={addonPath(step.page)}>{step.action}</Link>
              </Button>
            )}
          </li>
        ))}
      </ol>
    </SectionCard>
  );
}

function RunsOnSnippets() {
  const pools = usePools();
  const addonPath = useAddonPath();
  return (
    <SectionCard
      title="Use a pool in a workflow"
      description="Set runs-on to the name of a pool. This works in workflows of the organization or repository the pool is connected to."
      action={
        <Button asChild size="sm" variant="outline">
          <Link to={addonPath('pools')}>Manage pools</Link>
        </Button>
      }
    >
      {pools.isPending ? (
        <Skeleton className="ghr:h-10 ghr:w-full" />
      ) : pools.isError ? (
        <QueryError title="Could not load the pools" error={pools.error} onRetry={() => void pools.refetch()} />
      ) : pools.data.length === 0 ? (
        <p className="ghr:text-sm ghr:text-muted-foreground">No pools yet. Create one to get a runs-on value.</p>
      ) : (
        <ul className="ghr:space-y-2">
          {pools.data.map((pool) => (
            <li key={pool.id} className="ghr:flex ghr:items-center ghr:gap-2">
              <code className="ghr:min-w-0 ghr:flex-1 ghr:truncate ghr:rounded-md ghr:border ghr:bg-muted ghr:px-3 ghr:py-1.5 ghr:font-mono ghr:text-sm">
                runs-on: {pool.name}
              </code>
              {pool.enabled ? null : <StatusBadge tone="neutral">Disabled</StatusBadge>}
              <CopyButton value={`runs-on: ${pool.name}`} label={`Copy runs-on for ${pool.name}`} variant="outline" />
            </li>
          ))}
        </ul>
      )}
    </SectionCard>
  );
}

function QueuedJobHelp() {
  return (
    <SectionCard title="If a job stays queued" description="The usual reasons, most likely first.">
      <ul className="ghr:list-inside ghr:list-disc ghr:space-y-1 ghr:text-sm ghr:text-muted-foreground">
        <li>
          A runner takes 15 to 30 seconds to start, and longer the first time a node pulls the image.
        </li>
        <li>
          The pool shows an error on the Pools page. GitHub rejected the credentials, or Oakestra could
          not start the runner.
        </li>
        <li>
          <code>runs-on</code> does not match the pool name exactly, or lists other labels next to it.
        </li>
        <li>
          The repository is public. GitHub keeps public repositories away from an organization's
          runners until you allow them in the runner group settings.
        </li>
        <li>The pool already runs as many jobs as its max runners allows.</li>
      </ul>
    </SectionCard>
  );
}

function Limitations() {
  return (
    <SectionCard title="Limitations" description="What this version does not do yet.">
      <ul className="ghr:list-inside ghr:list-disc ghr:space-y-1 ghr:text-sm ghr:text-muted-foreground">
        <li>
          Jobs cannot use <code>container:</code>, service containers, Docker actions or{' '}
          <code>docker build</code>.
        </li>
        <li>
          Runners use the minimal official runner image, not the full ubuntu-latest toolset. Install
          what a job needs in a step.
        </li>
        <li>CPU and memory of a pool guide scheduling. They are not enforced as limits.</li>
      </ul>
    </SectionCard>
  );
}

export default function OverviewPage() {
  const status = useStatus();
  const addonPath = useAddonPath();

  return (
    <div className="ghr:space-y-6">
      <PageHeader
        title="GitHub runners"
        description="Run GitHub Actions jobs on your Oakestra cluster. Each job gets a fresh runner container."
        actions={
          <Button asChild variant="outline">
            <Link to={addonPath('runners')}>View runners</Link>
          </Button>
        }
      />
      {status.isError ? (
        <QueryError title="Could not load the status" error={status.error} onRetry={() => void status.refetch()} />
      ) : null}
      {status.data ? (
        <>
          <StatGroup>
            <StatCard label="Pending runners" value={status.data.runners.pending} hint="Starting on Oakestra" />
            <StatCard label="Idle runners" value={status.data.runners.idle} hint="Waiting for a job" />
            <StatCard label="Busy runners" value={status.data.runners.busy} hint="Running a job" />
          </StatGroup>
          <SetupChecklist status={status.data} />
        </>
      ) : status.isPending ? (
        <Skeleton className="ghr:h-48 ghr:w-full" />
      ) : null}
      <RunsOnSnippets />
      <QueuedJobHelp />
      <Limitations />
    </div>
  );
}
