import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RunInspector } from './RunInspector';
import { buildRunStory } from './inspect';
import { TRANSFORM_CARD_NARRATION, XFORM_EXPANDER_LABEL } from './StepDetail';
import { DEMO_STEP_ID, REMOTE_ZONE_CAPTION } from './FlowMap';
import {
  DEMO_REMOTE_CAPTION,
  DEMO_REPLAY_FAILURE_NOTE,
  DEMO_RESULT_REFUSAL,
  DEMO_STEP_CLASS_CAPTION,
  DEMO_STEP_LABEL,
  FROZEN_SOURCE_NODE,
  LOCAL_DEMO_CHIP,
} from './bridgingmeta';
import type { BridgingCapture, HistorySummary, KitEvent, RunResult } from './types';
import ehrUc03 from './fixtures/run-ehr-uc03.json';
import runDemoRefusal from './fixtures/run-demo-refusal.json';

// vi.mock factories are hoisted above the rest of the module, so ApiError
// must be created through vi.hoisted (mirrors StepDetail.test.tsx).
const { ApiError } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
    }
  }
  return { ApiError };
});

vi.mock('./api', () => ({
  getBridgingCapture: vi.fn(),
  ApiError,
}));

import * as api from './api';

beforeEach(() => {
  vi.clearAllMocks();
});

const ehrEvents = ehrUc03 as unknown as KitEvent[];
const ehrRunId = ehrEvents[0].runId as string;
const ehrStory = buildRunStory(ehrRunId, ehrEvents);

function evt(partial: Partial<KitEvent> & { seq: number; type: string; runId: string }): KitEvent {
  return { time: '2026-07-03T00:00:00Z', ...partial };
}

function observerFrame(partial: Record<string, unknown> & { kind: string }): Record<string, unknown> {
  return { seq: 1, time: '2026-07-03T00:00:00.000000-04:00', ...partial };
}

describe('RunInspector — empty / loading / missing states', () => {
  it('no runId renders the pinned "run a scenario" copy', () => {
    render(<RunInspector events={[]} source="missing" results={[]} />);
    expect(screen.getByText('Run a scenario to see its flow.')).toBeDefined();
  });

  it('source "loading" renders a loading state', () => {
    render(<RunInspector runId="run-x" events={[]} source="loading" results={[]} />);
    expect(screen.getByText(/loading/i)).toBeDefined();
  });

  it('source "missing" renders the pinned "no longer available" copy', () => {
    render(<RunInspector runId="run-x" events={[]} source="missing" results={[]} />);
    expect(screen.getByText('This run is no longer available.')).toBeDefined();
  });
});

describe('RunInspector — fixture replay (ehr uc03)', () => {
  it('header shows lane/uc + result badge; FlowMap renders the story steps; default selection is the first step', () => {
    const results: RunResult[] = [
      { runId: ehrRunId, lane: 'ehr', uc: 'uc03', branch: '', state: 'passed', detail: 'approved, auth #A1' },
    ];
    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="live" results={results} />);

    expect(screen.getByText('ehr/uc03')).toBeDefined();
    expect(screen.getByText('Passed')).toBeDefined();

    const buttons = document.querySelectorAll('.step');
    expect(buttons).toHaveLength(ehrStory.steps.length);

    const selected = document.querySelector('.step.sel') as HTMLElement;
    expect(selected.getAttribute('data-step-id')).toBe(ehrStory.steps[0].id);
    expect(screen.getByText(ehrStory.steps[0].narration)).toBeDefined();
  });

  it('branch renders from the `summary` prop only (not from `results`)', () => {
    const summary: HistorySummary = {
      runId: ehrRunId,
      lane: 'ehr',
      uc: 'uc03',
      branch: 'covered',
      state: 'passed',
      detail: 'approved, auth #A1',
      time: '2026-07-03T00:00:00Z',
      eventCount: ehrEvents.length,
    };
    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="history" results={[]} summary={summary} />);

    expect(screen.getByText('ehr/uc03 (covered)')).toBeDefined();
    expect(screen.getByText('Passed')).toBeDefined();
  });

  it('a results entry carrying a non-empty branch but NO summary prop shows no branch suffix (events/results carry no branch — branch is summary-only)', () => {
    const results: RunResult[] = [
      { runId: ehrRunId, lane: 'ehr', uc: 'uc03', branch: 'covered', state: 'passed', detail: 'approved, auth #A1' },
    ];
    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="live" results={results} />);

    expect(screen.getByText('ehr/uc03')).toBeDefined();
    expect(screen.queryByText('ehr/uc03 (covered)')).toBeNull();
    expect(screen.getByText('Passed')).toBeDefined();
  });

  it('a run.started event carrying the retired lane: "provider-data" renders as the Plain EHR lane', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started', runId: 'run-legacy-pd', lane: 'provider-data', uc: 'uc03' }),
    ];
    render(<RunInspector runId="run-legacy-pd" events={events} source="live" results={[]} />);

    expect(screen.getByText('ehr/uc03')).toBeDefined();
  });

  it('clicking a step shows StepDetail for it', async () => {
    const user = userEvent.setup();
    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="live" results={[]} />);

    const buttons = Array.from(document.querySelectorAll('.step')) as HTMLElement[];
    const target = ehrStory.steps[2];
    const targetButton = buttons.find((b) => b.getAttribute('data-step-id') === target.id);
    expect(targetButton).toBeDefined();

    await user.click(targetButton as HTMLElement);

    expect(document.querySelector('.step.sel')?.getAttribute('data-step-id')).toBe(target.id);
    expect(screen.getByText(target.narration)).toBeDefined();
  });
});

describe('RunInspector — substrate toggle + audit anchors', () => {
  const auditEvents: KitEvent[] = [
    evt({ seq: 1, type: 'run.started', runId: 'run-audit', lane: 'ehr', uc: 'uc03' }),
    evt({
      seq: 2,
      type: 'observer',
      runId: 'run-audit',
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
      type: 'audit',
      runId: 'run-audit',
      audit: {
        seq: 10,
        timestamp: '2026-07-03T23:20:25-04:00',
        sender: 'kit-provider',
        recipient: 'payer',
        transactionType: 'pas-claim',
        authorityFrame: 'provider-tpo',
        scope: 'pas-bundle',
        outcome: 'allowed',
      },
    }),
    evt({
      seq: 4,
      type: 'observer',
      runId: 'run-audit',
      observer: observerFrame({ kind: 'leg.response', legType: 'pas-claim', correlationId: 'c-1' }),
    }),
    evt({ seq: 5, type: 'run.finished', runId: 'run-audit' }),
  ];

  it('one control labeled "Network view"; clinical view hides the audit strip; flipping shows it with one row per AuditAnchor, and audit rows never render inside the step-detail pane', async () => {
    const user = userEvent.setup();
    render(<RunInspector runId="run-audit" events={auditEvents} source="live" results={[]} />);

    expect(document.querySelector('.audit-anchors')).toBeNull();

    const toggle = screen.getByLabelText('Network view');
    await user.click(toggle);

    expect(document.querySelector('.audit-anchors')).not.toBeNull();
    const rows = document.querySelectorAll('.audit-anchor-row');
    expect(rows).toHaveLength(1);
    expect(rows[0].textContent).toContain('pas-claim');
    expect(rows[0].textContent).toContain('kit-provider');
    expect(rows[0].textContent).toContain('payer');
    expect(rows[0].textContent).toContain('provider-tpo');
    expect(rows[0].textContent).toContain('allowed');

    // Boundary: audit rows are a sibling of the step-detail pane,
    // never nested inside it.
    const stepDetail = document.querySelector('.detail') as HTMLElement;
    expect(stepDetail).not.toBeNull();
    for (const row of Array.from(rows)) {
      expect(stepDetail.contains(row)).toBe(false);
    }
  });

  it('with auditNote set (merge skipped), the strip shows the explanation instead of rows', async () => {
    const user = userEvent.setup();
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started', runId: 'run-noaudit', lane: 'ehr', uc: 'uc03' }),
      evt({ seq: 2, type: 'audit.unavailable', runId: 'run-noaudit', detail: 'audit merge skipped: seq window unavailable' }),
      evt({ seq: 3, type: 'run.finished', runId: 'run-noaudit' }),
    ];
    render(<RunInspector runId="run-noaudit" events={events} source="live" results={[]} />);

    await user.click(screen.getByLabelText('Network view'));

    expect(screen.getByText('audit merge skipped: seq window unavailable')).toBeDefined();
    expect(document.querySelectorAll('.audit-anchor-row')).toHaveLength(0);
  });
});

describe('RunInspector — run.failed terminal (failure is content)', () => {
  it('highlights the failed step in the map, shows the header failed badge, and renders the terminal detail sentence', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started', runId: 'run-fail', lane: 'ehr', uc: 'uc08' }),
      evt({
        seq: 2,
        type: 'observer',
        runId: 'run-fail',
        observer: observerFrame({
          kind: 'leg.originated',
          legType: 'pas-claim',
          correlationId: 'c-1',
          counterpart: 'payer',
          op: 'pas-submit',
        }),
      }),
      evt({
        seq: 3,
        type: 'observer',
        runId: 'run-fail',
        observer: observerFrame({
          kind: 'leg.failed',
          legType: 'pas-claim',
          correlationId: 'c-1',
          detail: 'connection timed out',
        }),
      }),
      evt({ seq: 4, type: 'run.failed', runId: 'run-fail', detail: 'the payer leg did not complete' }),
    ];
    const results: RunResult[] = [
      { runId: 'run-fail', lane: 'ehr', uc: 'uc08', branch: '', state: 'failed', detail: 'the payer leg did not complete' },
    ];

    render(<RunInspector runId="run-fail" events={events} source="live" results={results} />);

    expect(screen.getByText('Failed')).toBeDefined();
    expect(screen.getByText('the payer leg did not complete')).toBeDefined();

    const failedButton = document.querySelector('.step[data-status="failed"]');
    expect(failedButton).not.toBeNull();
  });
});

describe('RunInspector — providerLabel forwarding', () => {
  it('forwards providerLabel through to the FlowMap provider node', () => {
    render(
      <RunInspector
        runId={ehrRunId}
        events={ehrEvents}
        source="live"
        results={[]}
        providerLabel="Your EHR (FHIR data source)"
      />,
    );

    const providerNode = document.querySelector('[data-node="provider"]');
    expect(providerNode?.textContent).toBe('Your EHR (FHIR data source)');
  });
});

describe('RunInspector — posture forwarding', () => {
  it('forwards posture through to StepDetail\'s ValidationBadge for the selected validate step', async () => {
    const user = userEvent.setup();
    const validateStep = ehrStory.steps.find((s) => s.kind === 'validate');
    expect(validateStep).toBeDefined();

    render(
      <RunInspector runId={ehrRunId} events={ehrEvents} source="live" results={[]} posture="packaged" />,
    );

    const target = document.querySelector(
      `.step[data-step-id="${validateStep?.id}"]`,
    ) as HTMLElement;
    expect(target).not.toBeNull();
    await user.click(target);

    expect(screen.getByText("checked by the Kit's local HL7 validator (offline IG set)")).toBeDefined();
  });

  it('posture omitted defaults to the stand-in sentence (the honest fallback threaded all the way down)', async () => {
    const user = userEvent.setup();
    const validateStep = ehrStory.steps.find((s) => s.kind === 'validate');

    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="live" results={[]} />);

    const target = document.querySelector(
      `.step[data-step-id="${validateStep?.id}"]`,
    ) as HTMLElement;
    await user.click(target);

    expect(
      screen.getByText(
        "checked by the Kit's stand-in validator — real conformance validation is off in this development build",
      ),
    ).toBeDefined();
  });
});

describe('RunInspector — register forwarding', () => {
  // A transform step (leg.transformed joined onto a leg.originated/
  // leg.response pair, same success-path ordering inspect.ts documents:
  // leg.transformed arrives BEFORE leg.originated for the same
  // correlationId) — the only step kind whose narration is register-aware
  // (StepDetail.tsx's TransformCard).
  const transformEvents: KitEvent[] = [
    evt({ seq: 1, type: 'run.started', runId: 'run-transform', lane: 'conformant', uc: 'uc03' }),
    evt({
      seq: 2,
      type: 'observer',
      runId: 'run-transform',
      observer: observerFrame({ kind: 'leg.transformed', correlationId: 'c-t1', detail: 'pa.dtr 2.2->2.1' }),
    }),
    evt({
      seq: 3,
      type: 'observer',
      runId: 'run-transform',
      observer: observerFrame({
        kind: 'leg.originated',
        legType: 'dtr-questionnaire-fetch',
        correlationId: 'c-t1',
        counterpart: 'payer',
      }),
    }),
    evt({
      seq: 4,
      type: 'observer',
      runId: 'run-transform',
      observer: observerFrame({ kind: 'leg.response', legType: 'dtr-questionnaire-fetch', correlationId: 'c-t1' }),
    }),
    evt({ seq: 5, type: 'run.finished', runId: 'run-transform' }),
  ];

  it('register="technical" reaches StepDetail\'s TransformCard — the exported pinned const, byte-exact', () => {
    render(<RunInspector runId="run-transform" events={transformEvents} source="live" results={[]} register="technical" />);

    // Double-assert per house rule: the const's own literal, and its
    // rendered presence — never paraphrase either half.
    expect(TRANSFORM_CARD_NARRATION.technical).toBe(
      "This leg's payload passed through a chain of compatibility steps before it left the gateway. The loss report below names every element carried across unread for the other side to restore, and every element deterministically synthesized rather than fabricated.",
    );
    expect(screen.getByText(TRANSFORM_CARD_NARRATION.technical)).toBeDefined();
    expect(screen.queryByText(TRANSFORM_CARD_NARRATION.overview)).toBeNull();
  });

  it('register omitted preserves StepDetail\'s existing default (overview narration, not technical)', () => {
    render(<RunInspector runId="run-transform" events={transformEvents} source="live" results={[]} />);

    expect(screen.getByText(TRANSFORM_CARD_NARRATION.overview)).toBeDefined();
    expect(screen.queryByText(TRANSFORM_CARD_NARRATION.technical)).toBeNull();
  });
});

describe('RunInspector — switching the selected step never leaks the previous step\'s expander state', () => {
  // Two bridged legs, back to back (leg A fully closes before leg B opens,
  // so pairing is unambiguous) — each carries a distinct correlationId and
  // a distinct transform frame, the two facts a genuine per-leg capture
  // fetch must key on.
  const twoLegEvents: KitEvent[] = [
    evt({ seq: 1, type: 'run.started', runId: 'run-two-legs', lane: 'conformant', uc: 'uc03' }),
    evt({
      seq: 2,
      type: 'observer',
      runId: 'run-two-legs',
      observer: observerFrame({ kind: 'leg.transformed', correlationId: 'c-leg-a', detail: 'pa.dtr 2.2->2.1' }),
    }),
    evt({
      seq: 3,
      type: 'observer',
      runId: 'run-two-legs',
      observer: observerFrame({
        kind: 'leg.originated',
        legType: 'dtr-questionnaire-fetch',
        correlationId: 'c-leg-a',
        counterpart: 'payer',
      }),
    }),
    evt({
      seq: 4,
      type: 'observer',
      runId: 'run-two-legs',
      observer: observerFrame({ kind: 'leg.response', legType: 'dtr-questionnaire-fetch', correlationId: 'c-leg-a' }),
    }),
    evt({
      seq: 5,
      type: 'observer',
      runId: 'run-two-legs',
      observer: observerFrame({ kind: 'leg.transformed', correlationId: 'c-leg-b', detail: 'pa.pas 2.2->2.1' }),
    }),
    evt({
      seq: 6,
      type: 'observer',
      runId: 'run-two-legs',
      observer: observerFrame({
        kind: 'leg.originated',
        legType: 'pas-claim',
        correlationId: 'c-leg-b',
        counterpart: 'payer',
        op: 'pas-submit',
      }),
    }),
    evt({
      seq: 7,
      type: 'observer',
      runId: 'run-two-legs',
      observer: observerFrame({ kind: 'leg.response', legType: 'pas-claim', correlationId: 'c-leg-b', op: 'pas-response' }),
    }),
    evt({ seq: 8, type: 'run.finished', runId: 'run-two-legs' }),
  ];
  const twoLegStory = buildRunStory('run-two-legs', twoLegEvents);
  const legAStepId = twoLegStory.steps.find((s) => s.correlationId === 'c-leg-a')!.id;
  const legBStepId = twoLegStory.steps.find((s) => s.correlationId === 'c-leg-b')!.id;

  function captureFor(correlationId: string, marker: string): BridgingCapture {
    return {
      correlationId,
      legType: 'dtr-questionnaire-fetch',
      contract: 'pa.dtr',
      from: '2.2',
      to: '2.1',
      chain: [{ module: 'pa.dtr 2.2->2.1', from: '2.2', to: '2.1', class: 'carry' }],
      lossReports: [],
      before: { resourceType: 'QuestionnaireResponse', marker: `${marker}-before` },
      after: { resourceType: 'QuestionnaireResponse', marker: `${marker}-after` },
      capturedAt: '2026-08-16T00:00:00Z',
    };
  }

  it('selecting leg B after expanding leg A starts leg B collapsed, with none of leg A\'s payload visible, and fetches leg B\'s own correlationId on expand', async () => {
    const user = userEvent.setup();
    vi.mocked(api.getBridgingCapture).mockImplementation((correlationId: string) =>
      Promise.resolve(captureFor(correlationId, correlationId === 'c-leg-a' ? 'leg-a-marker' : 'leg-b-marker')),
    );
    render(<RunInspector runId="run-two-legs" events={twoLegEvents} source="live" results={[]} />);

    // Leg A is the default selection (first step) — expand it and let its
    // capture fetch resolve.
    expect(document.querySelector('.step.sel')?.getAttribute('data-step-id')).toBe(legAStepId);
    await user.click(screen.getByText(XFORM_EXPANDER_LABEL));
    expect(await screen.findByText(/leg-a-marker-before/)).toBeDefined();
    expect(api.getBridgingCapture).toHaveBeenCalledTimes(1);
    expect(api.getBridgingCapture).toHaveBeenCalledWith('c-leg-a');

    // Select leg B. Its expander must come up idle/collapsed — the pinned
    // collapsed label, no leftover xform-diff DOM, and none of leg A's
    // payload text anywhere on the page — and it must not have fetched
    // anything on its own account yet.
    const legBButton = document.querySelector(`.step[data-step-id="${legBStepId}"]`) as HTMLElement;
    await user.click(legBButton);

    expect(document.querySelector('.step.sel')?.getAttribute('data-step-id')).toBe(legBStepId);
    expect(screen.getByText(XFORM_EXPANDER_LABEL)).toBeDefined();
    expect(screen.queryByText('Hide transformation')).toBeNull();
    expect(document.querySelector('.xform-diff')).toBeNull();
    expect(screen.queryByText(/leg-a-marker/)).toBeNull();
    expect(api.getBridgingCapture).toHaveBeenCalledTimes(1);

    // Expanding leg B fetches its OWN correlationId, and only its own
    // payload ever renders.
    await user.click(screen.getByText(XFORM_EXPANDER_LABEL));
    expect(await screen.findByText(/leg-b-marker-before/)).toBeDefined();
    expect(api.getBridgingCapture).toHaveBeenCalledTimes(2);
    expect(api.getBridgingCapture).toHaveBeenLastCalledWith('c-leg-b');
    expect(screen.queryByText(/leg-a-marker/)).toBeNull();
  });
});

describe('RunInspector — Replay run button enable rule (source is IRRELEVANT)', () => {
  it('Replay run is enabled for a history-backed completed story (has terminal)', () => {
    const summary: HistorySummary = {
      runId: ehrRunId,
      lane: 'ehr',
      uc: 'uc03',
      branch: 'covered',
      state: 'passed',
      detail: 'approved, auth #A1',
      time: '2026-07-03T00:00:00Z',
      eventCount: ehrEvents.length,
    };
    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="history" results={[]} summary={summary} />);

    const button = screen.getByRole('button', { name: 'Replay run' });
    expect(button).not.toBeDisabled();
  });

  it('Replay run is enabled for a live-sourced story whose terminal already arrived — a just-completed run stays source: "live" until ring eviction (useRunEvents.ts:31), so gating on source would disable the button exactly when users most want it', () => {
    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="live" results={[]} />);

    expect(ehrStory.terminal).toBeDefined();
    const button = screen.getByRole('button', { name: 'Replay run' });
    expect(button).not.toBeDisabled();
  });

  it('Replay run is disabled while the story has no terminal (still streaming)', () => {
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started', runId: 'run-streaming', lane: 'ehr', uc: 'uc03' }),
      evt({
        seq: 2,
        type: 'observer',
        runId: 'run-streaming',
        observer: observerFrame({
          kind: 'leg.originated',
          legType: 'pas-claim',
          correlationId: 'c-1',
          counterpart: 'payer',
        }),
      }),
      // no run.finished / run.failed — still in flight
    ];
    render(<RunInspector runId="run-streaming" events={events} source="live" results={[]} />);

    const button = screen.getByRole('button', { name: 'Replay run' });
    expect(button).toBeDisabled();
  });
});

describe('RunInspector — Replay run button click behavior', () => {
  it('clicking Replay run increments the replay token passed to FlowMap (does not throw, button stays present)', async () => {
    const user = userEvent.setup();
    render(<RunInspector runId={ehrRunId} events={ehrEvents} source="live" results={[]} />);

    const button = screen.getByRole('button', { name: 'Replay run' });
    expect(button).not.toBeDisabled();
    await user.click(button);

    expect(screen.getByRole('button', { name: 'Replay run' })).toBeDefined();
  });

  // Deferred finding 8 regression: the Replay button's disabled -> re-enabled
  // round trip. Clicking sets `replaying` (disable); FlowMap's onReplayEnd
  // clears it (re-enable). With FlowMap now ALWAYS signalling its end, the
  // button can never wedge disabled after a replay. A CONFORMANT-lane sor step
  // is edge-less (FlowMap flashes the gateway node for a real ~300ms dwell
  // instead of pulsing a drawn edge), which holds the replay genuinely
  // in-flight long enough for the disabled state to be observable in jsdom
  // (a pulsed edge resolves synchronously here, draining before we could
  // observe it) — so this asserts BOTH halves of the round trip deterministically.
  it('disables the Replay button while a replay is in flight and re-enables it once the run signals its end', async () => {
    const user = userEvent.setup();
    const events: KitEvent[] = [
      evt({ seq: 1, type: 'run.started', runId: 'run-replay-rt', lane: 'conformant', uc: 'uc03' }),
      evt({
        seq: 2,
        type: 'observer',
        runId: 'run-replay-rt',
        observer: observerFrame({ kind: 'sor.read', op: 'ResolvePatient', detail: 'found', seq: 2 }),
      }),
      evt({ seq: 3, type: 'run.finished', runId: 'run-replay-rt' }),
    ];
    render(<RunInspector runId="run-replay-rt" events={events} source="live" results={[]} />);

    const button = screen.getByRole('button', { name: 'Replay run' });
    expect(button).not.toBeDisabled();

    await user.click(button);
    // in flight -> disabled (the edge-less gateway-flash dwell holds it)
    expect(screen.getByRole('button', { name: 'Replay run' })).toBeDisabled();

    // onReplayEnd fires -> re-enabled (never wedged)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Replay run' })).not.toBeDisabled(),
    );
  });
});

describe('RunInspector — live auto-follow vs. manual pin', () => {
  const step1 = observerFrame({
    kind: 'leg.originated',
    legType: 'crd-order-select',
    correlationId: 'c-1',
    counterpart: 'payer',
    op: 'crd-order-select',
  });
  const step1Close = observerFrame({ kind: 'leg.response', legType: 'crd-order-select', correlationId: 'c-1', op: 'crd-cards' });
  const step2 = observerFrame({
    kind: 'leg.originated',
    legType: 'dtr-questionnaire-fetch',
    correlationId: 'c-2',
    counterpart: 'payer',
  });
  const step2Close = observerFrame({ kind: 'leg.response', legType: 'dtr-questionnaire-fetch', correlationId: 'c-2' });

  function eventsUpTo(n: number): KitEvent[] {
    const all: KitEvent[] = [
      evt({ seq: 1, type: 'run.started', runId: 'run-live', lane: 'conformant', uc: 'uc03' }),
      evt({ seq: 2, type: 'observer', runId: 'run-live', observer: { ...step1, seq: 2 } }),
      evt({ seq: 3, type: 'observer', runId: 'run-live', observer: { ...step1Close, seq: 3 } }),
      evt({ seq: 4, type: 'observer', runId: 'run-live', observer: { ...step2, seq: 4 } }),
      evt({ seq: 5, type: 'observer', runId: 'run-live', observer: { ...step2Close, seq: 5 } }),
    ];
    return all.slice(0, n);
  }

  it('newest step auto-selects as steps append; a manual click pins the selection against further appends', async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <RunInspector runId="run-live" events={eventsUpTo(2)} source="live" results={[]} />,
    );

    // Single step so far — it's both first and newest.
    expect(document.querySelector('.step.sel')?.getAttribute('data-step-id')).toBe('2');

    // A second step appends — selection follows the newest.
    rerender(<RunInspector runId="run-live" events={eventsUpTo(4)} source="live" results={[]} />);
    expect(document.querySelector('.step.sel')?.getAttribute('data-step-id')).toBe('4');

    // Manual pick of the first step.
    const buttons = Array.from(document.querySelectorAll('.step')) as HTMLElement[];
    const firstButton = buttons.find((b) => b.getAttribute('data-step-id') === '2') as HTMLElement;
    await user.click(firstButton);
    expect(document.querySelector('.step.sel')?.getAttribute('data-step-id')).toBe('2');

    // Closing the second leg (still no terminal) must not steal the pin.
    rerender(<RunInspector runId="run-live" events={eventsUpTo(5)} source="live" results={[]} />);
    expect(document.querySelector('.step.sel')?.getAttribute('data-step-id')).toBe('2');
  });
});

// Local-demonstration species fixtures — the same captured run-demo-
// refusal.json events.ts/StepDetail.test.tsx already drive, not a hand-built
// shape: a hand-built demo.exhibit with chain: null (no chain and no
// own/peer/bridgeIssue) lands RefusalCard on its defensive, unclassified
// branch — a genuinely degraded render the real gateway never produces (its
// ChainSteps call always populates a chain before a refusal is even known).
// demoEvt stays only for the one row below a real capture cannot drive (a
// demo.started with no demo.exhibit yet).
function demoEvt(partial: Partial<KitEvent> & { seq: number; type: string; runId: string }): KitEvent {
  return { time: '2026-08-16T00:00:00Z', lane: 'demo', ...partial };
}

const demoRefusalEvents = runDemoRefusal as unknown as KitEvent[];
const demoRefusalRunId = demoRefusalEvents[0].runId as string;

describe('RunInspector — local-demonstration species', () => {
  it('renders the demo title, LocalDemoChip, DemoResultChip, and a disabled substrate toggle', () => {
    render(<RunInspector runId={demoRefusalRunId} events={demoRefusalEvents} source="live" results={[]} />);

    expect(screen.getByText('demo/refusal-engine')).toBeDefined();
    expect(screen.getByText(LOCAL_DEMO_CHIP)).toBeDefined();
    expect(screen.getByText(DEMO_RESULT_REFUSAL)).toBeDefined();

    const substrateToggle = screen.getByRole('checkbox') as HTMLInputElement;
    expect(substrateToggle.disabled).toBe(true);
  });

  it("FlowMap renders the three-zone demonstration shape: static source node, no validator node, dimmed remote zone with the demo caption", () => {
    render(<RunInspector runId={demoRefusalRunId} events={demoRefusalEvents} source="live" results={[]} />);

    const provider = document.querySelector('[data-node="provider"]');
    expect(provider?.textContent).toBe(FROZEN_SOURCE_NODE);
    expect(provider?.getAttribute('data-static')).toBe('true');
    expect(document.querySelector('[data-node="validator"]')).toBeNull();
    expect(document.querySelector('.remote')?.className).toMatch(/\bnot-involved\b/);
    expect(screen.getByText(DEMO_REMOTE_CAPTION)).toBeDefined();
    expect(screen.queryByText(REMOTE_ZONE_CAPTION)).toBeNull();
  });

  it('ignores a providerLabel prop for a demo run — routed around deriveProviderLabel; the FlowMap variant supplies FROZEN_SOURCE_NODE itself', () => {
    render(
      <RunInspector
        runId={demoRefusalRunId}
        events={demoRefusalEvents}
        source="live"
        results={[]}
        providerLabel="Your Da Vinci system"
      />,
    );
    expect(document.querySelector('[data-node="provider"]')?.textContent).toBe(FROZEN_SOURCE_NODE);
  });

  it("Replay run re-executes the exhibit through onReplayDemo, mapping the demo's uc to postBridgingExhibit's 'refusal'/'carry' vocabulary — a semantic fork from the wire-run animation replay", async () => {
    const user = userEvent.setup();
    const onReplayDemo = vi.fn();
    render(
      <RunInspector
        runId={demoRefusalRunId}
        events={demoRefusalEvents}
        source="live"
        results={[]}
        onReplayDemo={onReplayDemo}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Replay run' }));

    expect(onReplayDemo).toHaveBeenCalledTimes(1);
    expect(onReplayDemo).toHaveBeenCalledWith('refusal');
  });

  it('clicking Replay run on a demo run never touches the wire-run flow-map animation (no replay-token throw, onReplayDemo optional)', async () => {
    const user = userEvent.setup();
    render(<RunInspector runId={demoRefusalRunId} events={demoRefusalEvents} source="live" results={[]} />);
    await user.click(screen.getByRole('button', { name: 'Replay run' }));
    expect(screen.getByRole('button', { name: 'Replay run' })).toBeDefined();
  });

  it('a demo.started event with no demo.exhibit yet renders the loading state, never a fabricated demo header', () => {
    const startedOnly: KitEvent[] = [
      demoEvt({ seq: 1, type: 'demo.started', runId: 'demo-pending', uc: 'refusal-engine' }),
    ];
    render(<RunInspector runId="demo-pending" events={startedOnly} source="live" results={[]} />);
    expect(screen.getByText(/loading/i)).toBeDefined();
  });

  it('renders exactly one demo step row on the FlowMap rail, with the pinned label and class caption (regression: the rail used to render an empty <ol>)', () => {
    render(<RunInspector runId={demoRefusalRunId} events={demoRefusalEvents} source="live" results={[]} />);

    expect(document.querySelectorAll('.steps li')).toHaveLength(1);
    expect(screen.getByText(DEMO_STEP_LABEL)).toBeDefined();
    expect(screen.getByText(DEMO_STEP_CLASS_CAPTION)).toBeDefined();
  });

  it('clicking the demo step row selects it (same rail mechanics as a wire step) without disturbing the always-rendered DemoStepDetail', async () => {
    const user = userEvent.setup();
    render(<RunInspector runId={demoRefusalRunId} events={demoRefusalEvents} source="live" results={[]} />);

    const row = document.querySelector(`.step[data-step-id="${DEMO_STEP_ID}"]`) as HTMLElement;
    expect(row.getAttribute('aria-pressed')).toBe('false');

    await user.click(row);
    expect(row.getAttribute('aria-pressed')).toBe('true');
    expect(row.className).toMatch(/\bsel\b/);
    // DemoStepDetail's rendering doesn't key off the rail selection at all —
    // still present, byte-unchanged, after the click.
    expect(document.querySelector('.demo-detail')).not.toBeNull();
  });

  it('disables the demo Replay control while a replay is in flight, and re-enables it once it resolves (no second call on a double-click)', async () => {
    const user = userEvent.setup();
    let resolveReplay: () => void = () => undefined;
    const onReplayDemo = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveReplay = resolve;
        }),
    );
    render(
      <RunInspector
        runId={demoRefusalRunId}
        events={demoRefusalEvents}
        source="live"
        results={[]}
        onReplayDemo={onReplayDemo}
      />,
    );

    const button = screen.getByRole('button', { name: 'Replay run' }) as HTMLButtonElement;
    expect(button.disabled).toBe(false);

    await user.click(button);
    expect(button.disabled).toBe(true);

    // A second click while in flight must not fire a second api call — the
    // button's own `disabled` attribute is the guard (userEvent respects it,
    // same as a real click would).
    await user.click(button);
    expect(onReplayDemo).toHaveBeenCalledTimes(1);

    await waitFor(() => {
      resolveReplay();
    });
    await waitFor(() => expect(button.disabled).toBe(false));
  });

  it('renders an inline role="alert" failure message when the demo replay rejects, cleared by the next attempt', async () => {
    const user = userEvent.setup();
    const onReplayDemo = vi
      .fn()
      .mockRejectedValueOnce(new Error('502'))
      .mockResolvedValueOnce(undefined);
    render(
      <RunInspector
        runId={demoRefusalRunId}
        events={demoRefusalEvents}
        source="live"
        results={[]}
        onReplayDemo={onReplayDemo}
      />,
    );

    expect(screen.queryByRole('alert')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Replay run' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(DEMO_REPLAY_FAILURE_NOTE);

    await user.click(screen.getByRole('button', { name: 'Replay run' }));
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
  });
});
