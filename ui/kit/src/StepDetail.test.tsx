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
    expect(screen.getByText(/200/)).toBeDefined();
  });

  it('an open step (no response) shows the pinned open-step sentence instead of a Response payload', () => {
    render(<StepDetail step={openLegStep()} view="clinical" />);
    expect(screen.getByText('No response observed — the flow stopped here.')).toBeDefined();
    expect(OPEN_STEP_NOTE).toBe('No response observed — the flow stopped here.');
  });

  it('a failed step renders failed styling and the failure detail', () => {
    render(<StepDetail step={failedLegStep()} view="clinical" />);
    expect(screen.getByText('connection timed out')).toBeDefined();
    const root = document.querySelector('.step-detail');
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
    const root = document.querySelector('.step-detail');
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
    expect(document.querySelector('.leg-facts')).toBeNull();

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
    expect(document.querySelector('.leg-facts')).toBeNull();
    expect(screen.getByText('Invalid')).toBeDefined();
    expect(screen.getByText(VALIDATE_SUBSTRATE_NOTE)).toBeDefined();
    expect(screen.getByText('invalid: missing required element Claim.type').className).toContain(
      'validation-detail',
    );
    expect(document.querySelectorAll('.failure-detail')).toHaveLength(0);
  });
});
