import { test, expect } from 'vitest';
import { deriveHealth } from './HealthPill';
import type { ChildStatus } from './types';
// ChildStatus requires detail:string + pid:number (types.ts) — the fixture must supply them or tsc fails.
const ch = (name: string, state: string, restarts = 0): ChildStatus => ({ name, state, detail: '', pid: 0, restarts });
const ready = (n: number) => Array.from({ length: n }, (_, i) => ch(`c${i}`, 'ready'));
test('all ready', () => expect(deriveHealth(ready(4)).level).toBe('ready'));
test('still starting', () => expect(deriveHealth([...ready(2), ch('g', 'starting')]).level).toBe('starting'));
test('degraded when a child fails mid-run', () =>
  expect(deriveHealth([...ready(3), ch('validator', 'failed', 1)]).level).toBe('degraded'));
test('degraded while a child is restarting', () =>
  expect(deriveHealth([...ready(3), ch('data-server', 'restarting', 2)]).level).toBe('degraded'));
test('no children at all reads as unknown ("no status"), not calm starting progress', () => {
  const health = deriveHealth([]);
  expect(health.level).toBe('unknown');
  expect(health.label).toBe('No status');
});
