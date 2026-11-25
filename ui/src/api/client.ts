import { describeError, useAddonApi } from '@oakestra/dashboard-sdk';

import type { ApiErrorBody } from './types';

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }

  get isForbidden() {
    return this.status === 403;
  }

  get isConflict() {
    return this.status === 409;
  }
}

async function toApiError(response: Response): Promise<ApiError> {
  let body: Partial<ApiErrorBody> = {};
  try {
    body = (await response.json()) as Partial<ApiErrorBody>;
  } catch {
    // A proxy in front of the backend answers with HTML or nothing. The status is all we have.
  }
  const code = body.code ?? 'http_error';
  const message =
    body.message ??
    (response.status === 502 || response.status === 504
      ? 'The runner backend is not reachable.'
      : `The request failed (HTTP ${response.status}).`);
  return new ApiError(response.status, code, message);
}

export type Send = <T>(
  method: 'GET' | 'POST' | 'PUT' | 'DELETE',
  path: string,
  body?: unknown,
  signal?: AbortSignal,
) => Promise<T>;

/** JSON calls to the addon backend through the shell, which adds the user's token. */
export function useSend(): Send {
  const api = useAddonApi();
  return async <T,>(
    method: 'GET' | 'POST' | 'PUT' | 'DELETE',
    path: string,
    body?: unknown,
    signal?: AbortSignal,
  ) => {
    const response = await api.fetch(path, {
      method,
      signal,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) throw await toApiError(response);
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  };
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return error.isForbidden ? 'Only administrators can change this.' : error.message;
  }
  return describeError(error);
}
