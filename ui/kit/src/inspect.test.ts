import { describe, it, expect } from 'vitest';
import { buildRunStory, buildDemoStory, isDemoRun, parseObserver } from './inspect';
import type { KitEvent } from './types';
import ehrUc03 from './fixtures/run-ehr-uc03.json';
import conformantUc03 from './fixtures/run-conformant-uc03.json';
import runDemoRefusal from './fixtures/run-demo-refusal.json';
import runDemoCarry from './fixtures/run-demo-carry.json';

const ehrEvents = ehrUc03 as unknown as KitEvent[];
const conformantEvents = conformantUc03 as unknown as KitEvent[];
// wireFixtureEvents: any genuine wire-run fixture, used below for the
// both-direction rejection row — buildDemoStory must never assemble a
// demonstration story out of a real run's run.*/observer/audit events.
const wireFixtureEvents = ehrEvents;

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

  it('pairs exactly 12 interleaved steps — 3 ingress + 3 leg + 6 sor (ground truth: hand-traced fixture, re-captured with sor.read frames)', () => {
    expect(story.steps).toHaveLength(12);
    expect(story.steps.filter((s) => s.kind === 'ingress')).toHaveLength(3);
    expect(story.steps.filter((s) => s.kind === 'leg')).toHaveLength(3);
    expect(story.steps.filter((s) => s.kind === 'sor')).toHaveLength(6);
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

describe('sor.read steps', () => {
  const sorEvent = (seq: number, op: string, detail: string): KitEvent =>
    evt({
      seq,
      runId: 'run-s',
      type: 'observer',
      observer: observerFrame({ seq, kind: 'sor.read', direction: 'sor', op, detail }),
    });

  it('a sor.read frame becomes a single ok step carrying op and detail', () => {
    const story = buildRunStory('run-s', [sorEvent(10, 'OpenOrder', 'found')]);
    expect(story.steps).toHaveLength(1);
    const s = story.steps[0];
    expect(s.kind).toBe('sor');
    expect(s.legType).toBe('sor.read');
    expect(s.status).toBe('ok');
    expect(s.sorOp).toBe('OpenOrder');
    expect(s.sorDetail).toBe('found');
  });

  it('a not-found read is still an ok step (a miss is a normal branch)', () => {
    const story = buildRunStory('run-s', [sorEvent(11, 'SupplementalReport', 'not found')]);
    expect(story.steps[0].status).toBe('ok');
    expect(story.steps[0].sorDetail).toBe('not found');
  });

  it('sor narration is data-source-flavored, known op', () => {
    const story = buildRunStory('run-s', [sorEvent(12, 'OpenOrder', 'found')]);
    expect(story.steps[0].narration).toBe('The gateway read the member’s open order from its data source.');
  });

  it('sor narration fallback for an unknown op NEVER says "hosted counterparty"', () => {
    const story = buildRunStory('run-s', [sorEvent(13, 'FutureNewRead', 'found')]);
    expect(story.steps[0].narration).toBe('The gateway read FutureNewRead from its data source.');
    expect(story.steps[0].narration).not.toMatch(/hosted counterparty/);
  });
});

describe('buildRunStory — leg.transformed / leg.refused / leg.downgrade / route / status', () => {
  it('leg.transformed arriving BEFORE leg.originated (the success-path ordering) attaches via the pending map', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.transformed',
          legType: 'pas-claim',
          correlationId: 'c-10',
          counterpart: 'payer',
          detail: 'pa.pas 2.0->2.1, pa.pas 2.1->2.2',
        }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.originated',
          legType: 'pas-claim',
          correlationId: 'c-10',
          counterpart: 'payer',
          authorityFrame: 'provider-tpo',
          op: 'pas-submit',
          route: {
            token: 'pa.pas@2.2',
            buildLine: '2.0',
            chain: [{ module: 'pa.pas 2.0->2.1', from: '2.0', to: '2.1', class: 'full' }],
          },
        }),
      }),
      evt({
        seq: 4,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.response', legType: 'pas-claim', correlationId: 'c-10' }),
      }),
      evt({ seq: 5, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.steps.filter((s) => s.kind === 'leg')).toHaveLength(1);
    const step = story.steps.find((s) => s.kind === 'leg');
    expect(step?.transform?.kind).toBe('leg.transformed');
    expect(step?.transform?.detail).toBe('pa.pas 2.0->2.1, pa.pas 2.1->2.2');
    expect(step?.route?.token).toBe('pa.pas@2.2');
    expect(step?.route?.chain).toEqual([{ module: 'pa.pas 2.0->2.1', from: '2.0', to: '2.1', class: 'full' }]);
    expect(step?.status).toBe('ok');
  });

  it('leg.refused pushes a self-contained failed step carrying own/peer/bridgeIssue and its own narration', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.refused',
          legType: 'pas-claim',
          correlationId: 'c-11',
          counterpart: 'payer',
          detail: 'no shared contract line',
          route: { own: ['2.2'], peer: ['1.0'], bridgeIssue: 'no adjacent-line bridge covers 2.2<->1.0' },
        }),
      }),
      evt({ seq: 3, type: 'run.failed', detail: 'leg refused' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.steps).toHaveLength(1);
    const step = story.steps[0];
    expect(step.kind).toBe('leg');
    expect(step.status).toBe('failed');
    expect(step.refusal?.own).toEqual(['2.2']);
    expect(step.refusal?.peer).toEqual(['1.0']);
    expect(step.refusal?.bridgeIssue).toBe('no adjacent-line bridge covers 2.2<->1.0');
    expect(step.narration).toBe(
      'The Smart Gateway found no contract line it shares with the hosted counterparty for this leg, and refused before sending anything.',
    );
  });

  it('leg.downgrade attaches its Detail string to the already-open leg step', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.originated',
          legType: 'crd-order-select',
          correlationId: 'c-12',
          counterpart: 'payer',
          op: 'crd-order-select',
        }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.downgrade',
          legType: 'crd-order-select',
          correlationId: 'c-12',
          op: 'crd-cards',
          detail: 'recipient advertises frame v1 but answered bare; processing as legacy (stale-feed downgrade)',
        }),
      }),
      evt({
        seq: 4,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.response', legType: 'crd-order-select', correlationId: 'c-12' }),
      }),
      evt({ seq: 5, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);
    const step = story.steps.find((s) => s.kind === 'leg');
    expect(step?.downgrade).toBe(
      'recipient advertises frame v1 but answered bare; processing as legacy (stale-feed downgrade)',
    );
  });

  it('a route-carrying leg.failed with a correlationId-matching open step closes it and sets refusal', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.originated',
          legType: 'pas-claim',
          correlationId: 'c-14',
          counterpart: 'payer',
          op: 'pas-submit',
        }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.failed',
          legType: 'pas-claim',
          correlationId: 'c-14',
          detail: 'transform chain refused',
          route: { own: ['2.2'], peer: ['1.0'], bridgeIssue: 'no bridge' },
        }),
      }),
      evt({ seq: 4, type: 'run.failed', detail: 'leg failed' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.steps).toHaveLength(1);
    const step = story.steps[0];
    expect(step.status).toBe('failed');
    expect(step.refusal?.bridgeIssue).toBe('no bridge');
    expect(step.response?.detail).toBe('transform chain refused');
  });

  // The Route shape below is deliberately the CHAIN-carrying one
  // (token/buildLine/chain), not own/peer/bridgeIssue: on the real wire, a
  // leg.failed with Route present is produced by exactly two engine sites —
  // egressAdapt's transform-chain refusal and guardPendCarry's carry-integrity
  // refusal (see :456-463 above and the carry-refusal fixture just below) —
  // and BOTH populate Route.Chain, never Own/Peer/BridgeIssue (that shape is
  // leg.refused's alone, a different kind altogether: no shared contract line
  // found, so no chain was ever attempted). A fixture wearing the wrong shape
  // here would still pass inspect.ts's narration check (isCarryRefusalDetail
  // keys on the Detail marker only), but would silently diverge from reality
  // one layer down: StepDetail's RefusalCard keys `hasChain` FIRST (chain
  // empty ⇒ falls through to the route-refusal species), so an own/peer/
  // bridgeIssue-shaped transform-refusal fixture would render the WRONG card
  // ("No shared contract line…") under this step's own transform-refusal
  // narration — the two discriminators would visibly disagree on the same
  // event. They differ by design (a recorded follow-up, not this change's
  // problem to unify) but must still agree for every realistic wire shape;
  // see the RefusalCard-side assertion of this exact event in
  // StepDetail.test.tsx ("the SAME event as inspect.test.ts's...").
  it('a route-carrying leg.failed with NO open step becomes a self-contained failed step (egressAdapt transform-refusal precedes leg.originated)', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
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
        }),
      }),
      evt({ seq: 3, type: 'run.failed', detail: 'leg failed' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.steps).toHaveLength(1);
    const step = story.steps[0];
    expect(step.kind).toBe('leg');
    expect(step.status).toBe('failed');
    expect(step.refusal?.chain).toEqual([{ module: 'pa.pas 2.0->2.2', from: '2.0', to: '2.2', class: 'gated' }]);
    // Not the route-refusal shape: this Route carries no own/peer/bridgeIssue,
    // only the attempted chain — the shape a real transform refusal wears.
    expect(step.refusal?.own).toBeUndefined();
    expect(step.refusal?.peer).toBeUndefined();
    expect(step.refusal?.bridgeIssue).toBeUndefined();
    // The dedicated transform-refused narration, fetched directly:
    // the legType-keyed path previously fell through to the generic
    // '…exchanged "pas-claim"…' fallback — a false sentence rendered directly
    // above RefusalCard's "zero bytes crossed the network".
    expect(step.narration).toBe(
      'The Smart Gateway could not honestly bridge this leg to the hosted counterparty’s contract line, and refused before sending anything.'
    );
  });

  it('a route-carrying leg.failed whose detail bears the carry marker narrates as a carry refusal, not a transform refusal', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({
          kind: 'leg.failed',
          legType: 'pas-claim-update',
          correlationId: 'c-16',
          counterpart: 'payer',
          detail:
            'engine: pended carry not intact at resume (pin pa.pas@2.1): engine: verifyCarryPresent: declared carry "Claim.extension[0]" not found in the payload about to be restored — the payload no longer bears content its own loss record declares carried',
          route: {
            token: 'pa.pas@2.1',
            buildLine: '2.2',
            chain: [{ module: 'pa.pas 2.2->2.1', from: '2.2', to: '2.1', class: 'carry' }],
          },
        }),
      }),
      evt({ seq: 3, type: 'run.failed', detail: 'leg failed' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.steps).toHaveLength(1);
    const step = story.steps[0];
    expect(step.status).toBe('failed');
    expect(step.narration).toBe(
      'The Smart Gateway found this resumed request no longer carries content its own record says it must, and refused before sending anything.'
    );
  });

  it('a genuinely unknown observer kind is still silently skipped, never a step', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.mystery-future-kind', legType: 'pas-claim', correlationId: 'c-99' }),
      }),
      evt({ seq: 3, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);
    expect(story.steps).toEqual([]);
  });

  it('a relayed non-2xx status is parsed as a number and surfaced on the response frame (display only — step status logic unchanged)', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started' }),
      evt({
        seq: 2,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.originated', legType: 'pas-claim', correlationId: 'c-13', op: 'pas-submit' }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        observer: observerFrame({ kind: 'leg.response', legType: 'pas-claim', correlationId: 'c-13', status: 422 }),
      }),
      evt({ seq: 4, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);
    const step = story.steps.find((s) => s.kind === 'leg');
    expect(step?.response?.status).toBe(422);
    // Step status logic byte-unchanged: leg.response always closes 'ok',
    // even when it carries a relayed non-2xx status.
    expect(step?.status).toBe('ok');
  });

  it('ingress status logic stays byte-unchanged: ingress.responded "200" still closes ok', () => {
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
        observer: observerFrame({ kind: 'ingress.responded', legType: 'pas-ingress', detail: '200' }),
      }),
      evt({ seq: 4, type: 'run.finished' }),
    ];

    const story = buildRunStory('run-t', events);
    const step = story.steps.find((s) => s.kind === 'ingress');
    expect(step?.status).toBe('ok');
    expect(step?.httpStatus).toBe('200');
  });
});

describe('parseObserver — route/status', () => {
  it('parses status as a number', () => {
    const e = evt({ seq: 1, type: 'observer', observer: observerFrame({ kind: 'leg.response', status: 422 }) });
    expect(parseObserver(e)?.status).toBe(422);
  });

  it('parses a well-formed selected route (token/buildLine/chain)', () => {
    const e = evt({
      seq: 1,
      type: 'observer',
      observer: observerFrame({
        kind: 'leg.originated',
        route: {
          token: 'pa.pas@2.2',
          buildLine: '2.0',
          chain: [{ module: 'pa.pas 2.0->2.1', from: '2.0', to: '2.1', class: 'full' }],
        },
      }),
    });
    const frame = parseObserver(e);
    expect(frame?.route?.token).toBe('pa.pas@2.2');
    expect(frame?.route?.buildLine).toBe('2.0');
    expect(frame?.route?.chain).toEqual([{ module: 'pa.pas 2.0->2.1', from: '2.0', to: '2.1', class: 'full' }]);
  });

  it('parses a refusal-shaped route (own/peer/bridgeIssue, no token/chain)', () => {
    const e = evt({
      seq: 1,
      type: 'observer',
      observer: observerFrame({
        kind: 'leg.refused',
        route: { own: ['2.2'], peer: ['1.0'], bridgeIssue: 'no adjacent-line bridge' },
      }),
    });
    const frame = parseObserver(e);
    expect(frame?.route?.own).toEqual(['2.2']);
    expect(frame?.route?.peer).toEqual(['1.0']);
    expect(frame?.route?.bridgeIssue).toBe('no adjacent-line bridge');
    expect(frame?.route?.token).toBeUndefined();
  });

  it('route is undefined when absent, and never throws on a malformed route', () => {
    const noRoute = evt({ seq: 1, type: 'observer', observer: observerFrame({ kind: 'leg.originated' }) });
    expect(parseObserver(noRoute)?.route).toBeUndefined();

    const malformed = evt({
      seq: 2,
      type: 'observer',
      observer: observerFrame({ kind: 'leg.originated', route: 'not-an-object' as unknown }),
    });
    expect(() => parseObserver(malformed)).not.toThrow();
    expect(parseObserver(malformed)?.route).toBeUndefined();
  });
});

// Local-demonstration events — a disjoint species from the wire vocabulary
// (demo.started/demo.exhibit/demo.finished, never observer/audit/run.*).
// Both rows below are REAL captured local-demonstration runs
// (test/kitlive's TestBridgingGate_MixedVersion — docs/kit-ci-operations.md's
// fixture-recapture recipe), never hand-typed: a drift in
// kit/kitd/bridging.go's own wire shape fails HERE, not silently. `demoEvt`
// stays only for the one row below a real capture genuinely cannot drive: a
// JSON-null chain (a nil Go slice with no `omitempty` on demoRecord — the
// wire producer's own possible shape; today's captured refusal always has a
// populated chain because gateway/app/demo_endpoint.go's ChainSteps runs
// before the refusal is even known, so a real capture can never exercise the
// null case).
function demoEvt(partial: Partial<KitEvent> & { seq: number; type: string; runId: string }): KitEvent {
  return { time: '2026-08-16T00:00:00Z', lane: 'demo', ...partial };
}

const demoRefusalEvents = runDemoRefusal as unknown as KitEvent[];
const refusalRunId = demoRefusalEvents[0].runId as string;

const demoCarryEvents = runDemoCarry as unknown as KitEvent[];
const carryRunId = demoCarryEvents[0].runId as string;

describe('isDemoRun', () => {
  it('is true iff a demo.started with this runId exists', () => {
    expect(isDemoRun(demoRefusalEvents, refusalRunId)).toBe(true);
    expect(isDemoRun(demoRefusalEvents, 'run-1')).toBe(false);
    expect(isDemoRun(wireFixtureEvents, refusalRunId)).toBe(false);
  });
});

describe('buildDemoStory', () => {
  it('assembles a refusal demonstration from demo.* events', () => {
    const story = buildDemoStory(refusalRunId, demoRefusalEvents);
    expect(story).toBeDefined();
    expect(story?.runId).toBe(refusalRunId);
    expect(story?.uc).toBe('refusal-engine');
    expect(story?.record.kind).toBe('refusal-engine');
    expect(story?.record.contract).toBe('pa.dtr');
    // The refusal fires on the up leg's single hop (pa.dtr 2.1->2.2) — the
    // attempted chain ChainSteps reports even though the step itself refused.
    expect(story?.record.chain).toEqual([{ module: 'pa.dtr 2.1->2.2', from: '2.1', to: '2.2', class: 'carry' }]);
    expect(story?.record.input).toMatchObject({
      resourceType: 'QuestionnaireResponse',
      questionnaire: 'http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri|1.0.0',
    });
    // The live engine's own wording (gateway/engine/transform_pas.go's
    // *engine.SemanticChangeError), round-tripped byte-faithfully through
    // the wire — not a paraphrase.
    expect(story?.record.refusal).toBe(
      'shn: semantic-change refusal: pa.dtr 2.1->2.2 (up direction): no honest byte-level source for ' +
        'QuestionnaireResponse.extension:qr-coverage (ambiguous: 2 Coverage-referencing qr-context entries, multi-coverage source)',
    );
    expect(story?.record.semanticChange).toBe(true);
    expect(story?.verdict).toBe('refused as expected');
  });

  it('assembles a carry demonstration (intermediate + output + restored)', () => {
    const story = buildDemoStory(carryRunId, demoCarryEvents);
    expect(story).toBeDefined();
    expect(story?.uc).toBe('carry-engine');
    expect(story?.record.kind).toBe('carry-engine');
    expect(story?.record.chain).toEqual([
      { module: 'pa.dtr 2.2->2.1', from: '2.2', to: '2.1', class: 'carry' },
      { module: 'pa.dtr 2.1->2.2', from: '2.1', to: '2.2', class: 'carry' },
    ]);
    expect(story?.record.input).toMatchObject({ resourceType: 'QuestionnaireResponse' });
    expect(story?.record.intermediate).toMatchObject({ resourceType: 'QuestionnaireResponse' });
    expect(story?.record.output).toMatchObject({ resourceType: 'QuestionnaireResponse' });
    expect(story?.record.restored).toBe(true);
    // Down leg carries itemWeight into shn-carried-content (nothing for the
    // up leg to carry back — it is restoring what the down leg captured).
    expect(story?.record.lossReports).toHaveLength(2);
    expect(story?.record.lossReports?.[0].module).toBe('pa.dtr 2.2->2.1');
    expect(story?.record.lossReports?.[0].carried).toHaveLength(1);
    expect(story?.record.lossReports?.[0].carried?.[0].path).toBe(
      'QuestionnaireResponse.item.answer.value.extension:itemWeight',
    );
    expect(story?.verdict).toBe('restored exactly');
  });

  it('returns undefined for a wire run (rejection: no demo story from run.*/observer events)', () => {
    const story = buildDemoStory('run-1', wireFixtureEvents);
    expect(story).toBeUndefined();
  });

  it('returns undefined when there is no demo.started/demo.exhibit pair, never throws', () => {
    expect(buildDemoStory('nonexistent-run', demoRefusalEvents)).toBeUndefined();
    expect(() => buildDemoStory('nonexistent-run', demoRefusalEvents)).not.toThrow();
  });

  it('yields zero wire steps from demo events (rejection: demo events never enter a wire story)', () => {
    const s = buildRunStory(refusalRunId, demoRefusalEvents);
    expect(s.steps).toHaveLength(0);
  });

  it('tolerates a JSON-null chain on the refusal record without throwing (a shape a real capture cannot drive today — see the note above demoEvt)', () => {
    const runId = 'demo-null-chain-1';
    const events: KitEvent[] = [
      demoEvt({ seq: 1, type: 'demo.started', runId, uc: 'refusal-engine' }),
      demoEvt({
        seq: 2,
        type: 'demo.exhibit',
        runId,
        uc: 'refusal-engine',
        demo: {
          kind: 'refusal-engine',
          contract: 'pa.dtr',
          chain: null,
          input: { resourceType: 'QuestionnaireResponse' },
          refusal: 'no honest byte-level source for a placeholder element',
          semanticChange: true,
        },
      }),
      demoEvt({ seq: 3, type: 'demo.finished', runId, uc: 'refusal-engine', detail: 'refused as expected' }),
    ];
    expect(() => buildDemoStory(runId, events)).not.toThrow();
    expect(buildDemoStory(runId, events)?.record.chain).toEqual([]);
  });
});
