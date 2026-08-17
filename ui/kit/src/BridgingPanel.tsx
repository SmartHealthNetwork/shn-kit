// BridgingPanel.tsx — the Bridging destination's guided panel (working
// column). Presentational + dispatch, fully controlled via props (the
// UCCards/WatchPanel idiom): App owns `status`/`events`/`results`/
// `register` and threads them straight through, same shapes those
// components already receive.
//
// Panel zones, top to bottom: explainer cards (contract lines +
// the three compat-manifest step classes, register-driven) -> demo status
// strip (probe states, demo-mode toggle, badge) -> run actions (Run bridged
// exchange; the visually-separated Refusal exhibit, wire + engine + carry
// parts) -> the static route-refusal grammar block. An ABSENT
// `status.bridging` key (feature not configured on this Kit build — never
// conflated with demoMode:false) collapses the WHOLE panel to a single
// feature-unavailable state; nothing else renders.
import { useEffect, useState } from 'react';
import type { JSX } from 'react';
import type {
  BridgingExhibitCarryResponse,
  BridgingExhibitRefusalResponse,
  Lane,
  Probe,
  Register,
  RunResult,
  StatusResponse,
} from './types';
import type { EventsView } from './useEvents';
import { ApiError, postBridgingDemo, postBridgingExhibit, postRun } from './api';
import { RegisterSwitch } from './RegisterSwitch';
import { StatusChip, TickIcon } from './StatusChip';
import {
  BRIDGING_REMOTE_CAPTION,
  CONTRACT_LINE_EXPLAINER,
  DEMO_MODE_BADGE,
  DEMO_RECEIPT_CARRY,
  DEMO_RECEIPT_REFUSAL,
  REFUSAL_EXHIBIT_FRAMING,
  ROUTE_REFUSAL_GRAMMAR_EXAMPLE,
  STEP_CLASSES,
  STEP_CLASS_META,
  VIEW_IN_INSPECTOR_LINK,
} from './bridgingmeta';

export interface BridgingPanelProps {
  status?: StatusResponse;
  register?: Register;
  onRegister?(r: Register): void;
  events: EventsView;
  results: RunResult[];
  // App's computed disable reason (gateway not ready / stack starting / a
  // run or watch in flight) — the SAME reason UCCards' Run buttons honor.
  // Wire-run actions here (Run bridged exchange, the refusal exhibit's wire
  // part) go through the identical runner lock, so they honor it too.
  disabledReason?: string;
  onSelectRun(runId: string): void;
}

// The UCCards IN_FLIGHT_NOTICE idiom (UCCards.tsx's own local const of this
// exact text) — reused verbatim here for the same 409 case (a belt-and-
// braces catch, since disabledReason already client-side-gates the common
// path; SSE is lossy and a race can still reach the server).
const IN_FLIGHT_NOTICE = 'A run is already in flight — wait for it to finish before starting another.';

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// latestOf mirrors App's latestByRow closure (last match wins) — BridgingPanel
// receives the flat `results` array directly rather than a resolver function,
// since it only ever needs two specific rows.
function latestOf(results: RunResult[], lane: Lane, uc: string, branch: string): RunResult | undefined {
  let found: RunResult | undefined;
  for (const r of results) {
    if (r.lane === lane && r.uc === uc && r.branch === branch) found = r;
  }
  return found;
}

function ProbeRow({ label, probe }: { label: string; probe: Probe | undefined }): JSX.Element {
  if (!probe) {
    // Absent (BridgeProbes' matching holder id left "" at boot) — an honest
    // "not configured" state, never a fabricated red (bootstrap/verify.go's
    // own BridgeProbes doc).
    return (
      <li className="verify-probe bridging-probe-absent">
        <span className="probe-name">{label}</span>
        <span className="probe-detail">not configured for this Kit</span>
      </li>
    );
  }
  return (
    <li className={`verify-probe verify-${probe.ok ? 'ok' : 'failed'}`}>
      <span className="probe-name">{probe.name}</span>
      <span className="probe-detail">{probe.detail}</span>
    </li>
  );
}

interface WireRunActionProps {
  title: string;
  description: string;
  latest?: RunResult;
  active: boolean;
  posting: boolean;
  disabledReason?: string;
  notice?: string;
  runLabel: string;
  onRun(): void;
  onSelectRun(runId: string): void;
}

// WireRunAction is the shared row shape for BOTH real runner-driven actions
// (Run bridged exchange; the refusal exhibit's wire part) — same
// latest-result/active-chip/view-in-inspector/disabled-copy idiom UCCard
// already uses for the eight seeded rows.
function WireRunAction({
  title,
  description,
  latest,
  active,
  posting,
  disabledReason,
  notice,
  runLabel,
  onRun,
  onSelectRun,
}: WireRunActionProps): JSX.Element {
  return (
    <div className="bridging-action-row">
      <div className="bridging-action-copy">
        <h3>{title}</h3>
        <p>{description}</p>
      </div>
      <div className="bridging-action-controls">
        {active ? (
          <span className="chip run">
            <span className="spin" aria-hidden="true" />
            Running…
          </span>
        ) : (
          latest && <StatusChip state={latest.state} />
        )}
        {latest && (
          <button type="button" className="link" onClick={() => onSelectRun(latest.runId)}>
            View in inspector
          </button>
        )}
        <button
          type="button"
          className={latest ? 'btn ghost sm' : 'btn primary sm'}
          disabled={Boolean(disabledReason) || posting}
          onClick={onRun}
        >
          {latest ? 'Run again' : runLabel}
        </button>
      </div>
      {disabledReason && <p className="bridging-disabled-reason">{disabledReason}</p>}
      {notice && (
        <p role="alert" className="run-notice">
          {notice}
        </p>
      )}
    </div>
  );
}

type ExhibitState<T> =
  | { kind: 'idle' }
  | { kind: 'running' }
  | { kind: 'done'; result: T }
  | { kind: 'error'; message: string };

// ExhibitReceipt is the engine exhibits' shared success-state row — a tick,
// the kind-pinned receipt sentence, and a link into the inspector where the
// demonstration itself now lives (RunInspector/StepDetail render the actual
// engine run; this panel no longer duplicates it inline). Replaces the
// former inline verdict box (bridging-exhibit-result) for BOTH exhibits.
function ExhibitReceipt({
  text,
  runId,
  onSelectRun,
}: {
  text: string;
  runId: string;
  onSelectRun(runId: string): void;
}): JSX.Element {
  return (
    <p className="bridging-exhibit-receipt">
      {TickIcon}
      {text}{' '}
      <button type="button" className="link" onClick={() => onSelectRun(runId)}>
        {VIEW_IN_INSPECTOR_LINK}
      </button>
    </p>
  );
}

export function BridgingPanel({
  status,
  register = 'overview',
  onRegister,
  events,
  results,
  disabledReason,
  onSelectRun,
}: BridgingPanelProps): JSX.Element {
  const bridging = status?.bridging;

  // Local demoMode override — CONFIRM-then-apply, not optimistic: the state
  // only ever advances once postBridgingDemo's promise actually RESOLVES
  // (handleToggle's try block, below), never on click. handleBridgingDemo
  // (kitd) awaits the gateway restart synchronously, so the response already
  // carries the confirmed new state the instant it resolves — this override
  // renders that immediately rather than waiting for App's next status poll
  // (up to 3s) to catch up, and drops itself once that poll actually agrees.
  // A rejected toggle (409/500/network) never touches this state at all —
  // demoMode falls straight back to bridging?.demoMode, the last confirmed
  // value, exactly as if the click had never happened.
  const [demoOverride, setDemoOverride] = useState<boolean | undefined>(undefined);
  useEffect(() => {
    if (demoOverride !== undefined && bridging?.demoMode === demoOverride) {
      setDemoOverride(undefined);
    }
  }, [bridging?.demoMode, demoOverride]);
  const demoMode = demoOverride ?? bridging?.demoMode ?? false;

  const [toggleState, setToggleState] = useState<
    { kind: 'idle' } | { kind: 'posting' } | { kind: 'error'; message: string }
  >({ kind: 'idle' });

  const handleToggle = async () => {
    setToggleState({ kind: 'posting' });
    try {
      const res = await postBridgingDemo(!demoMode);
      setDemoOverride(res.demoMode);
      setToggleState({ kind: 'idle' });
    } catch (err) {
      setToggleState({
        kind: 'error',
        message: err instanceof ApiError && err.status === 409 ? IN_FLIGHT_NOTICE : errorMessage(err),
      });
    }
  };

  // Which run (if any) currently holds the runner's sequential lock —
  // mirrors UCCards' activeRunStarted derivation off the SAME event stream
  // (no new plumbing). Attribution precedence: when the
  // active run.started carries a branch, it decides ALONE — the two
  // exhibits' branches ("bridge-demo"/"bridge-refuse") never collide, so
  // this is exact even though both exhibits' wire actions post to lane+uc
  // pairs a plain UCCards uc03 row (branch "") can ALSO produce. Only when
  // branch is absent (an older relayed event, or the plain "" run whose
  // empty branch elides off the wire the same way) does this fall back to
  // the coarser lane+uc match UCCards' own chip already accepts.
  const activeRunStarted =
    events.activeRunId !== undefined
      ? events.all.find((e) => e.type === 'run.started' && e.runId === events.activeRunId)
      : undefined;
  const bridgedExchangeActive =
    activeRunStarted?.branch !== undefined
      ? activeRunStarted.branch === 'bridge-demo'
      : activeRunStarted?.lane === 'conformant' && activeRunStarted?.uc === 'uc03';
  const refusalWireActive =
    activeRunStarted?.branch !== undefined
      ? activeRunStarted.branch === 'bridge-refuse'
      : activeRunStarted?.lane === 'ehr' && activeRunStarted?.uc === 'uc03';

  const bridgedExchangeLatest = latestOf(results, 'conformant', 'uc03', 'bridge-demo');
  const refusalWireLatest = latestOf(results, 'ehr', 'uc03', 'bridge-refuse');

  const [bridgedPosting, setBridgedPosting] = useState(false);
  const [bridgedNotice, setBridgedNotice] = useState<string | undefined>(undefined);
  const runBridgedExchange = () => {
    setBridgedNotice(undefined);
    setBridgedPosting(true);
    postRun('conformant', 'uc03', 'bridge-demo')
      .then(
        (res) => onSelectRun(res.runId),
        (err: unknown) => {
          setBridgedNotice(err instanceof ApiError && err.status === 409 ? IN_FLIGHT_NOTICE : errorMessage(err));
        },
      )
      .finally(() => setBridgedPosting(false));
  };

  const [refusalWirePosting, setRefusalWirePosting] = useState(false);
  const [refusalWireNotice, setRefusalWireNotice] = useState<string | undefined>(undefined);
  const runRefusalWireExhibit = () => {
    setRefusalWireNotice(undefined);
    setRefusalWirePosting(true);
    postRun('ehr', 'uc03', 'bridge-refuse')
      .then(
        (res) => onSelectRun(res.runId),
        (err: unknown) => {
          setRefusalWireNotice(err instanceof ApiError && err.status === 409 ? IN_FLIGHT_NOTICE : errorMessage(err));
        },
      )
      .finally(() => setRefusalWirePosting(false));
  };

  // Honest disable copy naming the missing precondition, App's disabledReason
  // (gateway/runner state) first, then this action's own two preconditions
  // (demo mode on, the matching counterparty probe green).
  function bridgedExchangeDisabledReason(): string | undefined {
    if (disabledReason) return disabledReason;
    if (!demoMode) return 'Turn on the compatibility simulation below to run this.';
    if (!bridging?.peer) return 'No demo counterparty is configured for this Kit.';
    if (!bridging.peer.ok) return `Demo counterparty unavailable: ${bridging.peer.detail}`;
    return undefined;
  }
  function refusalWireDisabledReason(): string | undefined {
    if (disabledReason) return disabledReason;
    if (!demoMode) return 'Turn on the compatibility simulation below to run the refusal exhibit.';
    if (!bridging?.refusePeer) return 'No refusal-demo counterparty is configured for this Kit.';
    if (!bridging.refusePeer.ok) return `Refusal-demo counterparty unavailable: ${bridging.refusePeer.detail}`;
    return undefined;
  }

  // The engine exhibits (refusal + carry) are NOT runner-driven — kitd
  // proxies straight to the gateway child's loopback demo endpoint, so
  // neither one touches the runner's sequential lock or depends on demo
  // mode / a counterparty probe. They stay clickable whenever the feature
  // itself is configured.
  const [refusalEngineState, setRefusalEngineState] = useState<
    ExhibitState<BridgingExhibitRefusalResponse>
  >({ kind: 'idle' });
  const runRefusalEngineExhibit = async () => {
    setRefusalEngineState({ kind: 'running' });
    try {
      const res = await postBridgingExhibit('refusal');
      if (res.kind !== 'refusal') throw new Error(`unexpected exhibit kind ${res.kind}`);
      setRefusalEngineState({ kind: 'done', result: res });
    } catch (err) {
      setRefusalEngineState({ kind: 'error', message: errorMessage(err) });
    }
  };

  const [carryState, setCarryState] = useState<ExhibitState<BridgingExhibitCarryResponse>>({ kind: 'idle' });
  const runCarryExhibit = async () => {
    setCarryState({ kind: 'running' });
    try {
      const res = await postBridgingExhibit('carry');
      if (res.kind !== 'carry') throw new Error(`unexpected exhibit kind ${res.kind}`);
      setCarryState({ kind: 'done', result: res });
    } catch (err) {
      setCarryState({ kind: 'error', message: errorMessage(err) });
    }
  };

  if (!bridging) {
    return (
      <div className="col bridging-panel">
        <div className="col-head">
          <h1>Bridging</h1>
        </div>
        <p className="bridging-unavailable" role="status">
          Bridging demo mode isn&apos;t available on this Kit build.
        </p>
      </div>
    );
  }

  return (
    <div className="col bridging-panel">
      <div className="col-head">
        <div className="col-head-row">
          <h1>Bridging</h1>
          <RegisterSwitch register={register} onRegister={onRegister ?? (() => undefined)} />
        </div>
        <p className="mode-caption">{CONTRACT_LINE_EXPLAINER[register]}</p>
      </div>

      <div className="bridging-body">
        <section className="card bridging-step-classes">
          <h2>How a bridged leg is built</h2>
          <ul className="bridging-step-class-list">
            {STEP_CLASSES.map((cls) => (
              <li key={cls} className={`bridging-step-class bridging-step-class-${cls}`}>
                <span className="bridging-step-class-label">{STEP_CLASS_META[cls].label}</span>
                <p>{STEP_CLASS_META[cls].description[register]}</p>
              </li>
            ))}
          </ul>
        </section>

        <section className="card bridging-status-strip">
          <h2>Demo status</h2>
          <ul className="verify-list bridging-probe-list">
            <ProbeRow label="bridge-demo-payer" probe={bridging.peer} />
            <ProbeRow label="bridge-demo-refuse" probe={bridging.refusePeer} />
          </ul>

          <label className="toggle bridging-demo-toggle">
            <input
              type="checkbox"
              checked={demoMode}
              disabled={Boolean(disabledReason) || toggleState.kind === 'posting'}
              onChange={() => {
                void handleToggle();
              }}
            />
            <span className="sw" />
            Compatibility simulation
          </label>
          <p className="bridging-toggle-note">
            Turning this on or off restarts the Smart Gateway to apply the change — any run in progress is
            interrupted.
          </p>
          {disabledReason && <p className="bridging-disabled-reason">{disabledReason}</p>}
          {toggleState.kind === 'error' && (
            <p role="alert" className="bridging-toggle-error">
              {toggleState.message}
            </p>
          )}
          {demoMode && <p className="demo-mode-badge">{DEMO_MODE_BADGE}</p>}
          {/* "this traffic is real" is only true when a demo counterparty is
              actually configured (its probe row exists) — with no peer there
              is no remote traffic to describe. */}
          {bridging.peer && <p className="bridging-remote-caption">{BRIDGING_REMOTE_CAPTION}</p>}
        </section>

        <section className="card bridging-run-actions">
          <h2>Run actions</h2>
          <WireRunAction
            title="Run bridged exchange"
            description="UC-03 through the demo counterparty — CRD and DTR legs bridge live; the PAS leg completes unbridged, on the shared line."
            latest={bridgedExchangeLatest}
            active={bridgedExchangeActive}
            posting={bridgedPosting}
            disabledReason={bridgedExchangeDisabledReason()}
            notice={bridgedNotice}
            runLabel="Run bridged exchange"
            onRun={runBridgedExchange}
            onSelectRun={onSelectRun}
          />
        </section>

        <section className="card bridging-refusal-exhibit">
          <h2>Refusal exhibit</h2>
          <p className="refusal-exhibit-framing">{REFUSAL_EXHIBIT_FRAMING}</p>

          <WireRunAction
            title="Wire part — refuse before forward"
            description="A real PAS submit toward the refusal demo counterparty — gate-refused locally before anything is sealed or sent. Zero bytes cross the network."
            latest={refusalWireLatest}
            active={refusalWireActive}
            posting={refusalWirePosting}
            disabledReason={refusalWireDisabledReason()}
            notice={refusalWireNotice}
            runLabel="Run refusal wire exhibit"
            onRun={runRefusalWireExhibit}
            onSelectRun={onSelectRun}
          />

          <div className="bridging-action-row">
            <div className="bridging-action-copy">
              <h3>Engine part — clinical-content grammar</h3>
              <p>A crafted multi-coverage questionnaire response through the real DTR 2.1→2.2 module.</p>
            </div>
            <div className="bridging-action-controls">
              <button
                type="button"
                className="btn ghost sm"
                disabled={refusalEngineState.kind === 'running'}
                onClick={() => {
                  void runRefusalEngineExhibit();
                }}
              >
                {refusalEngineState.kind === 'running' ? 'Running…' : 'Run refusal engine exhibit'}
              </button>
            </div>
          </div>
          {refusalEngineState.kind === 'error' && (
            <p role="alert" className="bridging-exhibit-error">
              {refusalEngineState.message}
            </p>
          )}
          {refusalEngineState.kind === 'done' && (
            <ExhibitReceipt
              text={DEMO_RECEIPT_REFUSAL}
              runId={refusalEngineState.result.runId}
              onSelectRun={onSelectRun}
            />
          )}

          <div className="bridging-action-row">
            <div className="bridging-action-copy">
              <h3>Carry content exhibit</h3>
              <p>
                A frozen reference questionnaire response run down then up through the real DTR module — the
                carried content restored exactly.
              </p>
            </div>
            <div className="bridging-action-controls">
              <button
                type="button"
                className="btn ghost sm"
                disabled={carryState.kind === 'running'}
                onClick={() => {
                  void runCarryExhibit();
                }}
              >
                {carryState.kind === 'running' ? 'Running…' : 'Run carry content exhibit'}
              </button>
            </div>
          </div>
          {carryState.kind === 'error' && (
            <p role="alert" className="bridging-exhibit-error">
              {carryState.message}
            </p>
          )}
          {carryState.kind === 'done' && (
            <ExhibitReceipt text={DEMO_RECEIPT_CARRY} runId={carryState.result.runId} onSelectRun={onSelectRun} />
          )}
        </section>

        <section className="card bridging-refusal-grammar">
          <h2>What a real route refusal looks like</h2>
          <p>
            This exact grammar is what a genuine cross-network refusal renders as — it isn&apos;t produced
            live in this demo (every demo counterparty always shares a bridge). Shown here as reference.
          </p>
          <pre className="bridging-refusal-grammar-example">{ROUTE_REFUSAL_GRAMMAR_EXAMPLE}</pre>
        </section>
      </div>
    </div>
  );
}

export default BridgingPanel;
