// FlowMap.tsx — the flow map, default zoom level: a fixed node rail
// (provider → gateway → validator → remote zone) whose steps light in event
// order. Shown-never-faked, honesty about the hosted side: the remote zone
// (hub / payer-gateway / payer-engine) is rendered as a distinct container
// the Kit does NOT observe inside — it lights only from what the Smart
// Gateway itself sent and the verified response it got back, never from
// assuming the hosted side did anything more than that. Pure
// presentational component: selection is controlled via props, no internal
// fetch/state beyond what's needed to render.
import { useEffect, useRef, useState, type JSX } from 'react';
import type { Lane } from './types';
import type { RunStory, Step } from './inspect';
import { FlowEdges, type FlowEdgesHandle } from './FlowEdges';

export interface FlowMapProps {
  story: RunStory;
  lane: Lane;
  selectedStepId?: string;
  onSelectStep(id: string): void;
  // Overrides the lane-default provider node label (a BYO swap's honest
  // lane label — "Your EHR (FHIR data source)" / "Your Da Vinci system").
  // Purely cosmetic: it never changes the lit/isStatic behavior below,
  // which stays keyed on `lane` exactly as before.
  providerLabel?: string;
  // replayToken: an incrementing counter (RunInspector's Replay-run button)
  // — each increment starts one whole-story replay in observed event order.
  // The VALUE is otherwise meaningless; only "did it change since last
  // render" matters (see the mount-skip guard in the effect below).
  replayToken?: number;
  // Called once a replay run has finished sequencing every step (or was
  // interrupted by unmount/a stale token) and the map has returned to
  // showing the full, un-cut story.
  onReplayEnd?(): void;
}

// Pinned exactly — the honest caption on the remote-zone container.
export const REMOTE_ZONE_CAPTION =
  'derived from what the Smart Gateway sent and the verified response it received — the Kit does not observe inside the hosted side';

// ehr lane: the provider node names the plain-EHR seeded data source
// directly. It lights off sor.read steps — the observer DOES instrument
// fhirsor reads — so a real sor-instrumented gateway shows a genuinely lit
// source node. Only when the story carries NO sor steps at
// all (an old, un-instrumented gateway) does it degrade to the static
// dashed seeded-source treatment (`data-static="true"`, never lit) — the
// honest fallback for a run this Kit build can't observe into.
export const EHR_PROVIDER_LABEL = 'Plain EHR (seeded data source)';
// conformant lane: the provider node is the real Da Vinci client the
// ingress observed — it lights when the story carries ingress steps.
export const CONFORMANT_PROVIDER_LABEL = 'Provider system';

type NodeId = 'provider' | 'gateway' | 'validator' | 'hub' | 'payer-gateway' | 'payer-engine';

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
  if (step.kind === 'sor') return lane === 'ehr' ? 'src' : undefined;
  if (step.kind === 'ingress') return 'src';
  if (step.kind === 'validate') return 'val';
  return 'leg';
}

// phasesForStep: the pulse phase sequence a selected step replays along its
// edge (see the phase table below). Only called once a step has resolved to
// a real edge (edgeForStep(step, lane) !== undefined) — the conformant-lane sor
// case (no edge) is handled separately (gateway-node flash, no pulse).
// - leg: ok -> out,back (a genuine response came back); open/failed -> out
//   only (nothing verified back yet, and a failed leg never gets a back
//   pulse — same honesty rule as the edge lighting itself).
// - ingress: responded -> out,back; still open -> out only.
// - validate and sor (ehr lane only, reached via 'src') always replay
//   out,back — both are single round-trip exchanges observed complete.
function phasesForStep(step: Step): Array<'out' | 'back'> {
  if (step.kind === 'leg') return step.status === 'ok' ? ['out', 'back'] : ['out'];
  if (step.kind === 'ingress') return step.response !== undefined ? ['out', 'back'] : ['out'];
  return ['out', 'back'];
}

function edgeFor(step: Step, lane: Lane): { from: string; to: string } {
  if (step.kind === 'sor') return lane === 'ehr' ? { from: 'gateway', to: 'provider' } : { from: 'gateway', to: 'gateway' };
  if (step.kind === 'ingress') return { from: 'provider', to: 'gateway' };
  if (step.kind === 'validate') return { from: 'gateway', to: 'validator' };
  return { from: 'gateway', to: 'remote' };
}

function classNames(...parts: Array<string | false | undefined>): string {
  return parts.filter((p): p is string => Boolean(p)).join(' ');
}

// prefersReducedMotion mirrors FlowEdges' own check (kept local rather than
// exported/shared — it's a three-line media-query read, not part of the
// single pulse animation path the replay loop below reuses via
// FlowEdgesHandle.pulse). Replay uses it only to size the edge-less
// gateway-flash dwell (0ms under reduced motion, matching pulse()'s own
// instant-resolve behavior for the pulsed steps).
function prefersReducedMotion(): boolean {
  return Boolean(window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches);
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

// isPayerLegType classifies a leg's counterpart-ness by its legType FAMILY
// (the narration vocabulary), never a raw counterpart string match (no
// classifier existed before this — FlowMap lit all three remote nodes off
// one shared `remoteLit`, and the committed fixtures carry only
// counterpart "payer", so a string match would be untested off that one
// value). `federated-query-submit` and
// `patient-dtr-request` are non-payer legs (a named holder/facility is the
// counterpart); `coverage-eligibility`, every `crd-*` leg, and
// `dtr-questionnaire-fetch` are payer legs; `pas-*` (pas-submit's wire
// legType `pas-claim`, and the amended `pas-update-submit`) are payer legs.
// An unrecognized legType lights the Hub only — the honest fallback,
// mirroring the narration table's own degradation rule.
function isPayerLegType(legType: string): boolean {
  if (legType === 'federated-query-submit' || legType === 'patient-dtr-request') return false;
  if (legType === 'coverage-eligibility' || legType === 'dtr-questionnaire-fetch') return true;
  if (legType.startsWith('crd-') || legType.startsWith('pas-')) return true;
  return false;
}

function FlowNode({
  id,
  label,
  lit,
  remote,
  isStatic,
  flash,
}: {
  id: NodeId;
  label: string;
  lit: boolean;
  remote?: boolean;
  isStatic?: boolean;
  flash?: boolean;
}): JSX.Element {
  return (
    <div
      className={classNames('node', remote && 'remote', lit && 'lit', flash && 'flash')}
      data-node={id}
      data-static={isStatic ? 'true' : undefined}
    >
      <span className="b" />
      {label}
    </div>
  );
}

export function FlowMap({
  story,
  lane,
  selectedStepId,
  onSelectStep,
  providerLabel: providerLabelOverride,
  replayToken,
  onReplayEnd,
}: FlowMapProps): JSX.Element {
  const steps = story.steps;

  // replayCut: while non-null, every node/edge derivation below reads
  // `effectiveSteps` (steps.slice(0, replayCut)) instead of the full story —
  // the progressive-lighting illusion. null (the resting state, both before
  // and after a replay) falls back to the full story. The step LIST below
  // (`<ol className="steps">`) intentionally stays on the full `steps` at
  // all times — replay animates the map's lighting, not which steps are
  // clickable.
  const [replayCut, setReplayCut] = useState<number | null>(null);
  const effectiveSteps = replayCut !== null ? steps.slice(0, replayCut) : steps;

  const hasIngress = effectiveSteps.some((s) => s.kind === 'ingress');
  const hasValidate = effectiveSteps.some((s) => s.kind === 'validate');
  // Remote lighting is gated on a genuine response (status ok) only — an
  // open leg (request sent, nothing observed back yet) must never light the
  // hosted side (shown-never-faked). A failed leg still reached the
  // boundary — it marks the zone's edge failed, but the interior never
  // claims a confirmed response it didn't get.
  //
  // The Hub lights on ANY ok leg — it is the one node every leg genuinely
  // crosses, payer or not. The payer pair (payer-gateway, payer-engine)
  // lights ONLY on an ok leg whose legType is payer-family (isPayerLegType)
  // — a non-payer leg (e.g. a federated query to a named holder) reached
  // the Hub, never the hosted payer, and lighting the payer nodes for it
  // would be the rendered-story lie an honest flow map must avoid.
  const remoteLit = effectiveSteps.some((s) => s.kind === 'leg' && s.status === 'ok');
  const remoteFailed = effectiveSteps.some((s) => s.kind === 'leg' && s.status === 'failed');
  const payerLit = effectiveSteps.some(
    (s) => s.kind === 'leg' && s.status === 'ok' && isPayerLegType(s.legType),
  );

  const hasSor = effectiveSteps.some((s) => s.kind === 'sor');

  const providerLabel =
    providerLabelOverride ?? (lane === 'ehr' ? EHR_PROVIDER_LABEL : CONFORMANT_PROVIDER_LABEL);
  const providerLit = (lane === 'conformant' && hasIngress) || (lane === 'ehr' && hasSor);
  const providerStatic = lane === 'ehr' && !hasSor;

  const flowRef = useRef<HTMLDivElement | null>(null);
  const edgesHandleRef = useRef<FlowEdgesHandle>(null);
  const edges = edgeStatesFor(effectiveSteps, lane);

  const selectedStep = steps.find((s) => s.id === selectedStepId);
  const selectedEdge = selectedStep ? edgeForStep(selectedStep, lane) : undefined;

  // gatewayFlash: the edge-less replay treatment for a conformant-lane sor
  // step (edgeForStep returns undefined — the read is gateway-internal, no
  // drawn edge to pulse). A brief 600ms class on the gateway node instead.
  const [gatewayFlash, setGatewayFlash] = useState(false);

  // Selection sync: replays the selected step's pulse phase sequence along
  // its edge, or flashes the gateway node for the edge-less case. Depends
  // only on selectedStepId (not steps/lane) — a step's own edge and phases
  // are fixed for the run being viewed, and keying the effect this way gives
  // us the stale-sequence guard for free: React runs this effect's cleanup
  // (setting `cancelled`) before starting the next one whenever
  // selectedStepId changes, or on unmount, so a still-running phase sequence
  // for the PREVIOUS selection bails at its next loop check instead of
  // pulsing the wrong edge (or calling setState) after the fact.
  useEffect(() => {
    let cancelled = false;
    const step = steps.find((s) => s.id === selectedStepId);
    if (!step) return undefined;
    const edge = edgeForStep(step, lane);
    if (edge === undefined) {
      setGatewayFlash(true);
      const timer = window.setTimeout(() => {
        if (!cancelled) setGatewayFlash(false);
      }, 600);
      return () => {
        cancelled = true;
        window.clearTimeout(timer);
      };
    }
    const phases = phasesForStep(step);
    void (async () => {
      for (const dir of phases) {
        if (cancelled) return;
        await edgesHandleRef.current?.pulse(edge, dir);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentionally
    // keyed on selectedStepId only, see comment above.
  }, [selectedStepId]);

  // Replay: RunInspector's Replay-run button increments `replayToken`; each
  // increment sequences the WHOLE story again in observed event order —
  // cut 0 (nothing lit), then cut 1..steps.length one step at a time,
  // pulsing (or edge-less gateway-flashing) each step exactly as selection
  // does, then back to cut null (the resting, full-story state) + the
  // onReplayEnd callback.
  //
  // isFirstReplayRun skips the mount commit unconditionally — RunInspector
  // hands FlowMap a real starting token (0), so keying only on "did the
  // value change since last render" would still fire on mount if we
  // compared against an uninitialized ref; a plain "was this the first run
  // of this effect" flag sidesteps that regardless of what value the token
  // starts at.
  //
  // replayingRef re-entry guard: RunInspector's button is disabled while a
  // replay is in flight (App-level state, not enforced here), but this
  // guard makes FlowMap itself refuse to start a second overlapping replay
  // loop if a caller ever fires two tokens in a row before the first
  // finishes.
  const isFirstReplayRun = useRef(true);
  const replayingRef = useRef(false);
  useEffect(() => {
    const isMount = isFirstReplayRun.current;
    isFirstReplayRun.current = false;
    if (isMount) return undefined;
    if (replayToken === undefined) return undefined;
    if (replayingRef.current) return undefined;

    let cancelled = false;
    replayingRef.current = true;
    setReplayCut(0);

    // signalEnd fires onReplayEnd EXACTLY ONCE per replay run, on whichever
    // path ends the run first: normal completion (the loop sequenced every
    // step) OR interruption (this effect's cleanup — a stale token, or an
    // unmount mid-replay, e.g. RunInspector's loading early-return unmounting
    // FlowMap while a history fetch is in flight). The prop's own doc comment
    // promises this; without the cleanup path a mid-replay unmount would leave
    // the caller's `replaying` state stuck true and its Replay button wedged.
    // The `ended` latch guards against the double-call when normal completion
    // and a later cleanup both run.
    let ended = false;
    const signalEnd = () => {
      if (ended) return;
      ended = true;
      replayingRef.current = false;
      onReplayEnd?.();
    };

    void (async () => {
      for (let i = 0; i < steps.length; i++) {
        if (cancelled) break;
        const step = steps[i];
        const edge = edgeForStep(step, lane);
        if (edge !== undefined) {
          for (const dir of phasesForStep(step)) {
            if (cancelled) break;
            await edgesHandleRef.current?.pulse(edge, dir);
          }
        } else {
          setGatewayFlash(true);
          await delay(prefersReducedMotion() ? 0 : 300);
          if (!cancelled) setGatewayFlash(false);
        }
        if (cancelled) break;
        setReplayCut(i + 1);
      }
      if (!cancelled) setReplayCut(null);
      signalEnd();
    })();

    return () => {
      cancelled = true;
      signalEnd();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentionally
    // keyed on replayToken only; steps/lane/onReplayEnd are read fresh via
    // closure each time a new token starts the effect.
  }, [replayToken]);

  return (
    <div className="flow" ref={flowRef}>
      <div className="k">Flow</div>
      <FlowEdges ref={edgesHandleRef} edges={edges} selectedEdge={selectedEdge} railRef={flowRef} />
      <FlowNode id="provider" label={providerLabel} lit={providerLit} isStatic={providerStatic} />
      <FlowNode id="gateway" label="Smart Gateway" lit={effectiveSteps.length > 0} flash={gatewayFlash} />
      <FlowNode id="validator" label="Validator" lit={hasValidate} />
      <div className={classNames('remote', remoteLit && 'lit', remoteFailed && 'failed')}>
        <p className="cap">{REMOTE_ZONE_CAPTION}</p>
        <FlowNode id="hub" label="Hub" lit={remoteLit} remote />
        <FlowNode id="payer-gateway" label="Payer Smart Gateway" lit={payerLit} remote />
        <FlowNode id="payer-engine" label="Payer system" lit={payerLit} remote />
      </div>

      <ol className="steps">
        {steps.map((step) => {
          const { from, to } = edgeFor(step, lane);
          const selected = step.id === selectedStepId;
          return (
            <li key={step.id}>
              <button
                type="button"
                className={classNames('step', step.status, selected && 'sel')}
                data-step-id={step.id}
                data-from={from}
                data-to={to}
                data-status={step.status}
                aria-pressed={selected}
                onClick={() => onSelectStep(step.id)}
              >
                <span className="lt">{step.legType}</span>
                {step.kind === 'leg' && (
                  <span className="cp">{step.counterpart ?? 'the hosted counterparty'}</span>
                )}
                {step.kind === 'sor' && <span className="cp">{step.sorOp ?? 'read'}</span>}
              </button>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

export default FlowMap;
