// FlowMap.tsx — the flow map, default zoom level: a fixed node rail
// (provider → gateway → validator → remote zone) whose steps light in event
// order. Shown-never-faked, honesty about the hosted side: the remote zone
// (hub / payer-gateway / payer-engine) is rendered as a distinct container
// the Kit does NOT observe inside — it lights only from what the Smart
// Gateway itself sent and the verified response it got back, never from
// assuming the hosted side did anything more than that. Pure
// presentational component: selection is controlled via props, no internal
// fetch/state beyond what's needed to render.
import type { JSX } from 'react';
import type { Lane } from './types';
import type { RunStory, Step } from './inspect';

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
}

// Pinned exactly — the honest caption on the remote-zone container.
export const REMOTE_ZONE_CAPTION =
  'derived from what the Smart Gateway sent and the verified response it received — the Kit does not observe inside the hosted side';

// ehr lane: the provider node names the plain-EHR seeded data source
// directly — this node is a static engine-internal seam (the observer
// never instruments fhirsor reads) and never lights, regardless of what
// the story contains.
export const EHR_PROVIDER_LABEL = 'Plain EHR (seeded data source)';
// conformant lane: the provider node is the real Da Vinci client the
// ingress observed — it lights when the story carries ingress steps.
export const CONFORMANT_PROVIDER_LABEL = 'Provider system';

type NodeId = 'provider' | 'gateway' | 'validator' | 'hub' | 'payer-gateway' | 'payer-engine';

function edgeFor(step: Step): { from: string; to: string } {
  if (step.kind === 'ingress') return { from: 'provider', to: 'gateway' };
  if (step.kind === 'validate') return { from: 'gateway', to: 'validator' };
  return { from: 'gateway', to: 'remote' };
}

function classNames(...parts: Array<string | false | undefined>): string {
  return parts.filter((p): p is string => Boolean(p)).join(' ');
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
}: {
  id: NodeId;
  label: string;
  lit: boolean;
  remote?: boolean;
  isStatic?: boolean;
}): JSX.Element {
  return (
    <div
      className={classNames('node', remote && 'remote', lit && 'lit')}
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
}: FlowMapProps): JSX.Element {
  const steps = story.steps;

  const hasIngress = steps.some((s) => s.kind === 'ingress');
  const hasValidate = steps.some((s) => s.kind === 'validate');
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
  const remoteLit = steps.some((s) => s.kind === 'leg' && s.status === 'ok');
  const remoteFailed = steps.some((s) => s.kind === 'leg' && s.status === 'failed');
  const payerLit = steps.some((s) => s.kind === 'leg' && s.status === 'ok' && isPayerLegType(s.legType));

  const providerLabel =
    providerLabelOverride ?? (lane === 'ehr' ? EHR_PROVIDER_LABEL : CONFORMANT_PROVIDER_LABEL);
  const providerLit = lane === 'conformant' && hasIngress;
  const providerStatic = lane === 'ehr';

  return (
    <div className="flow">
      <div className="k">Flow</div>
      <FlowNode id="provider" label={providerLabel} lit={providerLit} isStatic={providerStatic} />
      <FlowNode id="gateway" label="Smart Gateway" lit={steps.length > 0} />
      <FlowNode id="validator" label="Validator" lit={hasValidate} />
      <div className={classNames('remote', remoteLit && 'lit', remoteFailed && 'failed')}>
        <p className="cap">{REMOTE_ZONE_CAPTION}</p>
        <FlowNode id="hub" label="Hub" lit={remoteLit} remote />
        <FlowNode id="payer-gateway" label="Payer Smart Gateway" lit={payerLit} remote />
        <FlowNode id="payer-engine" label="Payer system" lit={payerLit} remote />
      </div>

      <ol className="steps">
        {steps.map((step) => {
          const { from, to } = edgeFor(step);
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
              </button>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

export default FlowMap;
