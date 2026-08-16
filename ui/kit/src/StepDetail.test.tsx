import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  StepDetail,
  VALIDATOR_POSTURE_LABEL,
  PACKAGED_VALIDATOR_POSTURE_LABEL,
  OPEN_STEP_NOTE,
  SUBSTRATE_FRAMING,
  VALIDATE_SUBSTRATE_NOTE,
  SOR_LOCAL_READ_NOTE,
  TRANSFORM_EMPTY_CONTENT_NOTE,
  IDENTITY_CHAIN_NOTE,
  ZERO_BYTES_NOTE,
  CARRY_REFUSAL_NOTE,
  LEG_DOWNGRADE_NOTE,
  TRANSFORM_CARD_NARRATION,
  relayedStatusLine,
  directionRows,
} from './StepDetail';
import { buildRunStory } from './inspect';
import type { Step } from './inspect';
import type { KitEvent } from './types';
import ehrUc03 from './fixtures/run-ehr-uc03.json';
import conformantUc03 from './fixtures/run-conformant-uc03.json';

const ehrEvents = ehrUc03 as unknown as KitEvent[];
const conformantEvents = conformantUc03 as unknown as KitEvent[];

const ehrStory = buildRunStory(ehrEvents[0].runId as string, ehrEvents);
const conformantStory = buildRunStory(conformantEvents[0].runId as string, conformantEvents);

// A distinctive string living only inside crd-order-select's request payload
// (the ServiceRequest.code.coding.display) — used to prove the substrate
// view never renders payload JSON.
const FIXTURE_PAYLOAD_MARKER = 'MRI lumbar spine w/o contrast';

function findStep(steps: Step[], legType: string): Step {
  const step = steps.find((s) => s.legType === legType);
  if (!step) throw new Error(`fixture missing expected step legType=${legType}`);
  return step;
}

function openLegStep(): Step {
  return {
    id: '1',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'open',
    request: {
      seq: 1,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'pas-claim',
      direction: 'originate',
      correlationId: 'c-open-1',
      counterpart: 'payer',
      authorityFrame: 'provider-tpo',
      op: 'pas-submit',
      payload: { hello: 'world' },
    },
    correlationId: 'c-open-1',
    counterpart: 'payer',
    requestAuthority: 'provider-tpo',
    narration: 'The Smart Gateway submitted the prior-authorization request through the Hub, awaiting its decision.',
  };
}

function failedLegStep(): Step {
  return {
    id: '2',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'failed',
    request: {
      seq: 2,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'pas-claim',
      direction: 'originate',
      correlationId: 'c-fail-1',
      counterpart: 'payer',
      authorityFrame: 'provider-tpo',
      op: 'pas-submit',
      payload: { claim: 'stub' },
    },
    response: {
      seq: 3,
      time: '2026-07-03T00:00:01Z',
      kind: 'leg.failed',
      legType: 'pas-claim',
      correlationId: 'c-fail-1',
      detail: 'connection timed out',
    },
    correlationId: 'c-fail-1',
    counterpart: 'payer',
    requestAuthority: 'provider-tpo',
    narration: 'The Smart Gateway’s prior-authorization submission to the hosted payer through the Hub did not complete.',
  };
}

function invalidValidateStep(): Step {
  return {
    id: '4',
    kind: 'validate',
    legType: 'validate.result',
    status: 'failed',
    request: {
      seq: 4,
      time: '2026-07-03T00:00:00Z',
      kind: 'validate.result',
      detail: 'invalid: missing required element Claim.type',
    },
    validation: 'invalid: missing required element Claim.type',
    narration: 'The Smart Gateway found this resource did not validate against its FHIR profile.',
  };
}

// The following four fixtures (okLegStep/sorStep/validateStep/ingressStep)
// are duplicated from FlowMap.test.tsx rather than shared via a module —
// this file already defines its OWN openLegStep()/failedLegStep() (reused
// below for the directionRows open/failed cases) with different literal
// field values than FlowMap.test.tsx's same-named helpers, so importing
// FlowMap's versions would collide. Duplicating just these four keeps the
// diff smallest without renaming either file's existing fixtures.
function okLegStep(counterpart = 'payer'): Step {
  return {
    id: '3',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'ok',
    request: {
      seq: 3,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'pas-claim',
      correlationId: 'c-2',
      counterpart,
    },
    response: {
      seq: 4,
      time: '2026-07-03T00:00:01Z',
      kind: 'leg.response',
      legType: 'pas-claim',
      correlationId: 'c-2',
    },
    correlationId: 'c-2',
    counterpart,
    narration: 'ok leg narration',
  };
}

function sorStep(op = 'OpenOrder'): Step {
  return {
    id: '9',
    kind: 'sor',
    legType: 'sor.read',
    status: 'ok',
    request: { seq: 9, time: '2026-07-03T00:00:00Z', kind: 'sor.read', op, detail: 'found' },
    sorOp: op,
    sorDetail: 'found',
    narration: 'sor narration',
  };
}

// A sor step whose returned resource bytes are carried on request.payload
// (the observer frame's payload = the RETURNED resource for a sor.read) — a
// distinctive marker string proves whether/where those bytes reach the DOM.
const SOR_RETURNED_MARKER = 'sor-returned-marker';
function sorStepWithPayload(op = 'OpenOrder'): Step {
  return {
    id: '9',
    kind: 'sor',
    legType: 'sor.read',
    status: 'ok',
    request: {
      seq: 9,
      time: '2026-07-03T00:00:00Z',
      kind: 'sor.read',
      op,
      detail: 'found',
      payload: { resourceType: 'ServiceRequest', id: SOR_RETURNED_MARKER },
    },
    sorOp: op,
    sorDetail: 'found',
    narration: 'sor narration',
  };
}

function validateStep(): Step {
  return {
    id: '5',
    kind: 'validate',
    legType: 'validate.result',
    status: 'ok',
    request: { seq: 7, time: '2026-07-03T00:00:00Z', kind: 'validate.result', detail: 'valid' },
    validation: 'valid',
    narration: 'validate narration',
  };
}

function ingressStep(): Step {
  return {
    id: '1',
    kind: 'ingress',
    legType: 'crd-ingress',
    status: 'ok',
    request: { seq: 1, time: '2026-07-03T00:00:00Z', kind: 'ingress.received', legType: 'crd-ingress' },
    response: { seq: 2, time: '2026-07-03T00:00:01Z', kind: 'ingress.responded', legType: 'crd-ingress', detail: '200' },
    httpStatus: '200',
    narration: 'ingress narration',
  };
}

describe('directionRows', () => {
  it('ok leg: request out via the Hub, verified response back', () => {
    const rows = directionRows(okLegStep());
    expect(rows).toHaveLength(2);
    expect(rows[0]).toEqual({ arrow: '→', who: 'Smart Gateway → Hub → payer', what: 'pas-claim request' });
    expect(rows[1].who).toBe('payer → Hub → Smart Gateway');
    expect(rows[1].what).toMatch(/verified response/);
  });
  it('open leg: outbound row only — never claims a response it has not seen', () => {
    expect(directionRows(openLegStep())).toHaveLength(1);
  });
  it('failed leg: back row says no verified response', () => {
    const rows = directionRows(failedLegStep());
    expect(rows[1].what).toMatch(/no verified response/);
  });
  it('sor step: data-source read, both rows, no Hub/counterparty language', () => {
    const rows = directionRows(sorStep());
    expect(rows[0].who).toBe('Smart Gateway → its data source');
    expect(rows[0].what).toBe('read: OpenOrder');
    expect(rows[1].what).toBe('found');
    expect(JSON.stringify(rows)).not.toMatch(/Hub|counterpart/);
  });
  it('validate + ingress rows', () => {
    expect(directionRows(validateStep())[1].what).toBe('result: valid');
    expect(directionRows(ingressStep())[1].what).toBe('HTTP 200 response');
  });
});

describe('StepDetail — clinical view', () => {
  it('renders the narration paragraph and Request/Response JsonView sections from the frames', () => {
    const step = findStep(ehrStory.steps, 'crd-order-select');
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.getByText(step.narration)).toBeDefined();
    expect(screen.getByText('Request')).toBeDefined();
    expect(screen.getByText('Response')).toBeDefined();
    // "MBR-COVERED" sits two levels deep (context.patientId) — within
    // JsonView's default collapse depth, so it's visible without a search.
    expect(screen.getByText('MBR-COVERED')).toBeDefined();
  });

  // Validate-step label: a leg/
  // ingress step's search label + section header stay "Request"/"Response"
  // (a real request/response pair) — only the validate-step-only rendering
  // below renames to "Resource".
  it('a non-validate step keeps the "Search request and response" label and the "Request" header', () => {
    const step = findStep(ehrStory.steps, 'crd-order-select');
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.getByLabelText('Search request and response')).toBeDefined();
    expect(screen.getByText('Request')).toBeDefined();
  });

  it('has one search input wired to both the request and response panes', async () => {
    const user = userEvent.setup();
    const step = findStep(ehrStory.steps, 'crd-order-select');
    render(<StepDetail step={step} view="clinical" />);

    const inputs = screen.getAllByRole('textbox');
    expect(inputs).toHaveLength(1);

    // "lumbar" appears in the request payload ("MRI lumbar spine w/o
    // contrast") AND in the response payload (the CDS card's questionnaire
    // url "pa-lumbar-mri") — one search term, both panes react.
    await user.type(inputs[0], 'lumbar');

    // both sections report at least one match with the same search term —
    // one shared search state driving two JsonViews.
    const summaries = screen.getAllByText(/match(es)?$/);
    expect(summaries.length).toBeGreaterThanOrEqual(2);
    for (const s of summaries) {
      expect(s.textContent).not.toBe('no matches');
    }
  });

  it('posture "stand-in" (or omitted — the honest default) shows the pinned stand-in sentence, styled ok for "valid"', () => {
    const step = ehrStory.steps.find((s) => s.kind === 'validate');
    expect(step).toBeDefined();
    render(<StepDetail step={step as Step} view="clinical" />);

    expect(screen.getByText('Valid')).toBeDefined();
    expect(
      screen.getByText(
        "checked by the Kit's stand-in validator — real conformance validation is off in this development build",
      ),
    ).toBeDefined();
    expect(VALIDATOR_POSTURE_LABEL).toBe(
      "checked by the Kit's stand-in validator — real conformance validation is off in this development build",
    );
    // The old v6/v7 sentence must not survive anywhere on the page.
    expect(document.body.textContent).not.toContain('arrives with the S8 components');
  });

  it('posture "stand-in" passed explicitly renders the same sentence as the default', () => {
    const step = ehrStory.steps.find((s) => s.kind === 'validate');
    render(<StepDetail step={step as Step} view="clinical" posture="stand-in" />);
    expect(screen.getByText(VALIDATOR_POSTURE_LABEL)).toBeDefined();
  });

  it('posture "packaged" shows the pinned packaged sentence instead', () => {
    const step = ehrStory.steps.find((s) => s.kind === 'validate');
    render(<StepDetail step={step as Step} view="clinical" posture="packaged" />);

    expect(screen.getByText('Valid')).toBeDefined();
    expect(screen.getByText("checked by the Kit's local HL7 validator (offline IG set)")).toBeDefined();
    expect(PACKAGED_VALIDATOR_POSTURE_LABEL).toBe(
      "checked by the Kit's local HL7 validator (offline IG set)",
    );
    expect(screen.queryByText(VALIDATOR_POSTURE_LABEL)).toBeNull();
  });

  it('shows the validation badge styled failed for an invalid verdict, same stand-in posture label', () => {
    render(<StepDetail step={invalidValidateStep()} view="clinical" />);

    const badge = screen.getByText('Invalid');
    expect(badge.className).toContain('validation-failed');
    expect(screen.getByText(VALIDATOR_POSTURE_LABEL)).toBeDefined();
  });

  // Detail-less validate badge: the
  // badge already received the full `validation` reason string (frame.detail)
  // but only ever rendered the bare "Invalid"/"Valid" bit — the WHY was only
  // ever shown via a separate, easy-to-miss `.failure-detail` paragraph
  // elsewhere in the tree. The badge now carries its own reason.
  it('an invalid verdict renders the validation detail text as part of the badge group itself (no longer detail-less)', () => {
    render(<StepDetail step={invalidValidateStep()} view="clinical" />);

    const group = document.querySelector('.validation-badge-group');
    expect(group?.textContent).toContain('invalid: missing required element Claim.type');
    expect(screen.getByText('invalid: missing required element Claim.type').className).toContain(
      'validation-detail',
    );
    // No longer duplicated as a second, separate paragraph.
    expect(document.querySelectorAll('.failure-detail')).toHaveLength(0);
  });

  it('shows httpStatus for an ingress step', () => {
    const step = conformantStory.steps.find((s) => s.kind === 'ingress' && s.httpStatus !== undefined);
    expect(step).toBeDefined();
    render(<StepDetail step={step as Step} view="clinical" />);
    // Disambiguated from the dir-rows "HTTP 200 response" row —
    // this test's own intent is the dedicated `.http-status` paragraph.
    expect(document.querySelector('.http-status')?.textContent).toMatch(/200/);
  });

  it('an open step (no response) shows the pinned open-step sentence instead of a Response payload', () => {
    render(<StepDetail step={openLegStep()} view="clinical" />);
    expect(screen.getByText('No response observed — the flow stopped here.')).toBeDefined();
    expect(OPEN_STEP_NOTE).toBe('No response observed — the flow stopped here.');
  });

  it('a failed step renders failed styling and the failure detail', () => {
    render(<StepDetail step={failedLegStep()} view="clinical" />);
    expect(screen.getByText('connection timed out')).toBeDefined();
    const root = document.querySelector('.detail');
    expect(root?.className).toContain('step-status-failed');
  });

  // CRITICAL — shown-never-faked: a `validate`
  // step never has a `response` by design (inspect.ts makeValidateStep is
  // single-frame); gating OPEN_STEP_NOTE purely on `!step.response` renders
  // the false "flow stopped here" note next to a "Valid" badge.
  it('a successful validate step suppresses the Response section and OPEN_STEP_NOTE entirely, and renders "Resource"/"Search resource" (not "Request")', () => {
    const step = ehrStory.steps.find((s) => s.kind === 'validate' && s.status === 'ok') as Step;
    expect(step).toBeDefined();
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.queryByText(OPEN_STEP_NOTE)).toBeNull();
    expect(screen.queryByText('Response')).toBeNull();
    expect(screen.queryByText('Request')).toBeNull();
    expect(screen.getByText('Resource')).toBeDefined();
    expect(screen.getByLabelText('Search resource')).toBeDefined();
    expect(screen.getByText('Valid')).toBeDefined();
  });

  it('a failed validate step also suppresses the Response section and OPEN_STEP_NOTE, and keeps the "Resource" naming', () => {
    render(<StepDetail step={invalidValidateStep()} view="clinical" />);

    expect(screen.queryByText(OPEN_STEP_NOTE)).toBeNull();
    expect(screen.queryByText('Response')).toBeNull();
    expect(screen.getByText('Resource')).toBeDefined();
    expect(screen.getByText('Invalid')).toBeDefined();
  });

  // CRITICAL — shown-never-faked: a `sor` step is a single-frame LOCAL read;
  // its request.payload holds the RETURNED resource bytes, and it never has a
  // response. Rendering it as a paired exchange would (a) mislabel the
  // returned bytes as "Request", and (b) show OPEN_STEP_NOTE ("flow stopped
  // here") next to a completed read. Both are suppressed; the returned bytes
  // are labeled for what they are + a local-read note replaces the false
  // Response pane.
  it('a sor step with payload labels the returned bytes "Returned resource" (not "Request"), suppresses the Response pane + OPEN_STEP_NOTE, and adds a local-read note', () => {
    render(<StepDetail step={sorStepWithPayload()} view="clinical" />);

    expect(screen.queryByText(OPEN_STEP_NOTE)).toBeNull();
    expect(screen.queryByText('Request')).toBeNull();
    expect(screen.queryByText('Response')).toBeNull();
    expect(screen.getByText('Returned resource')).toBeDefined();
    // the returned bytes ARE rendered (clinical view) — the marker is visible
    expect(document.body.textContent).toContain(SOR_RETURNED_MARKER);
    expect(screen.getByText(SOR_LOCAL_READ_NOTE)).toBeDefined();
    expect(SOR_LOCAL_READ_NOTE).toBe(
      "Read locally from the gateway's configured data source — this step never crosses the Hub.",
    );
  });
});

describe('StepDetail — substrate view', () => {
  it('renders leg facts and the framing sentence, and NEVER renders payload JSON', () => {
    const step = findStep(ehrStory.steps, 'crd-order-select');
    render(<StepDetail step={step} view="substrate" />);

    // the fixture-unique payload string must be entirely absent from the DOM
    expect(screen.queryByText(FIXTURE_PAYLOAD_MARKER)).toBeNull();
    expect(document.body.textContent).not.toContain(FIXTURE_PAYLOAD_MARKER);

    expect(screen.getByText(step.correlationId as string)).toBeDefined();
    expect(screen.getByText('originate')).toBeDefined();
    expect(screen.getByText('payer')).toBeDefined();
    expect(screen.getByText('provider-tpo')).toBeDefined();
    expect(screen.getByText('payer-coverage')).toBeDefined();
    expect(screen.getAllByText(/≈ \d+ KB/).length).toBeGreaterThanOrEqual(1);
    expect(
      screen.getByText('Carried as a sealed envelope through the payload-blind Hub; authority evaluated per leg.'),
    ).toBeDefined();
  });

  it('an open step (no response) shows the open-step sentence too', () => {
    render(<StepDetail step={openLegStep()} view="substrate" />);
    expect(screen.getByText('No response observed — the flow stopped here.')).toBeDefined();
  });

  it('a failed step renders failed styling and the failure detail, still with no payload JSON', () => {
    render(<StepDetail step={failedLegStep()} view="substrate" />);
    expect(screen.getByText('connection timed out')).toBeDefined();
    const root = document.querySelector('.detail');
    expect(root?.className).toContain('step-status-failed');
    expect(document.body.textContent).not.toContain('"claim"');
    expect(document.body.textContent).not.toContain('stub');
  });

  // CRITICAL — shown-never-faked: substrate view
  // must not claim a validate step crossed "the payload-blind Hub" (it never
  // does — it's a LOCAL check) nor render a mostly-empty leg-facts table for
  // a step kind that carries none of those facts. Finding 3 (badge coverage)
  // + Finding 4 (SUBSTRATE_FRAMING double-pinned) are exercised here too.
  it('a successful validate step suppresses leg-facts and SUBSTRATE_FRAMING, shows the validate-specific line and exact (default stand-in) badge label', () => {
    const step = ehrStory.steps.find((s) => s.kind === 'validate' && s.status === 'ok') as Step;
    expect(step).toBeDefined();
    render(<StepDetail step={step} view="substrate" />);

    expect(screen.queryByText(OPEN_STEP_NOTE)).toBeNull();
    expect(screen.queryByText(SUBSTRATE_FRAMING)).toBeNull();
    expect(document.querySelector('.facts')).toBeNull();

    expect(screen.getByText('Valid')).toBeDefined();
    expect(screen.getByText(VALIDATOR_POSTURE_LABEL)).toBeDefined();

    expect(screen.getByText(VALIDATE_SUBSTRATE_NOTE)).toBeDefined();
    expect(VALIDATE_SUBSTRATE_NOTE).toBe(
      "Checked locally against the Kit's validator — this step never crosses the Hub.",
    );
    expect(SUBSTRATE_FRAMING).toBe(
      'Carried as a sealed envelope through the payload-blind Hub; authority evaluated per leg.',
    );
  });

  it('posture "packaged" in the substrate view shows the packaged sentence', () => {
    const step = ehrStory.steps.find((s) => s.kind === 'validate' && s.status === 'ok') as Step;
    render(<StepDetail step={step} view="substrate" posture="packaged" />);
    expect(screen.getByText(PACKAGED_VALIDATOR_POSTURE_LABEL)).toBeDefined();
    expect(screen.queryByText(VALIDATOR_POSTURE_LABEL)).toBeNull();
  });

  // The absent-posture (undefined prop) fallback row — old daemons/races
  // that never carry `validator` on GET /api/status must never be over-read
  // as "packaged" (the honest default).
  it('posture omitted entirely (absent-posture fallback) renders the stand-in sentence, not packaged', () => {
    const step = ehrStory.steps.find((s) => s.kind === 'validate' && s.status === 'ok') as Step;
    render(<StepDetail step={step} view="substrate" />);
    expect(screen.getByText(VALIDATOR_POSTURE_LABEL)).toBeDefined();
    expect(screen.queryByText(PACKAGED_VALIDATOR_POSTURE_LABEL)).toBeNull();
  });

  it('a failed validate step also suppresses leg-facts and SUBSTRATE_FRAMING, still shows the validate-specific line, and carries the detail on the badge (no separate failure-detail paragraph)', () => {
    render(<StepDetail step={invalidValidateStep()} view="substrate" />);

    expect(screen.queryByText(OPEN_STEP_NOTE)).toBeNull();
    expect(screen.queryByText(SUBSTRATE_FRAMING)).toBeNull();
    expect(document.querySelector('.facts')).toBeNull();
    expect(screen.getByText('Invalid')).toBeDefined();
    expect(screen.getByText(VALIDATE_SUBSTRATE_NOTE)).toBeDefined();
    expect(screen.getByText('invalid: missing required element Claim.type').className).toContain(
      'validation-detail',
    );
    expect(document.querySelectorAll('.failure-detail')).toHaveLength(0);
  });

  // CRITICAL — shown-never-faked: a `sor` step never crosses the Hub, so the
  // substrate view must NOT render SUBSTRATE_FRAMING ("sealed envelope
  // through the payload-blind Hub") nor the false OPEN_STEP_NOTE. It shows
  // the honest read facts (op + outcome + returned size) and the local-read
  // note. The returned resource bytes still never reach the DOM as JSON.
  it('a sor step suppresses SUBSTRATE_FRAMING + OPEN_STEP_NOTE, shows honest read facts + local-read note, and never renders the returned JSON', () => {
    render(<StepDetail step={sorStepWithPayload()} view="substrate" />);

    expect(screen.queryByText(SUBSTRATE_FRAMING)).toBeNull();
    expect(screen.queryByText(OPEN_STEP_NOTE)).toBeNull();
    expect(screen.getByText(SOR_LOCAL_READ_NOTE)).toBeDefined();
    // the read op is a fact worth surfacing; the returned bytes are not
    expect(screen.getByText('OpenOrder')).toBeDefined();
    expect(screen.getByText('found')).toBeDefined();
    expect(document.body.textContent).not.toContain(SOR_RETURNED_MARKER);
  });
});

// ---------------------------------------------------------------------------
// TransformCard + RefusalCard fixtures. Provenance payloads mirror
// sdk/provenance.go's BuildTransformProvenance output shape byte-for-byte
// (resourceType/target/recorded/agent/activity/extension, the shn-loss-report
// extension as a single valueString-encoded JSON array) — the same shape
// StepDetail's parseLossReports reads.
// ---------------------------------------------------------------------------

const SHN_LOSS_REPORT_EXT_URL = 'http://smarthealth.network/fhir/StructureDefinition/shn-loss-report';
const CARRY_PATH_MARKER = 'QuestionnaireResponse.item.answer.extension:itemWeight';
const SYNTH_PATH_MARKER = 'Claim.item.extension:itemTraceNumber';

function transformProvenancePayload(reports: unknown[]): Record<string, unknown> {
  return {
    resourceType: 'Provenance',
    target: [{ reference: 'urn:shn:leg:c-t1' }],
    recorded: '2026-08-14T00:00:00Z',
    agent: [{ who: { reference: 'Organization/test-holder' } }],
    activity: {
      coding: [{ system: 'urn:shn:transform:module', code: 'pa.dtr 2.2->2.1', display: '2.2->2.1' }],
      text: 'pa.dtr 2.2->2.1',
    },
    extension: [{ url: SHN_LOSS_REPORT_EXT_URL, valueString: JSON.stringify(reports) }],
  };
}

function bridgedLegStep(overrides: Partial<Step> = {}): Step {
  return {
    id: 't1',
    kind: 'leg',
    legType: 'dtr-questionnaire-fetch',
    status: 'ok',
    request: {
      seq: 1,
      time: '2026-08-14T00:00:00Z',
      kind: 'leg.originated',
      legType: 'dtr-questionnaire-fetch',
      correlationId: 'c-t1',
      counterpart: 'payer',
    },
    response: {
      seq: 2,
      time: '2026-08-14T00:00:01Z',
      kind: 'leg.response',
      legType: 'dtr-questionnaire-fetch',
      correlationId: 'c-t1',
    },
    correlationId: 'c-t1',
    counterpart: 'payer',
    narration: 'transform leg narration',
    route: {
      token: 'pa.dtr@2.1',
      buildLine: '2.2',
      chain: [{ module: 'pa.dtr 2.2->2.1', from: '2.2', to: '2.1', class: 'carry' }],
    },
    transform: {
      seq: 3,
      time: '2026-08-14T00:00:00.5Z',
      kind: 'leg.transformed',
      detail: 'pa.dtr 2.2->2.1',
      payload: transformProvenancePayload([
        {
          module: 'pa.dtr 2.2->2.1',
          source: '2.2',
          target: '2.1',
          carried: [{ path: CARRY_PATH_MARKER, detail: 'itemWeight extension carried; source line 2.2' }],
          synthesized: [{ path: SYNTH_PATH_MARKER, detail: 'deterministically minted default per 2.1' }],
        },
      ]),
    },
    ...overrides,
  };
}

describe('StepDetail — TransformCard', () => {
  it('renders Carried and Synthesized rows from a real Provenance fixture, the chain hops, posture label, and the raw Provenance disclosure', () => {
    render(<StepDetail step={bridgedLegStep()} view="clinical" />);

    expect(screen.getByText('Cross-version transform')).toBeDefined();
    expect(document.querySelector('.chain-hop-line')?.textContent).toBe('2.2 → 2.1');
    expect(screen.getByText('Carry')).toBeDefined(); // STEP_CLASS_META label for the 'carry' hop

    expect(screen.getByText('Carried, not lost')).toBeDefined();
    expect(screen.getByText(CARRY_PATH_MARKER)).toBeDefined();
    expect(document.body.textContent).toContain('itemWeight extension carried; source line 2.2');

    expect(screen.getByText('Synthesized')).toBeDefined();
    expect(screen.getByText(SYNTH_PATH_MARKER)).toBeDefined();

    expect(screen.getByText(VALIDATOR_POSTURE_LABEL)).toBeDefined();

    expect(screen.getByText('Raw Provenance')).toBeDefined();
    // The <details> disclosure holds the actual Provenance JSON — proven via
    // a top-level field visible at JsonView's default collapse depth
    // (`recorded`, a leaf at depth 1; `target` is a depth-1 container and
    // collapses by default, so its own nested value isn't a safe marker here).
    expect(document.querySelector('.raw-provenance')?.textContent).toContain('2026-08-14T00:00:00Z');
  });

  it('renders in the substrate view too (leg branches = clinical generic + substrate generic, per the brief)', () => {
    render(<StepDetail step={bridgedLegStep()} view="substrate" />);
    expect(screen.getByText('Cross-version transform')).toBeDefined();
    expect(screen.getByText(CARRY_PATH_MARKER)).toBeDefined();
  });

  it('passes the posture prop through — packaged renders the packaged sentence, not the stand-in one', () => {
    render(<StepDetail step={bridgedLegStep()} view="clinical" posture="packaged" />);
    expect(screen.getByText(PACKAGED_VALIDATOR_POSTURE_LABEL)).toBeDefined();
    expect(screen.queryByText(VALIDATOR_POSTURE_LABEL)).toBeNull();
  });

  it('register-aware narration: overview (the default) and technical render distinct, register-correct copy', () => {
    const { rerender } = render(<StepDetail step={bridgedLegStep()} view="clinical" />);
    expect(screen.getByText(TRANSFORM_CARD_NARRATION.overview)).toBeDefined();
    expect(screen.queryByText(TRANSFORM_CARD_NARRATION.technical)).toBeNull();

    rerender(<StepDetail step={bridgedLegStep()} view="clinical" register="technical" />);
    expect(screen.getByText(TRANSFORM_CARD_NARRATION.technical)).toBeDefined();
    expect(screen.queryByText(TRANSFORM_CARD_NARRATION.overview)).toBeNull();
  });

  it('a step with no transform frame renders no TransformCard at all', () => {
    render(<StepDetail step={okLegStep()} view="clinical" />);
    expect(screen.queryByText('Cross-version transform')).toBeNull();
  });

  // Empty-content note selection (spec: chosen by the CHAIN's class shape,
  // never by "did this run happen to carry nothing" alone).
  it('empty content + a NON-full-only chain (e.g. carry) shows TRANSFORM_EMPTY_CONTENT_NOTE', () => {
    const step = bridgedLegStep({
      transform: {
        seq: 3,
        time: '2026-08-14T00:00:00.5Z',
        kind: 'leg.transformed',
        detail: 'pa.dtr 2.2->2.1',
        payload: transformProvenancePayload([{ module: 'pa.dtr 2.2->2.1', source: '2.2', target: '2.1' }]),
      },
    });
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.getByText(TRANSFORM_EMPTY_CONTENT_NOTE)).toBeDefined();
    expect(screen.queryByText(IDENTITY_CHAIN_NOTE)).toBeNull();
    expect(TRANSFORM_EMPTY_CONTENT_NOTE).toBe('no content differences on this leg — transport envelope');
  });

  it('empty content + every hop class="full" shows IDENTITY_CHAIN_NOTE instead', () => {
    const step = bridgedLegStep({
      route: {
        token: 'pa.crd@2.1',
        buildLine: '2.0',
        chain: [{ module: 'pa.crd 2.0->2.1', from: '2.0', to: '2.1', class: 'full' }],
      },
      transform: {
        seq: 3,
        time: '2026-08-14T00:00:00.5Z',
        kind: 'leg.transformed',
        detail: 'pa.crd 2.0->2.1',
        payload: transformProvenancePayload([{ module: 'pa.crd 2.0->2.1', source: '2.0', target: '2.1' }]),
      },
    });
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.getByText(IDENTITY_CHAIN_NOTE)).toBeDefined();
    expect(screen.queryByText(TRANSFORM_EMPTY_CONTENT_NOTE)).toBeNull();
    expect(IDENTITY_CHAIN_NOTE).toBe('identity chain: bytes unchanged, proven');
  });

  it('an unresolved/empty chain (no route.chain) never claims an identity chain — falls to TRANSFORM_EMPTY_CONTENT_NOTE', () => {
    const step = bridgedLegStep({
      route: { token: 'pa.dtr@2.1', buildLine: '2.1' }, // arm-1/2: no chain at all
      transform: {
        seq: 3,
        time: '2026-08-14T00:00:00.5Z',
        kind: 'leg.transformed',
        detail: 'pa.dtr@2.1',
        payload: transformProvenancePayload([]),
      },
    });
    render(<StepDetail step={step} view="clinical" />);
    expect(screen.getByText(TRANSFORM_EMPTY_CONTENT_NOTE)).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// parseLossReports' defensive branches (IMPORTANT):
// each malformed-payload shape must degrade to the honest empty-content
// note, never crash the render.
// ---------------------------------------------------------------------------

describe('StepDetail — TransformCard degrades honestly on a malformed Provenance payload (never crashes)', () => {
  it('extension array present but no entry has the shn-loss-report URL', () => {
    const step = bridgedLegStep({
      transform: {
        seq: 3,
        time: '2026-08-14T00:00:00.5Z',
        kind: 'leg.transformed',
        detail: 'pa.dtr 2.2->2.1',
        payload: {
          resourceType: 'Provenance',
          extension: [{ url: 'http://example.org/some-other-extension', valueString: '[]' }],
        },
      },
    });
    expect(() => render(<StepDetail step={step} view="clinical" />)).not.toThrow();
    // route.chain is the default carry-class hop from bridgedLegStep() —
    // not all-full, so the weaker empty note is the honest one here.
    expect(screen.getByText(TRANSFORM_EMPTY_CONTENT_NOTE)).toBeDefined();
    expect(screen.queryByText(CARRY_PATH_MARKER)).toBeNull();
  });

  it('the matching extension\'s valueString is not valid JSON', () => {
    const step = bridgedLegStep({
      transform: {
        seq: 3,
        time: '2026-08-14T00:00:00.5Z',
        kind: 'leg.transformed',
        detail: 'pa.dtr 2.2->2.1',
        payload: {
          resourceType: 'Provenance',
          extension: [{ url: SHN_LOSS_REPORT_EXT_URL, valueString: 'not json at all {{{' }],
        },
      },
    });
    expect(() => render(<StepDetail step={step} view="clinical" />)).not.toThrow();
    expect(screen.getByText(TRANSFORM_EMPTY_CONTENT_NOTE)).toBeDefined();
  });

  it('the valueString parses to valid JSON that is NOT an array', () => {
    const step = bridgedLegStep({
      transform: {
        seq: 3,
        time: '2026-08-14T00:00:00.5Z',
        kind: 'leg.transformed',
        detail: 'pa.dtr 2.2->2.1',
        payload: {
          resourceType: 'Provenance',
          extension: [{ url: SHN_LOSS_REPORT_EXT_URL, valueString: JSON.stringify({ not: 'an array' }) }],
        },
      },
    });
    expect(() => render(<StepDetail step={step} view="clinical" />)).not.toThrow();
    expect(screen.getByText(TRANSFORM_EMPTY_CONTENT_NOTE)).toBeDefined();
  });

  // A chain of all-full hops proves the SAME degrade-to-honest-note path
  // still picks the STRONGER note correctly off a malformed payload — the
  // note choice is driven by the chain's class shape, never by whether the
  // parse happened to succeed.
  it('a malformed payload on an all-full chain still shows IDENTITY_CHAIN_NOTE (the note is chain-driven, not parse-driven)', () => {
    const step = bridgedLegStep({
      route: {
        token: 'pa.crd@2.1',
        buildLine: '2.0',
        chain: [{ module: 'pa.crd 2.0->2.1', from: '2.0', to: '2.1', class: 'full' }],
      },
      transform: {
        seq: 3,
        time: '2026-08-14T00:00:00.5Z',
        kind: 'leg.transformed',
        detail: 'pa.crd 2.0->2.1',
        payload: { resourceType: 'Provenance' }, // no extension array at all
      },
    });
    expect(() => render(<StepDetail step={step} view="clinical" />)).not.toThrow();
    expect(screen.getByText(IDENTITY_CHAIN_NOTE)).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// RefusalCard — three species, never conflated: transform refusal, route
// refusal, and carry-integrity refusal.
// ---------------------------------------------------------------------------

function routeRefusedStep(): Step {
  return {
    id: 'ref-route',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'failed',
    response: {
      seq: 1,
      time: '2026-08-14T00:00:00Z',
      kind: 'leg.refused',
      legType: 'pas-claim',
      correlationId: 'c-ref-route',
      counterpart: 'acme-payer',
      detail:
        'no shared contract line for pa.pas (leg pas-claim): this gateway speaks pa.pas@2.0; recipient "acme-payer" declares pa.pas@2.3 — no bridge available (no transform chain bridges to line 2.3)',
    },
    correlationId: 'c-ref-route',
    counterpart: 'acme-payer',
    narration: 'The Smart Gateway found no contract line it shares with the hosted counterparty for this leg, and refused before sending anything.',
    refusal: {
      own: ['pa.pas@2.0'],
      peer: ['pa.pas@2.3'],
      bridgeIssue: 'no bridge available (no transform chain bridges to line 2.3)',
    },
  };
}

// The Detail text's MissingElements tail deliberately mirrors
// bridgingassets/README.md's refusal-input-2.1 exhibit: ONE element whose
// own parenthesized detail contains a comma — proving the paren-aware split
// (not a naive split(',')) keeps it as one element.
const TRANSFORM_REFUSAL_ELEMENT =
  'QuestionnaireResponse.extension:qr-coverage (ambiguous: 2 Coverage-referencing qr-context entries, multi-coverage source)';

function transformRefusedStep(): Step {
  return {
    id: 'ref-transform',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'failed',
    response: {
      seq: 1,
      time: '2026-08-14T00:00:00Z',
      kind: 'leg.failed',
      legType: 'pas-claim',
      correlationId: 'c-ref-transform',
      counterpart: 'payer',
      detail: `shn: semantic-change refusal: pa.dtr 2.1->2.2 (up direction): no honest byte-level source for ${TRANSFORM_REFUSAL_ELEMENT}`,
    },
    correlationId: 'c-ref-transform',
    counterpart: 'payer',
    narration:
      'The Smart Gateway could not honestly bridge this leg to the hosted counterparty’s contract line, and refused before sending anything.',
    refusal: {
      chain: [
        { module: 'pa.dtr 2.0->2.1', from: '2.0', to: '2.1', class: 'full' },
        { module: 'pa.dtr 2.1->2.2', from: '2.1', to: '2.2', class: 'gated' },
      ],
    },
  };
}

describe('StepDetail — RefusalCard, three species', () => {
  it('route refusal (own/peer/bridgeIssue): own-tokens vs peer-tokens chips, bridgeIssue highlighted, exact Detail text — and NO transform-species markup', () => {
    const step = routeRefusedStep();
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.getByText('No shared contract line — refused before sending anything')).toBeDefined();
    expect(document.querySelector('.refusal-card-route')).not.toBeNull();
    expect(document.querySelector('.refusal-card-transform')).toBeNull();

    const ownChip = document.querySelector('.token-chip.own-token');
    expect(ownChip?.textContent).toBe('pa.pas@2.0');
    const peerChip = document.querySelector('.token-chip.peer-token');
    expect(peerChip?.textContent).toBe('pa.pas@2.3');

    expect(screen.getByText('no bridge available (no transform chain bridges to line 2.3)')).toBeDefined();
    expect(document.body.textContent).toContain(step.response?.detail);

    // never conflated: no chain hops, no zero-bytes note (that's the OTHER species)
    expect(document.querySelector('.chain-hops')).toBeNull();
    expect(screen.queryByText(ZERO_BYTES_NOTE)).toBeNull();

    // directionRows never fabricates an outbound request for a refused leg
    expect(document.querySelectorAll('.dir-row')).toHaveLength(0);
    expect(directionRows(step)).toEqual([]);
  });

  it('transform refusal (Route.Chain): the attempted chain, the parsed elements list (paren-aware split keeps ONE element with an internal comma), and the pinned ZERO_BYTES_NOTE — and NO route-species markup', () => {
    const step = transformRefusedStep();
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.getByText('Refused mid-bridge — no honest source for the target line')).toBeDefined();
    expect(document.querySelector('.refusal-card-transform')).not.toBeNull();
    expect(document.querySelector('.refusal-card-route')).toBeNull();

    // both chain hops render, with their per-hop class label
    expect(screen.getByText('2.0 → 2.1')).toBeDefined();
    expect(screen.getByText('2.1 → 2.2')).toBeDefined();
    expect(screen.getByText('Full')).toBeDefined();
    expect(screen.getByText('Gated')).toBeDefined();

    // the paren-aware parse: exactly ONE <li>, the full element text intact
    // (a naive split(',') would have produced two fragments here)
    const items = document.querySelectorAll('.refusal-elements li');
    expect(items).toHaveLength(1);
    expect(items[0].textContent).toBe(TRANSFORM_REFUSAL_ELEMENT);

    expect(screen.getByText(ZERO_BYTES_NOTE)).toBeDefined();
    expect(ZERO_BYTES_NOTE).toBe('refused before sending — zero bytes crossed the network');

    // never conflated: no own/peer token chips, no bridgeIssue line
    expect(document.querySelectorAll('.token-chip')).toHaveLength(0);
    expect(document.querySelector('.bridge-issue')).toBeNull();
  });

  // The SAME event as
  // inspect.test.ts's "a route-carrying leg.failed with NO open step becomes
  // a self-contained failed step" (identical KitEvent shape, built through
  // the real buildRunStory pipeline rather than a hand-authored Step) must
  // discriminate identically through BOTH inspect.ts's narration keying (the
  // Detail marker only, isCarryRefusalDetail) and RefusalCard's species
  // keying (`hasChain` first, :547-549) — for this realistic chain-carrying
  // wire shape they agree. They differ BY DESIGN for a hypothetical
  // chainless transform-refusal Detail (a recorded follow-up); this test
  // does not touch that gap, it only proves the realistic case lines up.
  it('the SAME event as inspect.test.ts\'s no-open-step transform refusal discriminates identically through inspect.ts narration and RefusalCard species', () => {
    const events: KitEvent[] = [
      { runId: 'run-t', time: '2026-07-03T00:00:00Z', seq: 1, type: 'run.started' },
      {
        runId: 'run-t',
        time: '2026-07-03T00:00:00Z',
        seq: 2,
        type: 'observer',
        observer: {
          seq: 2,
          time: '2026-07-03T00:00:00.000000-04:00',
          kind: 'leg.failed',
          legType: 'pas-claim',
          correlationId: 'c-15',
          counterpart: 'payer',
          detail: 'transform chain refused: no honest byte-level source',
          route: {
            token: 'pa.pas@2.2',
            buildLine: '2.0',
            chain: [{ module: 'pa.pas 2.0->2.2', from: '2.0', to: '2.2', class: 'gated' }],
          },
        },
      },
      { runId: 'run-t', time: '2026-07-03T00:00:00Z', seq: 3, type: 'run.failed', detail: 'leg failed' },
    ] as unknown as KitEvent[];

    const story = buildRunStory('run-t', events);
    expect(story.steps).toHaveLength(1);
    const step = story.steps[0];

    // inspect.ts's discriminator (Detail marker only): the transform-refused
    // narration, not the carry-refused or fallback text.
    expect(step.narration).toBe(
      'The Smart Gateway could not honestly bridge this leg to the hosted counterparty’s contract line, and refused before sending anything.'
    );

    render(<StepDetail step={step} view="clinical" />);

    // RefusalCard's discriminator (hasChain first): the transform species
    // card, never the route-refusal or carry species card, for this same
    // step object.
    expect(screen.getByText('Refused mid-bridge — no honest source for the target line')).toBeDefined();
    expect(document.querySelector('.refusal-card-transform')).not.toBeNull();
    expect(document.querySelector('.refusal-card-route')).toBeNull();
    expect(document.querySelector('.refusal-card-carry')).toBeNull();
    expect(screen.getByText(ZERO_BYTES_NOTE)).toBeDefined();

    // Both discriminators rendered together, in the same DOM, off the same
    // step — the narration line sits directly above RefusalCard's headline.
    expect(document.querySelector('.narr')?.textContent).toBe(step.narration);
  });

  it('carry refusal (third species): its own headline + pinned note + parsed missing path — NEVER the transform species headline, NEVER a raw Go error as the only story', () => {
    const step = transformRefusedStep();
    step.response = {
      ...step.response!,
      legType: 'pas-claim-update',
      detail:
        'engine: pended carry not intact at resume (pin pa.pas@2.1): engine: verifyCarryPresent: declared carry "Claim.extension[url=https://smarthealth.network/fhir/StructureDefinition/shn-carried-content]" not found in the payload about to be restored — the payload no longer bears content its own loss record declares carried',
    };
    render(<StepDetail step={step} view="clinical" />);

    expect(screen.getByText('Refused at resume — previously carried content is missing')).toBeDefined();
    expect(document.querySelector('.refusal-card-carry')).not.toBeNull();
    // the false attribution this species exists to prevent:
    expect(screen.queryByText('Refused mid-bridge — no honest source for the target line')).toBeNull();
    expect(document.querySelector('.refusal-card-transform')).toBeNull();

    expect(screen.getByText(CARRY_REFUSAL_NOTE)).toBeDefined();
    expect(CARRY_REFUSAL_NOTE).toBe(
      'This resumed request no longer carries content its own record says it must, so the Smart Gateway refused rather than send a request that silently lost it.'
    );

    // the chain still renders (it IS the routed story), and the missing
    // declared path is parsed out rather than dumping the raw error alone
    expect(screen.getByText('2.0 → 2.1')).toBeDefined();
    const items = document.querySelectorAll('.refusal-elements li');
    expect(items).toHaveLength(1);
    expect(items[0].textContent).toBe(
      'Claim.extension[url=https://smarthealth.network/fhir/StructureDefinition/shn-carried-content]'
    );

    expect(screen.getByText(ZERO_BYTES_NOTE)).toBeDefined();
  });

  it('a carry refusal whose Detail path does not parse falls back to the whole Detail text under the carry headline', () => {
    const step = transformRefusedStep();
    step.response = {
      ...step.response!,
      detail: 'engine: pended carry not intact at resume (pin pa.pas@2.1): malformed tail',
    };
    render(<StepDetail step={step} view="clinical" />);

    expect(document.querySelector('.refusal-card-carry')).not.toBeNull();
    expect(document.querySelector('.refusal-elements')).toBeNull();
    expect(screen.getByText('engine: pended carry not intact at resume (pin pa.pas@2.1): malformed tail')).toBeDefined();
  });

  it('a transform refusal whose Detail has no parseable marker falls back to showing the whole Detail text', () => {
    const step = transformRefusedStep();
    step.response = { ...step.response!, detail: 'an unrelated transform failure with no marker text' };
    render(<StepDetail step={step} view="clinical" />);

    expect(document.querySelector('.refusal-elements')).toBeNull();
    expect(screen.getByText('an unrelated transform failure with no marker text')).toBeDefined();
    expect(screen.getByText(ZERO_BYTES_NOTE)).toBeDefined();
  });

  it('both species render in the substrate view too, suppressing SUBSTRATE_FRAMING (nothing was carried through the Hub — nothing was sent)', () => {
    render(<StepDetail step={routeRefusedStep()} view="substrate" />);
    expect(screen.queryByText(SUBSTRATE_FRAMING)).toBeNull();
    expect(screen.queryByText(OPEN_STEP_NOTE)).toBeNull();
    expect(document.querySelector('.refusal-card-route')).not.toBeNull();

    render(<StepDetail step={transformRefusedStep()} view="substrate" />);
    expect(document.querySelectorAll('.refusal-card-transform').length).toBeGreaterThan(0);
  });

  it('a step with no refusal renders no RefusalCard at all', () => {
    render(<StepDetail step={okLegStep()} view="clinical" />);
    expect(document.querySelector('.refusal-card')).toBeNull();
  });

  // The relayed non-2xx status carried on the wire and parsed out of the
  // observer event must actually RENDER — a payer's 422 previously closed the
  // leg green with "the payer's decision came back" and no visible rejection.
  // Display-only by design: step status logic is unchanged.
  it('a leg whose response carries a relayed non-2xx status renders the relayed-status line (clinical) and the Relayed status fact (substrate)', () => {
    const step = okLegStep();
    step.response = { ...step.response!, status: 422 };
    render(<StepDetail step={step} view="clinical" />);
    expect(screen.getByText(relayedStatusLine(422))).toBeDefined();
    expect(relayedStatusLine(422)).toBe(
      'The counterparty’s application answered HTTP 422 — relayed unchanged as this leg’s response.'
    );

    render(<StepDetail step={step} view="substrate" />);
    expect(screen.getByText('Relayed status')).toBeDefined();
    expect(screen.getByText('422')).toBeDefined();
  });

  it('a leg with an ordinary 2xx response (no relayed status on the frame) renders no relayed-status line', () => {
    render(<StepDetail step={okLegStep()} view="clinical" />);
    expect(document.querySelector('.relayed-status')).toBeNull();
  });

  // Fix round 1, minor 2: a Route present on a refusal but shaped like
  // NEITHER species (no own/peer/bridgeIssue, no chain — refusalRouteInfo's
  // own doc calls this nil-through in practice, never emitted by the real
  // gateway) still renders inside the same `.refusal-card` shell as the two
  // named species, with only the raw Detail text (if any) — never a bare,
  // unstyled paragraph, and never a crash.
  it('a refusal shaped like neither species renders inside .refusal-card with only the raw Detail (defensive fallback)', () => {
    const step: Step = {
      id: 'ref-neither',
      kind: 'leg',
      legType: 'pas-claim',
      status: 'failed',
      response: {
        seq: 1,
        time: '2026-08-14T00:00:00Z',
        kind: 'leg.failed',
        legType: 'pas-claim',
        correlationId: 'c-ref-neither',
        counterpart: 'payer',
        detail: 'an unclassifiable refusal',
      },
      correlationId: 'c-ref-neither',
      counterpart: 'payer',
      narration: 'refusal narration',
      refusal: {}, // no own/peer/bridgeIssue/chain — neither species
    };
    expect(() => render(<StepDetail step={step} view="clinical" />)).not.toThrow();

    const card = document.querySelector('.refusal-card');
    expect(card).not.toBeNull();
    expect(card?.className).toContain('refusal-card-unclassified');
    expect(document.querySelector('.refusal-card-transform')).toBeNull();
    expect(document.querySelector('.refusal-card-route')).toBeNull();
    expect(card?.textContent).toBe('an unclassifiable refusal');
  });
});

describe('StepDetail — leg.downgrade annotation', () => {
  it('a leg step carrying a downgrade Detail shows the pinned partner-copy note, never the raw engine prose', () => {
    const step = { ...okLegStep(), downgrade: 'recipient advertises frame v1 but answered bare; processing as legacy (stale-feed downgrade)' };
    render(<StepDetail step={step} view="clinical" />);
    expect(LEG_DOWNGRADE_NOTE).toBe(
      'The counterparty announced a newer envelope format but answered in the older one; the Smart Gateway processed the answer in the older format.',
    );
    expect(screen.getByText(LEG_DOWNGRADE_NOTE)).toBeDefined();
    expect(screen.queryByText(step.downgrade)).toBeNull();
  });

  it('a leg step with no downgrade shows no annotation', () => {
    render(<StepDetail step={okLegStep()} view="clinical" />);
    expect(document.querySelector('.leg-downgrade-note')).toBeNull();
  });
});
