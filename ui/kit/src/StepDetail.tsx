// StepDetail.tsx — the step detail pane: renders one Step in two
// presentations. Clinical view is the narration + the raw request/response
// payloads (a JsonView each, one search box wired to both). Substrate view
// is the payload-BLIND leg-facts framing: no payload JSON reaches the DOM
// in that view — only the envelope facts (correlation id, direction,
// counterpart, authority frames, approximate sizes).
import { useId, useMemo, useState } from 'react';
import type { JSX } from 'react';
import { isCarryRefusalDetail } from './inspect';
import type { RouteFrame, Step } from './inspect';
import type { BridgingCapture, DemoRecord, Register } from './types';
import { DEMO_RESTORED_VERDICT, LOCAL_DEMO_FRAMING, STEP_CLASS_META, type StepClass } from './bridgingmeta';
import { JsonView } from './JsonView';
import { TickIcon } from './StatusChip';
import { ApiError, getBridgingCapture } from './api';
import { computeXformDiff } from './xformclassify';
import { XformDiff } from './XformDiff';

export type InspectorView = 'clinical' | 'substrate';

// ValidatorPosture: GET /api/status's runtime-derived `validator` field
// ("stand-in" | "packaged"), threaded down through App -> RunInspector ->
// StepDetail. Absent (old daemon, boot-window race) degrades to 'stand-in'
// — the honest fallback; never assume 'packaged'.
export type ValidatorPosture = 'stand-in' | 'packaged';

export interface StepDetailProps {
  step: Step;
  view: InspectorView;
  posture?: ValidatorPosture;
  // Overview/Technical detail-level choice (RegisterSwitch, same idiom as
  // bridgingmeta.ts's Register-keyed copy) driving TransformCard's narration
  // line. undefined ⇒ 'overview' — the same honest-default idiom `posture`
  // above uses. Threaded App -> RunInspector -> StepDetail (App's existing
  // RegisterSwitch state) — no longer test-only.
  register?: Register;
}

// Every validation badge carries a posture label verbatim — a partner
// must never read the Kit's stand-in validator's verdict as FHIR
// conformance. The v1 wording ("...arrives with the packaged components")
// is now false either way (packaging shipped, but THIS validator instance
// may still be the stand-in).
// Pinned exactly; do not paraphrase.
export const VALIDATOR_POSTURE_LABEL =
  "checked by the Kit's stand-in validator — real conformance validation is off in this development build";

// The packaged posture's label, once the real HL7 validator child is
// actually running this check. Pinned exactly; do not paraphrase.
export const PACKAGED_VALIDATOR_POSTURE_LABEL =
  "checked by the Kit's local HL7 validator (offline IG set)";

function postureLabel(posture: ValidatorPosture): string {
  return posture === 'packaged' ? PACKAGED_VALIDATOR_POSTURE_LABEL : VALIDATOR_POSTURE_LABEL;
}

// The open-step copy — pinned exactly.
export const OPEN_STEP_NOTE = 'No response observed — the flow stopped here.';

// The leg.downgrade annotation, in partner copy (the raw engine Detail says
// "frame v1"/"stale-feed downgrade", internal vocabulary a partner cannot
// act on). One fixed sentence: the engine emits exactly one
// downgrade cause today, so the copy can be specific without parsing the
// Detail. Pinned exactly; do not paraphrase.
export const LEG_DOWNGRADE_NOTE =
  'The counterparty announced a newer envelope format but answered in the older one; the Smart Gateway processed the answer in the older format.';

// relayedStatusLine: the display-only sentence for a leg whose counterparty
// answered with a relayed non-2xx application status (ObserverEvent.Status).
// Display-only by design: the step's own ok/failed logic
// is deliberately unchanged (the exchange itself completed — the counterparty
// ANSWERED), but a rejection must never read as silently green.
export function relayedStatusLine(status: number): string {
  return `The counterparty’s application answered HTTP ${status} — relayed unchanged as this leg’s response.`;
}

// The substrate view's fixed framing sentence — pinned exactly.
export const SUBSTRATE_FRAMING =
  'Carried as a sealed envelope through the payload-blind Hub; authority evaluated per leg.';

// Shown-never-faked: a `validate` step is a LOCAL check against the Kit's
// stand-in validator (inspect.ts makeValidateStep is single-frame by
// design — it never sets `response`, and nothing crosses the Hub for this
// step). SUBSTRATE_FRAMING's "sealed envelope through the
// payload-blind Hub" framing would be a false claim for this step kind, so
// validate steps get their own line instead. Pinned exactly; do not
// paraphrase.
export const VALIDATE_SUBSTRATE_NOTE =
  "Checked locally against the Kit's validator — this step never crosses the Hub.";

// Shown-never-faked: a `sor` step is a SINGLE observer frame — one read of
// the gateway's configured data source. Its request.payload holds the
// RETURNED resource bytes (never a "request"), and it never has a response,
// so it never crosses the Hub. Both views borrow this honest local-read line
// instead of the leg-step "Request/Response" pairing (clinical) or the
// SUBSTRATE_FRAMING sealed-envelope claim (substrate). Pinned exactly; do not
// paraphrase. "gateway"/"data source" register — never "substrate".
export const SOR_LOCAL_READ_NOTE =
  "Read locally from the gateway's configured data source — this step never crosses the Hub.";

// ---------------------------------------------------------------------------
// Bridging content — TransformCard's two pinned empty-content notes
// + the transform-refusal species' pinned zero-bytes note. Chosen by a
// LossReport's own shape (TransformCard) or by which RouteFrame fields are
// populated (RefusalCard) — never by guessing at legType/kind. Pinned
// exactly; do not paraphrase.
// ---------------------------------------------------------------------------

// The DTR-fetch case: a leg genuinely crossed a compat-manifest chain, but
// this run's payload happened to carry nothing the chain needed to carry or
// synthesize — an honest "nothing to show", not a claim the chain is
// lossless BY CONSTRUCTION (that's IDENTITY_CHAIN_NOTE below). Pinned
// exactly; do not paraphrase.
export const TRANSFORM_EMPTY_CONTENT_NOTE =
  'no content differences on this leg — transport envelope';

// The CRD-full case: every hop of the routed chain is class `full` —
// lossless by construction, not merely lossless on this run's payload — so
// the stronger claim is honest here specifically. Pinned exactly; do not
// paraphrase.
export const IDENTITY_CHAIN_NOTE = 'identity chain: bytes unchanged, proven';

// The transform-refusal species (a leg.failed carrying Route.Chain): the
// gated step refused BEFORE egressAdapt produced anything to send — nothing
// left the gateway for this leg. Pinned exactly; do not paraphrase.
export const ZERO_BYTES_NOTE = 'refused before sending — zero bytes crossed the network';

// The carry-integrity refusal species (the third leg.failed + Route
// producer): a resumed pended request whose own record says content was
// carried across a version bridge, arriving without that content. NOT a
// mid-bridge "no honest source" refusal — the payload was refused before the
// bridge ran at all. Pinned exactly; do not paraphrase.
export const CARRY_REFUSAL_NOTE =
  'This resumed request no longer carries content its own record says it must, so the Smart Gateway refused rather than send a request that silently lost it.';

export interface DirectionRow {
  arrow: '→' | '←';
  who: string;
  what: string;
}

// directionRows: the who-sent-what-to-whom summary above the narration —
// derived ONLY from what the step observed (an open leg gets no back row;
// a failed leg's back row says exactly that).
//
// A refused leg (step.refusal set — either species) returns NO
// rows at all: both leg.refused (no shared contract line) and the
// egressAdapt transform-refusal leg.failed fire BEFORE anything is sent —
// the generic leg case's unconditional "→ … request" row below would
// fabricate an outbound exchange that never happened. RefusalCard (below)
// carries the honest "nothing was sent" story instead.
export function directionRows(step: Step): DirectionRow[] {
  switch (step.kind) {
    case 'leg': {
      if (step.refusal !== undefined) return [];
      const cp = step.counterpart ?? 'the hosted counterparty';
      const rows: DirectionRow[] = [
        { arrow: '→', who: `Smart Gateway → Hub → ${cp}`, what: `${step.request?.op ?? step.legType} request` },
      ];
      if (step.status === 'ok') {
        rows.push({ arrow: '←', who: `${cp} → Hub → Smart Gateway`, what: `${step.response?.op ?? 'response'} — verified response` });
      } else if (step.status === 'failed') {
        rows.push({ arrow: '←', who: `${cp} → Hub → Smart Gateway`, what: `no verified response — ${step.response?.detail ?? 'the leg did not complete'}` });
      }
      return rows;
    }
    case 'ingress': {
      const rows: DirectionRow[] = [
        { arrow: '→', who: 'Provider system → Smart Gateway', what: `${step.legType} request received` },
      ];
      if (step.response !== undefined) {
        rows.push({ arrow: '←', who: 'Smart Gateway → Provider system', what: `HTTP ${step.httpStatus ?? '?'} response` });
      }
      return rows;
    }
    case 'validate':
      return [
        { arrow: '→', who: 'Smart Gateway → Validator', what: 'resource sent for $validate' },
        { arrow: '←', who: 'Validator → Smart Gateway', what: `result: ${step.validation ?? 'unknown'}` },
      ];
    case 'sor':
      return [
        { arrow: '→', who: 'Smart Gateway → its data source', what: `read: ${step.sorOp ?? 'record'}` },
        { arrow: '←', who: 'its data source → Smart Gateway', what: step.sorDetail ?? 'returned' },
      ];
  }
}

function DirectionRows({ step }: { step: Step }): JSX.Element {
  return (
    <div className="dir-rows">
      {directionRows(step).map((r, i) => (
        <div key={i} className="dir-row">
          <span className="arr">{r.arrow}</span>
          <span className="who">{r.who}</span>
          <span className="what">{r.what}</span>
        </div>
      ))}
    </div>
  );
}

function sizeLabel(payload: unknown): string | undefined {
  if (payload === undefined) return undefined;
  const bytes = JSON.stringify(payload).length;
  const kb = Math.max(1, Math.round(bytes / 1024));
  return `≈ ${kb} KB`;
}

// Detail-less validate badge: the badge
// received the full `validation` detail string all along (frame.detail —
// "valid", or the invalid reason, e.g. "invalid: missing required element
// Claim.type") but discarded everything except the ok/not-ok bit, rendering
// a bare "Invalid" with no reason attached to the badge itself — a consumer
// of ValidationBadge in isolation lost the WHY. Now the badge carries its
// own reason for an invalid verdict (nothing useful to add for "valid").
// Callers no longer need a separate failureDetail paragraph to repeat the
// same sentence for a validate step (see the isValidate branches below).
function ValidationBadge({
  validation,
  posture,
}: {
  validation: string;
  posture: ValidatorPosture;
}): JSX.Element {
  const ok = validation === 'valid';
  return (
    <div className="validation-badge-group">
      <span className={`validation-badge ${ok ? 'validation-ok' : 'validation-failed'}`}>
        {ok && TickIcon}
        {ok ? 'Valid' : 'Invalid'}
      </span>
      {!ok && <span className="validation-detail">{validation}</span>}
      <span className="validator-posture-label">{postureLabel(posture)}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// TransformCard — the LossReport story a bridged leg carries.
// ---------------------------------------------------------------------------

// TRANSFORM_CARD_NARRATION is register-aware copy (RegisterSwitch's
// Overview/Technical choice, same idiom as bridgingmeta.ts's
// CONTRACT_LINE_EXPLAINER) but NOT one of the three verbatim-pinned strings
// above — it's a framing sentence, not a claim StepDetail.test.tsx has to
// double-assert byte-exact. House register rules still apply: no internal
// vocabulary — never "substrate"/"arm 3"/"knob", and never
// "compat-manifest"/"minted" either; "compatibility steps" is the
// partner-facing name, matching bridgingmeta.ts's CONTRACT_LINE_EXPLAINER.
export const TRANSFORM_CARD_NARRATION: Record<Register, string> = {
  overview:
    'This step crossed a version boundary before it left the gateway. Below is exactly what traveled across unread, and what the network filled in deterministically rather than guessed.',
  technical:
    "This leg's payload passed through a chain of compatibility steps before it left the gateway. The loss report below names every element carried across unread for the other side to restore, and every element deterministically synthesized rather than fabricated.",
};

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null;
}

export interface ParsedLossEntry {
  path: string;
  detail?: string;
}

export interface ParsedLossReport {
  module: string;
  source: string;
  target: string;
  carried?: ParsedLossEntry[];
  synthesized?: ParsedLossEntry[];
}

function parseLossEntries(v: unknown): ParsedLossEntry[] | undefined {
  if (!Array.isArray(v)) return undefined;
  const out: ParsedLossEntry[] = [];
  for (const item of v) {
    if (!isRecord(item) || typeof item.path !== 'string') continue;
    out.push({ path: item.path, detail: typeof item.detail === 'string' ? item.detail : undefined });
  }
  return out;
}

function parseLossReport(v: unknown): ParsedLossReport | undefined {
  if (!isRecord(v)) return undefined;
  const { module, source, target } = v;
  if (typeof module !== 'string' || typeof source !== 'string' || typeof target !== 'string') return undefined;
  return {
    module,
    source,
    target,
    carried: parseLossEntries(v.carried),
    synthesized: parseLossEntries(v.synthesized),
  };
}

// SHN_LOSS_REPORT_EXT_URL mirrors sdk/carry.go's LossReportExtURL
// ("http://smarthealth.network/fhir/StructureDefinition/shn-loss-report")
// byte-for-byte. ui/kit is a separate module pinned against published
// shn-gateway/shn-sdk releases (kit/go.mod) — it cannot import the Go sdk to
// read the constant live, so this is a literal copy, same precedent as
// kit/kitd/bridgingassets/README.md's hand-regenerated golden copies: if
// sdk/carry.go's LossReportExtURL ever changes, this string goes
// stale silently — there is no cross-module CI tie — and the parse below
// just finds no matching extension (degrades to `undefined`, never throws).
const SHN_LOSS_REPORT_EXT_URL = 'http://smarthealth.network/fhir/StructureDefinition/shn-loss-report';

// parseLossReports reads a transform leg's Provenance JSON (transform.payload
// — the resource sdk/provenance.go's BuildTransformProvenance built) for its
// shn-loss-report extension and shape-checks the valueString back into
// ParsedLossReport[] — the same never-throw idiom as inspect.ts's
// parseObserver/parseRoute: anything malformed (wrong shape, unparsable
// JSON, no matching extension) degrades to `undefined`, never an exception.
export function parseLossReports(payload: unknown): ParsedLossReport[] | undefined {
  if (!isRecord(payload)) return undefined;
  const extensions = payload.extension;
  if (!Array.isArray(extensions)) return undefined;
  const ext = extensions.find((e) => isRecord(e) && e.url === SHN_LOSS_REPORT_EXT_URL);
  if (!isRecord(ext) || typeof ext.valueString !== 'string') return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(ext.valueString);
  } catch {
    return undefined;
  }
  if (!Array.isArray(parsed)) return undefined;
  const reports: ParsedLossReport[] = [];
  for (const item of parsed) {
    const report = parseLossReport(item);
    if (report) reports.push(report);
  }
  return reports;
}

function ChainHops({ chain }: { chain: RouteFrame['chain'] }): JSX.Element | null {
  if (!chain || chain.length === 0) return null;
  return (
    <ol className="chain-hops">
      {chain.map((hop, i) => (
        <li key={i} className={`chain-hop chain-hop-${hop.class}`}>
          <span className="chain-hop-line">
            {hop.from} → {hop.to}
          </span>
          <span className="chain-hop-class">{STEP_CLASS_META[hop.class as StepClass]?.label ?? hop.class}</span>
        </li>
      ))}
    </ol>
  );
}

function LossEntryList({ title, entries }: { title: string; entries: ParsedLossEntry[] }): JSX.Element {
  return (
    <div className="loss-entries">
      <h5>{title}</h5>
      <ul>
        {entries.map((e, i) => (
          <li key={i}>
            <span className="loss-path">{e.path}</span>
            {e.detail && <span className="loss-detail"> — {e.detail}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}

// ---------------------------------------------------------------------------
// TransformExpander — the on-demand before/after transformation view: a
// collapsed-by-default "Show transformation" affordance that fetches the
// gateway's edge capture for this leg ONLY once expanded (never eagerly
// alongside the rest of a step's frames), caches the result across
// collapse/re-expand, and renders it through XformDiff.tsx once it arrives.
// Three pinned strings below; do not paraphrase.
// ---------------------------------------------------------------------------

// XFORM_EXPANDER_LABEL: the collapsed expander's pinned label. Pinned
// exactly; do not paraphrase.
export const XFORM_EXPANDER_LABEL = 'Show transformation';

// CAPTURE_POSTURE_NOTE: shown alongside a successfully fetched capture — the
// same mandatory honesty gate every other content-bearing card in this file
// carries (postureLabel/ValidatorPosture), but for a capture rather than a
// $validate verdict: an edge capture is an INSPECTION view kept in memory
// while the compatibility simulation is on, never an audit or wire record.
// Pinned exactly; do not paraphrase.
export const CAPTURE_POSTURE_NOTE =
  "Captured at your gateway's edge as this leg left — an inspection view, available while the compatibility simulation is on. This is not an audit or wire record.";

// CAPTURE_UNAVAILABLE_NOTE: shown when the fetch resolves 404 — either wire
// reason (the compatibility simulation is off, or this leg's capture aged
// out/was never recorded) collapses to this SAME honest sentence; the two
// reasons are never distinguished client-side (api.ts's getBridgingCapture
// rejects both as ApiError(404) with no further discrimination). Pinned
// exactly; do not paraphrase.
export const CAPTURE_UNAVAILABLE_NOTE =
  'No transformation capture is available for this leg. Captures are kept in memory for recent runs while the compatibility simulation is on, and clear when it turns off.';

// CAPTURE_ERROR_NOTE: shown when the capture fetch fails for any reason
// other than a 404 (a transport hiccup, the gateway child unreachable, an
// unexpected server error) — a fixed, participant-facing sentence, never the
// raw Error/ApiError message (which can carry internal transport detail no
// partner needs, or shouldn't see). Pinned exactly; do not paraphrase.
export const CAPTURE_ERROR_NOTE = "Couldn't load this leg's transformation capture.";

// xformPaneLabels: the before/after pane labels XformDiff.tsx renders for a
// live leg's fetched capture below. The gateway's own EdgeCapture is a
// PRE-SEAL snapshot (captured just before this leg's payload is sealed for
// Hub transit, never a wire read) — "as sent" is this surface's honest
// shorthand for that moment, not a claim about bytes actually observed on
// the wire.
function xformPaneLabels(contract: string, from: string, to: string): { beforeLabel: string; afterLabel: string } {
  return {
    beforeLabel: `Before — as built (${contract} ${from})`,
    afterLabel: `After — as sent (${contract} ${to})`,
  };
}

// demoXformPaneLabels: the same before-pane wording, but "as carried" for
// the after pane — the carry demonstration's local record in
// DemoStepDetail never sent anything (LOCAL_DEMO_FRAMING says so
// explicitly); "as sent" would be a false claim for a pair that only ever
// existed in the engine's in-process exhibit.
function demoXformPaneLabels(contract: string, from: string, to: string): { beforeLabel: string; afterLabel: string } {
  return {
    beforeLabel: `Before — as built (${contract} ${from})`,
    afterLabel: `After — as carried (${contract} ${to})`,
  };
}

type CaptureFetchState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'found'; capture: BridgingCapture }
  | { kind: 'not-found' }
  | { kind: 'error'; message: string };

function captureErrorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}

// TransformExpander: collapsed by default (state stays 'idle' and nothing is
// fetched until the participant clicks) — the SAME lazy-fetch idiom
// FreeFormPanel.tsx's patient-context read already uses (idle/loading/
// loaded/error), with a 'not-found' state added for the honest 404 case.
// Caching is structural, not an extra flag: once `state` leaves 'idle' it
// never resets on collapse, so re-expanding the SAME step never re-fetches.
function TransformExpander({ correlationId }: { correlationId: string | undefined }): JSX.Element {
  const [expanded, setExpanded] = useState(false);
  const [state, setState] = useState<CaptureFetchState>({ kind: 'idle' });
  // Every step in a run can render its own TransformExpander, so the body id
  // must be per-instance, not the same literal reused across the page — a
  // duplicate DOM id would break aria-controls (and any other id-targeted
  // lookup) once more than one is mounted at once. useId() derives a stable,
  // component-instance-unique id.
  const bodyId = `transform-expander-body-${useId()}`;

  // capturedRaw: JSON.stringify of the fetched capture's before/after,
  // hoisted into its own useMemo rather than called inline in the JSX below
  // — state.capture is a stable object reference once fetched (only a fresh
  // fetch ever replaces it), so an inline call would re-run the stringify
  // (and pay its cost, up to the capture's full size) on every unrelated
  // re-render of this component (e.g. a sibling search box's keystroke),
  // defeating the point of XformDiff's own memoization one level down.
  const capture = state.kind === 'found' ? state.capture : undefined;
  const capturedRaw = useMemo(
    () => (capture ? { rawBefore: JSON.stringify(capture.before), rawAfter: JSON.stringify(capture.after) } : undefined),
    [capture],
  );

  // fetchCapture: the one place that actually calls getBridgingCapture —
  // shared by the first expand (handleToggle, gated on state.kind ===
  // 'idle') and by a retry from the error state (handleRetry, which calls
  // it unconditionally). Both paths converge here so a retry re-runs
  // exactly the same request/response handling as the original attempt.
  const fetchCapture = () => {
    if (correlationId === undefined) return;
    setState({ kind: 'loading' });
    getBridgingCapture(correlationId)
      .then((capture) => setState({ kind: 'found', capture }))
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not-found' });
        } else {
          setState({ kind: 'error', message: captureErrorMessage(err) });
        }
      });
  };

  const handleToggle = () => {
    const nextExpanded = !expanded;
    setExpanded(nextExpanded);
    if (nextExpanded && state.kind === 'idle') fetchCapture();
  };

  const handleRetry = () => fetchCapture();

  return (
    <div className="transform-expander">
      <button
        type="button"
        className="transform-expander-toggle"
        aria-expanded={expanded}
        aria-controls={bodyId}
        onClick={handleToggle}
      >
        {expanded ? 'Hide transformation' : XFORM_EXPANDER_LABEL}
      </button>
      {expanded && (
        <div className="transform-expander-body" id={bodyId}>
          {state.kind === 'loading' && <p className="transform-expander-loading">Loading…</p>}
          {state.kind === 'found' && capturedRaw && (
            <>
              <XformDiff
                before={state.capture.before}
                after={state.capture.after}
                rawBefore={capturedRaw.rawBefore}
                rawAfter={capturedRaw.rawAfter}
                lossReports={state.capture.lossReports}
                {...xformPaneLabels(state.capture.contract, state.capture.from, state.capture.to)}
              />
              <p className="capture-posture-note">{CAPTURE_POSTURE_NOTE}</p>
            </>
          )}
          {state.kind === 'not-found' && <p className="capture-unavailable-note">{CAPTURE_UNAVAILABLE_NOTE}</p>}
          {state.kind === 'error' && (
            <div role="alert" className="transform-expander-error">
              <p className="transform-expander-error-message">{CAPTURE_ERROR_NOTE}</p>
              <button type="button" className="transform-expander-retry" onClick={handleRetry}>
                Retry
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// TransformCard: the LossReport story for a step whose leg was bridged
// across a compat-manifest chain (step.transform present — inspect.ts's
// joined leg.transformed frame). Content:
//   - the attempted chain, preferring the PAIRED leg step's own Route
//     (leg.originated's Chain — the source of truth for what was actually
//     selected) over transform.route (leg.transformed never sets Route
//     itself per gateway/engine/gateway.go's transformedObserverEvent, so
//     this fallback is defensive, not the common path).
//   - the LossReport rows parsed from the Provenance payload — Carried
//     entries labeled "carried, not lost" (still travels, unread by the
//     target line, restored on the way back); Synthesized entries their own
//     labeled section (deterministically minted, never a guess).
//   - the empty-content pinned note, chosen by the CHAIN's own class shape
//     (never by "did this happen to carry nothing this run" alone): every
//     hop `full` ⇒ IDENTITY_CHAIN_NOTE (lossless BY CONSTRUCTION);
//     anything else (including an unresolved/empty chain) ⇒
//     TRANSFORM_EMPTY_CONTENT_NOTE (the honest, weaker "nothing on THIS
//     leg" claim).
//   - the raw Provenance, disclosed (never hidden) in a <details>.
//   - the validator posture label — the same mandatory honesty gate every
//     other content-bearing card in this file carries, reusing
//     postureLabel/ValidatorPosture; there's no $validate verdict for a
//     transform leg, so this renders unconditionally rather than gated on
//     step.validation (which leg steps never set).
function TransformCard({
  step,
  posture,
  register,
}: {
  step: Step;
  posture: ValidatorPosture;
  register: Register;
}): JSX.Element | null {
  const transform = step.transform;
  if (!transform) return null;

  const chain = step.route?.chain ?? transform.route?.chain ?? [];
  const reports = parseLossReports(transform.payload) ?? [];
  const hasContent = reports.some(
    (r) => (r.carried && r.carried.length > 0) || (r.synthesized && r.synthesized.length > 0),
  );
  const identityChain = chain.length > 0 && chain.every((h) => h.class === 'full');
  const emptyNote = identityChain ? IDENTITY_CHAIN_NOTE : TRANSFORM_EMPTY_CONTENT_NOTE;

  return (
    <div className="transform-card">
      <h4>Cross-version transform</h4>
      <p className="transform-narration">{TRANSFORM_CARD_NARRATION[register]}</p>
      <ChainHops chain={chain} />
      {hasContent ? (
        <div className="loss-report-list">
          {reports.map((r, i) => (
            <div key={i} className="loss-report">
              <div className="loss-report-module">
                {r.module} <span className="loss-report-lines">{r.source} → {r.target}</span>
              </div>
              {r.carried && r.carried.length > 0 && <LossEntryList title="Carried, not lost" entries={r.carried} />}
              {r.synthesized && r.synthesized.length > 0 && (
                <LossEntryList title="Synthesized" entries={r.synthesized} />
              )}
            </div>
          ))}
        </div>
      ) : (
        <p className="transform-empty-note">{emptyNote}</p>
      )}
      <TransformExpander correlationId={step.correlationId} />
      <p className="validator-posture-label">{postureLabel(posture)}</p>
      <details className="raw-provenance">
        <summary>Raw Provenance</summary>
        <JsonView value={transform.payload} />
      </details>
    </div>
  );
}

// ---------------------------------------------------------------------------
// RefusalCard — the three refusal species (transform refusal, route
// refusal, carry-integrity refusal), never conflated. Species are
// discriminated ONLY by which RouteFrame fields the refusal carries plus
// the engine's Detail marker —
// Route.Chain non-empty + the carry marker (isCarryRefusalDetail) ⇒ the
// carry-integrity species (guardPendCarry's leg.failed); Route.Chain
// non-empty otherwise ⇒ the transform-refusal species (egressAdapt's
// leg.failed); Own/Peer/BridgeIssue populated ⇒ the route-refusal
// species (leg.refused, no shared contract line) — NEVER by guessing from
// step.kind/legType/narration (all species produce the same kind:'leg' Step
// shape; inspect.ts's makeRefusedLegStep and makeTransformRefusedLegStep are
// otherwise structurally identical).
// ---------------------------------------------------------------------------

// splitOnTopLevelCommas: a paren-depth-aware comma split — a
// SemanticChangeError's MissingElements can themselves read like
// "QuestionnaireResponse.extension:qr-coverage (ambiguous: 2
// Coverage-referencing qr-context entries, multi-coverage source)"
// (bridgingassets/README.md's refusal-input-2.1 exhibit) — ONE element
// whose own parenthesized detail contains a comma. A naive split(',') would
// cut that single element into two. Depth-tracked so a comma inside
// parens never splits; only top-level commas (gateway/engine/transform_pas.go's
// own `strings.Join(e.MissingElements, ", ")` separator) do.
function splitOnTopLevelCommas(s: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let cur = '';
  for (const ch of s) {
    if (ch === '(') depth++;
    if (ch === ')') depth--;
    if (ch === ',' && depth === 0) {
      parts.push(cur.trim());
      cur = '';
    } else {
      cur += ch;
    }
  }
  if (cur.trim()) parts.push(cur.trim());
  return parts;
}

// parseMissingElements: pulls the "no honest byte-level source for X, Y"
// tail off a *SemanticChangeError's Error() string (gateway/engine/
// transform_pas.go's exact format) — undefined (never throws) when the
// marker isn't present or the tail is empty, so callers fall back to
// showing the whole Detail text verbatim rather than a mis-parsed list.
function parseMissingElements(detail: string | undefined): string[] | undefined {
  if (detail === undefined) return undefined;
  const marker = 'no honest byte-level source for ';
  const idx = detail.indexOf(marker);
  if (idx === -1) return undefined;
  const tail = detail.slice(idx + marker.length).trim();
  if (!tail) return undefined;
  const parts = splitOnTopLevelCommas(tail).filter((p) => p.length > 0);
  return parts.length > 0 ? parts : undefined;
}

// parseCarryPath: pulls the missing declared-carry path out of a
// carry-integrity refusal's Detail (gateway/engine/gateway.go's
// verifyCarryPresent format: `declared carry "<path>" not found …`) —
// undefined when the marker isn't present, so the card falls back to the
// verbatim Detail rather than a mis-parsed fragment (same posture as
// parseMissingElements).
function parseCarryPath(detail: string | undefined): string | undefined {
  if (detail === undefined) return undefined;
  const marker = 'declared carry "';
  const start = detail.indexOf(marker);
  if (start === -1) return undefined;
  const rest = detail.slice(start + marker.length);
  const end = rest.indexOf('"');
  if (end <= 0) return undefined;
  return rest.slice(0, end);
}

function TokenChips({ tokens, className }: { tokens: string[] | undefined; className: string }): JSX.Element {
  if (!tokens || tokens.length === 0) {
    return <span className="token-chip-none">none declared</span>;
  }
  return (
    <div className="token-chips">
      {tokens.map((t) => (
        <span key={t} className={`token-chip ${className}`}>
          {t}
        </span>
      ))}
    </div>
  );
}

function RefusalCard({ step }: { step: Step }): JSX.Element | null {
  const refusal = step.refusal;
  if (!refusal) return null;
  const detail = step.response?.detail;

  // Precedence: if both field-sets were somehow populated, the chain-borne
  // species win — server-side they are mutually exclusive by construction
  // (routeInfoFor sets Chain, refusalRouteInfo sets Own/Peer/BridgeIssue; a
  // single RouteInfo value is only ever built by one of the two), so this
  // ordering is a defensive tie-break, never a path the real gateway
  // exercises. Within the chain-borne shape, the carry-integrity refusal
  // (guardPendCarry — the THIRD leg.failed + Route producer)
  // is discriminated by the engine's Detail marker: rendering it under the
  // transform species' "no honest source for the target line" headline would
  // be a false attribution — the payload was refused BEFORE the bridge ran,
  // for missing previously-carried content, not for an unbridgeable source.
  const hasChain = (refusal.chain?.length ?? 0) > 0;
  const isCarryRefusal = hasChain && isCarryRefusalDetail(detail);
  const isTransformRefusal = hasChain && !isCarryRefusal;
  const isRouteRefusal =
    !hasChain &&
    ((refusal.own?.length ?? 0) > 0 || (refusal.peer?.length ?? 0) > 0 || refusal.bridgeIssue !== undefined);

  if (isCarryRefusal) {
    const carryPath = parseCarryPath(detail);
    return (
      <div className="refusal-card refusal-card-carry">
        <h4>Refused at resume — previously carried content is missing</h4>
        <ChainHops chain={refusal.chain} />
        <p className="carry-refusal-note">{CARRY_REFUSAL_NOTE}</p>
        {carryPath ? (
          <div className="refusal-elements">
            <h5>Missing carried content</h5>
            <ul>
              <li>{carryPath}</li>
            </ul>
          </div>
        ) : (
          detail && <p className="refusal-detail">{detail}</p>
        )}
        <p className="zero-bytes-note">{ZERO_BYTES_NOTE}</p>
      </div>
    );
  }

  if (isTransformRefusal) {
    const elements = parseMissingElements(detail);
    return (
      <div className="refusal-card refusal-card-transform">
        <h4>Refused mid-bridge — no honest source for the target line</h4>
        <ChainHops chain={refusal.chain} />
        {elements ? (
          <div className="refusal-elements">
            <h5>No honest byte-level source for</h5>
            <ul>
              {elements.map((el, i) => (
                <li key={i}>{el}</li>
              ))}
            </ul>
          </div>
        ) : (
          detail && <p className="refusal-detail">{detail}</p>
        )}
        <p className="zero-bytes-note">{ZERO_BYTES_NOTE}</p>
      </div>
    );
  }

  if (isRouteRefusal) {
    return (
      <div className="refusal-card refusal-card-route">
        <h4>No shared contract line — refused before sending anything</h4>
        <div className="refusal-tokens">
          <div className="refusal-token-group">
            <h5>This gateway declares</h5>
            <TokenChips tokens={refusal.own} className="own-token" />
          </div>
          <div className="refusal-token-group">
            <h5>The counterparty declares</h5>
            <TokenChips tokens={refusal.peer} className="peer-token" />
          </div>
        </div>
        {refusal.bridgeIssue && <p className="bridge-issue">{refusal.bridgeIssue}</p>}
        {detail && <p className="refusal-detail">{detail}</p>}
      </div>
    );
  }

  // Defensive: a Route present on the refusal but shaped like neither
  // species (e.g. an all-undefined {} — refusalRouteInfo's own doc notes
  // this is nil-through in practice, never emitted by the real gateway).
  // Nothing honest to render beyond the raw Detail, if any — still wrapped
  // in the same `.refusal-card` shell as the two named species for visual
  // consistency (a refusal is a refusal, even an unclassifiable one).
  return (
    <div className="refusal-card refusal-card-unclassified">
      {detail && <p className="refusal-detail">{detail}</p>}
    </div>
  );
}

export function StepDetail({ step, view, posture = 'stand-in', register = 'overview' }: StepDetailProps): JSX.Element {
  const [search, setSearch] = useState('');

  const rootClassName = `detail step-status-${step.status} step-kind-${step.kind}`;
  const failureDetail = step.status === 'failed' ? step.response?.detail ?? step.request?.detail : undefined;
  const isValidate = step.kind === 'validate';
  const isSor = step.kind === 'sor';
  // isRefused: step.refusal is set on BOTH self-contained failed-
  // leg species (inspect.ts's makeRefusedLegStep/makeTransformRefusedLegStep)
  // — neither ever got a request/response pair for an actual exchange (both
  // fire before anything was sent), so the generic leg-facts/Request-Response
  // rendering below would show phantom/undefined bytes. Carved out the same
  // way as isValidate/isSor above, RefusalCard replacing the payload panes.
  const isRefused = step.refusal !== undefined;

  if (view === 'substrate') {
    // sor carve-out: a sor read never crosses the Hub, so SUBSTRATE_FRAMING's
    // "sealed envelope through the payload-blind Hub" claim would be false,
    // and the generic leg-facts table (correlation id / counterpart /
    // authority frames) carries none of this single-frame read's facts.
    // Show the honest read facts (op + outcome + returned size — never the
    // returned JSON) and the local-read line, mirroring the validate carve-
    // out above.
    if (isSor) {
      const returnedSize = sizeLabel(step.request?.payload);
      return (
        <div className={rootClassName} data-view="substrate">
          <dl className="facts">
            <dt>Read</dt>
            <dd>{step.sorOp ?? '—'}</dd>
            <dt>Outcome</dt>
            <dd>{step.sorDetail ?? '—'}</dd>
            {returnedSize && (
              <>
                <dt>Returned size</dt>
                <dd>{returnedSize}</dd>
              </>
            )}
          </dl>
          <p className="sor-local-read-note">{SOR_LOCAL_READ_NOTE}</p>
        </div>
      );
    }

    // Finding 1: validate steps never gate on `!step.response` (they never
    // have one, by design — makeValidateStep is single-frame). Rendering the
    // generic leg-facts dl + SUBSTRATE_FRAMING + OPEN_STEP_NOTE for a
    // SUCCESSFUL validate step would be a shown-never-faked violation: an
    // empty/near-empty leg-facts table, a false "sealed envelope through the
    // Hub" claim, and a false "flow stopped here" note next to a "Valid"
    // badge. Suppress all three; show only the badge + the honest line.
    if (isValidate) {
      return (
        <div className={rootClassName} data-view="substrate">
          {step.validation !== undefined && (
            <ValidationBadge validation={step.validation} posture={posture} />
          )}
          <p className="validate-substrate-note">{VALIDATE_SUBSTRATE_NOTE}</p>
        </div>
      );
    }

    // Refusal carve-out (all three species): SUBSTRATE_FRAMING's "sealed envelope
    // through the payload-blind Hub" claim is false for a refusal — nothing
    // was sent, so nothing was carried through the Hub at all. Show only the
    // two facts a refusal genuinely has (leg id, counterpart) plus
    // RefusalCard's own species-specific story; no sizes (no payload ever
    // existed), no OPEN_STEP_NOTE (that note is for a request awaiting a
    // response, not a request that was never made).
    if (isRefused) {
      return (
        <div className={rootClassName} data-view="substrate">
          <dl className="facts">
            <dt>Leg</dt>
            <dd>{step.correlationId ?? '—'}</dd>
            <dt>Counterpart</dt>
            <dd>{step.counterpart ?? '—'}</dd>
          </dl>
          <RefusalCard step={step} />
        </div>
      );
    }

    const requestSize = sizeLabel(step.request?.payload);
    const responseSize = sizeLabel(step.response?.payload);

    return (
      <div className={rootClassName} data-view="substrate">
        <p className="substrate-framing">{SUBSTRATE_FRAMING}</p>
        <dl className="facts">
          <dt>Leg</dt>
          <dd>{step.correlationId ?? '—'}</dd>
          <dt>Direction</dt>
          <dd>{step.request?.direction ?? step.response?.direction ?? '—'}</dd>
          <dt>Counterpart</dt>
          <dd>{step.counterpart ?? '—'}</dd>
          <dt>Request authority</dt>
          <dd>{step.requestAuthority ?? '—'}</dd>
          <dt>Response authority</dt>
          <dd>{step.responseAuthority ?? '—'}</dd>
          {requestSize && (
            <>
              <dt>Request size</dt>
              <dd>{requestSize}</dd>
            </>
          )}
          {responseSize && (
            <>
              <dt>Response size</dt>
              <dd>{responseSize}</dd>
            </>
          )}
          {step.kind === 'ingress' && step.httpStatus !== undefined && (
            <>
              <dt>HTTP status</dt>
              <dd>{step.httpStatus}</dd>
            </>
          )}
          {step.kind === 'leg' && step.response?.status !== undefined && (
            <>
              <dt>Relayed status</dt>
              <dd>{step.response.status}</dd>
            </>
          )}
        </dl>
        {step.validation !== undefined && (
          <ValidationBadge validation={step.validation} posture={posture} />
        )}
        {!step.response && <p className="open-step-note">{OPEN_STEP_NOTE}</p>}
        {failureDetail && <p className="failure-detail">{failureDetail}</p>}
        {step.downgrade !== undefined && <p className="leg-downgrade-note">{LEG_DOWNGRADE_NOTE}</p>}
        {step.transform && <TransformCard step={step} posture={posture} register={register} />}
      </div>
    );
  }

  // Finding 1: same shown-never-faked gate as substrate — a validate step
  // never has a response, so gating the "No response observed" note on
  // `!step.response` alone would render it next to a "Valid" badge for a
  // successful, complete check. Suppress the Response section + open-step
  // note entirely and render only narration + the validated Request payload
  // + the badge.
  if (isValidate) {
    // Validate-step label: a
    // validate step's "Request" pane is not an HTTP request awaiting a
    // response — it's the FHIR resource under validation, checked once,
    // locally (VALIDATE_SUBSTRATE_NOTE says so explicitly). Calling it
    // "Request"/"Search request" borrowed the leg-step vocabulary for a
    // step kind that isn't a leg; renamed to "Resource"/"Search resource".
    return (
      <div className={rootClassName} data-view="clinical">
        <DirectionRows step={step} />
        <p className="narr">{step.narration}</p>
        <label className="json-search-label">
          Search resource
          <input
            type="text"
            className="json-search-input"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>
        <div className="pane request-payload">
          <h4>Resource</h4>
          <JsonView value={step.request?.payload} search={search} />
        </div>
        {step.validation !== undefined && (
          <ValidationBadge validation={step.validation} posture={posture} />
        )}
      </div>
    );
  }

  // sor carve-out: a sor step's request.payload is the RETURNED resource, not
  // a request, and there is no response — so the leg-step "Request"/"Response"
  // panes + OPEN_STEP_NOTE would be three false claims (mislabeled bytes, a
  // phantom empty Response, and a "flow stopped here" note next to a
  // completed read). Render the returned bytes honestly (only when present)
  // + a local-read note. Mirrors the validate carve-out above.
  if (isSor) {
    const returned = step.request?.payload;
    return (
      <div className={rootClassName} data-view="clinical">
        <DirectionRows step={step} />
        <p className="narr">{step.narration}</p>
        {returned !== undefined && (
          <>
            <label className="json-search-label">
              Search returned resource
              <input
                type="text"
                className="json-search-input"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </label>
            <div className="pane returned-payload">
              <h4>Returned resource</h4>
              <JsonView value={returned} search={search} />
            </div>
          </>
        )}
        <p className="sor-local-read-note">{SOR_LOCAL_READ_NOTE}</p>
      </div>
    );
  }

  // Refusal carve-out (all three species, clinical view): no species ever
  // has a request/response payload pair (all fire before anything was
  // sent), and directionRows() above already returns no rows for a refused
  // leg — the generic Request/Response panes below would render nothing but
  // "undefined". RefusalCard carries the honest, species-specific story
  // instead; its own <h4> is the load-bearing headline here, with the
  // dedicated refusal narrations (inspect.ts's leg.refused /
  // leg.transform-refused / leg.carry-refused entries) as the sentence above
  // it.
  if (isRefused) {
    return (
      <div className={rootClassName} data-view="clinical">
        <DirectionRows step={step} />
        <p className="narr">{step.narration}</p>
        <RefusalCard step={step} />
      </div>
    );
  }

  return (
    <div className={rootClassName} data-view="clinical">
      <DirectionRows step={step} />
      <p className="narr">{step.narration}</p>
      {step.kind === 'ingress' && step.httpStatus !== undefined && (
        <p className="http-status">HTTP {step.httpStatus}</p>
      )}
      {step.kind === 'leg' && step.response?.status !== undefined && (
        <p className="relayed-status">{relayedStatusLine(step.response.status)}</p>
      )}
      <label className="json-search-label">
        Search request and response
        <input
          type="text"
          className="json-search-input"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </label>
      <div className="pane request-payload">
        <h4>Request</h4>
        <JsonView value={step.request?.payload} search={search} />
      </div>
      <div className="pane response-payload">
        <h4>Response</h4>
        {step.response ? (
          <JsonView value={step.response.payload} search={search} />
        ) : (
          <p className="open-step-note">{OPEN_STEP_NOTE}</p>
        )}
      </div>
      {step.validation !== undefined && (
        <ValidationBadge validation={step.validation} posture={posture} />
      )}
      {failureDetail && <p className="failure-detail">{failureDetail}</p>}
      {step.downgrade !== undefined && <p className="leg-downgrade-note">{LEG_DOWNGRADE_NOTE}</p>}
      {step.transform && <TransformCard step={step} posture={posture} register={register} />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Local-demonstration species — DemoStepDetail. A disjoint rendering branch
// from wire-run StepDetail above: a demonstration record never crosses the
// Hub, never has a posture-checked validator verdict, and never carries a
// Provenance resource to disclose (SUBSTRATE_FRAMING/ValidationBadge/
// TransformCard's raw-Provenance <details> would all be false claims here —
// none of them are reused). What genuinely IS reused verbatim: RefusalCard
// (the refusal species) and the ChainHops/LossEntryList subcomponents (the
// carry species) — the same honest machinery a wire-bridged leg renders
// through, fed a view-model adapter instead of an observed frame.
// ---------------------------------------------------------------------------

// demoStepFromRecord adapts a DemoRecord (inspect.ts's buildDemoStory) into
// the Step shape StepDetail's existing RefusalCard already knows how to
// render — route.chain/refusal.chain and response.detail copied straight
// off the record, status set per the record's own kind. This is
// PRESENTATION ADAPTATION ONLY, never event synthesis: it invents no wire
// frame, mints no correlation id, and crosses no Hub — it exists solely so
// RefusalCard (built to read a Step) can render the SAME species discrimination
// and pinned copy for a demonstration refusal that it renders for a genuine
// leg.failed. Consumed only by DemoStepDetail below, never fed into the
// wire-run branches above (which assume a genuinely observed frame).
export function demoStepFromRecord(record: DemoRecord): Step {
  const isRefusal = record.kind === 'refusal-engine';
  const step: Step = {
    id: 'demo',
    kind: 'leg',
    legType: record.contract,
    status: isRefusal ? 'failed' : 'ok',
    route: { chain: record.chain },
    narration: '',
  };
  if (isRefusal) {
    step.refusal = { chain: record.chain };
    step.response = { seq: 0, time: '', kind: 'demo.refusal', detail: record.refusal };
  }
  return step;
}

// CONTRACT_MODULE_NAMES/moduleDisplayName: the demonstration dir-row's
// module name ("DTR") is derived from the record's own `contract` field
// ("pa.dtr"), never hardcoded per demonstration kind — a future third
// demonstration contract renders honestly (falls back to the raw token
// upper-cased) without a code change here. Matches CONTRACT_LINE_EXPLAINER's
// "CRD, DTR, PAS, or PDex" vocabulary (bridgingmeta.ts).
const CONTRACT_MODULE_NAMES: Record<string, string> = {
  'pa.crd': 'CRD',
  'pa.dtr': 'DTR',
  'pa.pas': 'PAS',
  'pa.pdex': 'PDex',
};

function moduleDisplayName(contract: string): string {
  return CONTRACT_MODULE_NAMES[contract] ?? contract.toUpperCase();
}

// chainPathLabel: the dir-row's version-path tail ("2.1→2.2" for the
// refusal exhibit's single hop; "2.2→2.1→2.2" for the carry exhibit's
// down-then-up round trip) — derived from the chain's own from/to fields,
// never hardcoded per kind, so it stays honest for either species' actual
// chain shape.
function chainPathLabel(chain: DemoRecord['chain']): string {
  if (chain.length === 0) return '';
  return [chain[0].from, ...chain.map((h) => h.to)].join('→');
}

// The demonstration dir-row's "what" half — the frozen fixture each
// exhibit runs over. Not a wire-observed fact (no request truly went out),
// so this is fixed copy per species, not derived from the record. Pinned
// exactly; do not paraphrase.
export const DEMO_REFUSAL_WHAT = 'crafted multi-coverage questionnaire response';
export const DEMO_CARRY_WHAT = 'itemWeight-bearing questionnaire response';

// DEMO_REFUSAL_NARRATION / DEMO_CARRY_NARRATION — the demonstration
// species' narration lines: pinned, participant-facing wording, asserted
// verbatim by tests. Register-neutral by design (a demonstration has one
// frozen outcome, not a request/done/failed lifecycle the existing
// NARRATION table's Register-agnostic-but-status-keyed shape assumes) —
// pinned exactly; do not paraphrase.
export const DEMO_REFUSAL_NARRATION =
  'The module refused to bridge this response up to 2.2: two Coverage-referencing entries make the qr-coverage extension ambiguous, and the network refuses loudly rather than guessing at clinical content.';

export const DEMO_CARRY_NARRATION =
  'The itemWeight extension has no slot on the 2.1 line, so it traveled wrapped and unread — and came back restored exactly.';

// The demonstration input/output pane headers — pinned, participant-facing
// wording, asserted verbatim by tests. Pinned exactly; do not paraphrase.
export const DEMO_INPUT_PANE_HEADER = 'Demonstration input — QuestionnaireResponse (frozen reference content)';
export const DEMO_OUTPUT_PANE_HEADER = 'Round-tripped output — QuestionnaireResponse';

// DEMO_RESTORED_VERDICT (imported from bridgingmeta.ts, the ONE pin-home
// shared with XformDiff.tsx's own identical-summary computation — see that
// constant's doc comment) — the carry demonstration's restored-verdict line.
// Rendered only when computeXformDiff over the record's OWN input/output
// pair actually reports byteIdentical — never trusted off the wire's
// `restored` flag alone (a record could in principle declare restored:true
// while carrying a genuinely differing output; the honest fallback here is
// to render nothing rather than repeat an unverified claim). Re-exported
// here so existing `from './StepDetail'` imports keep working.
export { DEMO_RESTORED_VERDICT };

export interface DemoStepDetailProps {
  record: DemoRecord;
}

// DemoStepDetail: StepDetail's demonstration-species rendering entry point,
// routed here from RunInspector's demo branch (isDemoRun) instead of the
// wire-run StepDetail above. Renders the two demonstration species —
// refusal (RefusalCard, reused verbatim over the demoStepFromRecord
// adapter, plus the searchable frozen input) and carry (the existing
// ChainHops/LossEntryList subcomponents plus both searchable JsonViews and
// the restored verdict) — never the wire-run-only affordances
// (ValidationBadge/posture label, TransformCard's raw-Provenance
// disclosure): neither is a true claim for a run that never crossed the
// Hub or the validator.
export function DemoStepDetail({ record }: DemoStepDetailProps): JSX.Element {
  const [search, setSearch] = useState('');
  const isRefusal = record.kind === 'refusal-engine';
  const step = demoStepFromRecord(record);
  const moduleName = moduleDisplayName(record.contract);
  const pathLabel = chainPathLabel(record.chain);
  const what = isRefusal ? DEMO_REFUSAL_WHAT : DEMO_CARRY_WHAT;
  const narration = isRefusal ? DEMO_REFUSAL_NARRATION : DEMO_CARRY_NARRATION;

  // outputDiff: the carry species' honest restored-verdict computation —
  // computeXformDiff over the record's OWN input/output pair (never the
  // wire's `restored` boolean alone). undefined for the refusal species
  // (record.output is only ever set for carry-engine) and unused there.
  // Memoized on `record` (a stable prop reference across this component's
  // own search-keystroke re-renders — RunInspector doesn't re-render just
  // because THIS component's local `search` state changed) so typing in
  // the search box below never re-walks a payload up to 2 MiB on every
  // keystroke.
  const outputDiff = useMemo(
    () =>
      !isRefusal && record.output !== undefined
        ? computeXformDiff(record.input, record.output, JSON.stringify(record.input), JSON.stringify(record.output), record.lossReports ?? [])
        : undefined,
    [isRefusal, record],
  );

  // intermediateRaw: JSON.stringify of the record's own input/intermediate
  // pair, hoisted into the same `record`-keyed useMemo as outputDiff above
  // (not called inline in the XformDiff render below) — the down-hop pane
  // XformDiff feeds its OWN useMemo off these two strings, so leaving the
  // stringify inline in JSX would re-run it (and pay its cost) on every
  // search-keystroke re-render, defeating that inner memoization.
  const intermediateRaw = useMemo(
    () => ({ rawBefore: JSON.stringify(record.input), rawAfter: JSON.stringify(record.intermediate) }),
    [record],
  );

  return (
    <div className="detail demo-detail" data-view="clinical">
      <div className="dir-rows">
        <div className="dir-row">
          <span className="arr">→</span>
          <span className="who">{`Smart Gateway → its ${moduleName} ${pathLabel} module`}</span>
          <span className="what">{what}</span>
        </div>
      </div>
      <p className="narr">{narration}</p>
      <p className="local-demo-framing">{LOCAL_DEMO_FRAMING}</p>

      {isRefusal ? (
        <>
          <RefusalCard step={step} />
          <label className="json-search-label">
            Search demonstration input
            <input
              type="text"
              className="json-search-input"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
          <div className="pane demo-input-payload">
            <h4>{DEMO_INPUT_PANE_HEADER}</h4>
            <JsonView value={record.input} search={search} />
          </div>
        </>
      ) : (
        <>
          <ChainHops chain={record.chain} />
          <div className="loss-report-list">
            {(record.lossReports ?? []).map((r, i) => (
              <div key={i} className="loss-report">
                <div className="loss-report-module">
                  {r.module} <span className="loss-report-lines">{r.source} → {r.target}</span>
                </div>
                {r.carried && r.carried.length > 0 && <LossEntryList title="Carried, not lost" entries={r.carried} />}
                {r.synthesized && r.synthesized.length > 0 && (
                  <LossEntryList title="Synthesized" entries={r.synthesized} />
                )}
              </div>
            ))}
          </div>
          {record.chain[0] !== undefined && record.intermediate !== undefined && (
            <XformDiff
              before={record.input}
              after={record.intermediate}
              rawBefore={intermediateRaw.rawBefore}
              rawAfter={intermediateRaw.rawAfter}
              lossReports={record.lossReports ?? []}
              {...demoXformPaneLabels(record.contract, record.chain[0].from, record.chain[0].to)}
            />
          )}
          {outputDiff?.byteIdentical && (
            // Labeled explicitly — this line and the XformDiff block right
            // above it both end in a "regions differ" summary, but they
            // describe two DIFFERENT pairs (input vs. the carried-down
            // intermediate, above; input vs. the round-tripped output,
            // here). Stacked with no label, they read as one confusing
            // number changing rather than two honest, separate comparisons.
            // The pinned literal itself stays intact, in its own element,
            // so the existing byte-exact assertions on it still hold.
            <p className="demo-restored-verdict">
              Input vs. round-tripped output —{' '}
              <span className="demo-restored-verdict-value">{DEMO_RESTORED_VERDICT}</span>
            </p>
          )}
          <label className="json-search-label">
            Search demonstration input and output
            <input
              type="text"
              className="json-search-input"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
          <div className="pane demo-input-payload">
            <h4>{DEMO_INPUT_PANE_HEADER}</h4>
            <JsonView value={record.input} search={search} />
          </div>
          <div className="pane demo-output-payload">
            <h4>{DEMO_OUTPUT_PANE_HEADER}</h4>
            <JsonView value={record.output} search={search} />
          </div>
        </>
      )}
    </div>
  );
}
