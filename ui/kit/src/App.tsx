// App.tsx — the renderer's top-level phase router. Polls kitd's
// bootstrap/status/runs routes and derives one of four phases; owns the
// single useEvents(token) instance and hands its EventsView down to the main
// surface.
import { useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';
import type {
  BootstrapResponse,
  BYOStatus,
  HistorySummary,
  KitEvent,
  Lane,
  Probe,
  Register,
  RunResult,
  StatusResponse,
} from './types';
import { ApiError, getBYO, getBootstrap, getHistory, getHistoryRecord, getRuns, getStatus } from './api';
import { canRestart, openExternal, resolveToken, restartKit } from './bridge';
import { useEvents } from './useEvents';
import SignIn from './SignIn';
import BootProgress from './BootProgress';
import { UCCards } from './UCCards';
import { useRunEvents } from './useRunEvents';
import { RunInspector } from './RunInspector';
import { RunHistory } from './RunHistory';
import { BYOPanel } from './BYOPanel';
import { FreeFormPanel } from './FreeFormPanel';
import { WatchPanel } from './WatchPanel';
import { TopBar } from './TopBar';
import { NavRail } from './NavRail';
import type { NavDest } from './NavRail';
import { SystemsPage } from './SystemsPage';
import './theme.css';
import './primitives.css';
import './shell.css';
import './scenarios.css';
import './inspector.css';
import './systems.css';
import './byo.css';
import './boot.css';

// The conformant lane's seeded cards stay LIVE under an EHR swap — "the
// other lane keeps running seeded WHEN the swap target carries the seeded
// members" — but the demo members they hardcode may or may not exist on the
// swapped SoR (one SoR, repointed for the whole gateway process). This
// banner names the requirement + remedy, the mandatory synthetic-data
// sentence, and the tri-state sentinel (byo.ehr.demoPersonas) EXACTLY as
// reported — shown, never assumed (null says nothing about presence).
function ConformantUnderEhrSwapBanner({ demoPersonas }: { demoPersonas: boolean | null }): JSX.Element {
  const sentinel =
    demoPersonas === true
      ? 'your server carries the demo personas'
      : demoPersonas === false
        ? 'your server does not carry the demo personas yet'
        : undefined;
  return (
    <div className="lane-banner byo-lane-banner" role="status">
      <p>
        Seeded scenarios resolve their members against your connected server — load the demo persona
        bundle to run them.
      </p>
      <p>The demo personas are synthetic — load them into a test server, never a production system.</p>
      {sentinel && <p className="byo-sentinel">{sentinel}</p>}
      <p>
        See <code>kit/seed/demo-personas-conformant.json</code> — see the README for how to load it.
      </p>
    </div>
  );
}

// The ehr lane's grey-out banner: the 8 seeded cards' data source is gone
// under an EHR swap, so they grey out in favor of the free-form panel —
// this names the swap and the restore path (BYOPanel's "Restore demo
// data", rendered below in the byo-panel-shell).
function EhrSwapBanner(): JSX.Element {
  return (
    <div className="lane-banner byo-lane-banner" role="status">
      <p>
        Your EHR is connected — the seeded scenarios have no data source under this swap; the
        free-form panel below is this lane&apos;s surface. Restore demo data in Bring your own to
        bring them back.
      </p>
    </div>
  );
}

// The conformant lane's Da Vinci-swap coexistence banner: registering an
// ingress client breaks nothing — the seeded conformant rows keep running;
// Watch narrates the partner system's OWN traffic alongside them, not
// instead of them.
function DaVinciCoexistenceBanner(): JSX.Element {
  return (
    <div className="lane-banner byo-lane-banner" role="status">
      <p>
        Your Da Vinci system is registered as an inbound ingress client — the seeded conformant
        scenarios keep running below; start watching to narrate your own system&apos;s traffic
        instead.
      </p>
    </div>
  );
}

const BOOTSTRAP_POLL_MS = 2000;
const STATUS_POLL_MS = 3000;

type Phase = 'signin' | 'boot' | 'main' | 'unreachable';

export function isGatewayReady(status: StatusResponse | undefined): boolean {
  return status?.children.some((c) => c.name === 'gateway' && c.state === 'ready') ?? false;
}

// The three App-derived disable reasons for the Run buttons, in priority
// order. Exported as a pure function (rather than only inlined) because
// the gateway-not-ready branch can't be exercised through the full phase
// router in a test — reaching the `main` phase at all already implies
// isGatewayReady(status), so this is unit-tested directly.
//
// `watching`: the in-flight run currently occupying the runner's sequential
// lock may itself be a watch session (uc "external", started by WatchPanel
// rather than a driven row) — that gets its own, more accurate copy rather
// than the generic "a run is in flight" sentence, since the operator's own
// action (stop watching) is what's actually blocking a new driven run.
export function computeDisabledReason(
  status: StatusResponse | undefined,
  runsLive: boolean,
  inFlight: boolean,
  watching = false,
): string | undefined {
  if (!isGatewayReady(status)) {
    return 'The Smart Gateway is not ready yet.';
  }
  if (!runsLive) {
    return 'The stack is still starting.';
  }
  if (inFlight) {
    if (watching) {
      return 'watching for incoming flows — stop watching to run scenarios';
    }
    return 'A run is in flight — wait for it to finish before starting another.';
  }
  return undefined;
}

// runStartedEvent resolves a run's OWN run.started event — never the
// currently-selected UCCards lane/uc toggle, which can differ from either
// inspector pane's run (most obviously the compare pane). Returns undefined
// when the run's run.started frame isn't in the given events (e.g. a
// history run whose events haven't loaded yet).
function runStartedEvent(runId: string, events: KitEvent[]): KitEvent | undefined {
  return events.find((e) => e.runId === runId && e.type === 'run.started');
}

// deriveProviderLabel: FlowMap's providerLabel override has two independent
// sources, and they must not be conflated:
//
//  - RECORD-derived (from the run's own run.started `uc`): a `freeform` run
//    ran off the partner's EHR BY CONSTRUCTION — that provenance is a fact
//    of the record itself, true forever, for a live run or a
//    history-reopened one alike. It does not depend on latestRunId or on
//    today's byo state.
//  - STATE-derived (from `byo` + `latestRunId`): the "your Da Vinci system"
//    label for a WATCH run (uc "external") describes the data source AT RUN
//    TIME — a fact the Kit does not record per-run (HistorySummary/
//    HistoryRecord carry only lane/uc/branch/state/detail/time/eventCount,
//    no swap snapshot). So it is honest ONLY for the current live/latest run
//    (`latestRunId`, tracked independently of the auto-follow/manual-pick
//    selection guard) — a history-reopened run (any runId other than
//    latestRunId) always keeps FlowMap's lane-default label; relabeling it
//    from TODAY's byo state would retroactively claim a provenance that
//    specific run never recorded. The label is further gated on `uc ===
//    'external'`: a SEEDED conformant run (e.g. uc03) run under a live
//    davinci swap did not itself go through the partner's system — only a
//    watch run genuinely did.
export function deriveProviderLabel(
  runId: string | undefined,
  latestRunId: string | undefined,
  events: KitEvent[],
  byo: BYOStatus | undefined,
): string | undefined {
  if (runId === undefined) return undefined;
  const started = runStartedEvent(runId, events);

  // Record-derived: independent of latestRunId/byo.
  if (started?.uc === 'freeform') return 'Your EHR (FHIR data source)';

  // State-derived: honest only for the current live/latest run.
  if (runId !== latestRunId) return undefined;
  const runLane = started?.lane === 'ehr' ? 'ehr' : 'conformant';
  if (runLane === 'ehr' && byo?.ehr?.applied) return 'Your EHR (FHIR data source)';
  if (runLane === 'conformant' && started?.uc === 'external' && byo?.davinci?.applied) {
    return 'Your Da Vinci system';
  }
  return undefined;
}

export default function App() {
  // Token acquisition: resolved once on mount. useEvents(undefined)
  // constructs no EventSource until it lands; the api module resolves its
  // own token internally per call.
  const [token, setToken] = useState<string | undefined>(undefined);
  useEffect(() => {
    resolveToken().then(setToken);
  }, []);
  const events = useEvents(token);

  const [boot, setBoot] = useState<BootstrapResponse>({ state: 'provisioning', verify: [] });
  const [bootError, setBootError] = useState<Error | undefined>(undefined);
  const [status, setStatus] = useState<StatusResponse | undefined>(undefined);
  const [runsLive, setRunsLive] = useState(false);
  const [results, setResults] = useState<RunResult[]>([]);
  const [lane, setLane] = useState<Lane>('conformant');
  // The scenario-card detail level (Overview | Technical). A single global
  // choice, threaded to UCCards; kept in App (not UCCards-local) so it survives
  // nav changes, mirroring how `lane` is held. Defaults to the plain register.
  const [register, setRegister] = useState<Register>('overview');

  // The active workbench destination — which surface fills the working
  // column (nav rail · working column · persistent inspector). The
  // inspector rides alongside 'scenarios' and 'history'; 'byo' and
  // 'systems' span full width (no inspector).
  const [nav, setNav] = useState<NavDest>('scenarios');

  // Run history: fetched once main phase is reached and re-fetched
  // whenever `results.length` changes (see the fetch effect below for why
  // results.length rather than a fixed timer).
  const [history, setHistory] = useState<HistorySummary[]>([]);

  // Once Reset completes and the daemon requires a real process restart,
  // the bootstrap poll flips `boot` to signin-required within ~2s — the
  // phase router would otherwise unmount
  // StatusPanel (and its "Restart the Kit to finish the reset" affordance)
  // and let the operator sign back in against the SAME in-process daemon
  // (boot goroutine never re-runs, gateway child keeps the pre-reset
  // identity — a degraded, non-obvious path). Hoisting "restart required"
  // into App state and rendering it as a persistent, phase-overriding screen
  // keeps the affordance alive across that flip. It only ever flips true;
  // only a real restart (which reloads the page) clears it. Because the
  // banner fully replaces the signin/boot/main content while set, any
  // staleness in status/runsLive/results underneath is moot — nothing else
  // renders until the process actually restarts.
  const [resetPending, setResetPending] = useState(false);

  // Update banner: shnkitd's one launch-time releases-feed check surfaces
  // as `status.update` — key-presence (undefined ⇒ never checked / dev
  // build / offline; both silent). Dismissing hides it for the rest of THIS
  // session (page load) — it never reappears from a later poll of the same
  // still-available update, only a fresh page load (a real restart) would
  // re-show it.
  const [updateDismissed, setUpdateDismissed] = useState(false);

  // Bring-your-own systems settings: fetched once main phase is reached
  // (effect below, alongside the history fetch); re-fetched on BYOPanel's
  // onSaved (a PUT/DELETE just landed, so the persisted config — and each
  // lane's applied bool — may have changed). undefined ⇒ not fetched yet
  // (BYOPanel isn't rendered until it lands).
  const [byo, setByo] = useState<BYOStatus | undefined>(undefined);
  const refetchBYO = () => {
    getBYO()
      .then(setByo)
      .catch(() => {
        // Transient — keep the last known BYO status rather than blanking the panel.
      });
  };

  // The bring-your-own "restart to apply" confirm dialog: BYOPanel itself
  // never touches the bridge — it just calls onRestart(), and App shows the
  // confirm + routes through the SAME bridge access path (canRestart/
  // restartKit) the reset-required screen above already uses.
  const [byoRestartConfirm, setByoRestartConfirm] = useState(false);

  // App applies POST /api/verify's fresh probes to boot.verify immediately
  // — StatusPanel's Re-check button need not wait for the next bootstrap
  // poll to reflect the result.
  const handleVerified = (probes: Probe[]) => {
    setBoot((b) => ({ ...b, verify: probes }));
  };

  // Selection state for the run inspector (the pane itself is
  // RunInspector). Auto-follows the latest run.started event unless the
  // user has explicitly picked a different past run (via UCCards' "View in
  // inspector") since the last terminal event — the guard resets once the
  // in-flight run's terminal SSE event is observed, so the NEXT run.started
  // resumes auto-follow.
  const [selectedRunId, setSelectedRunId] = useState<string | undefined>(undefined);
  const manualPickRef = useRef(false);
  const prevActiveRunIdRef = useRef<string | undefined>(undefined);

  // The current live/latest run's id — updated whenever a NEW run starts,
  // regardless of manualPickRef, so it stays accurate even while a manual
  // pick has pinned `selectedRunId` elsewhere. This (not `selectedRunId`) is
  // the honest signal for "is this the run the BYO provider label may
  // legitimately describe."
  const [latestRunId, setLatestRunId] = useState<string | undefined>(undefined);

  // Resolves the selected run's events ring-first, falling back to history
  // when the ring no longer holds it — one path for a live run and a
  // reopened historical one alike.
  const runEvents = useRunEvents(selectedRunId, events);

  // Compare pane: a second, independent run selection. useRunEvents is
  // called unconditionally here (hooks can't be conditional) — it resolves
  // to the empty/missing state when compareRunId is undefined, and the
  // compare pane itself only renders once it's set.
  const [compareRunId, setCompareRunId] = useState<string | undefined>(undefined);
  const compareEvents = useRunEvents(compareRunId, events);

  // Export failure surface: getHistoryRecord can reject (e.g. 404 — the run
  // was pruned between the history poll and the click). Mirrors
  // StatusPanel.handleConfirmReset's try/catch + role="alert" convention
  // rather than leaving exportRun's rejection unhandled with zero user
  // feedback. Cleared at the START of the next export attempt (success or
  // failure alike), so a retry never leaves a stale message on screen.
  const [exportError, setExportError] = useState<string | undefined>(undefined);

  // Poll getBootstrap() every 2s, for the life of the app.
  useEffect(() => {
    let cancelled = false;
    const poll = () => {
      getBootstrap()
        .then((b) => {
          if (cancelled) return;
          setBoot(b);
          setBootError(undefined);
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          setBootError(err instanceof Error ? err : new Error(String(err)));
        });
    };
    poll();
    const id = setInterval(poll, BOOTSTRAP_POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  // Poll getStatus()+getRuns() every 3s, once provisioned. Ready-ness
  // (waitKitReady) needs three facts: provisioned, gateway child 'ready',
  // AND getRuns() answering without a 503 — the runs-route call here exists
  // purely to observe that third fact.
  useEffect(() => {
    if (boot.state !== 'provisioned') return;
    let cancelled = false;
    const poll = () => {
      getStatus()
        .then((s) => {
          if (!cancelled) setStatus(s);
        })
        .catch(() => {
          // Transient — keep the last known status rather than blanking the panel.
        });
      getRuns()
        .then((r) => {
          if (cancelled) return;
          setRunsLive(true);
          setResults(r);
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          if (err instanceof ApiError && err.status === 503) {
            setRunsLive(false);
          }
        });
    };
    poll();
    const id = setInterval(poll, STATUS_POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [boot.state]);

  // Auto-follow the latest run.started event in the inspector, unless the
  // user picked an older run since the last terminal event.
  useEffect(() => {
    const activeRunId = events.activeRunId;
    const prev = prevActiveRunIdRef.current;

    if (activeRunId !== undefined && activeRunId !== prev) {
      setLatestRunId(activeRunId);
      if (!manualPickRef.current) {
        setSelectedRunId(activeRunId);
        // A brand-new live run never carries a stale comparison forward —
        // compareRunId is scoped to whatever pairing the operator set up
        // against the PREVIOUS selection; auto-following to a new run
        // starts that pairing over.
        setCompareRunId(undefined);
      }
    }
    if (activeRunId === undefined && prev !== undefined) {
      manualPickRef.current = false;
      // Clear latestRunId the moment the active run reaches terminal —
      // deriveProviderLabel's state-derived branch (`runId === latestRunId`)
      // is honest only for the CURRENT live run. Leaving latestRunId sticky
      // at the just-finished run's id would let a LATER reopen of that same
      // run (via history) still match `runId === latestRunId` and get
      // relabeled from TODAY's byo state — exactly the retroactive-
      // provenance claim deriveProviderLabel's own doc comment forbids.
      setLatestRunId(undefined);
    }
    prevActiveRunIdRef.current = activeRunId;
  }, [events.activeRunId]);

  const handleSelectRun = (runId: string) => {
    manualPickRef.current = true;
    setSelectedRunId(runId);
  };

  // Compare toggles: clicking Compare on the already-compared row closes
  // the pane; any other row replaces it.
  const handleCompare = (runId: string) => {
    setCompareRunId((prev) => (prev === runId ? undefined : runId));
  };

  // Nav switch: a comparison is destination-scoped (it's set up against
  // whatever run the CURRENT destination has selected), so switching
  // destinations closes it — otherwise a compare opened in Run history
  // survives into Scenarios and gets paired against whatever run
  // auto-follow selects there next.
  const handleNav = (d: NavDest) => {
    setNav(d);
    setCompareRunId(undefined);
  };

  // Export: the HistoryRecord IS the export format — fetched fresh (not
  // read off `history`, which only carries the summary) and downloaded as
  // `<runId>.json`.
  const exportRun = async (runId: string) => {
    setExportError(undefined);
    try {
      const rec = await getHistoryRecord(runId);
      const blob = new Blob([JSON.stringify(rec, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${runId}.json`;
      try {
        a.click();
      } finally {
        URL.revokeObjectURL(url);
      }
    } catch (err) {
      setExportError(err instanceof Error ? err.message : String(err));
    }
  };

  // In-flight reconciliation (SSE is lossy): a run is genuinely in flight
  // only while events.activeRunId is set AND results has no terminal entry
  // for that runId yet — a dropped terminal SSE frame must not disable the
  // Run buttons forever. The reconciled activeRunId (rather than the raw
  // SSE signal) is what's handed to UCCards, so its own in-flight fallback
  // can't get stuck either.
  const inFlight =
    events.activeRunId !== undefined && !results.some((r) => r.runId === events.activeRunId);
  const reconciledEvents = { ...events, activeRunId: inFlight ? events.activeRunId : undefined };

  const latestByRow = (queryLane: Lane, uc: string, branch: string): RunResult | undefined => {
    let found: RunResult | undefined;
    for (const r of results) {
      if (r.lane === queryLane && r.uc === uc && r.branch === branch) found = r;
    }
    return found;
  };

  // A watch session is a run on the bus shaped exactly like any other
  // (uc "external") — this reads the SAME raw activeRunId's run.started
  // frame `inFlight` above already keys off, so the two never disagree
  // about which run (if any) currently holds the lock.
  const activeRunStarted = events.all.find(
    (e) => e.type === 'run.started' && e.runId === events.activeRunId,
  );
  const watching = activeRunStarted?.uc === 'external';

  const disabledReason = computeDisabledReason(status, runsLive, inFlight, watching);

  // BYO lane-surface composition, decided here (from `byo` + the CURRENTLY
  // selected `lane`) and threaded into UCCards' generic
  // banner/replaceCards/extraPanel slots — UCCards itself stays fully
  // BYO-unaware. Neither swap applied ⇒ all three stay undefined ⇒ UCCards
  // renders exactly as it did before BYO existed (regression pin,
  // App.test.tsx).
  const ehrSwapped = Boolean(byo?.ehr?.applied);
  const davinciSwapped = Boolean(byo?.davinci?.applied);

  let laneBanner: JSX.Element | undefined;
  let laneReplaceCards: JSX.Element | undefined;
  let laneExtraPanel: JSX.Element | undefined;

  if (lane === 'ehr') {
    // The ehr lane's 8 seeded cards grey out in favor of the free-form
    // panel — their data source (the demo persona set) is gone once the
    // gateway's SoR is repointed at the partner EHR.
    if (ehrSwapped) {
      laneBanner = <EhrSwapBanner />;
      laneReplaceCards = (
        <FreeFormPanel events={reconciledEvents} results={results} onSelectRun={handleSelectRun} />
      );
    }
  } else {
    // conformant lane: per-lane independence — an EHR swap never hides
    // these cards, and a Da Vinci swap only ADDS the watch surface.
    const banners: JSX.Element[] = [];
    if (ehrSwapped) {
      banners.push(
        <ConformantUnderEhrSwapBanner key="ehr" demoPersonas={byo?.ehr?.demoPersonas ?? null} />,
      );
    }
    if (davinciSwapped) {
      banners.push(<DaVinciCoexistenceBanner key="davinci" />);
      laneExtraPanel = <WatchPanel events={reconciledEvents} onSelectRun={handleSelectRun} />;
    }
    if (banners.length > 0) {
      laneBanner = <>{banners}</>;
    }
  }

  const ready = boot.state === 'provisioned' && isGatewayReady(status) && runsLive;

  // The persistent inspector rides alongside the run-oriented destinations
  // ('scenarios' and 'history'); 'byo' and 'systems' span full width.
  const showInspector = nav === 'scenarios' || nav === 'history';

  let phase: Phase;
  if (bootError) {
    phase = 'unreachable';
  } else if (boot.state === 'signin-required' || boot.state === 'signing-in') {
    phase = 'signin';
  } else if (boot.state === 'provisioning' || (boot.state === 'provisioned' && !ready)) {
    phase = 'boot';
  } else {
    phase = 'main';
  }

  // History fetch: populated once main phase is reached and re-fetched
  // whenever `results.length` changes — a completed run (visible in
  // `results`) implies kitd has also persisted its HistoryRecord, so
  // keying off results.length keeps history in lockstep with the runs poll
  // above without a second timer.
  useEffect(() => {
    if (phase !== 'main') return;
    let cancelled = false;
    getHistory()
      .then((h) => {
        if (!cancelled) setHistory(h);
      })
      .catch(() => {
        // Transient — keep the last known history rather than blanking the panel.
      });
    return () => {
      cancelled = true;
    };
  }, [phase, results.length]);

  // Bring-your-own settings fetch: fetched exactly once when main phase is
  // first reached — subsequent changes come through onSaved's refetchBYO(),
  // not a re-run of this effect (mirrors the resolveToken once-on-mount
  // posture rather than the polling effects above: BYO config only changes
  // via this UI's own actions, never externally).
  useEffect(() => {
    if (phase !== 'main') return;
    refetchBYO();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase]);

  // Update banner content — undefined when there's nothing to show (no
  // update, or dismissed this session). Computed once here and spliced into
  // every phase's TopBar below, since TopBar is the one mount that survives
  // every phase.
  const updateBanner =
    status?.update?.available && !updateDismissed ? (
      <div className="update-banner" role="status">
        <span className="update-banner-text">
          A new version of the Kit is available{status.update.latest ? ` (${status.update.latest})` : ''}.
        </span>
        <button
          type="button"
          className="btn btn-link update-banner-link"
          onClick={() => openExternal(status.update?.url as string)}
        >
          View release
        </button>
        <button
          type="button"
          className="btn btn-link update-banner-dismiss"
          onClick={() => setUpdateDismissed(true)}
        >
          Dismiss
        </button>
      </div>
    ) : undefined;

  // resetPending is checked BEFORE the unreachable phase — while the daemon
  // flaps mid-restart after a reset, getBootstrap can start rejecting
  // (bootError set, phase would read 'unreachable'), but "restart required"
  // is the stronger, still-correct guidance: the operator already knows a
  // restart is needed, and "can't reach the daemon" would read as a NEW,
  // unrelated failure rather than the reset's own expected transient.
  if (resetPending) {
    return (
      <div className="app">
        <TopBar
          lane={lane}
          onLane={setLane}
          sseState={events.sseState}
          children={status?.children ?? []}
          identity={{ email: boot.email, holderId: boot.holderId }}
          updateBanner={updateBanner}
        />
        <main className="phase-shell">
          <div className="phase-card restart-required-card">
            <h1>Restart required</h1>
            <p>Restart the Kit to finish the reset. Runs in progress were reset.</p>
            {canRestart() ? (
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  void restartKit();
                }}
              >
                Restart
              </button>
            ) : (
              <p>Restart shnkitd manually to finish the reset.</p>
            )}
          </div>
        </main>
      </div>
    );
  }

  if (phase === 'unreachable') {
    // An expired/bad session token (401) is NOT the same
    // failure as "the daemon is unreachable" — the browser-debug mode's most
    // likely mistake gets its own, actionable copy.
    const is401 = bootError instanceof ApiError && bootError.status === 401;
    return (
      <div className="app">
        <TopBar
          lane={lane}
          onLane={setLane}
          sseState={events.sseState}
          children={status?.children ?? []}
          identity={{ email: boot.email, holderId: boot.holderId }}
          updateBanner={updateBanner}
        />
        <main className="phase-shell">
          <div className="phase-card unreachable-card">
            {is401 ? (
              <>
                <h1>Session expired</h1>
                <p>
                  The Kit daemon rejected the session token — reopen the UI with a fresh{' '}
                  <code>?token=</code> from session.json.
                </p>
              </>
            ) : (
              <>
                <h1>Can't reach the Kit daemon</h1>
                <p>{bootError?.message}</p>
                {canRestart() && (
                  <button
                    type="button"
                    className="btn btn-primary"
                    onClick={() => {
                      void restartKit();
                    }}
                  >
                    Restart
                  </button>
                )}
              </>
            )}
          </div>
        </main>
      </div>
    );
  }

  return (
    <div className="app">
      <TopBar
        lane={lane}
        onLane={setLane}
        sseState={events.sseState}
        children={status?.children ?? []}
        identity={{ email: boot.email, holderId: boot.holderId }}
        updateBanner={updateBanner}
      />

      {phase === 'signin' && (
        <main className="phase-shell">
          <SignIn boot={boot} />
        </main>
      )}

      {phase === 'boot' && (
        <main className="phase-shell">
          <BootProgress boot={boot} status={status} runsLive={runsLive} />
        </main>
      )}

      {phase === 'main' && (
        <main className={`workbench${showInspector ? '' : ' workbench--full'}`}>
          <NavRail nav={nav} onNav={handleNav} children={status?.children ?? []} />

          <section className="workbench-col">
            {nav === 'scenarios' && (
              <UCCards
                lane={lane}
                register={register}
                onRegister={setRegister}
                events={reconciledEvents}
                latestByRow={latestByRow}
                disabledReason={disabledReason}
                onSelectRun={handleSelectRun}
                banner={laneBanner}
                replaceCards={laneReplaceCards}
                extraPanel={laneExtraPanel}
              />
            )}

            {nav === 'history' && (
              <RunHistory
                history={history}
                selectedRunId={selectedRunId}
                compareRunId={compareRunId}
                onOpen={handleSelectRun}
                onCompare={handleCompare}
                onExport={(runId) => {
                  void exportRun(runId);
                }}
              />
            )}

            {nav === 'byo' && (
              <>
                {byo && (
                  <section className="byo-panel-shell">
                    <BYOPanel
                      byo={byo}
                      onSaved={refetchBYO}
                      onRestart={() => setByoRestartConfirm(true)}
                    />
                  </section>
                )}
                {byoRestartConfirm && (
                  <div className="byo-restart-confirm phase-card">
                    <p>Restarting applies your change. Runs in progress will be reset.</p>
                    {canRestart() ? (
                      <>
                        <button
                          type="button"
                          className="btn btn-primary"
                          onClick={() => {
                            setByoRestartConfirm(false);
                            void restartKit();
                          }}
                        >
                          Restart
                        </button>
                        <button
                          type="button"
                          className="btn btn-link"
                          onClick={() => setByoRestartConfirm(false)}
                        >
                          Cancel
                        </button>
                      </>
                    ) : (
                      <>
                        <p>Restart shnkitd manually to apply your change.</p>
                        <button
                          type="button"
                          className="btn btn-link"
                          onClick={() => setByoRestartConfirm(false)}
                        >
                          Close
                        </button>
                      </>
                    )}
                  </div>
                )}
              </>
            )}

            {nav === 'systems' && (
              <SystemsPage
                boot={boot}
                status={status}
                sseState={events.sseState}
                onResetComplete={() => setResetPending(true)}
                onVerified={handleVerified}
              />
            )}
          </section>

          {showInspector && (
            <aside className="workbench-inspector">
              {compareRunId !== undefined ? (
                <div className="inspector-split">
                  <div className="inspector-split-bar">
                    <button
                      type="button"
                      className="btn btn-link"
                      onClick={() => setCompareRunId(undefined)}
                    >
                      Close comparison
                    </button>
                  </div>
                  <RunInspector
                    runId={selectedRunId}
                    events={runEvents.events}
                    source={runEvents.source}
                    results={results}
                    summary={history.find((h) => h.runId === selectedRunId)}
                    providerLabel={deriveProviderLabel(selectedRunId, latestRunId, runEvents.events, byo)}
                    posture={status?.validator}
                  />
                  <RunInspector
                    runId={compareRunId}
                    events={compareEvents.events}
                    source={compareEvents.source}
                    results={results}
                    summary={history.find((h) => h.runId === compareRunId)}
                    providerLabel={deriveProviderLabel(compareRunId, latestRunId, compareEvents.events, byo)}
                    posture={status?.validator}
                  />
                </div>
              ) : (
                <div className="inspector-column">
                  <RunInspector
                    runId={selectedRunId}
                    events={runEvents.events}
                    source={runEvents.source}
                    results={results}
                    summary={history.find((h) => h.runId === selectedRunId)}
                    providerLabel={deriveProviderLabel(selectedRunId, latestRunId, runEvents.events, byo)}
                    posture={status?.validator}
                  />
                </div>
              )}
              {exportError && (
                <p role="alert" className="export-error">
                  Export failed: {exportError}
                </p>
              )}
            </aside>
          )}
        </main>
      )}
    </div>
  );
}
