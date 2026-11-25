import { addonQueryKey, useNotify } from '@oakestra/dashboard-sdk';
import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { ADDON_ID } from 'virtual:oakestra-addon';

import { errorMessage, useSend } from './client';
import type {
  AppManifest,
  Cluster,
  Connection,
  Installation,
  JobPage,
  OakestraStatus,
  Pool,
  PoolInput,
  Runner,
  Status,
} from './types';

/** Live views refetch on this interval. */
export const POLL_MS = 5000;

export const keys = {
  all: () => addonQueryKey(ADDON_ID),
  status: () => addonQueryKey(ADDON_ID, 'status'),
  runners: () => addonQueryKey(ADDON_ID, 'runners'),
  jobs: (poolId: string, result: string) => addonQueryKey(ADDON_ID, 'jobs', poolId, result),
  pools: () => addonQueryKey(ADDON_ID, 'pools'),
  connections: () => addonQueryKey(ADDON_ID, 'connections'),
  clusters: () => addonQueryKey(ADDON_ID, 'clusters'),
  installations: (id: string) => addonQueryKey(ADDON_ID, 'installations', id),
};

export function useStatus() {
  const send = useSend();
  return useQuery({
    queryKey: keys.status(),
    queryFn: ({ signal }) => send<Status>('GET', '/status', undefined, signal),
    refetchInterval: POLL_MS,
  });
}

export function useRunners() {
  const send = useSend();
  return useQuery({
    queryKey: keys.runners(),
    queryFn: ({ signal }) => send<Runner[]>('GET', '/runners', undefined, signal),
    refetchInterval: POLL_MS,
  });
}

export const JOBS_PAGE_SIZE = 50;

export function useJobs(poolId: string, result: string) {
  const send = useSend();
  return useInfiniteQuery({
    queryKey: keys.jobs(poolId, result),
    queryFn: ({ pageParam, signal }) => {
      const params = new URLSearchParams({ limit: String(JOBS_PAGE_SIZE) });
      if (poolId) params.set('pool_id', poolId);
      if (result) params.set('result', result);
      if (pageParam) params.set('before', pageParam);
      return send<JobPage>('GET', `/jobs?${params}`, undefined, signal);
    },
    initialPageParam: '',
    getNextPageParam: (last) => last.next || undefined,
    placeholderData: keepPreviousData,
    // Refetching re-reads every loaded page, so the list stays consistent while jobs finish.
    refetchInterval: POLL_MS,
  });
}

export function usePools() {
  const send = useSend();
  return useQuery({
    queryKey: keys.pools(),
    queryFn: ({ signal }) => send<Pool[]>('GET', '/pools', undefined, signal),
  });
}

export function useConnections() {
  const send = useSend();
  return useQuery({
    queryKey: keys.connections(),
    queryFn: ({ signal }) => send<Connection[]>('GET', '/connections', undefined, signal),
  });
}

export function useClusters() {
  const send = useSend();
  return useQuery({
    queryKey: keys.clusters(),
    queryFn: ({ signal }) => send<Cluster[]>('GET', '/clusters', undefined, signal),
    // Names are a nicety, so callers fall back to ids when this fails and a retry only delays that.
    retry: false,
    staleTime: 30_000,
  });
}

export function useInstallations(connectionId: string | undefined) {
  const send = useSend();
  return useQuery({
    queryKey: keys.installations(connectionId ?? ''),
    queryFn: ({ signal }) =>
      send<Installation[]>('GET', `/connections/${encodeURIComponent(connectionId ?? '')}/installations`, undefined, signal),
    enabled: connectionId !== undefined,
    retry: false,
  });
}

/**
 * A mutation that refetches everything of this addon afterwards and reports failures as a toast.
 * Config changes are rare, so refreshing all queries is simpler than tracking dependencies.
 */
function useWrite<TVars, TResult>(
  run: (send: ReturnType<typeof useSend>, vars: TVars) => Promise<TResult>,
  success?: string,
) {
  const send = useSend();
  const notify = useNotify();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (vars: TVars) => run(send, vars),
    onSuccess: () => {
      if (success) notify.success(success);
    },
    onError: (error) => notify.error(errorMessage(error)),
    onSettled: () => queryClient.invalidateQueries({ queryKey: keys.all() }),
  });
}

export function useSavePool() {
  return useWrite(
    (send, vars: { id?: string; input: PoolInput }) =>
      vars.id
        ? send<Pool>('PUT', `/pools/${encodeURIComponent(vars.id)}`, vars.input)
        : send<Pool>('POST', '/pools', vars.input),
  );
}

export function useDeletePool() {
  return useWrite(
    (send, id: string) => send<void>('DELETE', `/pools/${encodeURIComponent(id)}`),
    'Pool deleted',
  );
}

export function useDeleteConnection() {
  return useWrite(
    (send, id: string) => send<void>('DELETE', `/connections/${encodeURIComponent(id)}`),
    'Connection deleted',
  );
}

export function useCreateTokenConnection() {
  return useWrite(
    (send, input: { name: string; config_url: string; token: string }) =>
      send<Connection>('POST', '/connections', input),
    'Connection created',
  );
}

export function useStartAppManifest() {
  return useWrite(
    (send, input: { name: string; config_url: string; redirect_url: string }) =>
      send<AppManifest>('POST', '/github/app-manifest', input),
  );
}

export function useCompleteAppManifest() {
  return useWrite(
    (send, input: { state: string; code: string }) =>
      send<Connection>('POST', '/github/app-manifest/complete', input),
    'GitHub App created',
  );
}

export function useSelectInstallation() {
  return useWrite(
    (send, input: { connectionId: string; installationId: number }) =>
      send<Connection>(
        'PUT',
        `/connections/${encodeURIComponent(input.connectionId)}/installation`,
        { installation_id: input.installationId },
      ),
    'Installation selected',
  );
}

export function useSaveOakestraCredentials() {
  return useWrite(
    (send, input: { username: string; password: string; organization?: string }) =>
      send<OakestraStatus>('PUT', '/oakestra-credentials', input),
    'Oakestra credentials saved',
  );
}
