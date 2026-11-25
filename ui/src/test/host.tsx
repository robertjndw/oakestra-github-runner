import { renderInHost, type RenderInHostOptions } from '@oakestra/dashboard-sdk/testing';
import type { ReactElement } from 'react';
import { ADDON_ID } from 'virtual:oakestra-addon';

type Handler = (request: Request, body: unknown) => unknown;

export interface BackendCall {
  method: string;
  path: string;
  search: string;
  body: unknown;
}

const json = (data: unknown, status = 200) =>
  new Response(status === 204 ? null : JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

/**
 * Fake addon backend for `addonFetch`. Keys are "METHOD /path" below /api. A handler returns the
 * JSON body, or a Response for other statuses, or a promise of either to hold the answer back.
 * Unknown routes answer 404 so a page cannot call more than a test expects without noticing.
 */
export function fakeBackend(routes: Record<string, Handler | unknown>) {
  const calls: BackendCall[] = [];
  const addonFetch = async (request: Request) => {
    const url = new URL(request.url);
    const path = url.pathname.replace(/^.*\/api/, '');
    const text = await request.text();
    const body = text ? (JSON.parse(text) as unknown) : undefined;
    calls.push({ method: request.method, path, search: url.search, body });
    const route = routes[`${request.method} ${path}`];
    if (route === undefined) {
      return json({ code: 'not_found', message: `No fake route for ${request.method} ${path}` }, 404);
    }
    const value = typeof route === 'function' ? await (route as Handler)(request, body) : route;
    return value instanceof Response ? value : json(value);
  };
  return { addonFetch, calls };
}

/** A promise the test resolves by hand, for answers that must arrive after others. */
export function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

export function errorResponse(status: number, code: string, message: string) {
  return json({ code, message }, status);
}

export const ADMIN = { username: 'admin', roles: ['Admin'] };
export const VIEWER = { username: 'viewer', roles: ['Application_Provider'] };

export const BASE_PATH = `/addons/${ADDON_ID}`;

export function renderPage(
  ui: ReactElement,
  options: RenderInHostOptions & { admin?: boolean } = {},
) {
  const { admin = true, ...rest } = options;
  return renderInHost(ui, {
    user: admin ? ADMIN : VIEWER,
    addon: { addonId: ADDON_ID },
    ...rest,
  });
}

export const baseStatus = {
  version: 'test',
  runner_image: 'ghcr.io/actions/actions-runner:latest',
  oakestra: { configured: false, ok: false },
  connections: { total: 0, ready: 0 },
  pools: { total: 0, enabled: 0 },
  runners: { pending: 0, idle: 0, busy: 0 },
};

export const readyConnection = {
  id: 'c1',
  name: 'acme',
  config_url: 'https://github.com/acme',
  auth_kind: 'pat',
  status: 'ready',
  created_at: '2026-01-01T00:00:00Z',
};

export const pool = {
  id: 'p1',
  connection_id: 'c1',
  name: 'oakestra-4vcpu-ubuntu-2404',
  vcpus: 4,
  memory_mb: 8192,
  max_runners: 10,
  enabled: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};
