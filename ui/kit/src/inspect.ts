// inspect.ts — pure interpretation of a run's stamped event stream into a
// human story. No React, no fetch: buildRunStory takes the events a run
// has produced (live off the ring, or replayed from a history record — one
// interpretation pipeline either way) and turns them into a RunStory of
// Steps a UI can render. Tested against real captured event fixtures.
import type { KitEvent } from './types';

// ---------------------------------------------------------------------------
// Observer frame parsing
// ---------------------------------------------------------------------------

export interface ObserverFrame {
  seq: number;
  time: string;
  kind: string;
  legType?: string;
  direction?: string;
  correlationId?: string;
  counterpart?: string;
  authorityFrame?: string;
  op?: string;
  status?: number;
  payload?: unknown;
  detail?: string;
  route?: RouteFrame;
}

// RouteFrame mirrors gateway/engine/observer.go's RouteInfo (json tags
// token/buildLine/chain/own/peer/bridgeIssue) — the routed line + build
// story on leg.originated (Token/BuildLine/Chain), or the structured
// refusal on leg.refused / a transform-refusal leg.failed (Own/Peer/
// BridgeIssue; Token/BuildLine/Chain empty — nothing was selected).
export interface RouteFrame {
  token?: string;
  buildLine?: string;
  chain?: { module: string; from: string; to: string; class: string }[];
  own?: string[];
  peer?: string[];
  bridgeIssue?: string;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null;
}

function asString(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined;
}

function asNumber(v: unknown): number | undefined {
  return typeof v === 'number' ? v : undefined;
}

function asStringArray(v: unknown): string[] | undefined {
  if (!Array.isArray(v) || !v.every((x) => typeof x === 'string')) return undefined;
  return v as string[];
}

function parseChainStep(v: unknown): { module: string; from: string; to: string; class: string } | undefined {
  if (!isRecord(v)) return undefined;
  const module = asString(v.module);
  const from = asString(v.from);
  const to = asString(v.to);
  const cls = asString(v.class);
  if (module === undefined || from === undefined || to === undefined || cls === undefined) return undefined;
  return { module, from, to, class: cls };
}

// parseRoute reads e.observer.route — shape-checked the same never-throw
// way as parseObserver/parseAudit: undefined for anything that isn't a
// well-formed route object (absent field, wrong type, malformed chain
// entries silently dropped rather than the whole route rejected).
function parseRoute(v: unknown): RouteFrame | undefined {
  if (!isRecord(v)) return undefined;
  const chain = Array.isArray(v.chain)
    ? v.chain.map(parseChainStep).filter((c): c is { module: string; from: string; to: string; class: string } => c !== undefined)
    : undefined;
  return {
    token: asString(v.token),
    buildLine: asString(v.buildLine),
    chain,
    own: asStringArray(v.own),
    peer: asStringArray(v.peer),
    bridgeIssue: asString(v.bridgeIssue),
  };
}

// parseObserver reads e.observer — an already-parsed-JSON `unknown` (the SSE
// client JSON.parses the whole KitEvent, so by the time it reaches here the
// observer payload is a plain object, never a string to re-decode). It shape-
// checks `kind` and returns undefined for anything that isn't a well-formed
// observer frame — never throws, so a malformed or absent observer never
// takes the inspector down with it.
export function parseObserver(e: KitEvent): ObserverFrame | undefined {
  const o = e.observer;
  if (!isRecord(o)) return undefined;
  const kind = asString(o.kind);
  if (kind === undefined) return undefined;

  return {
    // The frame's seq is the KIT-BUS seq (e.seq) — the ordering axis steps are
    // paired and keyed on — not the nested observer envelope's own seq.
    seq: e.seq,
    // Prefer the observer payload's OWN `time` (the gateway-clock moment
    // the edge actually observed) over e.time (the kit-bus emission time),
    // falling back to e.time when the payload carries none. Deliberate —
    // this is the honest display time.
    time: asString(o.time) ?? e.time,
    kind,
    legType: asString(o.legType),
    direction: asString(o.direction),
    correlationId: asString(o.correlationId),
    counterpart: asString(o.counterpart),
    authorityFrame: asString(o.authorityFrame),
    op: asString(o.op),
    status: asNumber(o.status),
    payload: o.payload,
    detail: asString(o.detail),
    route: parseRoute(o.route),
  };
}

interface AuditFrame {
  seq: number;
  timestamp: string;
  sender: string;
  recipient: string;
  transactionType: string;
  authorityFrame: string;
  scope: string;
  outcome: string;
}

function parseAudit(e: KitEvent): AuditFrame | undefined {
  const a = e.audit;
  if (!isRecord(a)) return undefined;
  const transactionType = asString(a.transactionType);
  if (transactionType === undefined) return undefined;
  return {
    seq: typeof a.seq === 'number' ? a.seq : 0,
    timestamp: asString(a.timestamp) ?? '',
    sender: asString(a.sender) ?? '',
    recipient: asString(a.recipient) ?? '',
    transactionType,
    authorityFrame: asString(a.authorityFrame) ?? '',
    scope: asString(a.scope) ?? '',
    outcome: asString(a.outcome) ?? '',
  };
}

// ---------------------------------------------------------------------------
// Narration: a keyed copy table with a mandatory honest fallback.
// ---------------------------------------------------------------------------

interface NarrationEntry {
  request: string;
  done: string;
  failed: string;
}

// Lookup key: the request frame's `op` when present (the engine catalog's
// per-leg operation name — e.g. pas-claim's request op is "pas-submit", which
// is distinct from its wire legType "pas-claim"), else the frame's `legType`
// (ingress frames carry no op — the route tag IS the key), else the frame's
// `kind` (validate.result frames carry neither op nor legType).
//
// Seed keys (seeded from gateway/engine/workstream_pa.go's paCatalog +
// gateway/engine/gateway.go's ingress routes). Each entry below
// was checked against the two committed replay fixtures
// (run-ehr-uc03.json / run-conformant-uc03.json) at implementation time;
// keys that never occur in either fixture keep their seed sentence but are
// marked `// not fixture-verified`.
const NARRATION: Record<string, NarrationEntry> = {
  // Fixture-verified: both fixtures carry a crd-order-select leg
  // (request authorityFrame "provider-tpo", counterpart "payer", op
  // "crd-order-select"; response authorityFrame "payer-coverage", op
  // "crd-cards", payload = CDS Hooks cards).
  'crd-order-select': {
    request:
      'The Smart Gateway sent the clinician’s order-select context to the hosted payer through the Hub, awaiting CDS Hooks cards.',
    done: 'The Smart Gateway relayed the order-select context through the Hub; the hosted payer’s CDS Hooks cards came back with its coverage guidance.',
    failed: 'The Smart Gateway’s order-select exchange with the hosted payer through the Hub did not complete.',
  },
  // not fixture-verified: no captured run exercises order-sign/order-dispatch.
  'crd-order-dispatch': {
    request:
      'The Smart Gateway sent the clinician’s order-dispatch context to the hosted payer through the Hub, awaiting CDS Hooks cards.',
    done: 'The Smart Gateway relayed the order-dispatch context through the Hub; the hosted payer’s CDS Hooks cards came back with its coverage guidance.',
    failed: 'The Smart Gateway’s order-dispatch exchange with the hosted payer through the Hub did not complete.',
  },
  // not fixture-verified: no captured run exercises a coverage-eligibility leg.
  'eligibility-inquiry': {
    request: 'The Smart Gateway asked the hosted payer, through the Hub, to confirm the holder’s coverage eligibility.',
    done: 'The Smart Gateway’s eligibility inquiry reached the hosted payer through the Hub; its coverage-eligibility response came back in the sealed reply.',
    failed: 'The Smart Gateway’s coverage-eligibility inquiry to the hosted payer through the Hub did not complete.',
  },
  // Fixture-verified: both fixtures carry a dtr-questionnaire-fetch leg
  // (request authorityFrame "provider-tpo", counterpart "payer"; response
  // authorityFrame "payer-coverage", op "dtr-questionnaire", payload = a
  // Bundle containing a CQL-backed Questionnaire, cqf-library extension).
  'dtr-questionnaire-fetch': {
    request:
      'The Smart Gateway asked the hosted payer, through the Hub, for the prior-authorization questionnaire the CDS card named.',
    done: 'The Smart Gateway fetched the DTR questionnaire from the hosted payer through the Hub; the CQL-backed package came back in the sealed response.',
    failed: 'The Smart Gateway’s questionnaire fetch from the hosted payer through the Hub did not complete.',
  },
  // Fixture-verified: both fixtures carry a pas-claim leg (request op
  // "pas-submit", authorityFrame "provider-tpo", counterpart "payer";
  // response op "pas-response", authorityFrame "payer-coverage", payload =
  // ClaimResponse with reviewAction A1 "Certified in Total").
  'pas-submit': {
    request:
      'The Smart Gateway submitted the prior-authorization request (Claim + supporting QuestionnaireResponse) to the hosted payer through the Hub, awaiting its decision.',
    done: 'The Smart Gateway submitted the prior-authorization request through the Hub; the payer’s decision came back in the sealed response.',
    failed: 'The Smart Gateway’s prior-authorization submission to the hosted payer through the Hub did not complete.',
  },
  // not fixture-verified: no captured run exercises the amended re-submit leg.
  'pas-update-submit': {
    request:
      'The Smart Gateway submitted the amended prior-authorization request to the hosted payer through the Hub, awaiting its updated decision.',
    done: 'The Smart Gateway submitted the amended prior-authorization request through the Hub; the payer’s updated decision came back in the sealed response.',
    failed: 'The Smart Gateway’s amended prior-authorization submission to the hosted payer through the Hub did not complete.',
  },
  // not fixture-verified: no captured run exercises a federated-query leg.
  'federated-query-submit': {
    request: 'The Smart Gateway asked the named holder, through the Hub, for the specific documents the request scoped.',
    done: 'The Smart Gateway’s federated query reached the named holder through the Hub; its documents came back in the sealed response.',
    failed: 'The Smart Gateway’s federated query to the named holder through the Hub did not complete.',
  },
  // not fixture-verified: no captured run exercises a patient-dtr leg.
  'patient-dtr-request': {
    request: 'The Smart Gateway asked the holder’s own record, through the Hub, for the patient-authored questionnaire responses.',
    done: 'The Smart Gateway’s patient-authored DTR request reached the holder through the Hub; the responses came back in the sealed reply.',
    failed: 'The Smart Gateway’s patient-authored DTR request through the Hub did not complete.',
  },
  // Fixture-verified: the conformant fixture carries a crd-ingress route
  // (ingress.received/ingress.responded pair; detail "200"). Hook-neutral:
  // both order-select and order-sign CDS Hooks calls route through this one
  // ingress (the fixture's own hook is "order-sign"), so the copy names
  // neither hook by name rather than assert one that isn't always true.
  'crd-ingress': {
    request:
      'A CDS Hooks call from the provider’s Da Vinci client arrived at the Smart Gateway’s ingress — does this order need prior authorization?',
    done: 'The Smart Gateway answered the inbound CDS Hooks call with the cards it received back from routing the request onward.',
    failed: 'The Smart Gateway’s inbound CDS Hooks call did not receive a successful response.',
  },
  // Fixture-verified: the conformant fixture carries a dtr-ingress route
  // (ingress.received/ingress.responded pair; detail "200").
  'dtr-ingress': {
    request: 'A DTR $questionnaire-package request arrived at the Smart Gateway’s ingress; the request is being routed onward.',
    done: 'The Smart Gateway answered the inbound $questionnaire-package request with the package it received back from routing the request onward.',
    failed: 'The Smart Gateway’s inbound $questionnaire-package request did not receive a successful response.',
  },
  // Fixture-verified: the conformant fixture carries a pas-ingress route
  // (ingress.received/ingress.responded pair; detail "200").
  'pas-ingress': {
    request: 'A PAS Claim/$submit request arrived at the Smart Gateway’s ingress; the request is being routed onward.',
    done: 'The Smart Gateway answered the inbound Claim/$submit request with the decision it received back from routing the request onward.',
    failed: 'The Smart Gateway’s inbound Claim/$submit request did not receive a successful response.',
  },
  // Fixture-verified: both fixtures carry validate.result frames (detail
  // "valid"). This is always the Kit's stand-in validator's verdict in v1
  // (SHN_FAKE_VALIDATOR=1) — StepDetail carries that posture label; this
  // narration is just "what happened", not "who checked".
  'validate.result': {
    request: 'The Smart Gateway is validating this resource.',
    done: 'The Smart Gateway validated this resource against its FHIR profile.',
    failed: 'The Smart Gateway found this resource did not validate against its FHIR profile.',
  },
  // leg.refused: version-matched routing found no shared contract line and
  // refused before anything was sent — a dedicated key (fetched directly by
  // makeRefusedLegStep, never through narrationKey) because the ordinary
  // legType-keyed entries above all assume an exchange was attempted; this
  // one wasn't. The step is always self-contained-failed, so `.failed` is
  // the only branch actually reached; request/done are filled for shape
  // parity with NarrationEntry and future-proofing, not because either is
  // reachable today.
  'leg.refused': {
    request: 'The Smart Gateway is checking whether it and the hosted counterparty share a contract line for this leg.',
    done: 'The Smart Gateway found a shared contract line for this leg.',
    failed: 'The Smart Gateway found no contract line it shares with the hosted counterparty for this leg, and refused before sending anything.',
  },
  // leg.transform-refused: egressAdapt's chain refused mid-bridge BEFORE the
  // leg was sent (leg.failed + Route.Chain, no open step). A dedicated key,
  // fetched directly by makeTransformRefusedLegStep, for the same reason as
  // leg.refused: the ordinary legType/op-keyed entries (and the generic
  // fallback) all say an exchange was attempted or completed — for a pas leg
  // the fallback literally said "exchanged", directly above RefusalCard's
  // "zero bytes crossed the network". Only
  // `.failed` is reachable; request/done are shape parity, as in leg.refused.
  'leg.transform-refused': {
    request: 'The Smart Gateway is bridging this leg to the hosted counterparty’s contract line.',
    done: 'The Smart Gateway bridged this leg to the hosted counterparty’s contract line.',
    failed:
      'The Smart Gateway could not honestly bridge this leg to the hosted counterparty’s contract line, and refused before sending anything.',
  },
  // leg.carry-refused: the SAME leg.failed + Route seam, emitted by the
  // gateway's resume-time carry guard (a pended request's own record says
  // content was carried across a version bridge, and the resumed payload no
  // longer bears it). Distinguished from leg.transform-refused by the
  // engine's Detail marker (isCarryRefusalDetail) — same wire contract
  // discipline as parseMissingElements. Only `.failed` is reachable.
  'leg.carry-refused': {
    request: 'The Smart Gateway is checking the resumed request still carries what its record says it must.',
    done: 'The Smart Gateway confirmed the resumed request still carries what its record says it must.',
    failed:
      'The Smart Gateway found this resumed request no longer carries content its own record says it must, and refused before sending anything.',
  },
};

// isCarryRefusalDetail: discriminates the carry-integrity refusal from the
// ordinary transform-chain refusal on the shared leg.failed + Route seam,
// keyed on the engine's own error prefix (gateway/engine/gateway.go's
// verifyPendCarryIntact wrap, emitted via guardPendCarry) — a load-bearing
// Detail wire contract, same as parseMissingElements' marker.
export function isCarryRefusalDetail(detail: string | undefined): boolean {
  return detail !== undefined && detail.includes('pended carry not intact at resume');
}

function narrationKey(frame: ObserverFrame): string {
  return frame.op ?? frame.legType ?? frame.kind;
}

function fallbackNarration(legType: string, counterpart: string | undefined): string {
  return `The Smart Gateway exchanged "${legType}" with ${counterpart ?? 'the hosted counterparty'}.`;
}

// sor.read narration: keyed on the observer frame's Op (the Go
// SystemOfRecord method name — cannot collide with the kebab-case leg/route
// keys above). The fallback is sor-specific ON PURPOSE: the generic
// fallbackNarration says "with the hosted counterparty", which would be
// dishonest for a local data-source read.
const SOR_NARRATION: Record<string, string> = {
  ResolvePatient: 'The gateway looked the member up in its data source.',
  PatientFHIRRef: 'The gateway resolved the member’s FHIR Patient reference in its data source.',
  CoverageInforce: 'The gateway checked the member’s coverage record in its data source.', // not fixture-verified
  ClinicalContext: 'The gateway read the member’s clinical context from its data source.',
  SupplementalReport: 'The gateway looked for a supplemental report in its data source.', // not fixture-verified
  FacilityRecords: 'The gateway read the member’s facility records from its data source.', // not fixture-verified
  OpenOrder: 'The gateway read the member’s open order from its data source.', // not fixture-verified
  OpenCoverage: 'The gateway read the member’s in-force coverage record from its data source.',
  ResolveByReference: 'The gateway resolved a referenced resource from its data source.', // not fixture-verified
};

function sorNarration(op: string | undefined): string {
  if (op !== undefined && SOR_NARRATION[op] !== undefined) return SOR_NARRATION[op];
  return `The gateway read ${op ?? 'a record'} from its data source.`;
}

function narrationFor(step: Step): string {
  if (step.kind === 'sor') return sorNarration(step.sorOp);
  const request = step.request;
  const key = request ? narrationKey(request) : step.legType;
  const entry = NARRATION[key];
  if (!entry) {
    return fallbackNarration(step.legType, step.counterpart ?? request?.counterpart);
  }
  if (step.status === 'open') return entry.request;
  if (step.status === 'ok') return entry.done;
  return entry.failed;
}

// ---------------------------------------------------------------------------
// Step pairing
// ---------------------------------------------------------------------------

export type StepKind = 'ingress' | 'leg' | 'validate' | 'sor';
export type StepStatus = 'open' | 'ok' | 'failed';

export interface Step {
  id: string; // String(request.seq) — stable list key
  kind: StepKind;
  legType: string;
  status: StepStatus;
  request?: ObserverFrame;
  response?: ObserverFrame;
  correlationId?: string;
  counterpart?: string;
  requestAuthority?: string;
  responseAuthority?: string;
  httpStatus?: string; // ingress.responded Detail
  validation?: string; // validate.result Detail
  sorOp?: string; // sor.read frames: the SystemOfRecord method name
  sorDetail?: string; // sor.read frames: "found" / "not found" / coverage status
  narration: string; // narration table or fallback — never empty
  route?: RouteFrame; // leg.originated's Route — the routed line + build story
  transform?: ObserverFrame; // joined leg.transformed frame (correlationId ONLY)
  downgrade?: string; // leg.downgrade Detail (stale-feed downgrade)
  refusal?: RouteFrame; // leg.refused's Route, or a transform-refusal leg.failed's Route
}

export interface AuditAnchor {
  seq: number;
  timestamp: string;
  sender: string;
  recipient: string;
  transactionType: string;
  authorityFrame: string;
  scope: string;
  outcome: string;
}

export interface RunStory {
  runId: string;
  steps: Step[];
  audit: AuditAnchor[]; // run-scoped, never per-step
  auditNote?: string; // audit.unavailable detail
  startedAt?: string;
  terminal?: { type: string; detail?: string };
}

const TERMINAL_TYPES = new Set(['run.finished', 'run.failed']);

function openLegStep(frame: ObserverFrame): Step {
  const step: Step = {
    id: String(frame.seq),
    kind: 'leg',
    legType: frame.legType ?? 'unknown',
    status: 'open',
    request: frame,
    correlationId: frame.correlationId,
    counterpart: frame.counterpart,
    requestAuthority: frame.authorityFrame,
    route: frame.route,
    narration: '',
  };
  step.narration = narrationFor(step);
  return step;
}

// makeRefusedLegStep builds leg.refused's self-contained failed step: no
// leg.originated ever fires for a refused leg (routing refused BEFORE
// anything was selected), so there is no open step to close — this frame
// alone IS the step. Narration is the dedicated 'leg.refused' entry, fetched
// directly rather than through narrationFor/narrationKey: the generic path
// would key off frame.legType (e.g. "pas-claim") and surface the wrong,
// exchange-was-attempted copy.
function makeRefusedLegStep(frame: ObserverFrame): Step {
  const entry = NARRATION['leg.refused'];
  const step: Step = {
    id: String(frame.seq),
    kind: 'leg',
    legType: frame.legType ?? 'unknown',
    status: 'failed',
    response: frame,
    correlationId: frame.correlationId,
    counterpart: frame.counterpart,
    refusal: frame.route,
    narration: entry ? entry.failed : fallbackNarration(frame.legType ?? 'unknown', frame.counterpart),
  };
  return step;
}

// makeTransformRefusedLegStep builds the OTHER self-contained failed steps:
// a leg.failed with Route present and no open leg step matching its
// correlationId — both producers (egressAdapt's transform-chain refusal and
// guardPendCarry's carry-integrity refusal) precede roundTrip, so the leg
// never got a leg.originated at all. Narration is a dedicated entry fetched
// directly (same posture as makeRefusedLegStep): the ordinary legType-keyed
// path would fall through to the generic "exchanged …" fallback for legTypes
// with op-keyed entries (pas-claim), a false sentence directly above
// RefusalCard's zero-bytes note. The two species are
// discriminated by the engine's Detail marker (isCarryRefusalDetail).
function makeTransformRefusedLegStep(frame: ObserverFrame): Step {
  const entry = isCarryRefusalDetail(frame.detail)
    ? NARRATION['leg.carry-refused']
    : NARRATION['leg.transform-refused'];
  const step: Step = {
    id: String(frame.seq),
    kind: 'leg',
    legType: frame.legType ?? 'unknown',
    status: 'failed',
    response: frame,
    correlationId: frame.correlationId,
    counterpart: frame.counterpart,
    refusal: frame.route,
    narration: entry ? entry.failed : fallbackNarration(frame.legType ?? 'unknown', frame.counterpart),
  };
  return step;
}

function openIngressStep(frame: ObserverFrame): Step {
  const step: Step = {
    id: String(frame.seq),
    kind: 'ingress',
    legType: frame.legType ?? 'unknown',
    status: 'open',
    request: frame,
    narration: '',
  };
  step.narration = narrationFor(step);
  return step;
}

function makeValidateStep(frame: ObserverFrame): Step {
  const status: StepStatus = frame.detail === 'valid' ? 'ok' : 'failed';
  const step: Step = {
    id: String(frame.seq),
    kind: 'validate',
    legType: 'validate.result',
    status,
    request: frame,
    validation: frame.detail,
    narration: '',
  };
  step.narration = narrationFor(step);
  return step;
}

function makeSorStep(frame: ObserverFrame): Step {
  const step: Step = {
    id: String(frame.seq),
    kind: 'sor',
    legType: 'sor.read',
    status: 'ok', // a miss is a normal branch, never a failed step
    request: frame,
    sorOp: frame.op,
    sorDetail: frame.detail,
    narration: '',
  };
  step.narration = narrationFor(step);
  return step;
}

// closeOldestMatching implements the close-matching order: prefer the
// open step whose correlationId matches the closing frame's (leg steps
// only — ingress frames carry no correlationId, so this predicate always
// misses for them and callers rely on the legType fallback below); else the
// oldest open step with a matching legType (an ingress route, or a leg
// legType when correlationId is absent/unmatched); else just the oldest open
// step of the pool, unconditionally (unambiguous under sequential-only v1 —
// at most one step is open at a time in every scenario we drive today).
function closeOldestMatching(pool: Step[], frame: ObserverFrame): Step | undefined {
  let idx = -1;
  if (frame.correlationId !== undefined) {
    idx = pool.findIndex((s) => s.correlationId === frame.correlationId);
  }
  if (idx === -1) {
    idx = pool.findIndex((s) => s.legType === frame.legType);
  }
  if (idx === -1 && pool.length > 0) {
    idx = 0;
  }
  if (idx === -1) return undefined;
  const [step] = pool.splice(idx, 1);
  return step;
}

function closeLegStep(step: Step, frame: ObserverFrame, failed: boolean): void {
  step.response = frame;
  step.responseAuthority = frame.authorityFrame;
  step.status = failed ? 'failed' : 'ok';
  step.narration = narrationFor(step);
}

function closeIngressStep(step: Step, frame: ObserverFrame): void {
  step.response = frame;
  step.httpStatus = frame.detail;
  const code = frame.detail !== undefined ? Number.parseInt(frame.detail, 10) : NaN;
  step.status = Number.isFinite(code) && code >= 400 ? 'failed' : 'ok';
  step.narration = narrationFor(step);
}

// buildRunStory turns one run's stamped events into a RunStory: a flat,
// chronologically-ordered list of Steps (leg/ingress steps paired,
// validate.result always its own step) plus the run's Audit anchors
// (run-scoped — never attached to a Step) and terminal outcome.
export function buildRunStory(runId: string, events: KitEvent[]): RunStory {
  const runEvents = events.filter((e) => e.runId === runId).slice().sort((a, b) => a.seq - b.seq);

  const steps: Step[] = [];
  const openLegs: Step[] = [];
  const openIngress: Step[] = [];
  // pendingTransforms: leg.transformed frames not yet joined to a leg step,
  // keyed by correlationId. On the success path leg.transformed (egressAdapt)
  // fires BEFORE leg.originated (roundTrip) for the same leg, so a
  // leg.transformed frame almost always arrives with no step to attach to
  // yet — held here and attached the moment leg.originated shows up.
  // Orphan case: if no leg.originated with a matching correlationId ever
  // arrives for the run (e.g. an unrelated/malformed observer stream), the
  // entry sits unclaimed in this Map for the story's lifetime and that
  // transform is simply never rendered — no separate step, no error. This
  // was already true before the leg.transformed join existed (the frame was
  // dropped outright then), so it is a pre-existing, acceptable gap, not a
  // regression.
  // Precondition: every join in this function keys strictly on correlationId
  // equality (never legType/oldest-open fallbacks) and assumes it is unique
  // per run — a duplicate correlationId across two distinct legs (which
  // should never happen; the gateway mints a fresh one per leg) would let a
  // later frame silently steal an earlier match.
  const pendingTransforms = new Map<string, ObserverFrame>();
  const audit: AuditAnchor[] = [];
  let auditNote: string | undefined;
  let startedAt: string | undefined;
  let terminal: { type: string; detail?: string } | undefined;

  for (const e of runEvents) {
    if (e.type === 'run.started') {
      startedAt = e.time;
      continue;
    }
    if (TERMINAL_TYPES.has(e.type)) {
      terminal = { type: e.type, detail: e.detail };
      continue;
    }
    if (e.type === 'audit') {
      const a = parseAudit(e);
      if (a) audit.push(a);
      continue;
    }
    if (e.type === 'audit.unavailable') {
      auditNote = e.detail;
      continue;
    }
    if (e.type !== 'observer') continue;

    const frame = parseObserver(e);
    if (!frame) continue;

    switch (frame.kind) {
      case 'leg.originated': {
        const step = openLegStep(frame);
        if (frame.correlationId !== undefined) {
          const pending = pendingTransforms.get(frame.correlationId);
          if (pending) {
            step.transform = pending;
            pendingTransforms.delete(frame.correlationId);
          }
        }
        steps.push(step);
        openLegs.push(step);
        break;
      }
      case 'leg.response': {
        const step = closeOldestMatching(openLegs, frame);
        if (step) closeLegStep(step, frame, false);
        break;
      }
      case 'leg.failed': {
        // A Route-carrying leg.failed is the egressAdapt transform-refusal
        // variant — closed by a STRICT correlationId match against
        // openLegs only, never closeOldestMatching's legType/oldest
        // fallbacks (those would risk misfiring onto an unrelated open
        // step, since a transform-refused leg often has no open step of
        // its own at all — see the no-match branch below). A route-less
        // leg.failed is the ordinary roundTrip failure and keeps the
        // existing closeOldestMatching pairing unchanged.
        if (frame.route !== undefined) {
          const idx = frame.correlationId !== undefined
            ? openLegs.findIndex((s) => s.correlationId === frame.correlationId)
            : -1;
          if (idx !== -1) {
            const [step] = openLegs.splice(idx, 1);
            closeLegStep(step, frame, true);
            step.refusal = frame.route;
          } else {
            // egressAdapt precedes roundTrip: a transform-refused leg never
            // got a leg.originated, so there is no open step to close —
            // this frame alone becomes a self-contained failed step.
            steps.push(makeTransformRefusedLegStep(frame));
          }
          break;
        }
        const step = closeOldestMatching(openLegs, frame);
        if (step) closeLegStep(step, frame, true);
        break;
      }
      case 'leg.refused': {
        steps.push(makeRefusedLegStep(frame));
        break;
      }
      case 'leg.transformed': {
        // Strictly correlationId-matched over ALL steps (open or already
        // closed) — never closeOldestMatching, whose legType fallback would
        // misfire on ordering (transformed precedes originated on the
        // success path). No match yet (the common case) holds the frame in
        // pendingTransforms for leg.originated to pick up.
        const matched = frame.correlationId !== undefined
          ? steps.find((s) => s.kind === 'leg' && s.correlationId === frame.correlationId)
          : undefined;
        if (matched) {
          matched.transform = frame;
        } else if (frame.correlationId !== undefined) {
          pendingTransforms.set(frame.correlationId, frame);
        }
        break;
      }
      case 'leg.downgrade': {
        // leg.downgrade fires from inside roundTripInner, AFTER
        // leg.originated has already opened the step — strict
        // correlationId scan (same reasoning as leg.transformed), but in
        // practice always finds an already-open match.
        const matched = frame.correlationId !== undefined
          ? steps.find((s) => s.kind === 'leg' && s.correlationId === frame.correlationId)
          : undefined;
        if (matched) matched.downgrade = frame.detail;
        break;
      }
      case 'ingress.received': {
        const step = openIngressStep(frame);
        steps.push(step);
        openIngress.push(step);
        break;
      }
      case 'ingress.responded': {
        const step = closeOldestMatching(openIngress, frame);
        if (step) closeIngressStep(step, frame);
        break;
      }
      case 'validate.result': {
        steps.push(makeValidateStep(frame));
        break;
      }
      case 'sor.read': {
        steps.push(makeSorStep(frame));
        break;
      }
      default:
        // Unknown observer kind — not a paired step at all (only
        // leg/ingress/validate participate in the step model); silently
        // skip rather than fail the whole story over one frame.
        break;
    }
  }

  return { runId, steps, audit, auditNote, startedAt, terminal };
}
