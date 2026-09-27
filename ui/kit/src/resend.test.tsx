// resend.test.tsx — an amendment the payer answered 409 (Conflict)
// and that was sent once more: the Inspector links the two attempts, whether
// the Smart Gateway re-sent an amendment it built (its leg.resent observer
// event) or the provider's Da Vinci client re-sent one through the ingress (the
// Kit runner's run.resent event; the gateway only relays those). Plus the
// conformance finding's verdict: a valid verdict is not narrated as an issue.
import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  buildRunStory,
  conformanceUnavailableNarration,
  conformanceUnknownDecisionNarration,
  conformanceValidNarration,
  resendNote,
} from './inspect';
import { StepDetail } from './StepDetail';
import { FlowMap } from './FlowMap';
import type { KitEvent } from './types';

const RUN = 'run-r';

function obs(seq: number, observer: Record<string, unknown>): KitEvent {
  return { seq, time: '2026-09-27T00:00:00Z', type: 'observer', runId: RUN, observer: { time: '2026-09-27T00:00:00Z', ...observer } };
}

function leg(seq: number, kind: 'leg.originated' | 'leg.response', corr: string, status?: number): KitEvent {
  return obs(seq, {
    kind,
    direction: 'originate',
    legType: 'pas-claim-update',
    op: kind === 'leg.originated' ? 'pas-update-submit' : 'pas-response',
    correlationId: corr,
    counterpart: 'payer',
    ...(status !== undefined ? { status } : {}),
  });
}

function legResent(seq: number, corr: string, detail: string): KitEvent {
  return obs(seq, { kind: 'leg.resent', direction: 'originate', legType: 'pas-claim-update', correlationId: corr, counterpart: 'payer', status: 409, detail });
}

function runResent(seq: number, detail: string, runId = RUN): KitEvent {
  return { seq, time: '2026-09-27T00:00:00Z', type: 'run.resent', runId, lane: 'conformant', uc: 'uc04', detail };
}

function ingress(seq: number, kind: 'ingress.received' | 'ingress.responded', detail?: string): KitEvent {
  return obs(seq, { kind, direction: 'ingress', legType: 'pas-ingress', ...(detail !== undefined ? { detail } : {}) });
}

// The Plain EHR lane: the gateway built the amendment, the payer answered its
// first attempt 409, and the gateway sent it once more (gateway v0.56.0).
const gatewayResend: KitEvent[] = [
  { seq: 1, time: '2026-09-27T00:00:00Z', type: 'run.started', runId: RUN, lane: 'ehr', uc: 'uc04' },
  leg(2, 'leg.originated', 'corr-1'),
  leg(3, 'leg.response', 'corr-1', 409),
  legResent(4, 'corr-2', '{"refusedCorrelationId":"corr-1","status":409}'),
  leg(5, 'leg.originated', 'corr-2'),
  leg(6, 'leg.response', 'corr-2'),
];

// The Da Vinci lane: the Kit runner, as the requester, sent the amendment once
// more. Its run.resent reaches the bus on a different path from the gateway's
// relayed frames, so it lands last here: the link must not depend on order.
const requesterResend: KitEvent[] = [
  { seq: 1, time: '2026-09-27T00:00:00Z', type: 'run.started', runId: RUN, lane: 'conformant', uc: 'uc04' },
  ingress(2, 'ingress.received'),
  leg(3, 'leg.originated', 'kit-uc04-amend-aa'),
  leg(4, 'leg.response', 'kit-uc04-amend-aa', 409),
  ingress(5, 'ingress.responded', '409'),
  ingress(6, 'ingress.received'),
  leg(7, 'leg.originated', 'kit-uc04-amend-bb'),
  leg(8, 'leg.response', 'kit-uc04-amend-bb'),
  ingress(9, 'ingress.responded', '200'),
  runResent(10, '{"correlationId":"kit-uc04-amend-bb","refusedCorrelationId":"kit-uc04-amend-aa","status":409}'),
];

function legSteps(events: KitEvent[]) {
  return buildRunStory(RUN, events).steps.filter((s) => s.kind === 'leg');
}

describe('an amendment sent once more after the payer’s 409', () => {
  it('links the gateway’s own re-send (leg.resent) to the refused attempt, both ways, without a step of its own', () => {
    const story = buildRunStory(RUN, gatewayResend);
    expect(story.steps).toHaveLength(2);
    const [first, second] = story.steps;
    expect(first.correlationId).toBe('corr-1');
    expect(second.correlationId).toBe('corr-2');
    expect(second.resendOf).toEqual({ by: 'gateway', correlationId: 'corr-1', stepId: first.id });
    expect(first.resentAs).toEqual({ by: 'gateway', correlationId: 'corr-2', stepId: second.id });
    expect(resendNote(second)).toBe(
      'The payer answered the first attempt 409 (Conflict), so the Smart Gateway, which built this amendment, sent it once more under a new correlation id. First attempt: corr-1.',
    );
    expect(resendNote(first)).toBe(
      'The payer answered this amendment 409 (Conflict). The Smart Gateway, which built this amendment, sent it once more under a new correlation id. Second attempt: corr-2.',
    );
  });

  it('links the requester’s re-send (run.resent) the same way, whatever order the events land in', () => {
    const [first, second] = legSteps(requesterResend);
    expect(second.resendOf).toEqual({ by: 'requester', correlationId: 'kit-uc04-amend-aa', stepId: first.id });
    expect(first.resentAs).toEqual({ by: 'requester', correlationId: 'kit-uc04-amend-bb', stepId: second.id });
    expect(resendNote(second)).toBe(
      'The payer answered the first attempt 409 (Conflict), so the provider’s Da Vinci client sent this amendment once more under a new correlation id, and the Smart Gateway relayed it as sent. First attempt: kit-uc04-amend-aa.',
    );
    expect(resendNote(first)).toBe(
      'The payer answered this amendment 409 (Conflict). The Smart Gateway relayed that answer to the provider’s Da Vinci client, which sent the amendment once more under a new correlation id. Second attempt: kit-uc04-amend-bb.',
    );
    // Same result with the run.resent first.
    const reordered = [requesterResend[0], requesterResend[9], ...requesterResend.slice(1, 9)];
    const [f2, s2] = legSteps(reordered);
    expect(s2.resendOf?.stepId).toBe(f2.id);
    expect(f2.resentAs?.stepId).toBe(s2.id);
  });

  it('links nothing from a re-send that does not name its refused attempt, names itself, or belongs to another run', () => {
    for (const events of [
      [...gatewayResend.slice(0, 3), legResent(4, 'corr-2', '{"status":409}'), ...gatewayResend.slice(4)],
      [...gatewayResend.slice(0, 3), legResent(4, 'corr-2', 'not-json'), ...gatewayResend.slice(4)],
      [...gatewayResend.slice(0, 3), legResent(4, 'corr-1', '{"refusedCorrelationId":"corr-1","status":409}'), ...gatewayResend.slice(4)],
      [...requesterResend.slice(0, 9), runResent(10, '{"refusedCorrelationId":"kit-uc04-amend-aa","status":409}')],
      [...requesterResend.slice(0, 9), runResent(10, '{"correlationId":"kit-uc04-amend-bb","refusedCorrelationId":"kit-uc04-amend-aa","status":409}', 'run-other')],
    ]) {
      for (const step of buildRunStory(RUN, events).steps) {
        expect(step.resendOf).toBeUndefined();
        expect(step.resentAs).toBeUndefined();
        expect(resendNote(step)).toBeUndefined();
      }
    }
  });

  it('links only the two attempts, never another leg of the run', () => {
    const events = [...gatewayResend, leg(7, 'leg.originated', 'corr-3'), leg(8, 'leg.response', 'corr-3')];
    const other = legSteps(events).find((s) => s.correlationId === 'corr-3');
    expect(other?.resendOf).toBeUndefined();
    expect(other?.resentAs).toBeUndefined();
  });

  it('shows the link in the step detail, and its button selects the other attempt', async () => {
    const [first, second] = legSteps(requesterResend);
    const onSelectStep = vi.fn();
    render(<StepDetail step={second} view="clinical" onSelectStep={onSelectStep} />);
    expect(screen.getByText(resendNote(second) as string)).toBeDefined();
    await userEvent.click(screen.getByRole('button', { name: 'Show the first attempt' }));
    expect(onSelectStep).toHaveBeenCalledWith(first.id);
  });

  it('shows the link on the refused attempt in the network view too', async () => {
    const [first, second] = legSteps(gatewayResend);
    const onSelectStep = vi.fn();
    render(<StepDetail step={first} view="substrate" onSelectStep={onSelectStep} />);
    expect(screen.getByText(resendNote(first) as string)).toBeDefined();
    await userEvent.click(screen.getByRole('button', { name: 'Show the second attempt' }));
    expect(onSelectStep).toHaveBeenCalledWith(second.id);
  });

  it('tags both attempts on the steps rail', () => {
    const story = buildRunStory(RUN, requesterResend);
    const [first, second] = story.steps.filter((s) => s.kind === 'leg');
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);
    const row = (id: string) => document.querySelector(`[data-step-id="${id}"]`);
    expect(row(first.id)?.querySelector('.step-resend-tag')?.textContent).toBe('409, sent again');
    expect(row(second.id)?.querySelector('.step-resend-tag')?.textContent).toBe('second attempt');
    expect(document.querySelectorAll('.step-resend-tag')).toHaveLength(2);
  });

  it('shows no link on a step that was not re-sent', () => {
    const steps = legSteps([gatewayResend[0], leg(2, 'leg.originated', 'corr-9'), leg(3, 'leg.response', 'corr-9')]);
    render(<StepDetail step={steps[0]} view="clinical" onSelectStep={() => {}} />);
    expect(document.querySelector('.resend-note')).toBeNull();
  });

  it('the partner-facing copy never says "substrate"', () => {
    for (const events of [gatewayResend, requesterResend]) {
      for (const step of legSteps(events)) {
        expect(resendNote(step)).not.toMatch(/substrate/i);
      }
    }
  });
});

describe('a conformance finding’s verdict', () => {
  function finding(overrides: Record<string, unknown>): KitEvent {
    return obs(1, {
      kind: 'conformance.observed',
      direction: 'validate',
      legType: 'pas-claim',
      correlationId: 'c-1',
      detail: JSON.stringify({ kind: 'fhir-ingress', legType: 'pas-claim', level: 'observe', decision: 'relayed', ...overrides }),
    });
  }
  const stepFor = (overrides: Record<string, unknown>) => buildRunStory(RUN, [finding(overrides)]).steps[0];

  it('a valid verdict is narrated as valid, never as an issue', () => {
    const step = stepFor({ verdict: 'valid', line: '2.1', declaredLine: '2.0', lines: [{ line: '2.0', verdict: 'structural' }, { line: '2.1', verdict: 'valid' }] });
    expect(step.verdict).toBe('valid');
    expect(step.status).toBe('ok');
    expect(step.narration).toBe(conformanceValidNarration);
    expect(step.narration).not.toMatch(/issue/);
  });

  it('a defect verdict (absent, structural or deeper) is still narrated as an issue', () => {
    for (const verdict of [undefined, 'structural', 'deeper']) {
      const relayed = stepFor({ verdict });
      expect(relayed.narration).toBe('The Smart Gateway found a conformance issue with this message and relayed it as sent, recording the finding.');
      const refused = stepFor({ verdict, decision: 'refused' });
      expect(refused.status).toBe('failed');
      expect(refused.narration).toBe('The Smart Gateway found a conformance issue with this message and refused it.');
    }
  });

  it('an unavailable verdict says the check did not run, never that it found an issue', () => {
    const step = stepFor({ verdict: 'unavailable' });
    expect(step.narration).toBe(conformanceUnavailableNarration);
    expect(step.narration).not.toMatch(/issue/);
  });

  it('a valid verdict with a refused decision is a record this UI cannot square, and is not narrated as either', () => {
    const step = stepFor({ verdict: 'valid', decision: 'refused' });
    expect(step.narration).toBe(conformanceUnknownDecisionNarration);
  });

  it('shows the verdict among the finding’s facts', () => {
    render(<StepDetail step={stepFor({ verdict: 'valid' })} view="substrate" />);
    expect(screen.getByText('Verdict')).toBeDefined();
    expect(screen.getByText('valid')).toBeDefined();
  });
});
