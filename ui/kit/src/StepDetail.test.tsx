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
