import { describe, expect, it } from 'vitest';

import type { PoolInput } from '../api/types';
import { suggestPoolName, validatePool } from './validation';

const valid: PoolInput = {
  connection_id: 'c1',
  name: 'oakestra-4vcpu-ubuntu-2404',
  vcpus: 4,
  memory_mb: 8192,
  arch: '',
  max_runners: 10,
  cluster: '',
  node: '',
  runner_group: '',
  image: '',
  enabled: true,
};

describe('suggestPoolName', () => {
  it('proposes the size and adds -arm for arm64', () => {
    expect(suggestPoolName(4, '')).toBe('oakestra-4vcpu-ubuntu-2404');
    expect(suggestPoolName(8, 'amd64')).toBe('oakestra-8vcpu-ubuntu-2404');
    expect(suggestPoolName(2, 'arm64')).toBe('oakestra-2vcpu-ubuntu-2404-arm');
  });
});

describe('validatePool', () => {
  it('accepts a valid pool', () => {
    expect(validatePool(valid)).toEqual({});
  });

  it.each(['', 'Upper', '-leading', 'has space', 'a'.repeat(64)])('rejects the name %j', (name) => {
    expect(validatePool({ ...valid, name }).name).toBeDefined();
  });

  it.each(['a', '0', 'a.b_c-d', 'a'.repeat(63)])('accepts the name %j', (name) => {
    expect(validatePool({ ...valid, name }).name).toBeUndefined();
  });

  it('checks the ranges', () => {
    expect(validatePool({ ...valid, vcpus: 0 }).vcpus).toBeDefined();
    expect(validatePool({ ...valid, vcpus: 257 }).vcpus).toBeDefined();
    expect(validatePool({ ...valid, vcpus: 256 }).vcpus).toBeUndefined();
    expect(validatePool({ ...valid, vcpus: NaN }).vcpus).toBeDefined();
    expect(validatePool({ ...valid, memory_mb: 255 }).memory_mb).toBeDefined();
    expect(validatePool({ ...valid, memory_mb: 256 }).memory_mb).toBeUndefined();
    expect(validatePool({ ...valid, max_runners: 0 }).max_runners).toBeDefined();
    expect(validatePool({ ...valid, max_runners: 101 }).max_runners).toBeDefined();
    expect(validatePool({ ...valid, max_runners: 100 }).max_runners).toBeUndefined();
  });

  it('requires a cluster for a node and for a specific architecture', () => {
    expect(validatePool({ ...valid, node: 'w1' }).node).toBeDefined();
    expect(validatePool({ ...valid, arch: 'arm64' }).arch).toBeDefined();
    expect(validatePool({ ...valid, arch: 'arm64', cluster: 'edge' }).arch).toBeUndefined();
    expect(validatePool({ ...valid, node: 'w1', cluster: 'edge' }).node).toBeUndefined();
  });

  it('requires a connection', () => {
    expect(validatePool({ ...valid, connection_id: '' }).connection_id).toBeDefined();
  });
});
