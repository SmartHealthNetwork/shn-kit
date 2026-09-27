// flowMapModel.ts: FlowMap's pure derivations (the demonstration route tag and
// the edge lighting), kept out of FlowMap.tsx so that file exports only
// components and constants (react-refresh/only-export-components).
import type { DemoRecord, Lane } from './types';
import type { Step } from './inspect';

// demoRouteTag: the demonstration steps rail's route tag, derived from the
// record's own contract + chain, never hardcoded per demonstration kind, so
// it stays honest if the frozen fixtures ever change contract/lines. Today's
// fixtures render exactly "pa.dtr 2.1 -> 2.2 . local" (refusal) and
// "pa.dtr 2.2 -> 2.1 -> 2.2 . local" (carry) (arrows/dot are the real Unicode
// characters; ASCII'd here only in this comment) - both asserted literally
// in FlowMap.test.tsx against the current fixtures. An empty chain
// (defensive; today's records always carry one) degrades to
// "{contract} . local" rather than fabricating a path.
export function demoRouteTag(record: Pick<DemoRecord, 'contract' | 'chain'>): string {
  if (record.chain.length === 0) return `${record.contract} · local`;
  const path = [record.chain[0].from, ...record.chain.map((h) => h.to)].join(' → ');
  return `${record.contract} ${path} · local`;
}

export interface EdgeLight {
  out: boolean;
  back: boolean;
}
export type SrcEdge = EdgeLight | 'static'; // 'static' = ehr lane, no sor steps (old-gateway fallback)
export interface EdgeStates {
  src: SrcEdge;
  val: EdgeLight;
  leg: EdgeLight;
}
export type EdgeKey = 'src' | 'val' | 'leg';

// edgeStatesFor derives the directional edge lighting from OBSERVED steps
// only (shown-never-faked): out and back light independently — an open leg
// shows an outbound arrow and nothing back; a failed leg never lights the
// back arrow (no verified response); an ingress lights back only once
// ingress.responded closed it. In the ehr lane the provider edge lights off
// sor steps; with none (an old, un-instrumented gateway) it degrades to the
// 'static' dashed seeded-source treatment.
export function edgeStatesFor(steps: Step[], lane: Lane): EdgeStates {
  const hasSor = steps.some((s) => s.kind === 'sor');
  const hasIngress = steps.some((s) => s.kind === 'ingress');
  const hasIngressResponse = steps.some((s) => s.kind === 'ingress' && s.response !== undefined);
  const hasValidate = steps.some((s) => s.kind === 'validate');
  const hasLeg = steps.some((s) => s.kind === 'leg');
  const hasOkLeg = steps.some((s) => s.kind === 'leg' && s.status === 'ok');
  const src: SrcEdge =
    lane === 'ehr'
      ? hasSor
        ? { out: true, back: true }
        : 'static'
      : { out: hasIngress, back: hasIngressResponse };
  return { src, val: { out: hasValidate, back: hasValidate }, leg: { out: hasLeg, back: hasOkLeg } };
}

// edgeForStep: which drawn edge a step's exchange traversed. A conformant-
// lane sor step maps to NO edge — there the provider node is the calling
// Da Vinci client, not the data source; the read is gateway-internal.
export function edgeForStep(step: Step, lane: Lane): EdgeKey | undefined {
  // The Plain EHR lane reads the provider's data source; the conformant
  // lane's provider node is the calling client.
  if (step.kind === 'sor') return lane !== 'conformant' ? 'src' : undefined;
  if (step.kind === 'ingress') return 'src';
  if (step.kind === 'validate') return 'val';
  // conformance.observed is a local policy judgment, never a network hop —
  // no edge, in either lane. Falling through to the 'leg' default below
  // would pulse a false remote-node animation for a check that never left
  // the gateway; selecting one instead gets the same edge-less
  // gateway-flash treatment the conformant-lane sor case already has.
  if (step.kind === 'conformance') return undefined;
  return 'leg';
}
