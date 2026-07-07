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

export type StatusChipState = 'passed' | 'failed';

// Icons ported from the design mockup's status chips — currentColor
// stroke, aria-hidden (the chip's own text label carries the accessible
// name). Defined ONCE, here, and exported so the one other pass/fail-shaped
// surface that needs the tick alone (StepDetail's ValidationBadge, for its
// "Valid" state) can reuse it rather than re-declaring it a third time.
export const TickIcon = (
  <svg className="ic tick" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={3} aria-hidden="true">
    <path d="M5 13l4 4L19 7" />
  </svg>
);
export const CrossIcon = (
  <svg className="ic cross" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={3} aria-hidden="true">
    <path d="M6 6l12 12M18 6L6 18" />
  </svg>
);

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
