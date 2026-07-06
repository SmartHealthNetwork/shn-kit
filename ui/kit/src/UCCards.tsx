// UCCards.tsx — lanes + the eight UC cards. Presentational + dispatch
// only: `latestByRow` and `disabledReason` are computed by App; this
// component is fully controlled via props so it can be tested standalone.
// Branch state is local per card and resets when the lane changes (the two
// lanes offer different pickers per row).
import { useEffect, useState } from 'react';
import type { JSX } from 'react';
import type { Lane, RunResult } from './types';
import type { EventsView } from './useEvents';
import { postRun, ApiError } from './api';
import { UC_METAS, LANE_LABELS, type UCMeta } from './ucmeta';

export interface UCCardsProps {
  lane: Lane;
  onLane(l: Lane): void;
  events: EventsView;
  latestByRow(lane: Lane, uc: string, branch: string): RunResult | undefined;
  disabledReason?: string; // (a)-(c) reasons, computed by App
  onSelectRun(runId: string): void;
  // BYO lane-surface composition. App decides these from `byo` + the
  // CURRENTLY selected lane — this component stays a dumb slot-filler so
  // its own tests need no BYO awareness.
  // `banner`, when present, renders above the card list for the current
  // lane (swap/coexistence copy).
  banner?: JSX.Element;
  // `replaceCards`, when present, renders INSTEAD of the seeded card list
  // for the current lane — the ehr lane's cards "grey out in favor of" the
  // free-form panel once an EHR swap repoints their data source.
  replaceCards?: JSX.Element;
  // `extraPanel` renders AFTER the card list, ALONGSIDE it (never in place
  // of it) — the conformant lane's WatchPanel under a Da Vinci swap
  // (registering an ingress client breaks nothing; seeded conformant runs
  // stay live outside watch windows).
  extraPanel?: JSX.Element;
}

const IN_FLIGHT_NOTICE = 'A run is already in flight — wait for it to finish before starting another.';
const BOOT_RACE_NOTICE = 'The stack is still starting — try again in a moment.';

const LANES: Lane[] = ['conformant', 'ehr'];

interface UCCardProps {
  meta: UCMeta;
  lane: Lane;
  disabled: boolean;
  latestByRow(lane: Lane, uc: string, branch: string): RunResult | undefined;
  onRun(uc: string, branch: string): Promise<void>;
  onSelectRun(runId: string): void;
}

function UCCard({ meta, lane, disabled, latestByRow, onRun, onSelectRun }: UCCardProps): JSX.Element {
  const options = meta.branches?.[lane];
  const [branch, setBranch] = useState<string>(options?.[0]?.value ?? '');
  // The double-click pre-409 window: between a click and the SSE
  // run.started event landing (which flips `disabled` via
  // events.activeRunId), the backend hasn't rejected a second concurrent
  // postRun yet — a fast double-click could fire it twice. `posting`
  // disables THIS card's button synchronously on click, cleared once
  // postRun settles either way, closing the window without waiting on the
  // SSE round-trip.
  const [posting, setPosting] = useState(false);

  const provenance = lane === 'conformant' ? meta.provenance?.conformant : undefined;
  const selectedOption = options?.find((o) => o.value === branch);
  const showReadBackHint = selectedOption?.label.toLowerCase().includes('read-back') ?? false;
  const latest = latestByRow(lane, meta.uc, branch);

  const handleRunClick = () => {
    setPosting(true);
    onRun(meta.uc, branch).finally(() => setPosting(false));
  };

  return (
    <li className="uc-card" data-testid={`card-${meta.uc}`}>
      <h3>{meta.title}</h3>
      <p className="uc-description">{meta.description}</p>
      {provenance && <span className="provenance-tag">{provenance}</span>}

      {options && (
        <label className="branch-picker">
          Branch
          <select
            aria-label={`${meta.uc} branch`}
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
          >
            {options.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
      )}
      {showReadBackHint && (
        <p className="branch-hint" data-testid={`hint-${meta.uc}`}>
          Patient read-back included.
        </p>
      )}

      <button
        type="button"
        className="btn btn-primary"
        aria-label={`Run ${meta.uc}`}
        disabled={disabled || posting}
        onClick={handleRunClick}
      >
        Run
      </button>

      {latest && (
        <div className={`result-badge result-${latest.state}`}>
          <span className="badge-label">{latest.state === 'passed' ? 'Passed' : 'Failed'}</span>
          <p className="result-detail">{latest.detail}</p>
          <button type="button" className="btn btn-link" onClick={() => onSelectRun(latest.runId)}>
            View in inspector
          </button>
        </div>
      )}
    </li>
  );
}

export function UCCards({
  lane,
  onLane,
  events,
  latestByRow,
  disabledReason,
  onSelectRun,
  banner,
  replaceCards,
  extraPanel,
}: UCCardsProps): JSX.Element {
  const [apiNotice, setApiNotice] = useState<string | undefined>(undefined);

  const sseInFlight = events.activeRunId !== undefined;
  const disabled = Boolean(disabledReason) || sseInFlight;

  // A stale apiNotice (from a postRun catch) must not outlive the live SSE
  // signal superseding or resolving it — it used to persist until the next
  // Run click, masking the real state. Any transition of the live
  // in-flight signal — becoming true (superseded by the real signal) or
  // becoming false (resolved) — clears it.
  useEffect(() => {
    setApiNotice(undefined);
  }, [sseInFlight]);

  // disabledReason (App's computed reason) wins; otherwise a live in-flight
  // signal wins over a stale apiNotice (belt-and-braces catch-driven notice
  // — SSE is lossy, so apiNotice can still be showing this render before
  // the effect above clears it); otherwise the catch-driven notice.
  const notice = disabledReason ?? (sseInFlight ? IN_FLIGHT_NOTICE : apiNotice);

  const handleRun = (uc: string, branch: string): Promise<void> => {
    setApiNotice(undefined);
    // .then(ok, err) rather than .catch so the returned promise itself always
    // settles (resolves) once postRun does — UCCard's `posting` guard awaits
    // this to know when the pre-409 window has closed, success or failure.
    return postRun(lane, uc, branch).then(
      () => undefined,
      (err: unknown) => {
        if (err instanceof ApiError && err.status === 409) {
          setApiNotice(IN_FLIGHT_NOTICE);
        } else if (err instanceof ApiError && err.status === 503) {
          setApiNotice(BOOT_RACE_NOTICE);
        } else {
          setApiNotice(err instanceof Error ? err.message : String(err));
        }
      },
    );
  };

  return (
    <div className="uc-cards-surface">
      <div className="lane-tabs" role="tablist" aria-label="lane">
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
            {LANE_LABELS[l].title}
          </button>
        ))}
      </div>
      <p className="lane-blurb">{LANE_LABELS[lane].blurb}</p>

      {banner}

      {notice && (
        <div role="alert" className="run-notice">
          {notice}
        </div>
      )}

      {replaceCards ?? (
        <ul className="uc-cards">
          {UC_METAS.map((meta) => (
            <UCCard
              key={`${meta.uc}-${lane}`}
              meta={meta}
              lane={lane}
              disabled={disabled}
              latestByRow={latestByRow}
              onRun={handleRun}
              onSelectRun={onSelectRun}
            />
          ))}
        </ul>
      )}

      {extraPanel}
    </div>
  );
}
