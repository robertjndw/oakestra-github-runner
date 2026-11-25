import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { fakeBackend, pool, renderPage } from '../test/host';
import { ClusterRunnersWidget } from '../widgets/ClusterRunnersWidget';
import RunnersPage from './RunnersPage';

const runner = (name: string, extra = {}) => ({
  name,
  pool_id: 'p1',
  service_id: `s-${name}`,
  state: 'idle',
  created_at: new Date().toISOString(),
  ...extra,
});

describe('RunnersPage', () => {
  it('explains that runners appear when jobs are queued', async () => {
    const backend = fakeBackend({ 'GET /runners': [], 'GET /pools': [pool], 'GET /clusters': [] });
    renderPage(<RunnersPage />, { addonFetch: backend.addonFetch });
    expect(await screen.findByText(/Runners appear when jobs are queued/)).toBeTruthy();
  });

  it('lists runners with pool, state, cluster name and Oakestra status', async () => {
    const backend = fakeBackend({
      'GET /runners': [runner('ghr1', { state: 'busy', cluster_id: 'k1', oakestra_status: 'RUNNING', job_request_id: 42 })],
      'GET /pools': [pool],
      'GET /clusters': [{ id: 'k1', name: 'edge', active_nodes: 1, vcpus: 1, memory_mb: 1 }],
    });
    renderPage(<RunnersPage />, { addonFetch: backend.addonFetch });

    expect(await screen.findByText('ghr1')).toBeTruthy();
    expect(screen.getByText('oakestra-4vcpu-ubuntu-2404')).toBeTruthy();
    expect(screen.getByText('Busy')).toBeTruthy();
    expect(screen.getByText('RUNNING')).toBeTruthy();
    expect(await screen.findByText('edge')).toBeTruthy();
    expect(screen.getByText('#42')).toBeTruthy();
  });
});

describe('ClusterRunnersWidget', () => {
  it('lists only the runners of its cluster', async () => {
    const backend = fakeBackend({
      'GET /runners': [runner('here', { cluster_id: 'k1' }), runner('elsewhere', { cluster_id: 'k2' })],
    });
    renderPage(<ClusterRunnersWidget clusterId="k1" />, { addonFetch: backend.addonFetch });

    expect(await screen.findByText('here')).toBeTruthy();
    expect(screen.queryByText('elsewhere')).toBeNull();
  });
});
