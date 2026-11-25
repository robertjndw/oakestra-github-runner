import { fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { deferred, errorResponse, fakeBackend, pool, readyConnection, renderPage } from '../test/host';
import PoolsPage from './PoolsPage';

const routes = {
  'GET /pools': [{ ...pool, last_error: 'scale set rejected the name' }],
  'GET /connections': [readyConnection],
  'GET /clusters': [],
};

describe('PoolsPage', () => {
  it('shows a pool last_error in the table', async () => {
    const { addonFetch } = fakeBackend(routes);
    renderPage(<PoolsPage />, { addonFetch });

    expect(await screen.findByText('scale set rejected the name')).toBeTruthy();
    expect(screen.getByText('Error')).toBeTruthy();
  });

  it('resolves connection names that arrive after the pools', async () => {
    const connections = deferred<unknown>();
    const { addonFetch } = fakeBackend({ ...routes, 'GET /connections': () => connections.promise });
    renderPage(<PoolsPage />, { addonFetch });

    expect(await screen.findByText('c1')).toBeTruthy();
    connections.resolve([readyConnection]);
    expect(await screen.findByText('acme')).toBeTruthy();
  });

  it('disables creating and changing pools for non-admins', async () => {
    const { addonFetch } = fakeBackend(routes);
    renderPage(<PoolsPage />, { addonFetch, admin: false });

    const create = screen.getByRole('button', { name: /Create pool/ }) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    expect(create.title).toMatch(/administrators/);
    expect(await screen.findByText(/only administrators can change it/)).toBeTruthy();
  });

  it('opens the create dialog for admins', async () => {
    const { addonFetch } = fakeBackend(routes);
    renderPage(<PoolsPage />, { addonFetch });

    const create = screen.getByRole('button', { name: /Create pool/ }) as HTMLButtonElement;
    expect(create.disabled).toBe(false);
    fireEvent.click(create);
    expect(await screen.findByRole('dialog')).toBeTruthy();
  });

  it('reports a 403 from the backend', async () => {
    const backend = fakeBackend({
      ...routes,
      'POST /pools': errorResponse(403, 'forbidden', 'admin role required'),
    });
    const view = renderPage(<PoolsPage />, { addonFetch: backend.addonFetch });

    fireEvent.click(screen.getByRole('button', { name: /Create pool/ }));
    await waitFor(() =>
      expect((screen.getByLabelText('Connection').textContent ?? '').includes('acme')).toBe(true),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Create pool', hidden: false }));

    await waitFor(() => expect(view.notify.calls.at(-1)).toMatchObject({ level: 'error' }));
    expect(view.notify.calls.at(-1)?.message).toMatch(/administrators/);
    // The dialog stays open so nothing typed is lost.
    expect(screen.getByRole('dialog')).toBeTruthy();
  });
});
