// StatusChip.tsx — the shared pass/fail result chip. Every result pill in
// the renderer (UCCards' per-row result, FreeFormPanel's free-form result
// rows, RunInspector's header badge, RunHistory's row badge, and
// WatchPanel's stopped-watch result) renders through this ONE component now,
// so the tick/cross icon and the "Passed"/"Failed" copy can't drift between
// call sites again: two of the five sites used to hand-roll the same chip
// with their own copy of the icon SVGs, and three rendered bare text with no
// icon at all — the approved mockup shows the tick/cross on every one of
// them.
import type { JSX } from 'react';
import { CrossIcon, TickIcon } from './icons';

export type StatusChipState = 'passed' | 'failed';

export interface StatusChipProps {
  state: StatusChipState;
}

export function StatusChip({ state }: StatusChipProps): JSX.Element {
  const passed = state === 'passed';
  return (
    <span className={`chip ${passed ? 'pass' : 'fail'}`}>
      {passed ? TickIcon : CrossIcon}
      {passed ? 'Passed' : 'Failed'}
    </span>
  );
}

export default StatusChip;
