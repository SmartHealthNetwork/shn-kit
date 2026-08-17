// bridgingmeta.ts — ALL copy for the Bridging destination (ucmeta.ts idiom:
// participant-facing, both registers, true to asserted outcomes, no
// SHN-internal vocabulary in this file's exported strings; Da Vinci-domain
// terms — contract line, CRD, DTR, PAS, PDex, prior authorization — stay).
// The strings below are PINNED: exported verbatim, asserted both rendered
// (BridgingPanel/StepDetail, per string) and literal (the matching
// *.test.tsx), the existing VALIDATOR_POSTURE_LABEL idiom (StepDetail.tsx).
import type { Register } from './types';

// ---- Pinned exports — verbatim, do not paraphrase or reformat.

// Pinned exactly; do not paraphrase ("routes as", no "2.0" literal).
export const DEMO_MODE_BADGE =
  'Compatibility simulation active — this gateway routes as a build that predates the newer contract lines. Your registration and normal scenarios are unaffected.';

// Pinned exactly; do not paraphrase.
export const BRIDGING_REMOTE_CAPTION =
  'the demo counterparty is a Smart Health Network-operated gateway on the preview network — this traffic is real and crosses the network';

// Pinned exactly; do not paraphrase.
export const ENGINE_EXHIBIT_FRAMING =
  'engine demonstration over frozen reference content — the same modules your live legs route through';

// Pinned exactly; do not paraphrase.
export const REFUSAL_EXHIBIT_FRAMING =
  'a refusal is the successful outcome here: the network refuses loudly rather than guessing at clinical content, and nothing is sent';

// ---- BridgingPanel's exhibit receipt copy — the panel's own success-state
// replaces its former inline verdict box with a one-line receipt (tick +
// this text + the inspector link); the demonstration itself now lives in
// the run inspector (RunInspector/StepDetail), not in this panel. Pinned
// exactly; do not paraphrase.

export const DEMO_RECEIPT_REFUSAL = 'Ran just now — refused as expected.';
export const DEMO_RECEIPT_CARRY = 'Ran just now — restored exactly.';
export const VIEW_IN_INSPECTOR_LINK = 'View in inspector →';

// ---- Explainer copy: contract lines + the three compat-manifest step
// classes, both registers, driven by the existing RegisterSwitch. ----

export const CONTRACT_LINE_EXPLAINER: Record<Register, string> = {
  overview:
    "Every step of a prior-authorization exchange — checking coverage, filling out a questionnaire, submitting a request — is built to one shape at a time, called a contract line. Two systems exchange smoothly only when they share a line, or when one of them bridges between the lines each side speaks.",
  technical:
    "A contract line is one version token (e.g. pa.pas@2.2) of a Da Vinci workstream — CRD, DTR, PAS, or PDex. A leg between two systems routes on a line both declare, or, when their declared sets don't overlap, on a chain of compatibility steps that bridges between the lines each side speaks.",
};

export type StepClass = 'full' | 'carry' | 'gated';

export const STEP_CLASSES: StepClass[] = ['full', 'carry', 'gated'];

export interface StepClassMeta {
  label: string;
  description: Record<Register, string>;
}

export const STEP_CLASS_META: Record<StepClass, StepClassMeta> = {
  full: {
    label: 'Full',
    description: {
      overview:
        'Nothing is lost moving between the two versions — the older and newer shapes carry exactly the same information, in both directions.',
      technical:
        'A full step is lossless both directions: the compatibility module (or a byte-identical pass-through, when the two lines are behaviorally identical) carries every element across without approximation.',
    },
  },
  carry: {
    label: 'Carry',
    description: {
      overview:
        "The newer version has information the older one has no place for. Nothing is dropped: it travels alongside, unread by the older version, and is put back exactly when a newer version reads it again.",
      technical:
        'A carry step downcasts an element the target line has no honest slot for into a shn-carried-content extension; the matching upcast restores it byte-faithful. The loss is recorded and visible in the run inspector, never silent.',
    },
  },
  gated: {
    label: 'Gated',
    description: {
      overview:
        "Some content can't be honestly translated at all — there's no truthful way to guess what the older or newer version would have said. Rather than fabricate an answer, the network refuses.",
      technical:
        'A gated step has no honest byte-level source for a required element on at least one direction; the module refuses with a typed semantic-change error instead of synthesizing content it cannot ground.',
    },
  },
};

// ---- Local-demonstration species copy — RunInspector/FlowMap/DemoChips'
// rendering of an engine exhibit as an inspector run, a disjoint species
// from every wire-run string above. Pinned exactly; do not paraphrase.

// LocalDemoChip's text (DemoChips.tsx) — every demonstration run's species
// marker, distinct from a genuine result.
export const LOCAL_DEMO_CHIP = 'local demonstration — no network';

// DemoResultChip's kind-keyed verdict text (DemoChips.tsx) — the
// demonstration's own outcome sentence, rendered only for the one state a
// demonstration record can carry (passed; see kit/kitd/bridging.go's
// emitDemoRun, called from the success path only).
export const DEMO_RESULT_REFUSAL = 'Refused as expected';
export const DEMO_RESULT_CARRY = 'Restored exactly';

// FlowMap's demonstration-variant remote-zone caption (FlowMap.tsx) —
// replaces REMOTE_ZONE_CAPTION for this species only; a demonstration never
// crosses the network, so there is nothing "derived from" to describe.
export const DEMO_REMOTE_CAPTION =
  'not involved — this demonstration never left your Smart Gateway';

// FlowMap's demonstration-variant source-node label (FlowMap.tsx) — the
// static/italic node naming the embedded fixture the exhibit ran over.
export const FROZEN_SOURCE_NODE = 'Frozen reference content';

// StepDetail's demonstration framing line — ENGINE_EXHIBIT_FRAMING carries
// no terminal period by design (the panel composed it into its own
// sentence); this appends the inspector's closing sentence with an
// explicit '.' separator, which is load-bearing, not decorative.
export const LOCAL_DEMO_FRAMING = `${ENGINE_EXHIBIT_FRAMING}. Nothing crossed the network for this run.`;

// FlowMap's demonstration steps rail — the single
// synthetic row's fixed label and class caption. The row's third piece, the
// route tag, is DERIVED per record (FlowMap.tsx's demoRouteTag) from the
// record's own contract + chain rather than pinned as a fixed string, so it
// stays honest if the frozen fixtures ever change contract/lines.
export const DEMO_STEP_LABEL = 'dtr-questionnaire-response';
export const DEMO_STEP_CLASS_CAPTION = 'engine demonstration';

// DEMO_RESTORED_VERDICT: the byte-identical-round-trip verdict line, shared
// by BOTH of XformDiff.tsx's render paths — its own internal summary
// computation (a live leg's identity chain, or the carry demonstration fed
// locally) and StepDetail.tsx's DemoStepDetail (the carry species' own
// restored-verdict line, computed independently over the record's input/
// output pair via computeXformDiff, never trusted off the wire's `restored`
// flag alone). ONE exported constant, both sites read it, so the claim can
// never drift between the two surfaces that make it. Pinned exactly; do not
// paraphrase.
export const DEMO_RESTORED_VERDICT = '0 regions differ — byte-identical';

// RunInspector's demo-species Replay control — the inline role="alert"
// message on a rejected re-execution (App's handleReplayDemo posts to the
// same postBridgingExhibit endpoint the panel's own exhibit buttons use).
// Pinned exactly; do not paraphrase.
export const DEMO_REPLAY_FAILURE_NOTE = 'Replay failed — the exhibit could not run.';

// ---- Static route-refusal grammar, verbatim — displayed as reference
// COPY, never produced live in this demo
// (every demo counterparty always shares a bridge; strictness stays
// dormant). Verbatim from docs/PARTICIPANT_PROTOCOL.md's grammar example. ----

export const ROUTE_REFUSAL_GRAMMAR_EXAMPLE =
  '{"error":"no shared contract line for pa.pas (leg pas-claim): this gateway speaks pa.pas@2.0; recipient \\"acme-payer\\" declares pa.pas@2.3 — no bridge available (no transform chain bridges to line 2.3)"}';
