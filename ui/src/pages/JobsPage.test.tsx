import { fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import type { Job } from '../api/types';
import { fakeBackend, pool, renderPage } from '../test/host';
import JobsPage from './JobsPage';

const job = (id: number, extra: Partial<Job> = {}): Job => ({
  pool_id: 'p1',
  request_id: id,
  repository: `acme/repo-${id}`,
  job_name: `build-${id}`,
  workflow_ref: 'acme/repo/.github/workflows/ci.yml@refs/heads/main',
  result: 'succeeded',
  ...extra,
});

describe('JobsPage', () => {
  it('pages with the next cursor and appends the rows', async () => {
    const backend = fakeBackend({
      'GET /pools': [pool],
      'GET /clusters': [],
      'GET /jobs': (request: Request) => {
        const before = new URL(request.url).searchParams.get('before');
        return before === 'cursor-1'
          ? { items: [job(3)], next: '' }
          : { items: [job(1), job(2, { result: undefined })], next: 'cursor-1' };
      },
    });
    renderPage(<JobsPage />, { addonFetch: backend.addonFetch });

    expect(await screen.findByText('acme/repo-1')).toBeTruthy();
    // An empty result is a running job.
    expect(screen.getByText('Running')).toBeTruthy();
    expect(screen.queryByText('acme/repo-3')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Load more' }));

    expect(await screen.findByText('acme/repo-3')).toBeTruthy();
    expect(screen.getByText('acme/repo-1')).toBeTruthy();
    expect(backend.calls.some((call) => call.search.includes('before=cursor-1'))).toBe(true);
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull());
  });

  it('shows an empty state without jobs', async () => {
    const backend = fakeBackend({
      'GET /pools': [],
      'GET /clusters': [],
      'GET /jobs': { items: [], next: '' },
    });
    renderPage(<JobsPage />, { addonFetch: backend.addonFetch });
    expect(await screen.findByText('No jobs yet')).toBeTruthy();
  });
});
