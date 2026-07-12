import { describe, it, expect } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RunInspector } from './RunInspector';
import { buildRunStory } from './inspect';
import type { HistorySummary, KitEvent, RunResult } from './types';
import ehrUc03 from './fixtures/run-ehr-uc03.json';

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

  it('one control labeled "Substrate view"; clinical view hides the audit strip; flipping shows it with one row per AuditAnchor, and audit rows never render inside the step-detail pane', async () => {
    const user = userEvent.setup();
    render(<RunInspector runId="run-audit" events={auditEvents} source="live" results={[]} />);

    expect(document.querySelector('.audit-anchors')).toBeNull();

    const toggle = screen.getByLabelText('Substrate view');
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

    await user.click(screen.getByLabelText('Substrate view'));

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
