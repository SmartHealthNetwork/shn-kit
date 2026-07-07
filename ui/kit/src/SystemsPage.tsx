// SystemsPage.tsx — the "Systems" workbench destination: connectivity,
// child health, verify probes, and the reset/restart affordances. A thin
// wrapper around StatusPanel, which owns the full-width diagnostics card
// grid — kept separate so StatusPanel.test.tsx's detailed
// behavior suite keeps rendering a stable, directly-testable target. It
// forwards StatusPanel's props unchanged.
import type { JSX } from 'react';
import { StatusPanel } from './StatusPanel';
import type { StatusPanelProps } from './StatusPanel';

export function SystemsPage(props: StatusPanelProps): JSX.Element {
  return <StatusPanel {...props} />;
}
