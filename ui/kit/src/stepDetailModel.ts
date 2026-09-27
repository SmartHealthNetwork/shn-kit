// stepDetailModel.ts: StepDetail's pure view-model helpers (the relayed-status
// sentence, the direction rows, the transform narration and loss-report parsing,
// and the demonstration step adapter), kept out of StepDetail.tsx so that file
// exports only components and constants (react-refresh/only-export-components).
import type { Step } from './inspect';
import type { DemoRecord, Register } from './types';

// relayedStatusLine: the display-only sentence for a leg whose counterparty
// answered with a relayed non-2xx application status (ObserverEvent.Status).
// Display-only by design: the step's own ok/failed logic
// is deliberately unchanged (the exchange itself completed — the counterparty
// ANSWERED), but a rejection must never read as silently green.
export function relayedStatusLine(status: number): string {
  return `The counterparty’s application answered HTTP ${status} — relayed unchanged as this leg’s response.`;
}

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
// fabricate an outbound exchange that never happened. RefusalCard (in
// StepDetail.tsx) carries the honest "nothing was sent" story instead.
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
    // conformance: a single-frame local judgment, never a network hop — not
    // reached in practice (the render path carves this kind out before
    // calling DirectionRows, same as validate/sor), kept for shape parity
    // and so this switch stays exhaustive.
    case 'conformance':
      return [
        { arrow: '→', who: 'Smart Gateway → its own conformance check', what: `${step.findingKind ?? 'check'} against ${step.legType}` },
        { arrow: '←', who: 'its own conformance check → Smart Gateway', what: step.decision ?? 'recorded' },
      ];
  }
}

// TRANSFORM_CARD_NARRATION is register-aware copy (RegisterSwitch's
// Overview/Technical choice, same idiom as bridgingmeta.ts's
// CONTRACT_LINE_EXPLAINER) but NOT one of StepDetail.tsx's three
// verbatim-pinned strings — it's a framing sentence, not a claim StepDetail.test.tsx has to
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

// demoStepFromRecord adapts a DemoRecord (inspect.ts's buildDemoStory) into
// the Step shape StepDetail's existing RefusalCard already knows how to
// render — route.chain/refusal.chain and response.detail copied straight
// off the record, status set per the record's own kind. This is
// PRESENTATION ADAPTATION ONLY, never event synthesis: it invents no wire
// frame, mints no correlation id, and crosses no Hub — it exists solely so
// RefusalCard (built to read a Step) can render the SAME species discrimination
// and pinned copy for a demonstration refusal that it renders for a genuine
// leg.failed. Consumed only by StepDetail.tsx's DemoStepDetail, never fed into
// its wire-run branches (which assume a genuinely observed frame).
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
