// appModel.ts: the pure derivations App renders from, kept out of App.tsx so
// that file exports only components (react-refresh/only-export-components).
import type { BYOStatus, KitEvent, StatusResponse } from './types';
import { normaliseLane } from './inspect';

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
  // inspect.ts's shared read-side rule (old run-history records may still
  // carry the retired 'provider-data' lane value). An undefined `started`
  // (no run yet) stays 'conformant', today's default.
  const runLane = normaliseLane(started?.lane);
  if (runLane === 'ehr' && byo?.ehr?.applied) return 'Your EHR (FHIR data source)';
  if (runLane === 'conformant' && started?.uc === 'external' && byo?.davinci?.applied) {
    return 'Your Da Vinci system';
  }
  return undefined;
}
