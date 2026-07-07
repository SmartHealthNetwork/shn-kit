// HealthPill.tsx — the TopBar's health rollup: a pure derivation off the
// child-process statuses (ChildStatus[]) plus the small pill that renders
// it. deriveHealth is exported standalone (no side effects) so it can be
// reused wherever a health-at-a-glance summary is needed.
import type { JSX } from 'react';
import type { ChildStatus } from './types';

export type HealthLevel = 'ready' | 'starting' | 'degraded' | 'unknown';

export interface Health {
  level: HealthLevel;
  ready: number;
  total: number;
  label: string;
}

// A child in any of these states is not just "not yet up" — it was running
// and stopped being so. Surfacing that as degraded (not starting) is what
// catches a child dying/restarting mid-run, not only at boot.
const DEGRADED_STATES = new Set(['failed', 'exited', 'restarting', 'stopped']);

export function deriveHealth(children: ChildStatus[]): Health {
  const total = children.length;
  const ready = children.filter((c) => c.state === 'ready').length;

  if (children.some((c) => DEGRADED_STATES.has(c.state))) {
    return { level: 'degraded', ready, total, label: 'Degraded — check Systems' };
  }
  // No child statuses at all is not "0 of 0 starting" progress — that reads
  // as calm, in-motion boot when there may be no daemon to report on (e.g.
  // the daemon is unreachable, or a restart is required). Render it as a
  // distinct, neutral "no data" state instead of implying any progress.
  if (total === 0) {
    return { level: 'unknown', ready: 0, total: 0, label: 'No status' };
  }
  if (ready < total) {
    return { level: 'starting', ready, total, label: `${ready} of ${total} starting` };
  }
  return { level: 'ready', ready, total, label: 'All systems ready' };
}

export function HealthPill({ children }: { children: ChildStatus[] }): JSX.Element {
  const health = deriveHealth(children);
  return (
    <span className="health" data-level={health.level}>
      <span className="pulse" />
      {health.label}
    </span>
  );
}
