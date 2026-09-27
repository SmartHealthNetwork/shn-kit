// HealthPill.tsx — the small pill that renders the TopBar's health rollup.
// The rollup itself, deriveHealth, lives in health.ts.
import type { JSX } from 'react';
import type { ChildStatus } from './types';
import { deriveHealth } from './health';

export function HealthPill({ children }: { children: ChildStatus[] }): JSX.Element {
  const health = deriveHealth(children);
  return (
    <span className="health" data-level={health.level}>
      <span className="pulse" />
      {health.label}
    </span>
  );
}
