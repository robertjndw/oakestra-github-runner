// Hand-written mirror of the JSON in internal/core/types.go of the backend.

export type AuthKind = 'app' | 'pat';
export type ConnectionStatus = 'pending_installation' | 'ready';

export interface Connection {
  id: string;
  name: string;
  config_url: string;
  auth_kind: AuthKind;
  status: ConnectionStatus;
  app_id?: number;
  app_slug?: string;
  app_client_id?: string;
  app_html_url?: string;
  installation_id?: number;
  created_at: string;
}

export type Arch = '' | 'amd64' | 'arm64';

export interface Pool {
  id: string;
  connection_id: string;
  name: string;
  vcpus: number;
  memory_mb: number;
  arch?: string;
  max_runners: number;
  cluster?: string;
  node?: string;
  runner_group?: string;
  image?: string;
  enabled: boolean;
  scale_set_id?: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

/** The writable part of a pool, as sent to POST and PUT. */
export interface PoolInput {
  connection_id: string;
  name: string;
  vcpus: number;
  memory_mb: number;
  arch: string;
  max_runners: number;
  cluster: string;
  node: string;
  runner_group: string;
  image: string;
  enabled: boolean;
}

export type RunnerState = 'pending' | 'idle' | 'busy' | 'removed';

export interface Runner {
  name: string;
  pool_id: string;
  service_id: string;
  state: RunnerState;
  oakestra_status?: string;
  cluster_id?: string;
  worker_id?: string;
  job_request_id?: number;
  created_at: string;
  running_at?: string;
  removed_at?: string;
}

export interface Job {
  pool_id: string;
  request_id: number;
  runner_name?: string;
  repository: string;
  workflow_ref?: string;
  job_name: string;
  workflow_run_id?: number;
  event_name?: string;
  result?: string;
  queued_at?: string;
  started_at?: string;
  finished_at?: string;
  cluster_id?: string;
  worker_id?: string;
}

export interface JobPage {
  items: Job[];
  next: string;
}

export interface OakestraStatus {
  configured: boolean;
  source?: 'env' | 'stored' | string;
  username?: string;
  organization?: string;
  ok: boolean;
  error?: string;
}

export interface Status {
  version: string;
  runner_image: string;
  oakestra: OakestraStatus;
  connections: { total: number; ready: number };
  pools: { total: number; enabled: number };
  runners: { pending: number; idle: number; busy: number };
}

export interface Cluster {
  id: string;
  name: string;
  location?: string;
  active_nodes: number;
  vcpus: number;
  memory_mb: number;
  architectures?: string[];
}

export interface Installation {
  id: number;
  account: string;
  target_type: string;
  html_url: string;
}

export interface AppManifest {
  post_url: string;
  manifest: Record<string, unknown>;
  state: string;
}

export interface ApiErrorBody {
  code: string;
  message: string;
}
