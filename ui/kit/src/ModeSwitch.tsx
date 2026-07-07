// ModeSwitch.tsx — the two-lane segmented control, now part of the
// persistent TopBar chrome (formerly UCCards' own .lane-tabs block). Pure
// presentational + dispatch: the caller (App, via TopBar) owns `lane` state.
import type { JSX } from 'react';
import type { Lane } from './types';
import { LANE_LABELS, LANES } from './ucmeta';

export interface ModeSwitchProps {
  lane: Lane;
  onLane(l: Lane): void;
}

export function ModeSwitch({ lane, onLane }: ModeSwitchProps): JSX.Element {
  return (
    <div className="seg" role="tablist" aria-label="lane">
      {LANES.map((l) => (
        <button
          key={l}
          type="button"
          role="tab"
          aria-selected={lane === l}
          // These tabs are a SELECTION control (mutually-exclusive lane
          // choice), not a toggle — aria-pressed is for toggle buttons.
          // aria-current names the selected item among a set of related
          // items/pages, the correct semantics here (paired with
          // role="tab"'s own aria-selected above).
          aria-current={lane === l ? 'true' : undefined}
          onClick={() => onLane(l)}
        >
          <span className="d" aria-hidden="true" />
          {LANE_LABELS[l].short}
        </button>
      ))}
    </div>
  );
}
