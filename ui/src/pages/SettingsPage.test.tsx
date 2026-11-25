import { fireEvent, screen, waitFor } from '@testing-library/react';
import { useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { BASE_PATH, baseStatus, errorResponse, fakeBackend, readyConnection, renderPage } from '../test/host';
import SettingsPage from './SettingsPage';

function Location() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

const pendingApp = {
  id: 'c2',
  name: 'acme-app',
  config_url: 'https://github.com/acme',
  auth_kind: 'app',
  status: 'pending_installation',
  app_html_url: 'https://github.com/apps/acme-app',
  created_at: '2026-01-01T00:00:00Z',
};

afterEach(() => vi.restoreAllMocks());

describe('GitHub App manifest flow', () => {
  it('completes the callback from the URL, strips the parameters and offers the installation', async () => {
    const backend = fakeBackend({
      'GET /status': baseStatus,
      'GET /connections': [pendingApp],
      'POST /github/app-manifest/complete': pendingApp,
      'GET /connections/c2/installations': [
        { id: 77, account: 'acme', target_type: 'Organization', html_url: 'https://github.com/organizations/acme' },
      ],
    });
    renderPage(
      <>
        <SettingsPage />
        <Location />
      </>,
      { addonFetch: backend.addonFetch, route: `${BASE_PATH}/settings?code=abc123&state=st4te&keep=1` },
    );

    await waitFor(() =>
      expect(backend.calls.find((call) => call.path === '/github/app-manifest/complete')?.body).toEqual({
        code: 'abc123',
        state: 'st4te',
      }),
    );
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe(`${BASE_PATH}/settings?keep=1`));

    expect(await screen.findByText('Finish connecting acme-app')).toBeTruthy();
    const install = screen.getByRole('link', { name: /Install the GitHub App/ });
    expect(install.getAttribute('href')).toBe('https://github.com/apps/acme-app/installations/new');
    expect(await screen.findByRole('button', { name: 'Use this installation' })).toBeTruthy();
    // Exactly one exchange: the code is single use.
    expect(backend.calls.filter((call) => call.path === '/github/app-manifest/complete')).toHaveLength(1);
  });

  it('binds the chosen installation', async () => {
    const backend = fakeBackend({
      'GET /status': baseStatus,
      'GET /connections': [pendingApp],
      'GET /connections/c2/installations': [
        { id: 77, account: 'acme', target_type: 'Organization', html_url: 'x' },
      ],
      'PUT /connections/c2/installation': { ...pendingApp, status: 'ready' },
    });
    renderPage(<SettingsPage />, { addonFetch: backend.addonFetch });

    fireEvent.click(await screen.findByRole('button', { name: 'Use this installation' }));
    await waitFor(() =>
      expect(backend.calls.find((call) => call.method === 'PUT')?.body).toEqual({ installation_id: 77 }),
    );
  });

  it('asks the backend for a manifest and posts it to GitHub as a form', async () => {
    const manifest = { name: 'acme-runners', url: 'https://example.test' };
    const backend = fakeBackend({
      'GET /status': baseStatus,
      'GET /connections': [],
      'POST /github/app-manifest': {
        post_url: 'https://github.com/organizations/acme/settings/apps/new?state=s1',
        manifest,
        state: 's1',
      },
    });
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(function (this: HTMLFormElement) {
      const field = this.querySelector<HTMLInputElement>('input[name="manifest"]');
      expect(this.method.toLowerCase()).toBe('post');
      expect(this.action).toBe('https://github.com/organizations/acme/settings/apps/new?state=s1');
      expect(field && JSON.parse(field.value)).toEqual(manifest);
    });
    // The redirect URL comes from the browser location, which the memory router does not move.
    window.history.pushState({}, '', `${BASE_PATH}/settings?stale=1#top`);
    renderPage(<SettingsPage />, { addonFetch: backend.addonFetch, route: `${BASE_PATH}/settings` });

    fireEvent.change(screen.getByLabelText('Name', { selector: '#app-name' }), { target: { value: 'acme-runners' } });
    fireEvent.change(screen.getByLabelText('Organization or repository URL', { selector: '#app-url' }), {
      target: { value: 'https://github.com/acme' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));

    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    const request = backend.calls.find((call) => call.path === '/github/app-manifest');
    expect(request?.body).toMatchObject({
      name: 'acme-runners',
      config_url: 'https://github.com/acme',
      // Absolute, and without any query the page had.
      redirect_url: expect.stringMatching(new RegExp(`^https?://[^/]+${BASE_PATH}/settings$`)),
    });
  });

  it('rejects a URL that is not an organization or repository before calling the backend', () => {
    const backend = fakeBackend({ 'GET /status': baseStatus, 'GET /connections': [] });
    renderPage(<SettingsPage />, { addonFetch: backend.addonFetch });

    fireEvent.change(screen.getByLabelText('Name', { selector: '#app-name' }), { target: { value: 'x' } });
    fireEvent.change(screen.getByLabelText('Organization or repository URL', { selector: '#app-url' }), {
      target: { value: 'https://example.com/x' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Continue on GitHub' }));

    expect(screen.getByText(/Use https:\/\/github.com\//)).toBeTruthy();
    expect(backend.calls.some((call) => call.method === 'POST')).toBe(false);
  });
});

describe('connections and credentials', () => {
  it('explains a 409 when pools still use the connection', async () => {
    const backend = fakeBackend({
      'GET /status': baseStatus,
      'GET /connections': [readyConnection],
      'DELETE /connections/c1': errorResponse(409, 'conflict', 'connection is used by pools'),
    });
    renderPage(<SettingsPage />, { addonFetch: backend.addonFetch });

    fireEvent.click(await screen.findByRole('button', { name: 'Delete acme' }));
    fireEvent.click(screen.getByRole('button', { name: 'Delete connection' }));

    expect(await screen.findByText(/Pools still use this connection/)).toBeTruthy();
  });

  it('shows environment credentials read-only', async () => {
    const backend = fakeBackend({
      'GET /status': { ...baseStatus, oakestra: { configured: true, ok: true, source: 'env', username: 'ghr' } },
      'GET /connections': [],
    });
    renderPage(<SettingsPage />, { addonFetch: backend.addonFetch });

    expect(await screen.findByText(/cannot be changed here/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Save credentials' })).toBeNull();
  });

  it('saves stored credentials', async () => {
    const backend = fakeBackend({
      'GET /status': baseStatus,
      'GET /connections': [],
      'PUT /oakestra-credentials': { configured: true, ok: true, source: 'stored', username: 'ghr' },
    });
    renderPage(<SettingsPage />, { addonFetch: backend.addonFetch });

    fireEvent.change(await screen.findByLabelText('Username'), { target: { value: 'ghr' } });
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'secret' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save credentials' }));

    await waitFor(() =>
      expect(backend.calls.find((call) => call.method === 'PUT')?.body).toEqual({
        username: 'ghr',
        password: 'secret',
        organization: undefined,
      }),
    );
  });
});

describe('without the Admin role', () => {
  it('disables every write action and says why', async () => {
    const backend = fakeBackend({
      'GET /status': baseStatus,
      'GET /connections': [readyConnection],
    });
    renderPage(<SettingsPage />, { addonFetch: backend.addonFetch, admin: false });

    expect(await screen.findByText(/only administrators can change it/)).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Continue on GitHub' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Connect' }) as HTMLButtonElement).disabled).toBe(true);
    expect((await screen.findByRole('button', { name: 'Delete acme' }) as HTMLButtonElement).disabled).toBe(true);
    expect((await screen.findByRole('button', { name: 'Save credentials' }) as HTMLButtonElement).disabled).toBe(true);
  });
});
