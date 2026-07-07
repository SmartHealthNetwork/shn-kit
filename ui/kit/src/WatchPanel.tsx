// WatchPanel.tsx — the conformant lane's bring-your-own Da Vinci watch
// surface: starts/stops a watch session that attributes
// externally-originated (partner Da Vinci system) gateway traffic to a run
// on the bus. A watch is shaped exactly like a run — the live inspector,
// flow map, history, and export all work UNCHANGED once one is open.
// Renders ALONGSIDE the conformant lane's seeded UCCards, never in place of
// them (registering an ingress client breaks nothing; seeded conformant
// runs stay live outside watch windows).
// Presentational + local fetch state (the same component idioms used
// throughout the renderer).
import { useState } from 'react';
import type { JSX } from 'react';
import type { RunResult } from './types';
import type { EventsView } from './useEvents';
import { ApiError, deleteWatch, postWatch } from './api';
import { StatusChip } from './StatusChip';

export interface WatchPanelProps {
  events: EventsView;
  onSelectRun(runId: string): void;
}

// Outside-window honesty, pinned exactly: partner traffic outside a watch
// window relays unstamped and is not narrated — attribution soundness over
// completeness is a deliberate design choice, stated here.
export const OUTSIDE_WINDOW_NOTE =
  'Start watching before your system sends its first request — traffic outside a watch window relays but is not narrated in the inspector.';

// Pinned exactly — the watch's own narration-while-open copy.
export const WATCHING_NARRATION_NOTE = "your system's flows narrate in the inspector";

// Pinned exactly — the watch's provenance line (mirrors FreeFormPanel's
// FREEFORM_PROVENANCE_LINE for the ehr lane).
export const WATCH_PROVENANCE_LINE = 'originated by your system through the Smart Gateway ingress';

// The same idiom as UCCards' IN_FLIGHT_NOTICE: a 409 is an inline notice,
// not an error state — concurrent driven+external traffic is exactly the
// mis-attribution this design forbids, so this is the CORRECT, expected
// outcome.
const IN_FLIGHT_NOTICE = 'a run is in flight — wait for it to finish before starting a watch';

type WatchState =
  | { kind: 'idle' }
  | { kind: 'starting' }
  | { kind: 'watching'; runId: string }
  | { kind: 'stopping'; runId: string };

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}

export function WatchPanel({ events, onSelectRun }: WatchPanelProps): JSX.Element {
  const [state, setState] = useState<WatchState>({ kind: 'idle' });
  const [notice, setNotice] = useState<string | undefined>(undefined);
  const [error, setError] = useState<string | undefined>(undefined);
  const [result, setResult] = useState<RunResult | undefined>(undefined);

  const handleStart = async () => {
    setNotice(undefined);
    setError(undefined);
    setResult(undefined);
    setState({ kind: 'starting' });
    try {
      const res = await postWatch();
      setState({ kind: 'watching', runId: res.runId });
      onSelectRun(res.runId);
    } catch (err) {
      setState({ kind: 'idle' });
      if (err instanceof ApiError && err.status === 409) {
        setNotice(IN_FLIGHT_NOTICE);
      } else {
        setError(errorMessage(err));
      }
    }
  };

  const handleStop = async () => {
    if (state.kind !== 'watching') return;
    const { runId } = state;
    setState({ kind: 'stopping', runId });
    try {
      const res = await deleteWatch();
      setResult(res);
      setState({ kind: 'idle' });
    } catch (err) {
      setState({ kind: 'watching', runId });
      setError(errorMessage(err));
    }
  };

  return (
    <div className="card watch-panel">
      <h3>Watch for your system&apos;s traffic</h3>
      <p className="watch-provenance">{WATCH_PROVENANCE_LINE}</p>

      {state.kind === 'idle' && (
        <>
          <p className="watch-outside-window-note">{OUTSIDE_WINDOW_NOTE}</p>
          <button
            type="button"
            className="btn btn-primary"
            disabled={events.activeRunId !== undefined}
            onClick={() => {
              void handleStart();
            }}
          >
            Start watching
          </button>
        </>
      )}

      {state.kind === 'starting' && (
        <button type="button" className="btn btn-primary" disabled>
          Starting…
        </button>
      )}

      {(state.kind === 'watching' || state.kind === 'stopping') && (
        <div className="watch-active">
          <p className="watch-run-id">Watching — run {state.runId}</p>
          <p className="watch-narration-note">{WATCHING_NARRATION_NOTE}</p>
          <button
            type="button"
            className="btn ghost"
            disabled={state.kind === 'stopping'}
            onClick={() => {
              void handleStop();
            }}
          >
            Stop watching
          </button>
        </div>
      )}

      {result && (
        <div className={`watch-result watch-result-${result.state}`}>
          <StatusChip state={result.state} />
          <p className="watch-result-detail">{result.detail}</p>
          <button type="button" className="link" onClick={() => onSelectRun(result.runId)}>
            View in inspector
          </button>
        </div>
      )}

      {notice && (
        <div role="alert" className="watch-notice">
          {notice}
        </div>
      )}
      {error && (
        <p role="alert" className="watch-error">
          {error}
        </p>
      )}
    </div>
  );
}

export default WatchPanel;
