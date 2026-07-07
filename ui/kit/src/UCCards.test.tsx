import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within, fireEvent, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { UCCards } from './UCCards';
import { LANE_LABELS } from './ucmeta';
import type { EventsView } from './useEvents';
import type { KitEvent, RunResult } from './types';

// vi.mock factories are hoisted above the rest of the module, so ApiError
// must be created through vi.hoisted rather than a plain top-level class
// (mirrors App.test.tsx / SignIn.test.tsx).
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
  postRun: vi.fn(),
  ApiError,
}));

import * as api from './api';

function events(activeRunId?: string, all: KitEvent[] = []): EventsView {
  return {
    all,
    byRun: (runId: string) => all.filter((e) => e.runId === runId),
    activeRunId,
    sseState: 'open',
  };
}

// A run.started event for `runId` — the event shape UCCards reads to
// attribute the "Running" chip to the exact row that launched it.
function runStarted(runId: string, lane: string, uc: string): KitEvent {
  return { seq: 1, time: '2026-01-01T00:00:00Z', type: 'run.started', runId, lane, uc };
}

function noLatest(): RunResult | undefined {
  return undefined;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.postRun).mockResolvedValue({ runId: 'run-1' });
});

describe('UCCards', () => {
  it('renders exactly 8 rows in lane ehr', () => {
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(screen.getAllByTestId(/^card-uc0\d$/)).toHaveLength(8);
  });

  // The lane switch itself moved into TopBar's ModeSwitch (ModeSwitch.test.tsx
  // owns the tablist/aria-current/onLane-firing assertions now) — UCCards
  // keeps only the honest per-lane caption, never paraphrased into the
  // switch's concise chip label.
  it('renders the current lane\'s blurb as a caption, and no lane tablist', () => {
    const { rerender } = render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(screen.getByText(LANE_LABELS.ehr.blurb)).toBeDefined();
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(screen.queryByRole('tab')).toBeNull();

    rerender(
      <UCCards
        lane="conformant"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );
    expect(screen.getByText(LANE_LABELS.conformant.blurb)).toBeDefined();
  });

  it('the column header names the scenario list', () => {
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: /prior-authorization scenarios/i })).toBeDefined();
  });

  it('branch pickers appear exactly per the row table (uc01 both lanes; uc05/uc07 ehr only)', () => {
    const { rerender } = render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(within(screen.getByTestId('card-uc01')).getByRole('combobox')).toBeDefined();
    expect(within(screen.getByTestId('card-uc05')).getByRole('combobox')).toBeDefined();
    expect(within(screen.getByTestId('card-uc07')).getByRole('combobox')).toBeDefined();
    for (const uc of ['uc02', 'uc03', 'uc04', 'uc06', 'uc08']) {
      expect(within(screen.getByTestId(`card-${uc}`)).queryByRole('combobox')).toBeNull();
    }

    rerender(
      <UCCards
        lane="conformant"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(within(screen.getByTestId('card-uc01')).getByRole('combobox')).toBeDefined();
    expect(within(screen.getByTestId('card-uc05')).queryByRole('combobox')).toBeNull();
    expect(within(screen.getByTestId('card-uc07')).queryByRole('combobox')).toBeNull();
  });

  it('run click POSTs the exact selected row (uc01 notcovered; uc03 branchless)', async () => {
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    const uc01 = within(screen.getByTestId('card-uc01'));
    await userEvent.selectOptions(uc01.getByRole('combobox'), 'notcovered');
    await userEvent.click(uc01.getByRole('button', { name: /run/i }));
    expect(api.postRun).toHaveBeenCalledWith('ehr', 'uc01', 'notcovered');

    const uc03 = within(screen.getByTestId('card-uc03'));
    await userEvent.click(uc03.getByRole('button', { name: /run/i }));
    expect(api.postRun).toHaveBeenCalledWith('ehr', 'uc03', '');
  });

  it('never-run rows show the primary Run affordance; once a result exists, "Run again"', () => {
    const latestByRow = (lane: string, uc: string): RunResult | undefined =>
      lane === 'ehr' && uc === 'uc03'
        ? { runId: 'run-9', lane: 'ehr', uc: 'uc03', branch: '', state: 'passed', detail: 'approved' }
        : undefined;

    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={latestByRow}
        onSelectRun={vi.fn()}
      />,
    );

    const uc02 = within(screen.getByTestId('card-uc02'));
    expect(uc02.getByRole('button', { name: /^run uc02$/i }).textContent).toBe('Run');

    const uc03 = within(screen.getByTestId('card-uc03'));
    expect(uc03.getByRole('button', { name: /^run uc03$/i }).textContent).toBe('Run again');
  });

  it('disabledReason disables every Run button and renders the reason exactly once', () => {
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        disabledReason="the stack is still starting"
        onSelectRun={vi.fn()}
      />,
    );

    const runButtons = screen.getAllByRole('button', { name: /^run /i });
    expect(runButtons).toHaveLength(8);
    for (const b of runButtons) expect(b).toBeDisabled();

    const notices = screen.getAllByRole('alert');
    expect(notices).toHaveLength(1);
    expect(notices[0].textContent).toBe('the stack is still starting');
  });

  it('in-flight via SSE (events.activeRunId set) disables every Run button with in-flight copy', () => {
    render(
      <UCCards
        lane="ehr"
        events={events('run-123')}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );
    for (const b of screen.getAllByRole('button', { name: /^run /i })) expect(b).toBeDisabled();
    expect(screen.getByRole('alert').textContent).toMatch(/in flight/i);
  });

  it('postRun rejecting with ApiError(409) shows the inline in-flight notice (belt-and-braces: SSE is lossy)', async () => {
    vi.mocked(api.postRun).mockRejectedValueOnce(new ApiError('conflict', 409));
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    const uc02 = within(screen.getByTestId('card-uc02'));
    await userEvent.click(uc02.getByRole('button', { name: /run/i }));

    expect(await screen.findAllByRole('alert')).toHaveLength(1);
    expect(screen.getAllByRole('alert')[0].textContent).toMatch(/in flight/i);
  });

  it('postRun rejecting with ApiError(503) shows the boot-race notice', async () => {
    vi.mocked(api.postRun).mockRejectedValueOnce(new ApiError('unavailable', 503));
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    const uc02 = within(screen.getByTestId('card-uc02'));
    await userEvent.click(uc02.getByRole('button', { name: /run/i }));

    expect(await screen.findAllByRole('alert')).toHaveLength(1);
    expect(screen.getAllByRole('alert')[0].textContent).toMatch(/still starting/i);
  });

  it('a stale 409 notice is superseded by the live in-flight signal, not masked by it', async () => {
    vi.mocked(api.postRun).mockRejectedValueOnce(new ApiError('conflict', 409));
    const { rerender } = render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    const uc02 = within(screen.getByTestId('card-uc02'));
    await userEvent.click(uc02.getByRole('button', { name: /run/i }));
    expect(await screen.findAllByRole('alert')).toHaveLength(1);
    expect(screen.getAllByRole('alert')[0].textContent).toMatch(/in flight/i);

    // Live signal arrives — the notice is now sourced from the SSE signal,
    // not the stale apiNotice (same copy for this pair, but the source
    // matters: proven below by the signal resolving and the notice
    // disappearing with it, which a persisted stale apiNotice would not do).
    rerender(
      <UCCards
        lane="ehr"
        events={events('run-live')}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );
    expect(screen.getAllByRole('alert')).toHaveLength(1);
    expect(screen.getAllByRole('alert')[0].textContent).toMatch(/in flight/i);

    // Live signal resolves — the notice must vanish with it, proving it was
    // superseded (cleared) rather than a stale apiNotice persisting.
    rerender(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('a stale 503 notice clears entirely once the live signal resolves (activeRunId set then unset)', async () => {
    vi.mocked(api.postRun).mockRejectedValueOnce(new ApiError('unavailable', 503));
    const { rerender } = render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    const uc02 = within(screen.getByTestId('card-uc02'));
    await userEvent.click(uc02.getByRole('button', { name: /run/i }));
    expect(await screen.findAllByRole('alert')).toHaveLength(1);
    expect(screen.getAllByRole('alert')[0].textContent).toMatch(/still starting/i);

    rerender(
      <UCCards
        lane="ehr"
        events={events('run-live')}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );
    rerender(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('latestByRow result renders the passed/failed chip, and view-in-inspector calls onSelectRun', async () => {
    const onSelectRun = vi.fn();
    const latestByRow = (lane: string, uc: string): RunResult | undefined => {
      if (lane === 'ehr' && uc === 'uc03') {
        return { runId: 'run-9', lane: 'ehr', uc: 'uc03', branch: '', state: 'passed', detail: 'approved, auth #A1' };
      }
      if (lane === 'ehr' && uc === 'uc08') {
        return { runId: 'run-10', lane: 'ehr', uc: 'uc08', branch: '', state: 'failed', detail: 'denied as expected' };
      }
      return undefined;
    };

    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={latestByRow}
        onSelectRun={onSelectRun}
      />,
    );

    const uc03 = within(screen.getByTestId('card-uc03'));
    expect(uc03.getByText('Passed')).toBeDefined();

    const uc08 = within(screen.getByTestId('card-uc08'));
    expect(uc08.getByText('Failed')).toBeDefined();

    // Rows with no result carry neither chip.
    const uc02 = within(screen.getByTestId('card-uc02'));
    expect(uc02.queryByText('Passed')).toBeNull();
    expect(uc02.queryByText('Failed')).toBeNull();

    await userEvent.click(uc03.getByRole('button', { name: /view in inspector/i }));
    expect(onSelectRun).toHaveBeenCalledWith('run-9');
  });

  it('the in-flight run\'s OWN row shows a "Running" chip — never a different row or a different lane', () => {
    const { rerender } = render(
      <UCCards
        lane="ehr"
        events={events('run-live', [runStarted('run-live', 'ehr', 'uc04')])}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(within(screen.getByTestId('card-uc04')).getByText(/running/i)).toBeDefined();
    // No other row in the SAME lane picks up the chip.
    expect(within(screen.getByTestId('card-uc02')).queryByText(/running/i)).toBeNull();

    // The identical event, but for the OTHER lane, must not light up this
    // lane's uc04 row — a global in-flight signal is not enough; the run's
    // own lane/uc must match.
    rerender(
      <UCCards
        lane="conformant"
        events={events('run-live', [runStarted('run-live', 'ehr', 'uc04')])}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );
    expect(within(screen.getByTestId('card-uc04')).queryByText(/running/i)).toBeNull();
  });

  it('provenance tags render in conformant for uc01/uc05/uc06/uc07 only; none render in ehr', () => {
    const { rerender } = render(
      <UCCards
        lane="conformant"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(
      screen.getByText("SHN-originated gap-fill — eligibility isn't a Da Vinci ingress operation"),
    ).toBeDefined();
    expect(
      screen.getByText(
        'CDex evidence carried on the amended re-POST; the federated-query leg is SHN-bracketed, no consent branch (CXL-D11)',
      ),
    ).toBeDefined();
    expect(
      screen.getByText('Real $questionnaire-package via the ingress; manual DTR SPA deferred (D-2RI-1)'),
    ).toBeDefined();
    expect(screen.getByText('Hybrid patient-surface read-back (D-2RI-6 analog)')).toBeDefined();
    expect(screen.queryAllByText(/^—$/)).toHaveLength(0);

    rerender(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );
    expect(screen.queryByText("SHN-originated gap-fill — eligibility isn't a Da Vinci ingress operation")).toBeNull();
    expect(screen.queryByText('Hybrid patient-surface read-back (D-2RI-6 analog)')).toBeNull();
  });

  it('uc07 in ehr with hcpcs selected shows the patient read-back hint', async () => {
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    const uc07 = within(screen.getByTestId('card-uc07'));
    expect(uc07.queryByTestId('hint-uc07')).toBeNull();

    await userEvent.selectOptions(uc07.getByRole('combobox'), 'hcpcs');
    expect(uc07.getByTestId('hint-uc07').textContent).toMatch(/read-back/i);
  });

  it('aria: each Run button and branch picker carries a per-uc accessible name', () => {
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Run uc01' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'Run uc03' })).toBeDefined();
    expect(screen.getByLabelText('uc01 branch')).toBeDefined();
    expect(screen.getByLabelText('uc05 branch')).toBeDefined();
  });

  it('rapid double-click before postRun settles calls postRun exactly once (pre-409 window)', async () => {
    let resolvePost: (() => void) | undefined;
    vi.mocked(api.postRun).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolvePost = () => resolve({ runId: 'run-1' });
        }),
    );
    render(
      <UCCards
        lane="ehr"
        events={events()}
        latestByRow={noLatest}
        onSelectRun={vi.fn()}
      />,
    );

    const uc02 = within(screen.getByTestId('card-uc02'));
    const runButton = uc02.getByRole('button', { name: /run/i });

    fireEvent.click(runButton);
    fireEvent.click(runButton); // synchronous second click before postRun settles

    expect(api.postRun).toHaveBeenCalledTimes(1);
    expect(runButton).toBeDisabled();

    await act(async () => {
      resolvePost?.();
      await Promise.resolve();
    });

    expect(runButton).not.toBeDisabled();
  });
});
