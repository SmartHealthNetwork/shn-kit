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
  payload?: unknown;
  detail?: string;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null;
}

function asString(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined;
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
    payload: o.payload,
    detail: asString(o.detail),
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
};

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
    narration: '',
  };
  step.narration = narrationFor(step);
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
        steps.push(step);
        openLegs.push(step);
        break;
      }
      case 'leg.response':
      case 'leg.failed': {
        const step = closeOldestMatching(openLegs, frame);
        if (step) closeLegStep(step, frame, frame.kind === 'leg.failed');
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
