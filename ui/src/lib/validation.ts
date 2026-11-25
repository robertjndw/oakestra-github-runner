import type { PoolInput } from '../api/types';

export const NAME_PATTERN = /^[a-z0-9][a-z0-9._-]{0,62}$/;

export const MB_PER_VCPU = 2048;

export const SIZE_PRESETS = [2, 4, 8, 16] as const;

/** Name proposed for a size, e.g. oakestra-4vcpu-ubuntu-2404 or oakestra-4vcpu-ubuntu-2404-arm. */
export function suggestPoolName(vcpus: number, arch: string): string {
  const base = `oakestra-${vcpus}vcpu-ubuntu-2404`;
  return arch === 'arm64' ? `${base}-arm` : base;
}

export type PoolErrors = Partial<Record<'connection_id' | 'name' | 'vcpus' | 'memory_mb' | 'max_runners' | 'node' | 'arch', string>>;

/** Same rules as the backend, so the form fails before a round trip. */
export function validatePool(input: PoolInput): PoolErrors {
  const errors: PoolErrors = {};
  if (!input.connection_id) errors.connection_id = 'Choose a connection.';
  if (!NAME_PATTERN.test(input.name)) {
    errors.name =
      'Use lowercase letters, digits, dots, dashes and underscores. Start with a letter or digit, 63 characters at most.';
  }
  if (!Number.isInteger(input.vcpus) || input.vcpus < 1 || input.vcpus > 256) {
    errors.vcpus = 'Enter a whole number from 1 to 256.';
  }
  if (!Number.isInteger(input.memory_mb) || input.memory_mb < 256) {
    errors.memory_mb = 'Enter at least 256 MB.';
  }
  if (!Number.isInteger(input.max_runners) || input.max_runners < 1 || input.max_runners > 100) {
    errors.max_runners = 'Enter a whole number from 1 to 100.';
  }
  if (input.node && !input.cluster) errors.node = 'Pick a cluster to pin a node.';
  if (input.arch && !input.cluster) {
    errors.arch = 'Pin a cluster to use a specific architecture.';
  }
  return errors;
}

/** https://github.com/<org> or https://github.com/<org>/<repo>. */
export const GITHUB_URL_PATTERN = /^https:\/\/github\.com\/[A-Za-z0-9_.-]+(\/[A-Za-z0-9_.-]+)?\/?$/;
export const GITHUB_URL_HELP = 'Use https://github.com/<organization> or https://github.com/<organization>/<repository>.';
