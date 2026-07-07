// NavRail.tsx — the workbench's left navigation rail. Four destinations
// (Scenarios, Run history, Bring your own, Systems) plus a health foot
// derived from the child-process statuses. Owns no state: the active
// destination and the switch handler come from App, so the rail is a pure
// selection control (mirrors ModeSwitch's posture).
import type { JSX } from 'react';
import type { ChildStatus } from './types';
import { deriveHealth } from './HealthPill';

export type NavDest = 'scenarios' | 'history' | 'byo' | 'systems';

interface NavItem {
  dest: NavDest;
  label: string;
  count?: string;
  icon: JSX.Element;
}

// Icons ported from the design mockup's nav rail — currentColor stroke,
// no fill, aria-hidden (the button's text label carries the a11y name).
const DocIcon = (
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <path d="M5 4h9l5 5v11H5z" />
    <path d="M14 4v5h5" />
  </svg>
);
const ClockIcon = (
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <path d="M12 8v4l3 2" />
    <circle cx="12" cy="12" r="8" />
  </svg>
);
const LinesIcon = (
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <path d="M4 7h16M4 12h16M4 17h10" />
  </svg>
);
const GridIcon = (
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <rect x="4" y="4" width="16" height="16" rx="2" />
    <path d="M9 9h6v6H9z" />
  </svg>
);

// The two primary destinations, then a "Configure" group over the two
// setup destinations. The Scenarios count is the static 8 UCs; Run history
// carries no count (it is not part of the rail's props).
const PRIMARY: NavItem[] = [
  { dest: 'scenarios', label: 'Scenarios', count: '8', icon: DocIcon },
  { dest: 'history', label: 'Run history', icon: ClockIcon },
];
const CONFIGURE: NavItem[] = [
  { dest: 'byo', label: 'Bring your own', icon: LinesIcon },
  { dest: 'systems', label: 'Systems', icon: GridIcon },
];

export interface NavRailProps {
  nav: NavDest;
  onNav(d: NavDest): void;
  children: ChildStatus[];
}

export function NavRail({ nav, onNav, children }: NavRailProps): JSX.Element {
  const health = deriveHealth(children);

  const item = (i: NavItem) => (
    <button
      key={i.dest}
      type="button"
      className={`nav-item${nav === i.dest ? ' on' : ''}`}
      aria-current={nav === i.dest ? 'true' : undefined}
      onClick={() => onNav(i.dest)}
    >
      {i.icon}
      <span className="nav-label">{i.label}</span>
      {i.count && <span className="count">{i.count}</span>}
    </button>
  );

  return (
    <nav className="nav" aria-label="Workbench sections">
      {PRIMARY.map(item)}
      <div className="grp">Configure</div>
      {CONFIGURE.map(item)}

      <div className="foot">
        <div className="line">
          <span className="dot" data-level={health.level} />
          <span className="nav-health-label">{health.label}</span>
        </div>
        {children.length > 0 && (
          <div className="sub">{children.map((c) => c.name).join(' · ')}</div>
        )}
      </div>
    </nav>
  );
}
