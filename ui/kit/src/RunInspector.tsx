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
import type { HistorySummary, KitEvent, Lane, Register, RunResult } from './types';
import type { RunSource } from './useRunEvents';
import { buildDemoStory, buildRunStory, isDemoRun } from './inspect';
import { FlowMap } from './FlowMap';
import { DemoStepDetail, StepDetail, type InspectorView, type ValidatorPosture } from './StepDetail';
import { StatusChip } from './StatusChip';
import { DemoResultChip, LocalDemoChip } from './DemoChips';
import { DEMO_REPLAY_FAILURE_NOTE } from './bridgingmeta';

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
  // Overview/Technical register (App's RegisterSwitch state) — pure
  // passthrough to StepDetail's TransformCard narration.
  // undefined ⇒ StepDetail's own 'overview' default, the same
  // not-yet-wired-up posture this prop replaces.
  register?: Register;
  // onReplayDemo: the demonstration species' Replay control — a semantic
  // fork off the ordinary wire-run "Replay run" button (handleReplayClick,
  // below), which only ever re-plays the flow map's own ANIMATION. A local
  // demonstration has no wire animation to replay, and this component has
  // no api access of its own, so demonstration runs re-execute the exhibit
  // through this callback instead — unlike wire runs, whose Replay replays
  // the flow-map animation only. `kind` is handed the same 'carry'/'refusal'
  // vocabulary api.ts's postBridgingExhibit takes, so App can wire this prop
  // straight to it. undefined ⇒ the demo Replay button is a harmless no-op
  // (e.g. tests that don't exercise it). Returning a Promise (App's real
  // implementation does) lets this component track in-flight/failure the
  // same way it already tracks the wire-run `replaying` state below — a
  // bare `void` return (e.g. a synchronous test double) is also accepted
  // and treated as an immediate success.
  onReplayDemo?(kind: 'carry' | 'refusal'): void | Promise<void>;
}

function laneFromEvent(v: string | undefined): Lane {
  if (v === 'ehr' || v === 'provider-data') return v;
  return 'conformant';
}

export function RunInspector({
  runId,
  events,
  source,
  results,
  summary,
  providerLabel,
  posture,
  register,
  onReplayDemo,
}: RunInspectorProps): JSX.Element {
  const story = runId !== undefined ? buildRunStory(runId, events) : undefined;
  const steps = story?.steps ?? [];

  const [view, setView] = useState<InspectorView>('clinical');
  const [selectedStepId, setSelectedStepId] = useState<string | undefined>(undefined);
  // Replay-run token: an incrementing counter FlowMap watches to sequence a
  // whole-story replay. `replaying` tracks only "is one in flight right
  // now" (set true on click, cleared by FlowMap's onReplayEnd) — it is
  // deliberately NOT part of the enable rule's story-completeness check
  // below; it only ever ADDS a disable, never removes one.
  const [replayToken, setReplayToken] = useState(0);
  const [replaying, setReplaying] = useState(false);
  // replayingDemo/demoReplayError: the demonstration species' Replay-control
  // state, mirroring `replaying` immediately above — set true on click,
  // cleared once onReplayDemo's promise settles either way. Unlike the wire
  // species (a flow-map animation FlowMap itself signals the end of),
  // re-executing the exhibit can genuinely fail (503/502/500 from the
  // gateway child), so a rejection also sets an inline role="alert" message
  // rather than swallowing it — App.tsx's handleReplayDemo used to swallow
  // rejections silently, leaving a failed replay with zero feedback.
  const [replayingDemo, setReplayingDemo] = useState(false);
  const [demoReplayError, setDemoReplayError] = useState<string | undefined>(undefined);
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
      // Defensive reset: FlowMap always signals its end (even on a mid-replay
      // unmount), so `replaying` should already be false here — but switching
      // to a fresh run must never inherit a stale in-flight flag that would
      // leave THIS run's Replay button wedged disabled.
      setReplaying(false);
      // Same defensive reset for the demo species: a fresh run (including a
      // fresh demo run) never inherits a previous run's in-flight flag or
      // stale failure message.
      setReplayingDemo(false);
      setDemoReplayError(undefined);
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

  const handleReplayClick = () => {
    setReplaying(true);
    setReplayToken((t) => t + 1);
  };

  // handleReplayDemoClick: the demo species' Replay click handler. Guards on
  // onReplayDemo being present (undefined ⇒ a harmless no-op, per the prop's
  // own doc comment) and wraps its return in Promise.resolve() so a bare
  // `void`-returning test double (or a future caller) never throws trying to
  // .then() it — it just resolves immediately, same as a genuine success.
  const handleReplayDemoClick = (kind: 'carry' | 'refusal') => {
    if (!onReplayDemo) return;
    setDemoReplayError(undefined);
    setReplayingDemo(true);
    Promise.resolve(onReplayDemo(kind)).then(
      () => setReplayingDemo(false),
      () => {
        setReplayingDemo(false);
        setDemoReplayError(DEMO_REPLAY_FAILURE_NOTE);
      },
    );
  };

  const handleReplayEnd = () => {
    setReplaying(false);
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

  // Species branch: a local demonstration (an engine exhibit, identified
  // structurally by its demo.started event — inspect.ts's isDemoRun) is
  // rendered entirely separately from here down. buildRunStory already
  // yields zero steps for demo.* events (they carry no observer/audit
  // frames), so `activeStory` above is honestly empty for one — reused
  // as-is for FlowMap's demo variant below rather than built twice.
  // Wire-run rendering below this block is otherwise byte-untouched.
  if (isDemoRun(events, runId)) {
    const demoStory = buildDemoStory(runId, events);
    if (demoStory === undefined) {
      // demo.started has streamed in but demo.exhibit hasn't landed yet (a
      // brief SSE-ordering window, kitd emits the pair back-to-back) — the
      // same honest "still arriving" treatment as source==='loading' above,
      // never a fabricated demo header from a half-arrived record.
      return (
        <div className="insp loading-state">
          <p>Loading this run…</p>
        </div>
      );
    }
    return (
      <div className="insp">
        <div className="insp-head">
          <div className="insp-title">
            <span className="mono">{`demo/${demoStory.uc}`}</span>
            <div className="insp-tools">
              <button
                type="button"
                className="ctl"
                disabled={replayingDemo}
                onClick={() =>
                  handleReplayDemoClick(demoStory.uc === 'refusal-engine' ? 'refusal' : 'carry')
                }
              >
                Replay run
              </button>
              {/* A demonstration never crosses a leg, so it never produces the
                  observer/audit frames this toggle switches between — there
                  is honestly nothing here to show. Disabled, not hidden, so
                  the control stays in its usual place. */}
              <label className="toggle">
                <input type="checkbox" checked={false} disabled onChange={() => undefined} />
                <span className="sw" />
                Substrate view
              </label>
            </div>
          </div>
          <div className="insp-meta">
            <LocalDemoChip />
            <DemoResultChip kind={demoStory.uc} />
          </div>
        </div>

        {demoReplayError && (
          <p role="alert" className="demo-replay-error">
            {demoReplayError}
          </p>
        )}

        <div className="insp-body">
          <FlowMap
            story={activeStory}
            lane="conformant"
            selectedStepId={selectedStepId}
            onSelectStep={handleSelectStep}
            demo
            demoRecord={demoStory.record}
          />

          <div className="insp-detail">
            <DemoStepDetail record={demoStory.record} />
          </div>
        </div>
      </div>
    );
  }

  const runStartedEvent = events.find((e) => e.runId === runId && e.type === 'run.started');
  const lane = laneFromEvent(runStartedEvent?.lane);
  const uc = runStartedEvent?.uc ?? '';

  // Branch is sourced ONLY from `summary` — KitEvent carries no branch
  // field. The result badge falls back to `results` for live runs, where
  // no HistorySummary exists yet (App wires `summary` in).
  const badge = summary ?? results.find((r) => r.runId === runId);
  const selectedStep = steps.find((s) => s.id === selectedStepId);

  // Replay-run enable rule: disabled IF AND ONLY IF the story has no
  // terminal yet, or a replay is already in flight. `source` is IRRELEVANT
  // — useRunEvents.ts computes 'live' as "the run's events are still in the
  // ring", so a just-completed run stays source: 'live' until ring
  // eviction; gating on source would disable the button at exactly the
  // moment users most want it.
  const replayDisabled = activeStory.terminal === undefined || replaying;

  return (
    <div className="insp">
      <div className="insp-head">
        <div className="insp-title">
          <span className="mono">
            {`${lane}/${uc}`}
            {summary?.branch ? ` (${summary.branch})` : ''}
          </span>
          <div className="insp-tools">
            <button type="button" className="ctl" onClick={handleReplayClick} disabled={replayDisabled}>
              Replay run
            </button>
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
          replayToken={replayToken}
          onReplayEnd={handleReplayEnd}
        />

        <div className="insp-detail">
          {selectedStep ? (
            // key={selectedStep.id}: without it, switching the selected step
            // re-renders StepDetail in place — React reconciles the SAME
            // TransformExpander fiber across the switch, so its expanded/
            // fetched-capture state (a useState local to that component)
            // survives the prop change and keeps showing the PREVIOUS
            // step's payload under the newly selected step's card. Keying
            // on the step id forces a fresh mount per step, so a fresh
            // step always starts its expander collapsed and unfetched.
            <StepDetail key={selectedStep.id} step={selectedStep} view={view} posture={posture} register={register} />
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
