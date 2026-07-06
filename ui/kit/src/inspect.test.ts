import { describe, it, expect } from 'vitest';
import { buildRunStory, parseObserver } from './inspect';
import type { KitEvent } from './types';
import ehrUc03 from './fixtures/run-ehr-uc03.json';
import conformantUc03 from './fixtures/run-conformant-uc03.json';

const ehrEvents = ehrUc03 as unknown as KitEvent[];
const conformantEvents = conformantUc03 as unknown as KitEvent[];

function evt(partial: Partial<KitEvent> & { seq: number; type: string }): KitEvent {
  return { time: '2026-07-03T00:00:00Z', runId: 'run-t', ...partial };
}

function observerFrame(partial: Record<string, unknown> & { kind: string }): Record<string, unknown> {
  return { seq: 1, time: '2026-07-03T00:00:00.000000-04:00', ...partial };
}

describe('buildRunStory — replay against the ehr fixture (run-ehr-uc03.json)', () => {
  const runId = ehrEvents[0].runId as string;
  const story = buildRunStory(runId, ehrEvents);

  it('every step has a non-empty narration', () => {
    expect(story.steps.length).toBeGreaterThan(0);
    for (const step of story.steps) {
      expect(step.narration).not.toBe('');
    }
  });

  it('has at least one leg step with both request and response, status ok', () => {
    const closedOkLegs = story.steps.filter((s) => s.kind === 'leg' && s.request && s.response && s.status === 'ok');
    expect(closedOkLegs.length).toBeGreaterThanOrEqual(1);
  });

  it('has at least one validate step', () => {
    const validateSteps = story.steps.filter((s) => s.kind === 'validate');
    expect(validateSteps.length).toBeGreaterThanOrEqual(1);
  });

  it('leaves zero steps stuck open (the drain barrier’s client-visible payoff)', () => {
    const open = story.steps.filter((s) => s.status === 'open');
    expect(open).toEqual([]);
  });

  it('terminal is run.finished', () => {
    expect(story.terminal?.type).toBe('run.finished');
  });
});

describe('buildRunStory — replay against the conformant fixture (run-conformant-uc03.json)', () => {
  const runId = conformantEvents[0].runId as string;
  const story = buildRunStory(runId, conformantEvents);

  it('has at least one ingress step with request+response and an httpStatus', () => {
    const closedIngress = story.steps.filter(
      (s) => s.kind === 'ingress' && s.request && s.response && s.httpStatus !== undefined,
    );
    expect(closedIngress.length).toBeGreaterThanOrEqual(1);
    expect(closedIngress[0].httpStatus).toBe('200');
  });

  it('has leg steps present (the SHN-bridged legs)', () => {
    const legSteps = story.steps.filter((s) => s.kind === 'leg');
    expect(legSteps.length).toBeGreaterThanOrEqual(1);
  });

  it('pairs exactly 6 interleaved steps — 3 ingress + 3 leg (ground truth: hand-traced fixture)', () => {
    expect(story.steps).toHaveLength(6);
    expect(story.steps.filter((s) => s.kind === 'ingress')).toHaveLength(3);
    expect(story.steps.filter((s) => s.kind === 'leg')).toHaveLength(3);
  });

  it('leaves zero steps stuck open (the drain barrier’s client-visible payoff)', () => {
    const open = story.steps.filter((s) => s.status === 'open');
    expect(open).toEqual([]);
  });
});

describe('buildRunStory — hand-built branch coverage', () => {
  it('leg.failed closes its step as failed, carrying the failure detail on the response frame', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.originated',
          legType: 'pas-claim',
          correlationId: 'c-1',
          counterpart: 'payer',
          authorityFrame: 'provider-tpo',
          op: 'pas-submit',
        }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.failed',
          legType: 'pas-claim',
          correlationId: 'c-1',
          detail: 'connection timed out',
        }),
      }),
      evt({ seq: 4, type: 'run.failed', detail: 'leg failed' }),
    ];

    const story = buildRunStory('run-t', events);
    const step = story.steps.find((s) => s.kind === 'leg');
    expect(step).toBeDefined();
    expect(step?.status).toBe('failed');
    expect(step?.response?.detail).toBe('connection timed out');
    expect(step?.narration).not.toBe('');
  });

  it('ingress.responded Detail "422" closes the step failed', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({ kind: 'ingress.received', legType: 'pas-ingress' }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        observer: observerFrame({ kind: 'ingress.responded', legType: 'pas-ingress', detail: '422' }),
      }),
      evt({ seq: 4, type: 'run.finished', detail: 'rejected' }),
    ];

    const story = buildRunStory('run-t', events);
    const step = story.steps.find((s) => s.kind === 'ingress');
    expect(step?.status).toBe('failed');
    expect(step?.httpStatus).toBe('422');
  });

  it('a run cut off before a response leaves the step open, terminal run.failed', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.originated',
          legType: 'crd-order-select',
          correlationId: 'c-2',
          counterpart: 'payer',
          op: 'crd-order-select',
        }),
      }),
      evt({ seq: 3, type: 'run.failed', detail: 'child crashed' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.steps).toHaveLength(1);
    expect(story.steps[0].status).toBe('open');
    expect(story.steps[0].narration).not.toBe('');
    expect(story.terminal).toEqual({ type: 'run.failed', detail: 'child crashed' });
  });

  it('an unknown legType degrades to the pinned honest fallback narration', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.originated', legType: 'x-new-leg', correlationId: 'c-3' }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.response', legType: 'x-new-leg', correlationId: 'c-3' }),
      }),
      evt({ seq: 4, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);
    const step = story.steps.find((s) => s.kind === 'leg');
    expect(step?.narration).toBe('The Smart Gateway exchanged "x-new-leg" with the hosted counterparty.');
  });

  it('audit events decode into an ordered AuditAnchor list, never attached to any Step', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.originated', legType: 'pas-claim', correlationId: 'c-4' }),
      }),
      evt({
        seq: 3,
        type: 'audit',
        audit: {
          seq: 10,
          timestamp: '2026-07-03T23:20:25-04:00',
          sender: 'kit-provider',
          recipient: 'authz',
          transactionType: 'authz-decision:pas-submit-marker-xyz',
          authorityFrame: 'provider-tpo',
          scope: 'pas-bundle',
          outcome: 'allowed',
        },
      }),
      evt({
        seq: 4,
        type: 'audit',
        audit: {
          seq: 11,
          timestamp: '2026-07-03T23:20:26-04:00',
          sender: 'kit-provider',
          recipient: 'payer',
          transactionType: 'pas-claim',
          authorityFrame: 'provider-tpo',
          scope: 'pas-bundle',
          outcome: 'routed',
        },
      }),
      evt({
        seq: 5,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.response', legType: 'pas-claim', correlationId: 'c-4' }),
      }),
      evt({ seq: 6, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);

    expect(story.audit.map((a) => a.seq)).toEqual([10, 11]);
    expect(story.audit[0].transactionType).toBe('authz-decision:pas-submit-marker-xyz');
    expect(story.audit[1].outcome).toBe('routed');

    // Boundary: audit is run-scoped only — no step carries any field
    // referencing the audit records (assert the marker never leaks into a
    // step's serialized shape).
    for (const step of story.steps) {
      expect(JSON.stringify(step)).not.toContain('marker-xyz');
    }
  });

  it('audit.unavailable sets auditNote instead of populating audit anchors', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({ seq: 2, type: 'audit.unavailable', detail: 'audit merge skipped: seq window unavailable' }),
      evt({ seq: 3, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.audit).toEqual([]);
    expect(story.auditNote).toBe('audit merge skipped: seq window unavailable');
  });
});

describe('parseObserver', () => {
  it('returns undefined for a non-observer event (no observer field)', () => {
    const e = evt({ seq: 1, type: 'audit', audit: { transactionType: 'x' } });
    expect(parseObserver(e)).toBeUndefined();
  });

  it('returns undefined for a malformed/undecodable observer payload (no throw)', () => {
    const notAnObject = evt({ seq: 1, type: 'observer', observer: 'not-json' as unknown });
    expect(() => parseObserver(notAnObject)).not.toThrow();
    expect(parseObserver(notAnObject)).toBeUndefined();

    const missingKind = evt({ seq: 2, type: 'observer', observer: { direction: 'originate' } });
    expect(parseObserver(missingKind)).toBeUndefined();

    const nonStringKind = evt({ seq: 3, type: 'observer', observer: { kind: 42 } });
    expect(parseObserver(nonStringKind)).toBeUndefined();
  });

  it('prefers the observer payload’s own time over the kit event’s time', () => {
    const e = evt({
      seq: 1,
      time: '2026-07-03T23:20:25.923881-04:00',
      type: 'observer',
      observer: observerFrame({ kind: 'validate.result', time: '2026-07-03T23:20:25.923727-04:00', detail: 'valid' }),
    });
    const frame = parseObserver(e);
    expect(frame?.time).toBe('2026-07-03T23:20:25.923727-04:00');
    expect(frame?.seq).toBe(1);
  });
});
