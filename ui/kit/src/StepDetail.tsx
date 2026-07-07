// StepDetail.tsx — the step detail pane: renders one Step in two
// presentations. Clinical view is the narration + the raw request/response
// payloads (a JsonView each, one search box wired to both). Substrate view
// is the payload-BLIND leg-facts framing: no payload JSON reaches the DOM
// in that view — only the envelope facts (correlation id, direction,
// counterpart, authority frames, approximate sizes).
import { useState } from 'react';
import type { JSX } from 'react';
import type { Step } from './inspect';
import { JsonView } from './JsonView';
import { TickIcon } from './StatusChip';

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

export function StepDetail({ step, view, posture = 'stand-in' }: StepDetailProps): JSX.Element {
  const [search, setSearch] = useState('');

  const rootClassName = `detail step-status-${step.status} step-kind-${step.kind}`;
  const failureDetail = step.status === 'failed' ? step.response?.detail ?? step.request?.detail : undefined;
  const isValidate = step.kind === 'validate';

  if (view === 'substrate') {
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
        </dl>
        {step.validation !== undefined && (
          <ValidationBadge validation={step.validation} posture={posture} />
        )}
        {!step.response && <p className="open-step-note">{OPEN_STEP_NOTE}</p>}
        {failureDetail && <p className="failure-detail">{failureDetail}</p>}
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

  return (
    <div className={rootClassName} data-view="clinical">
      <p className="narr">{step.narration}</p>
      {step.kind === 'ingress' && step.httpStatus !== undefined && (
        <p className="http-status">HTTP {step.httpStatus}</p>
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
    </div>
  );
}
