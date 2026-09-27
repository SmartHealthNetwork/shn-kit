// icons.tsx — the shared pass/fail icons, ported from the design mockup's
// status chips: currentColor stroke, aria-hidden (the surrounding text label
// carries the accessible name). Defined ONCE, here, and used by StatusChip and
// by the surfaces that need the tick alone (StepDetail's ValidationBadge,
// DemoChips, BridgingPanel), rather than re-declared per site. They are element
// constants, not components, so they live outside StatusChip.tsx, which exports
// only components (react-refresh/only-export-components).
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
