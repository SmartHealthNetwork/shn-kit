// TopBar.tsx — persistent top-bar chrome: wordmark, SSE connection dot,
// the ModeSwitch (lane), a health rollup slot, signed-in identity, and the
// update-banner slot. Rendered once per phase by App.tsx so the mode
// switch/connection state survive every phase transition (mirrors the old
// app-header's "one mount that survives every phase" role).
import type { JSX } from 'react';
import type { ChildStatus, Lane } from './types';
import type { SSEState } from './useEvents';
import { ModeSwitch } from './ModeSwitch';
import { HealthPill } from './HealthPill';

export interface TopBarProps {
  lane: Lane;
  onLane(l: Lane): void;
  sseState: SSEState;
  // Child process statuses, rolled up into the HealthPill.
  children: ChildStatus[];
  identity: { email?: string; holderId?: string };
  updateBanner?: JSX.Element;
}

function initials(email: string): string {
  const local = email.split('@')[0] ?? '';
  return local.slice(0, 2).toUpperCase();
}

export function TopBar({ lane, onLane, sseState, children, identity, updateBanner }: TopBarProps): JSX.Element {
  return (
    <header className="topbar">
      <div className="wordmark">
        <span className="glyph">S</span> SHN Kit
      </div>
      <span
        className={`connection-dot connection-${sseState}`}
        aria-label={`events ${sseState}`}
      />

      <ModeSwitch lane={lane} onLane={onLane} />

      <div className="spring" />

      {updateBanner}

      <div className="cluster">
        <HealthPill children={children} />
        {identity.email && (
          <span className="who">
            <span className="av">{initials(identity.email)}</span>
            {identity.email}
          </span>
        )}
      </div>
    </header>
  );
}
