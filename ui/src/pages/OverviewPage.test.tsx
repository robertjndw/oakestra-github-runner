import { screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { baseStatus, fakeBackend, pool, renderPage } from '../test/host';
import OverviewPage, { buildSteps } from './OverviewPage';

function step(name: string) {
  return screen.getByText(name).closest('li') as HTMLElement;
}

describe('OverviewPage', () => {
  it('shows every step as open on a fresh install and links to the page that fixes it', async () => {
    const { addonFetch } = fakeBackend({ 'GET /status': baseStatus, 'GET /pools': [] });
    renderPage(<OverviewPage />, { addonFetch });

    await screen.findByText('Oakestra service user');
    expect(step('Oakestra service user').dataset.state).toBe('todo');
    expect(step('GitHub connection').dataset.state).toBe('todo');
    expect(step('Runner pool').dataset.state).toBe('todo');
    expect(within(step('Oakestra service user')).getByRole('link', { name: 'Set up' }).getAttribute('href')).toMatch(
      /\/settings$/,
    );
    expect(within(step('Runner pool')).getByRole('link', { name: 'Create a pool' }).getAttribute('href')).toMatch(
      /\/pools$/,
    );
  });

  it('flags a failed Oakestra login with the backend error', async () => {
    const { addonFetch } = fakeBackend({
      'GET /status': {
        ...baseStatus,
        oakestra: { configured: true, ok: false, error: 'invalid password', username: 'ghr' },
      },
      'GET /pools': [],
    });
    renderPage(<OverviewPage />, { addonFetch });

    await screen.findByText(/invalid password/);
    expect(step('Oakestra service user').dataset.state).toBe('error');
  });

  it('marks finished steps, shows the counts and a copyable runs-on snippet', async () => {
    const { addonFetch } = fakeBackend({
      'GET /status': {
        ...baseStatus,
        oakestra: { configured: true, ok: true, username: 'ghr' },
        connections: { total: 1, ready: 1 },
        pools: { total: 1, enabled: 1 },
        runners: { pending: 1, idle: 2, busy: 3 },
      },
      'GET /pools': [pool],
    });
    renderPage(<OverviewPage />, { addonFetch });

    expect(await screen.findByText('runs-on: oakestra-4vcpu-ubuntu-2404')).toBeTruthy();
    expect(step('Oakestra service user').dataset.state).toBe('done');
    expect(step('GitHub connection').dataset.state).toBe('done');
    expect(step('Runner pool').dataset.state).toBe('done');
    expect(screen.getByText('Pending runners')).toBeTruthy();
    expect(screen.getByText('3')).toBeTruthy();
    expect(screen.getByText('2')).toBeTruthy();
    expect(screen.getByRole('button', { name: /Copy runs-on for oakestra-4vcpu-ubuntu-2404/ })).toBeTruthy();
  });
});

describe('buildSteps', () => {
  it('asks to finish a connection that exists but is not ready', () => {
    const steps = buildSteps({ ...baseStatus, connections: { total: 1, ready: 0 } });
    expect(steps[1]?.state).toBe('todo');
    expect(steps[1]?.detail).toMatch(/not ready/);
  });

  it('treats a pool that exists but is disabled as open', () => {
    const steps = buildSteps({ ...baseStatus, pools: { total: 2, enabled: 0 } });
    expect(steps[2]?.state).toBe('todo');
    expect(steps[2]?.detail).toMatch(/disabled/);
  });
});
