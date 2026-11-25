import { fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { fakeBackend, pool, readyConnection, renderPage } from '../test/host';
import { PoolDialog } from './PoolDialog';

const clusters = [
  { id: 'k1', name: 'edge', active_nodes: 3, vcpus: 12, memory_mb: 24576, architectures: ['amd64'] },
];

function setup(existing?: typeof pool & { arch?: string; cluster?: string }) {
  const backend = fakeBackend({
    'GET /connections': [readyConnection],
    'GET /clusters': clusters,
    'POST /pools': (_request: Request, body: unknown) => ({ ...pool, ...(body as object), id: 'new' }),
    'PUT /pools/p1': (_request: Request, body: unknown) => ({ ...pool, ...(body as object) }),
  });
  const onOpenChange = vi.fn();
  const view = renderPage(<PoolDialog open onOpenChange={onOpenChange} pool={existing} />, {
    addonFetch: backend.addonFetch,
  });
  return { ...backend, onOpenChange, view };
}

// The select shows the first ready connection once the list has loaded.
const connectionLoaded = () =>
  waitFor(() => expect(screen.getByLabelText('Connection').textContent).toContain('acme'));

const nameInput = () => screen.getByLabelText('Name') as HTMLInputElement;

describe('PoolDialog', () => {
  it('proposes a name that follows the size until it is edited', async () => {
    setup();
    await connectionLoaded();
    expect(nameInput().value).toBe('oakestra-4vcpu-ubuntu-2404');

    fireEvent.click(screen.getByRole('radio', { name: '8 vCPU' }));
    expect(nameInput().value).toBe('oakestra-8vcpu-ubuntu-2404');

    fireEvent.change(nameInput(), { target: { value: 'my-runners' } });
    fireEvent.click(screen.getByRole('radio', { name: '16 vCPU' }));
    expect(nameInput().value).toBe('my-runners');
    expect(screen.getByText('oakestra-16vcpu-ubuntu-2404')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Use it' }));
    expect(nameInput().value).toBe('oakestra-16vcpu-ubuntu-2404');
  });

  it('shows validation errors and does not call the backend', async () => {
    const { calls } = setup();
    await connectionLoaded();

    fireEvent.change(nameInput(), { target: { value: 'Bad Name' } });
    fireEvent.change(screen.getByLabelText('Max runners'), { target: { value: '101' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create pool' }));

    expect(await screen.findByText(/Use lowercase letters/)).toBeTruthy();
    expect(screen.getByText('Enter a whole number from 1 to 100.')).toBeTruthy();
    expect(calls.some((call) => call.method === 'POST')).toBe(false);
  });

  it('asks for custom CPU and memory and validates them', async () => {
    const { calls } = setup();
    await connectionLoaded();

    fireEvent.click(screen.getByRole('radio', { name: 'Custom' }));
    fireEvent.change(screen.getByLabelText('vCPUs'), { target: { value: '300' } });
    fireEvent.change(screen.getByLabelText('Memory (MB)'), { target: { value: '128' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create pool' }));

    expect(await screen.findByText('Enter a whole number from 1 to 256.')).toBeTruthy();
    expect(screen.getByText('Enter at least 256 MB.')).toBeTruthy();
    expect(calls.some((call) => call.method === 'POST')).toBe(false);
  });

  it('creates the pool with the preset size and 2 GB per vCPU', async () => {
    const { calls, onOpenChange } = setup();
    await connectionLoaded();

    fireEvent.click(screen.getByRole('radio', { name: '8 vCPU' }));
    fireEvent.click(screen.getByRole('button', { name: 'Create pool' }));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    const post = calls.find((call) => call.method === 'POST' && call.path === '/pools');
    expect(post?.body).toMatchObject({
      connection_id: 'c1',
      name: 'oakestra-8vcpu-ubuntu-2404',
      vcpus: 8,
      memory_mb: 16384,
      arch: '',
      max_runners: 10,
      enabled: true,
    });
  });

  it('refuses a specific architecture without a pinned cluster and explains why', async () => {
    const { calls } = setup({ ...pool, arch: 'arm64' });
    await connectionLoaded();
    expect(screen.getByText(/does not schedule containers by architecture/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

    expect(await screen.findByText('Pin a cluster to use a specific architecture.')).toBeTruthy();
    expect(calls.some((call) => call.method === 'PUT')).toBe(false);
  });

  it('warns when the pinned cluster does not list the chosen architecture', async () => {
    setup({ ...pool, arch: 'arm64', cluster: 'edge' });
    expect(await screen.findByText(/lists amd64, not arm64/)).toBeTruthy();
  });
});
