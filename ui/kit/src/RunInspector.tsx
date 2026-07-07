// RunInspector.tsx — the pane that replaces a plain event list with the
// flow map + step detail + substrate toggle + run-scoped audit anchors.
// Interprets a run's stamped events via buildRunStory (inspect.ts, pure)
// and composes the presentational layers (StepDetail, FlowMap) around a
// header + one view toggle.
//
// Selection follow-then-pin: the same rule App itself uses for run
// selection — default to the first step, auto-follow the newest step while
// the run is genuinely live (source === 'live') and nothing has been
// manually picked, and let a manual click pin the selection so later
// appended steps never steal it back. The guard resets when `runId` itself
// changes (reopening a different run never inherits the previous run's pin).
//
// Audit anchors are rendered as a run-scoped strip, a SIBLING of the
// selected step's detail pane, never nested inside it — the seq-window merge
// attributes audit records to a run, not to any one step, and rendering them
// inside StepDetail's DOM would fake a precision the substrate doesn't emit.
import { useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';
import type { HistorySummary, KitEvent, Lane, RunResult } from './types';
import type { RunSource } from './useRunEvents';
import { buildRunStory } from './inspect';
import { FlowMap } from './FlowMap';
import { StepDetail, type InspectorView, type ValidatorPosture } from './StepDetail';
import { StatusChip } from './StatusChip';

export interface RunInspectorProps {
  runId?: string;
  events: KitEvent[]; // from useRunEvents (ring or history)
  source: RunSource;
  results: RunResult[]; // header badge for live runs
  summary?: HistorySummary; // header facts for history-backed runs (branch!)
  // App-derived honest BYO provider-node label (undefined ⇒ FlowMap's lane
  // default). Pure passthrough — App decides WHETHER a label applies (only
  // the current live/latest run, never a history-reopened one);
  // RunInspector just forwards it to FlowMap.
  providerLabel?: string;
  // Validator posture from App's GET /api/status poll (`status.validator`)
  // — pure passthrough to StepDetail's ValidationBadge. undefined ⇒
  // StepDetail's own 'stand-in' fallback (the honest default for an old
  // daemon or a boot-window race).
  posture?: ValidatorPosture;
}

function laneFromEvent(v: string | undefined): Lane {
  return v === 'ehr' ? 'ehr' : 'conformant';
}

export function RunInspector({
  runId,
  events,
  source,
  results,
  summary,
  providerLabel,
  posture,
}: RunInspectorProps): JSX.Element {
  const story = runId !== undefined ? buildRunStory(runId, events) : undefined;
  const steps = story?.steps ?? [];

  const [view, setView] = useState<InspectorView>('clinical');
  const [selectedStepId, setSelectedStepId] = useState<string | undefined>(undefined);
  const manualPickRef = useRef(false);
  const prevRunIdRef = useRef<string | undefined>(undefined);
  const prevStepCountRef = useRef(0);

  useEffect(() => {
    const isNewRun = runId !== prevRunIdRef.current;
    prevRunIdRef.current = runId;

    if (isNewRun) {
      manualPickRef.current = false;
      prevStepCountRef.current = steps.length;
      setSelectedStepId(steps[0]?.id);
      return;
    }

    const grew = steps.length > prevStepCountRef.current;
    prevStepCountRef.current = steps.length;

    if (grew && source === 'live' && !manualPickRef.current) {
      setSelectedStepId(steps[steps.length - 1]?.id);
    }
    // `steps` is a fresh array each render (buildRunStory re-runs whenever
    // `events` changes reference) — comparing its length against the ref is
    // deliberate; it's the only stable signal for "did the story grow".
  }, [runId, steps, source]);

  const handleSelectStep = (id: string) => {
    manualPickRef.current = true;
    setSelectedStepId(id);
  };

  if (runId === undefined) {
    return (
      <div className="insp empty-state">
        <p>Run a scenario to see its flow.</p>
      </div>
    );
  }

  if (source === 'loading') {
    return (
      <div className="insp loading-state">
        <p>Loading this run…</p>
      </div>
    );
  }

  if (source === 'missing') {
    return (
      <div className="insp missing-state">
        <p>This run is no longer available.</p>
      </div>
    );
  }

  // source is 'live' or 'history' here, so `story` is always defined (it was
  // built above whenever runId is set) — this check just keeps TypeScript's
  // narrowing honest without a non-null assertion; it can't actually miss.
  if (story === undefined) {
    return (
      <div className="insp loading-state">
        <p>Loading this run…</p>
      </div>
    );
  }
  const activeStory = story;
  const runStartedEvent = events.find((e) => e.runId === runId && e.type === 'run.started');
  const lane = laneFromEvent(runStartedEvent?.lane);
  const uc = runStartedEvent?.uc ?? '';

  // Branch is sourced ONLY from `summary` — KitEvent carries no branch
  // field. The result badge falls back to `results` for live runs, where
  // no HistorySummary exists yet (App wires `summary` in).
  const badge = summary ?? results.find((r) => r.runId === runId);
  const selectedStep = steps.find((s) => s.id === selectedStepId);

  return (
    <div className="insp">
      <div className="insp-head">
        <div className="insp-title">
          <span className="mono">
            {`${lane}/${uc}`}
            {summary?.branch ? ` (${summary.branch})` : ''}
          </span>
          <div className="insp-tools">
            <label className="toggle">
              <input
                type="checkbox"
                checked={view === 'substrate'}
                onChange={(e) => setView(e.target.checked ? 'substrate' : 'clinical')}
              />
              <span className="sw" />
              Substrate view
            </label>
          </div>
        </div>
        {badge && (
          <div className="insp-meta">
            <StatusChip state={badge.state} />
          </div>
        )}
      </div>

      {activeStory.terminal?.type === 'run.failed' && activeStory.terminal.detail && (
        <p className="run-terminal-detail">{activeStory.terminal.detail}</p>
      )}

      <div className="insp-body">
        <FlowMap
          story={activeStory}
          lane={lane}
          selectedStepId={selectedStep?.id}
          onSelectStep={handleSelectStep}
          providerLabel={providerLabel}
        />

        <div className="insp-detail">
          {selectedStep ? (
            <StepDetail step={selectedStep} view={view} posture={posture} />
          ) : (
            <p className="no-steps-note">No steps recorded for this run yet.</p>
          )}

          {view === 'substrate' && (
            <div className="audit-anchors">
              <h3>Audit anchors</h3>
              {activeStory.auditNote ? (
                <p className="audit-note">{activeStory.auditNote}</p>
              ) : (
                <ul className="audit-anchor-list">
                  {activeStory.audit.map((a) => (
                    <li key={a.seq} className="audit-anchor-row">
                      <span className="audit-anchor-type">{a.transactionType}</span>
                      <span className="audit-anchor-parties">
                        {a.sender} → {a.recipient}
                      </span>
                      <span className="audit-anchor-authority">{a.authorityFrame}</span>
                      <span className="audit-anchor-outcome">{a.outcome}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

export default RunInspector;
