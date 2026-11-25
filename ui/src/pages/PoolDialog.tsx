import {
  Alert,
  AlertDescription,
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Switch,
  ToggleGroup,
  ToggleGroupItem,
} from '@oakestra/ui';
import { TriangleAlertIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';

import { useClusters, useConnections, useSavePool } from '../api/hooks';
import type { Cluster, Pool, PoolInput } from '../api/types';
import { MB_PER_VCPU, SIZE_PRESETS, suggestPoolName, validatePool, type PoolErrors } from '../lib/validation';

const ANY = '__any__';
const CUSTOM = 'custom';

interface FormState {
  connection_id: string;
  name: string;
  /** False while the name follows the proposal for the chosen size. */
  nameEdited: boolean;
  size: string;
  vcpus: string;
  memory_mb: string;
  arch: string;
  max_runners: string;
  cluster: string;
  node: string;
  runner_group: string;
  image: string;
  enabled: boolean;
}

function initialState(pool: Pool | undefined, defaultConnection: string): FormState {
  if (!pool) {
    return {
      connection_id: defaultConnection,
      name: suggestPoolName(4, ''),
      nameEdited: false,
      size: '4',
      vcpus: '4',
      memory_mb: String(4 * MB_PER_VCPU),
      arch: '',
      max_runners: '10',
      cluster: '',
      node: '',
      runner_group: '',
      image: '',
      enabled: true,
    };
  }
  const preset = SIZE_PRESETS.find((vcpus) => vcpus === pool.vcpus && pool.memory_mb === vcpus * MB_PER_VCPU);
  return {
    connection_id: pool.connection_id,
    name: pool.name,
    nameEdited: true,
    size: preset ? String(preset) : CUSTOM,
    vcpus: String(pool.vcpus),
    memory_mb: String(pool.memory_mb),
    arch: pool.arch ?? '',
    max_runners: String(pool.max_runners),
    cluster: pool.cluster ?? '',
    node: pool.node ?? '',
    runner_group: pool.runner_group ?? '',
    image: pool.image ?? '',
    enabled: pool.enabled,
  };
}

function toInput(state: FormState): PoolInput {
  return {
    connection_id: state.connection_id,
    name: state.name.trim(),
    vcpus: state.vcpus.trim() === '' ? NaN : Number(state.vcpus),
    memory_mb: state.memory_mb.trim() === '' ? NaN : Number(state.memory_mb),
    arch: state.arch,
    max_runners: state.max_runners.trim() === '' ? NaN : Number(state.max_runners),
    cluster: state.cluster,
    node: state.node.trim(),
    runner_group: state.runner_group.trim(),
    image: state.image.trim(),
    enabled: state.enabled,
  };
}

function describeCluster(cluster: Cluster): string {
  const arch = cluster.architectures?.length ? cluster.architectures.join(', ') : 'unknown arch';
  return `${cluster.name} (${arch}, ${cluster.active_nodes} ${cluster.active_nodes === 1 ? 'node' : 'nodes'})`;
}

export function PoolDialog({
  open,
  onOpenChange,
  pool,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  pool?: Pool;
}) {
  const connections = useConnections();
  const clusters = useClusters();
  const save = useSavePool();

  const readyConnections = (connections.data ?? []).filter((connection) => connection.status === 'ready');
  const [state, setState] = useState<FormState>(() => initialState(pool, readyConnections[0]?.id ?? ''));
  const [submitted, setSubmitted] = useState(false);

  const connectionId = state.connection_id || readyConnections[0]?.id || '';
  const input = toInput({ ...state, connection_id: connectionId });
  const errors: PoolErrors = submitted ? validatePool(input) : {};

  const patch = (change: Partial<FormState>) => setState((current) => ({ ...current, ...change }));

  // While the name has not been edited by hand it follows the size and architecture.
  const patchSizing = (change: Partial<FormState>) =>
    setState((current) => {
      const next = { ...current, ...change };
      if (!next.nameEdited) next.name = suggestPoolName(Number(next.vcpus) || 0, next.arch);
      return next;
    });

  const selectedCluster = (clusters.data ?? []).find((cluster) => cluster.name === state.cluster);
  const archMissing =
    state.arch !== '' &&
    selectedCluster !== undefined &&
    (selectedCluster.architectures?.length ?? 0) > 0 &&
    !selectedCluster.architectures?.includes(state.arch);

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    const body = toInput({ ...state, connection_id: connectionId });
    if (Object.keys(validatePool(body)).length > 0) return;
    save.mutate(
      { id: pool?.id, input: body },
      {
        onSuccess: () => {
          onOpenChange(false);
        },
      },
    );
  };

  const suggestion = suggestPoolName(Number(state.vcpus) || 0, state.arch);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="ghr:max-h-[90vh] ghr:overflow-y-auto ghr:sm:max-w-xl">
        <form onSubmit={onSubmit} noValidate className="ghr:grid ghr:gap-4">
          <DialogHeader>
            <DialogTitle>{pool ? 'Edit pool' : 'Create pool'}</DialogTitle>
            <DialogDescription>
              A pool is one runner size. Workflows pick it with its name in <code>runs-on</code>.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field data-invalid={!!errors.connection_id}>
              <FieldLabel htmlFor="pool-connection">Connection</FieldLabel>
              <Select
                value={connectionId}
                onValueChange={(value) => patch({ connection_id: value })}
                disabled={pool !== undefined}
              >
                <SelectTrigger id="pool-connection" className="ghr:w-full" aria-invalid={!!errors.connection_id}>
                  <SelectValue placeholder="Choose a connection" />
                </SelectTrigger>
                <SelectContent>
                  {(connections.data ?? []).map((connection) => (
                    <SelectItem key={connection.id} value={connection.id} disabled={connection.status !== 'ready'}>
                      {connection.name}
                      {connection.status === 'ready' ? '' : ' (not ready)'}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FieldDescription>
                {readyConnections.length === 0
                  ? 'No ready connection. Connect GitHub in the settings first.'
                  : pool
                    ? 'The connection cannot change after the pool is created.'
                    : 'The GitHub organization or repository that can use this pool.'}
              </FieldDescription>
              <FieldError errors={[errors.connection_id ? { message: errors.connection_id } : undefined]} />
            </Field>

            <Field>
              <FieldLabel>Size</FieldLabel>
              <ToggleGroup
                type="single"
                variant="outline"
                value={state.size}
                aria-label="Size"
                onValueChange={(value) => {
                  if (!value) return;
                  if (value === CUSTOM) {
                    patch({ size: CUSTOM });
                    return;
                  }
                  patchSizing({
                    size: value,
                    vcpus: value,
                    memory_mb: String(Number(value) * MB_PER_VCPU),
                  });
                }}
              >
                {SIZE_PRESETS.map((vcpus) => (
                  <ToggleGroupItem key={vcpus} value={String(vcpus)}>
                    {vcpus} vCPU
                  </ToggleGroupItem>
                ))}
                <ToggleGroupItem value={CUSTOM}>Custom</ToggleGroupItem>
              </ToggleGroup>
              <FieldDescription>
                Presets use 2 GB of memory per vCPU. CPU and memory are used for scheduling on
                Oakestra. They are not enforced as limits inside the runner.
              </FieldDescription>
            </Field>

            {state.size === CUSTOM ? (
              <div className="ghr:grid ghr:grid-cols-2 ghr:gap-4">
                <Field data-invalid={!!errors.vcpus}>
                  <FieldLabel htmlFor="pool-vcpus">vCPUs</FieldLabel>
                  <Input
                    id="pool-vcpus"
                    type="number"
                    inputMode="numeric"
                    min={1}
                    max={256}
                    value={state.vcpus}
                    aria-invalid={!!errors.vcpus}
                    onChange={(event) => patchSizing({ vcpus: event.target.value })}
                  />
                  <FieldError errors={[errors.vcpus ? { message: errors.vcpus } : undefined]} />
                </Field>
                <Field data-invalid={!!errors.memory_mb}>
                  <FieldLabel htmlFor="pool-memory">Memory (MB)</FieldLabel>
                  <Input
                    id="pool-memory"
                    type="number"
                    inputMode="numeric"
                    min={256}
                    value={state.memory_mb}
                    aria-invalid={!!errors.memory_mb}
                    onChange={(event) => patch({ memory_mb: event.target.value })}
                  />
                  <FieldError errors={[errors.memory_mb ? { message: errors.memory_mb } : undefined]} />
                </Field>
              </div>
            ) : null}

            <Field data-invalid={!!errors.name}>
              <FieldLabel htmlFor="pool-name">Name</FieldLabel>
              <Input
                id="pool-name"
                value={state.name}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={!!errors.name}
                onChange={(event) => patch({ name: event.target.value, nameEdited: true })}
              />
              <FieldDescription>
                This is the <code>runs-on</code> value.{' '}
                {state.name === suggestion ? (
                  'It matches the proposal for this size.'
                ) : (
                  <>
                    Proposal: <code>{suggestion}</code>{' '}
                    <button
                      type="button"
                      className="ghr:underline ghr:underline-offset-2"
                      onClick={() => patch({ name: suggestion, nameEdited: false })}
                    >
                      Use it
                    </button>
                  </>
                )}
              </FieldDescription>
              <FieldError errors={[errors.name ? { message: errors.name } : undefined]} />
            </Field>

            <div className="ghr:grid ghr:grid-cols-2 ghr:gap-4">
              <Field data-invalid={!!errors.arch}>
                <FieldLabel htmlFor="pool-arch">Architecture</FieldLabel>
                <Select
                  value={state.arch || ANY}
                  onValueChange={(value) => patchSizing({ arch: value === ANY ? '' : value })}
                >
                  <SelectTrigger id="pool-arch" className="ghr:w-full" aria-invalid={!!errors.arch}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={ANY}>Any</SelectItem>
                    <SelectItem value="amd64">amd64</SelectItem>
                    <SelectItem value="arm64">arm64</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field data-invalid={!!errors.max_runners}>
                <FieldLabel htmlFor="pool-max">Max runners</FieldLabel>
                <Input
                  id="pool-max"
                  type="number"
                  inputMode="numeric"
                  min={1}
                  max={100}
                  value={state.max_runners}
                  aria-invalid={!!errors.max_runners}
                  onChange={(event) => patch({ max_runners: event.target.value })}
                />
                <FieldError errors={[errors.max_runners ? { message: errors.max_runners } : undefined]} />
              </Field>
            </div>
            <FieldDescription>
              Max runners is how many jobs of this pool run at the same time. Further jobs wait in
              the GitHub queue. Oakestra does not schedule containers by architecture, so a
              specific architecture only works when you pin a cluster whose workers all have it.
            </FieldDescription>
            {errors.arch ? <FieldError errors={[{ message: errors.arch }]} /> : null}

            <Field>
              <FieldLabel htmlFor="pool-cluster">Cluster</FieldLabel>
              <Select
                value={state.cluster || ANY}
                onValueChange={(value) =>
                  patch({ cluster: value === ANY ? '' : value, node: value === ANY ? '' : state.node })
                }
              >
                <SelectTrigger id="pool-cluster" className="ghr:w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ANY}>Any cluster</SelectItem>
                  {(clusters.data ?? []).map((cluster) => (
                    <SelectItem key={cluster.id} value={cluster.name}>
                      {describeCluster(cluster)}
                    </SelectItem>
                  ))}
                  {state.cluster && !selectedCluster ? (
                    <SelectItem value={state.cluster}>{state.cluster}</SelectItem>
                  ) : null}
                </SelectContent>
              </Select>
              {clusters.isError ? (
                <FieldDescription>Could not load the clusters. Existing pins are kept.</FieldDescription>
              ) : null}
            </Field>
            {archMissing ? (
              <Alert variant="warning">
                <TriangleAlertIcon aria-hidden />
                <AlertDescription>
                  Cluster {state.cluster} lists {selectedCluster?.architectures?.join(', ')}, not{' '}
                  {state.arch}. Runners of this pool may never start.
                </AlertDescription>
              </Alert>
            ) : null}

            <Field data-invalid={!!errors.node}>
              <FieldLabel htmlFor="pool-node">Node</FieldLabel>
              <Input
                id="pool-node"
                value={state.node}
                autoComplete="off"
                disabled={!state.cluster}
                aria-invalid={!!errors.node}
                placeholder={state.cluster ? 'Optional' : 'Pick a cluster first'}
                onChange={(event) => patch({ node: event.target.value })}
              />
              <FieldDescription>Hostname of one worker node in the cluster. Leave empty to let Oakestra pick.</FieldDescription>
              <FieldError errors={[errors.node ? { message: errors.node } : undefined]} />
            </Field>

            <div className="ghr:grid ghr:grid-cols-2 ghr:gap-4">
              <Field>
                <FieldLabel htmlFor="pool-group">Runner group</FieldLabel>
                <Input
                  id="pool-group"
                  value={state.runner_group}
                  autoComplete="off"
                  placeholder="default"
                  onChange={(event) => patch({ runner_group: event.target.value })}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="pool-image">Image override</FieldLabel>
                <Input
                  id="pool-image"
                  value={state.image}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="Default runner image"
                  onChange={(event) => patch({ image: event.target.value })}
                />
              </Field>
            </div>
            <FieldDescription>
              The runner group has to exist on GitHub already. Use one to limit which repositories
              may use this pool. A custom image needs the GitHub runner at{' '}
              <code>/home/runner/run.sh</code>, the easiest way is to build on the official image
              and add your tools.
            </FieldDescription>

            <Field orientation="horizontal">
              <Switch id="pool-enabled" checked={state.enabled} onCheckedChange={(enabled) => patch({ enabled })} />
              <FieldLabel htmlFor="pool-enabled">Enabled</FieldLabel>
            </Field>
            <FieldDescription>
              A disabled pool takes no new jobs. Jobs that are already running finish.
            </FieldDescription>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? 'Saving...' : pool ? 'Save changes' : 'Create pool'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
