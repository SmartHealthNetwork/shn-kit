import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, within, act, fireEvent } from '@testing-library/react';
import App from './App';
import { computeDisabledReason, isGatewayReady } from './App';
import type { BootstrapResponse, HistoryRecord, HistorySummary, RunResult, StatusResponse } from './types';
import type { EventsView } from './useEvents';

// vi.mock factories are hoisted above the rest of the module, so ApiError
// must be created through vi.hoisted rather than a plain top-level class.
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
  getBootstrap: vi.fn(),
  getStatus: vi.fn(),
  getRuns: vi.fn(),
  postSignIn: vi.fn(),
  postReset: vi.fn(),
  postVerify: vi.fn(),
  postRun: vi.fn(),
  eventsUrl: vi.fn(() => '/events'),
  getHistory: vi.fn(),
  getHistoryRecord: vi.fn(),
  getBYO: vi.fn(),
  putBYOEhr: vi.fn(),
  deleteBYOEhr: vi.fn(),
  putBYODaVinci: vi.fn(),
  deleteBYODaVinci: vi.fn(),
  // FreeFormPanel's and WatchPanel's own api calls — App mounts the REAL
  // components (not mocked), so their api module calls (shared with App's
  // own vi.mock('./api')) need stubs too.
  getBYOPatients: vi.fn(),
  getBYOContext: vi.fn(),
  postWatch: vi.fn(),
  deleteWatch: vi.fn(),
  // StatusPanel (mounted for real by App) now calls these — getAbout via
  // its nested, also-real AboutPanel mount.
  getAbout: vi.fn(() => new Promise(() => {})),
  postChildRestart: vi.fn(),
  supportBundleUrl: vi.fn(() => '/api/support-bundle'),
  ApiError,
}));

// Token resolution is bridge.test's concern (already pinned in bridge.test
// coverage via api.test.ts) — App tests never need a resolved token, so
// resolveToken() is left permanently pending here: useEvents(undefined)
// never constructs an EventSource, keeping these tests focused on the
// phase router.
vi.mock('./bridge', () => ({
  resolveToken: vi.fn(() => new Promise<string>(() => {})),
  canRestart: vi.fn(() => false),
  restartKit: vi.fn(),
  openExternal: vi.fn(),
}));

// The SSE hook itself is useEvents.test.ts's concern; App tests mock it
// directly so run-inspector selection / in-flight reconciliation can be
// driven without a real EventSource.
vi.mock('./useEvents', () => ({
  useEvents: vi.fn(),
}));

import * as api from './api';
import * as bridge from './bridge';
import { useEvents } from './useEvents';

function boot(overrides: Partial<BootstrapResponse> = {}): BootstrapResponse {
  return { state: 'signin-required', verify: [], ...overrides };
}

function statusReady(): StatusResponse {
  return { children: [{ name: 'gateway', state: 'ready', detail: 'ok', pid: 1, restarts: 0 }] };
}

function eventsView(overrides: Partial<EventsView> = {}): EventsView {
  const all = overrides.all ?? [];
  return {
    activeRunId: undefined,
    sseState: 'open',
    ...overrides,
    // `byRun` is always derived fresh from `all` (rather than accepting an
    // override) so useRunEvents' ring-lookup ("does the ring hold this run's
    // run.started?") behaves like the real useEvents hook regardless of what
    // callers pass for `all` — a stub `byRun: () => []` would make every
    // selected run look non-live and fall through to the history fetch.
    all,
    byRun: (runId: string) => all.filter((e) => e.runId === runId),
  };
}

async function flush() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

// Click a NavRail destination. The workbench swaps the working column (and,
// for byo/systems, drops the inspector) — the re-housed panels (RunHistory,
// BYOPanel, StatusPanel) render only once their destination is active.
async function clickNav(name: RegExp): Promise<void> {
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name }));
  });
}

const STATUS_POLL_MS = 3000;

beforeEach(() => {
  vi.useFakeTimers();
  // mockImplementation (not mockResolvedValue) so every poll call returns a
  // FRESH object/array — a real fetch would too, and React bails out of a
  // state update (no re-render) when a poll resolves with the exact same
  // reference as last time, which would mask polling-driven re-renders.
  vi.mocked(api.getStatus).mockImplementation(() => Promise.resolve({ children: [] }));
  vi.mocked(api.getRuns).mockImplementation(() => Promise.resolve([]));
  vi.mocked(api.getHistory).mockImplementation(() => Promise.resolve([]));
  // BYO status, fetched once main phase is reached — default to the
  // empty/nothing-configured shape so tests that don't exercise BYO don't
  // need to stub it themselves.
  vi.mocked(api.getBYO).mockImplementation(() =>
    Promise.resolve({ ehr: null, davinci: null, ingress: null }),
  );
  // Default FreeFormPanel/WatchPanel fetches to the empty/
  // never-called-in-this-test shape — only the BYO lane-surface tests below
  // stub these to something meaningful.
  vi.mocked(api.getBYOPatients).mockImplementation(() => Promise.resolve([]));
  vi.mocked(useEvents).mockReturnValue(eventsView());
  // Any selected run whose events aren't in the ring falls back to
  // getHistoryRecord (useRunEvents) — left permanently pending here
  // (mirrors resolveToken above) so these phase-router/selection tests never
  // need to drive a real history fetch to completion; RunInspector.test.tsx
  // owns the history-record rendering itself.
  vi.mocked(api.getHistoryRecord).mockImplementation(() => new Promise(() => {}));
});

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

async function renderMain(): Promise<void> {
  vi.mocked(api.getBootstrap).mockResolvedValue(boot({ state: 'provisioned' }));
  vi.mocked(api.getStatus).mockImplementation(() => Promise.resolve(statusReady()));
  render(<App />);
  await flush(); // bootstrap poll resolves -> boot state flips to 'provisioned'
  await flush(); // status+runs poll (now enabled) resolves -> ready
}

describe('App phase router', () => {
  it('bootstrap signin-required renders SignIn', async () => {
    vi.mocked(api.getBootstrap).mockResolvedValue(boot({ state: 'signin-required' }));
    render(<App />);
    await flush();

    expect(screen.getByRole('button', { name: /sign in/i })).toBeDefined();
  });

  it('bootstrap provisioning renders BootProgress', async () => {
    vi.mocked(api.getBootstrap).mockResolvedValue(boot({ state: 'provisioning' }));
    render(<App />);
    await flush();

    expect(screen.getByText('Starting the Kit')).toBeDefined();
  });

  it('provisioned + gateway ready + getRuns resolving renders the main surface (Scenarios destination: UCCards + RunInspector)', async () => {
    await renderMain();

    // UCCards: the lane tablist + all 8 cards.
    expect(screen.getByRole('tablist', { name: 'lane' })).toBeDefined();
    expect(screen.getAllByTestId(/^card-uc0\d$/)).toHaveLength(8);

    // RunInspector: empty-state copy with no run selected.
    expect(screen.getByText('Run a scenario to see its flow.')).toBeDefined();

    // StatusPanel now lives behind the Systems destination — not on the
    // default Scenarios surface.
    expect(screen.queryByRole('button', { name: /^reset$/i })).toBeNull();

    expect(screen.queryByText('Starting the Kit')).toBeNull();
    expect(screen.queryByRole('button', { name: /sign in/i })).toBeNull();
  });

  it("getBootstrap rejecting with a network error renders the daemon-down screen", async () => {
    vi.mocked(api.getBootstrap).mockRejectedValue(new Error('fetch failed'));
    render(<App />);
    await flush();

    expect(screen.getByText(/can't reach the kit daemon/i)).toBeDefined();
  });

  it('getBootstrap rejecting with ApiError(401) renders the bad-token copy, NOT the daemon-down screen', async () => {
    vi.mocked(api.getBootstrap).mockRejectedValue(new ApiError('unauthorized', 401));
    render(<App />);
    await flush();

    expect(screen.getByText(/reopen the ui with a fresh/i)).toBeDefined();
    expect(screen.queryByText(/can't reach the kit daemon/i)).toBeNull();
  });
});

describe('App — workbench navigation', () => {
  it('defaults to the Scenarios destination: cards + inspector present, diagnostics absent', async () => {
    await renderMain();
    expect(screen.getAllByTestId(/^card-uc0\d$/)).toHaveLength(8);
    expect(screen.getByText('Run a scenario to see its flow.')).toBeDefined();
    expect(screen.queryByRole('button', { name: /^reset$/i })).toBeNull();
  });

  it('clicking Systems swaps the working column to diagnostics, hides the cards, and drops the inspector', async () => {
    await renderMain();
    await clickNav(/^systems$/i);

    // Diagnostics (StatusPanel) now visible: SSE liveness + reset.
    expect(screen.getByText('live')).toBeDefined();
    expect(screen.getByRole('button', { name: /^reset$/i })).toBeDefined();

    // Scenario cards hidden; inspector absent on the full-width destination.
    expect(screen.queryAllByTestId(/^card-uc0\d$/)).toHaveLength(0);
    expect(screen.queryByText('Run a scenario to see its flow.')).toBeNull();
  });

  it('Run history is reachable via nav: it renders the history panel while the inspector stays present', async () => {
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-a', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T10:00:00Z', eventCount: 1 },
    ]);
    await renderMain();
    await flush(); // getHistory resolves
    await clickNav(/^run history$/i);

    expect(screen.getByRole('heading', { name: 'Run history' })).toBeDefined();
    // The inspector rides alongside history (selecting a row drives it).
    expect(screen.getByText('Run a scenario to see its flow.')).toBeDefined();
    // Scenario cards are hidden.
    expect(screen.queryAllByTestId(/^card-uc0\d$/)).toHaveLength(0);
  });

  it('Bring your own is reachable via nav: it renders BYOPanel full-width (no inspector)', async () => {
    await renderMain();
    await flush(); // getBYO resolves
    await clickNav(/^bring your own$/i);

    expect(screen.getByRole('heading', { name: 'Bring your own' })).toBeDefined();
    expect(screen.queryByText('Run a scenario to see its flow.')).toBeNull();
  });
});

describe('App — computeDisabledReason', () => {
  it('gateway child not ready → reason mentions the Smart Gateway', () => {
    const notReady: StatusResponse = {
      children: [{ name: 'gateway', state: 'starting', detail: '', pid: 1, restarts: 0 }],
    };
    expect(isGatewayReady(notReady)).toBe(false);
    expect(computeDisabledReason(notReady, true, false)).toMatch(/smart gateway/i);
  });

  it('runs 503 (runsLive=false) → "still starting"', () => {
    expect(computeDisabledReason(statusReady(), false, false)).toMatch(/still starting/i);
  });

  it('in-flight → "a run is in flight"', () => {
    expect(computeDisabledReason(statusReady(), true, true)).toMatch(/a run is in flight/i);
  });

  it('none of the three conditions → undefined', () => {
    expect(computeDisabledReason(statusReady(), true, false)).toBeUndefined();
  });

  it('in-flight AND watching → the watch-specific copy', () => {
    expect(computeDisabledReason(statusReady(), true, true, true)).toMatch(
      /watching for incoming flows — stop watching to run scenarios/i,
    );
  });

  it('in-flight but NOT watching → the generic in-flight copy, unchanged', () => {
    expect(computeDisabledReason(statusReady(), true, true, false)).toMatch(/a run is in flight/i);
  });
});

describe('App — run inspector selection (auto-follow vs. manual pick)', () => {
  it('a run.started event auto-selects that run; a manual pick of an older run sticks until the next terminal event', async () => {
    const oldResult: RunResult = {
      runId: 'run-old',
      lane: 'conformant',
      uc: 'uc02',
      branch: '',
      state: 'passed',
      detail: 'covered, no PA required',
    };
    vi.mocked(api.getRuns).mockImplementation(() => Promise.resolve([{ ...oldResult }]));
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-new', lane: 'conformant', uc: 'uc03' },
        ],
        activeRunId: 'run-new',
      }),
    );

    await renderMain();

    // Auto-followed the newly started run — its events are in the ring, so
    // RunInspector renders it live.
    expect(screen.getByText('conformant/uc03')).toBeDefined();

    // User picks the older, already-terminal run via UCCards' "View in
    // inspector". Its events are NOT in the ring (this test's `all` array
    // never carries them) — RunInspector falls back to the (permanently
    // pending, per beforeEach) history fetch and renders its loading state,
    // proving selectedRunId flipped away from run-new.
    // (fireEvent, not userEvent — userEvent's internal delay/RAF machinery
    // deadlocks against vi.useFakeTimers() in this suite.)
    const uc02 = within(screen.getByTestId('card-uc02'));
    await act(async () => {
      fireEvent.click(uc02.getByRole('button', { name: /view in inspector/i }));
    });
    expect(screen.queryByText('conformant/uc03')).toBeNull();
    expect(screen.getByText(/loading this run/i)).toBeDefined();

    // A second run starts while the guard is active (no terminal event for
    // run-new has been observed yet) — the manual pick must stick.
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-new', lane: 'conformant', uc: 'uc03' },
          { seq: 2, time: '2026-07-03T14:00:05Z', type: 'run.started', runId: 'run-new2', lane: 'conformant', uc: 'uc04' },
        ],
        activeRunId: 'run-new2',
      }),
    );
    await advance(STATUS_POLL_MS);
    expect(screen.getByText(/loading this run/i)).toBeDefined();
    expect(screen.queryByText('conformant/uc04')).toBeNull();

    // The in-flight run terminates (activeRunId clears) — the guard resets.
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-new', lane: 'conformant', uc: 'uc03' },
          { seq: 2, time: '2026-07-03T14:00:05Z', type: 'run.started', runId: 'run-new2', lane: 'conformant', uc: 'uc04' },
          { seq: 3, time: '2026-07-03T14:00:10Z', type: 'run.finished', runId: 'run-new2' },
        ],
        activeRunId: undefined,
      }),
    );
    await advance(STATUS_POLL_MS);
    // Selection didn't move (no NEW run.started arrived yet) but the guard
    // is now clear for the next one.
    expect(screen.getByText(/loading this run/i)).toBeDefined();

    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 4, time: '2026-07-03T14:00:15Z', type: 'run.started', runId: 'run-new3', lane: 'ehr', uc: 'uc01' },
        ],
        activeRunId: 'run-new3',
      }),
    );
    await advance(STATUS_POLL_MS);
    expect(screen.getByText('ehr/uc01')).toBeDefined();
  });
});

describe('App — reset-required banner survives the phase flip', () => {
  it('main phase -> reset completes with restartRequired -> next bootstrap poll flips to signin-required -> the banner renders INSTEAD of the sign-in screen, and persists across further polls', async () => {
    await renderMain();
    await clickNav(/^systems$/i); // reset lives on the Systems destination
    vi.mocked(api.postReset).mockResolvedValue({ restartRequired: true });

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /confirm reset/i }));
    });
    await flush();

    // StatusPanel's own inline restartRequired copy is visible immediately.
    expect(screen.getByText(/restart the kit to finish the reset/i)).toBeDefined();

    // The next bootstrap poll flips boot.state to signin-required — this is
    // the router flip that used to unmount StatusPanel out from under the
    // operator.
    vi.mocked(api.getBootstrap).mockResolvedValue(boot({ state: 'signin-required' }));
    await advance(2000); // BOOTSTRAP_POLL_MS

    expect(screen.getByText(/restart the kit to finish the reset/i)).toBeDefined();
    expect(screen.queryByRole('button', { name: /^sign in$/i })).toBeNull();

    // Further polls (still signin-required) don't clear the banner — only a
    // real restart (page reload) does.
    await advance(2000);
    expect(screen.getByText(/restart the kit to finish the reset/i)).toBeDefined();
    expect(screen.queryByRole('button', { name: /^sign in$/i })).toBeNull();
  });

  it("the banner's Restart button calls restartKit() when canRestart() is true", async () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    await renderMain();
    await clickNav(/^systems$/i); // reset lives on the Systems destination
    vi.mocked(api.postReset).mockResolvedValue({ restartRequired: true });

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /confirm reset/i }));
    });
    await flush();

    vi.mocked(api.getBootstrap).mockResolvedValue(boot({ state: 'signin-required' }));
    await advance(2000);

    // Once the phase flips, StatusPanel has unmounted (its content is
    // replaced by the banner) — only the banner's Restart button remains.
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^restart$/i }));
    });
    expect(bridge.restartKit).toHaveBeenCalled();
  });
});

describe('App — in-flight reconciliation', () => {
  it('activeRunId set with no terminal SSE event, then getRuns returns that runId with a terminal state → Run buttons re-enable', async () => {
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-x', lane: 'conformant', uc: 'uc02' },
        ],
        activeRunId: 'run-x',
      }),
    );
    vi.mocked(api.getRuns).mockResolvedValue([]);

    await renderMain();

    // `Run uc0X` scoping avoids the "Run history" nav button (also /^run /).
    for (const b of screen.getAllByRole('button', { name: /^run uc0\d$/i })) {
      expect(b).toBeDisabled();
    }
    expect(screen.getByRole('alert').textContent).toMatch(/in flight/i);

    // The next getRuns poll answers with a terminal result for run-x — the
    // dropped terminal SSE frame must not wedge the buttons disabled forever.
    vi.mocked(api.getRuns).mockResolvedValue([
      { runId: 'run-x', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok' },
    ]);
    await advance(3000);

    for (const b of screen.getAllByRole('button', { name: /^run uc0\d$/i })) {
      expect(b).not.toBeDisabled();
    }
    expect(screen.queryByRole('alert')).toBeNull();
  });
});

describe('App — run history', () => {
  it('fetches history once main phase is reached, and re-fetches when results.length changes (not on every poll)', async () => {
    vi.mocked(api.getHistory).mockResolvedValue([]);
    await renderMain();
    expect(api.getHistory).toHaveBeenCalledTimes(1);

    // A run completes — results.length changes — history is re-fetched.
    vi.mocked(api.getRuns).mockResolvedValue([
      { runId: 'run-1', lane: 'ehr', uc: 'uc02', branch: '', state: 'passed', detail: 'ok' },
    ]);
    await advance(STATUS_POLL_MS);
    expect(api.getHistory).toHaveBeenCalledTimes(2);

    // A further poll with the SAME results.length must not re-fetch again.
    await advance(STATUS_POLL_MS);
    expect(api.getHistory).toHaveBeenCalledTimes(2);
  });

  it('opening a history run via the RunHistory panel sets the inspector to it AND pins the manual selection (a later run.started does not steal it back)', async () => {
    const historyEntry: HistorySummary = {
      runId: 'run-hist',
      lane: 'conformant',
      uc: 'uc02',
      branch: '',
      state: 'passed',
      detail: 'covered, no PA required',
      time: '2026-07-03T10:00:00Z',
      eventCount: 3,
    };
    vi.mocked(api.getHistory).mockResolvedValue([historyEntry]);

    await renderMain();
    await flush(); // getHistory resolves; the row renders
    await clickNav(/^run history$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /open run-hist/i }));
    });

    // run-hist's events aren't in the ring — falls back to the (permanently
    // pending, per beforeEach) history fetch and renders its loading state,
    // proving selectedRunId flipped to it.
    expect(screen.getByText(/loading this run/i)).toBeDefined();

    // A run.started arrives while the manual pin is active — selection must
    // not move (mirrors the existing UCCards pin row; onOpen wires the exact
    // same handleSelectRun as UCCards' "View in inspector").
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-new', lane: 'conformant', uc: 'uc03' },
        ],
        activeRunId: 'run-new',
      }),
    );
    await advance(STATUS_POLL_MS);
    expect(screen.getByText(/loading this run/i)).toBeDefined();
    expect(screen.queryByText('conformant/uc03')).toBeNull();
  });

  it('onCompare renders two RunInspector panes side by side (two inspector headers); comparing the same run again closes it', async () => {
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-a', lane: 'conformant', uc: 'uc02' },
          { seq: 2, time: '2026-07-03T14:05:00Z', type: 'run.started', runId: 'run-b', lane: 'ehr', uc: 'uc04' },
        ],
        activeRunId: undefined,
      }),
    );
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-a', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T14:00:00Z', eventCount: 1 },
      { runId: 'run-b', lane: 'ehr', uc: 'uc04', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T14:05:00Z', eventCount: 1 },
    ]);

    await renderMain();
    await flush(); // getHistory resolves; both rows render
    await clickNav(/^run history$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /open run-a/i }));
    });
    let headers = Array.from(document.querySelectorAll('.insp-head'));
    expect(headers).toHaveLength(1);
    expect(headers[0].textContent).toContain('conformant/uc02');

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /compare run-b/i }));
    });
    headers = Array.from(document.querySelectorAll('.insp-head'));
    expect(headers).toHaveLength(2);
    expect(headers.some((h) => h.textContent?.includes('ehr/uc04'))).toBe(true);

    // Comparing the same run again closes the pane.
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /compare run-b/i }));
    });
    headers = Array.from(document.querySelectorAll('.insp-head'));
    expect(headers).toHaveLength(1);
    expect(headers.some((h) => h.textContent?.includes('ehr/uc04'))).toBe(false);
  });

  it('opening compare renders an explicit two-up split with a visible "Close comparison" control; clicking it clears compareRunId back to the single pane', async () => {
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-a', lane: 'conformant', uc: 'uc02' },
          { seq: 2, time: '2026-07-03T14:05:00Z', type: 'run.started', runId: 'run-b', lane: 'ehr', uc: 'uc04' },
        ],
        activeRunId: undefined,
      }),
    );
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-a', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T14:00:00Z', eventCount: 1 },
      { runId: 'run-b', lane: 'ehr', uc: 'uc04', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T14:05:00Z', eventCount: 1 },
    ]);

    await renderMain();
    await flush(); // getHistory resolves; both rows render
    await clickNav(/^run history$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /open run-a/i }));
    });

    // Before comparing: no split, no close control.
    expect(document.querySelector('.inspector-split')).toBeNull();
    expect(screen.queryByRole('button', { name: /close comparison/i })).toBeNull();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /compare run-b/i }));
    });

    // Comparing: an explicit split housing both inspectors, plus a visible close control.
    expect(document.querySelector('.inspector-split')).not.toBeNull();
    expect(document.querySelectorAll('.insp-head')).toHaveLength(2);
    const closeButton = screen.getByRole('button', { name: /close comparison/i });
    expect(closeButton).toBeDefined();

    await act(async () => {
      fireEvent.click(closeButton);
    });

    // Closing collapses back to the single pane — the compare inspector and
    // the close control both disappear, without touching the primary pane.
    expect(document.querySelector('.inspector-split')).toBeNull();
    const remainingHeaders = Array.from(document.querySelectorAll('.insp-head'));
    expect(remainingHeaders).toHaveLength(1);
    expect(screen.queryByRole('button', { name: /close comparison/i })).toBeNull();
    expect(remainingHeaders[0].textContent).toContain('conformant/uc02');
  });

  it('switching nav destinations closes an open comparison — a comparison is destination-scoped, not carried across nav (the working column still swaps as before)', async () => {
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-a', lane: 'conformant', uc: 'uc02' },
          { seq: 2, time: '2026-07-03T14:05:00Z', type: 'run.started', runId: 'run-b', lane: 'ehr', uc: 'uc04' },
        ],
        activeRunId: undefined,
      }),
    );
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-a', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T14:00:00Z', eventCount: 1 },
      { runId: 'run-b', lane: 'ehr', uc: 'uc04', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T14:05:00Z', eventCount: 1 },
    ]);

    await renderMain();
    await flush(); // getHistory resolves; both rows render
    await clickNav(/^run history$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /open run-a/i }));
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /compare run-b/i }));
    });
    expect(document.querySelector('.inspector-split')).not.toBeNull();

    // Switching to Scenarios still swaps the working column exactly as
    // before (existing nav-swap behavior) — AND closes the stale
    // comparison, rather than letting it survive into an unrelated
    // destination.
    await clickNav(/^scenarios/i);
    expect(screen.getAllByTestId(/^card-uc0\d$/)).toHaveLength(8);
    expect(document.querySelector('.inspector-split')).toBeNull();

    // The primary selection (run-a) survives the nav swap — only the
    // comparison closes.
    const headers = document.querySelectorAll('.insp-head');
    expect(headers).toHaveLength(1);
    expect(headers[0].textContent).toContain('conformant/uc02');
  });

  it('a new live run auto-following clears a stale comparison (never pairs a brand-new run against an unrelated compareRunId)', async () => {
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-a', lane: 'conformant', uc: 'uc02' },
        ],
        activeRunId: 'run-a',
      }),
    );
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-b', lane: 'ehr', uc: 'uc04', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T14:05:00Z', eventCount: 1 },
    ]);

    await renderMain();
    await flush(); // getHistory resolves; run-b's row renders
    await clickNav(/^run history$/i);

    // run-a is auto-followed (never manually opened) and compared against
    // the completed run-b.
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /compare run-b/i }));
    });
    expect(document.querySelector('.inspector-split')).not.toBeNull();

    // A brand-new run starts and auto-selects — manualPickRef was never
    // set (Compare, unlike Open, never pins a manual selection), so the
    // auto-follow branch fires and must drop the now-stale comparison.
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-a', lane: 'conformant', uc: 'uc02' },
          { seq: 2, time: '2026-07-03T15:00:00Z', type: 'run.started', runId: 'run-c', lane: 'conformant', uc: 'uc01' },
        ],
        activeRunId: 'run-c',
      }),
    );
    await advance(STATUS_POLL_MS);

    expect(document.querySelector('.inspector-split')).toBeNull();
  });

  it('export fetches the HistoryRecord, builds a Blob, and downloads it via a <runId>.json anchor click (the Record IS the export format)', async () => {
    const record: HistoryRecord = {
      runId: 'run-a',
      lane: 'conformant',
      uc: 'uc02',
      branch: '',
      state: 'passed',
      detail: 'ok',
      time: '2026-07-03T10:00:00Z',
      eventCount: 1,
      events: [{ seq: 1, time: '2026-07-03T10:00:00Z', type: 'run.started', runId: 'run-a', lane: 'conformant', uc: 'uc02' }],
    };
    vi.mocked(api.getHistoryRecord).mockResolvedValue(record);
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-a', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T10:00:00Z', eventCount: 1 },
    ]);

    const createObjectURL = vi.fn(() => 'blob:mock-url');
    const revokeObjectURL = vi.fn();
    const originalCreateObjectURL = URL.createObjectURL;
    const originalRevokeObjectURL = URL.revokeObjectURL;
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;

    const realCreateElement = document.createElement.bind(document);
    const anchor = realCreateElement('a');
    const clickSpy = vi.spyOn(anchor, 'click').mockImplementation(() => {});
    const createElementSpy = vi
      .spyOn(document, 'createElement')
      .mockImplementation((tagName: string, options?: ElementCreationOptions) => {
        if (tagName === 'a') return anchor;
        return realCreateElement(tagName, options);
      });

    try {
      await renderMain();
      await flush(); // getHistory resolves; the row renders
      await clickNav(/^run history$/i);

      fireEvent.click(screen.getByRole('button', { name: /export run-a/i }));
      await flush(); // exportRun's getHistoryRecord() await settles

      expect(api.getHistoryRecord).toHaveBeenCalledWith('run-a');
      expect(createObjectURL).toHaveBeenCalledTimes(1);
      expect(anchor.download).toBe('run-a.json');
      expect(clickSpy).toHaveBeenCalledTimes(1);
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:mock-url');
    } finally {
      createElementSpy.mockRestore();
      URL.createObjectURL = originalCreateObjectURL;
      URL.revokeObjectURL = originalRevokeObjectURL;
    }
  });

  it('exportRun failure (e.g. run pruned between the history poll and the click) renders a visible alert instead of an unhandled rejection', async () => {
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-a', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T10:00:00Z', eventCount: 1 },
    ]);
    vi.mocked(api.getHistoryRecord).mockRejectedValueOnce(new ApiError('not found', 404));

    await renderMain();
    await flush(); // getHistory resolves; the row renders
    await clickNav(/^run history$/i);

    fireEvent.click(screen.getByRole('button', { name: /export run-a/i }));
    await flush(); // exportRun's getHistoryRecord() rejection settles

    expect(screen.getByRole('alert').textContent).toMatch(/export failed.*not found/i);
  });

  it('a subsequent successful export clears the failure alert', async () => {
    const record: HistoryRecord = {
      runId: 'run-a',
      lane: 'conformant',
      uc: 'uc02',
      branch: '',
      state: 'passed',
      detail: 'ok',
      time: '2026-07-03T10:00:00Z',
      eventCount: 1,
      events: [{ seq: 1, time: '2026-07-03T10:00:00Z', type: 'run.started', runId: 'run-a', lane: 'conformant', uc: 'uc02' }],
    };
    vi.mocked(api.getHistory).mockResolvedValue([
      { runId: 'run-a', lane: 'conformant', uc: 'uc02', branch: '', state: 'passed', detail: 'ok', time: '2026-07-03T10:00:00Z', eventCount: 1 },
    ]);
    vi.mocked(api.getHistoryRecord).mockRejectedValueOnce(new ApiError('not found', 404));

    const createObjectURL = vi.fn(() => 'blob:mock-url');
    const revokeObjectURL = vi.fn();
    const originalCreateObjectURL = URL.createObjectURL;
    const originalRevokeObjectURL = URL.revokeObjectURL;
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;

    const realCreateElement = document.createElement.bind(document);
    const anchor = realCreateElement('a');
    const clickSpy = vi.spyOn(anchor, 'click').mockImplementation(() => {});
    const createElementSpy = vi
      .spyOn(document, 'createElement')
      .mockImplementation((tagName: string, options?: ElementCreationOptions) => {
        if (tagName === 'a') return anchor;
        return realCreateElement(tagName, options);
      });

    try {
      await renderMain();
      await flush(); // getHistory resolves; the row renders
      await clickNav(/^run history$/i);

      fireEvent.click(screen.getByRole('button', { name: /export run-a/i }));
      await flush();
      expect(screen.getByRole('alert').textContent).toMatch(/export failed/i);

      vi.mocked(api.getHistoryRecord).mockResolvedValueOnce(record);
      fireEvent.click(screen.getByRole('button', { name: /export run-a/i }));
      await flush();

      expect(screen.queryByRole('alert')).toBeNull();
      expect(clickSpy).toHaveBeenCalledTimes(1);
    } finally {
      createElementSpy.mockRestore();
      URL.createObjectURL = originalCreateObjectURL;
      URL.revokeObjectURL = originalRevokeObjectURL;
    }
  });
});

describe('App — resetPending over unreachable', () => {
  it('resetPending true AND getBootstrap rejecting renders the restart-required screen, not the daemon-down screen', async () => {
    await renderMain();
    await clickNav(/^systems$/i); // reset lives on the Systems destination
    vi.mocked(api.postReset).mockResolvedValue({ restartRequired: true });

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /confirm reset/i }));
    });
    await flush();

    expect(screen.getByText(/restart the kit to finish the reset/i)).toBeDefined();

    // The daemon flaps mid-restart — getBootstrap starts rejecting outright
    // (a network error, exactly the shape that otherwise renders the
    // daemon-down screen when resetPending is NOT set).
    vi.mocked(api.getBootstrap).mockRejectedValue(new Error('fetch failed'));
    await advance(2000); // BOOTSTRAP_POLL_MS

    expect(screen.getByText(/restart the kit to finish the reset/i)).toBeDefined();
    expect(screen.queryByText(/can't reach the kit daemon/i)).toBeNull();
  });
});

describe('App — bring your own', () => {
  it('fetches getBYO once on main-phase entry and renders a "Bring your own" section with BYOPanel', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: null },
      davinci: null,
      ingress: null,
    });

    await renderMain();
    await flush(); // getBYO resolves

    expect(api.getBYO).toHaveBeenCalledTimes(1);
    await clickNav(/^bring your own$/i);
    // "Bring your own" is both the nav label and BYOPanel's heading — scope
    // to the heading so the assertion isn't ambiguous.
    expect(screen.getByRole('heading', { name: 'Bring your own' })).toBeDefined();
    expect(screen.getByText(/connected — your ehr is the ehr lane's data source/i)).toBeDefined();
  });

  it("onSaved refetches getBYO (mock call count)", async () => {
    vi.mocked(api.getBYO).mockResolvedValue({ ehr: null, davinci: null, ingress: null });
    vi.mocked(api.putBYOEhr).mockResolvedValue({ restartRequired: true });

    await renderMain();
    await flush();
    expect(api.getBYO).toHaveBeenCalledTimes(1);
    await clickNav(/^bring your own$/i);

    const ehrSection = within(screen.getByText('EHR (data source)').closest('section') as HTMLElement);
    await act(async () => {
      fireEvent.change(ehrSection.getByLabelText(/data url/i), {
        target: { value: 'https://ehr.example.org/fhir' },
      });
    });
    await act(async () => {
      fireEvent.click(ehrSection.getByRole('button', { name: /^save$/i }));
    });
    await flush();

    expect(api.getBYO).toHaveBeenCalledTimes(2);
  });

  it('onRestart shows a confirm dialog ("Runs in progress will be reset.") and calls restartKit() through the same bridge path the restart-required screen uses', async () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: null },
      davinci: null,
      ingress: null,
    });
    vi.mocked(api.deleteBYOEhr).mockResolvedValue({ restartRequired: true });

    await renderMain();
    await flush();
    await clickNav(/^bring your own$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /restore demo data/i }));
    });
    await flush();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /restart the kit now/i }));
    });

    expect(
      screen.getByText(/restarting applies your change\. runs in progress will be reset\./i),
    ).toBeDefined();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^restart$/i }));
    });
    expect(bridge.restartKit).toHaveBeenCalled();
  });

  it('the browser-debug fallback (no bridge) renders manual-restart instructions instead of calling restartKit()', async () => {
    vi.mocked(bridge.canRestart).mockReturnValue(false);
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: null },
      davinci: null,
      ingress: null,
    });
    vi.mocked(api.deleteBYOEhr).mockResolvedValue({ restartRequired: true });

    await renderMain();
    await flush();
    await clickNav(/^bring your own$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /restore demo data/i }));
    });
    await flush();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /restart the kit now/i }));
    });

    expect(
      screen.getByText(/restarting applies your change\. runs in progress will be reset\./i),
    ).toBeDefined();
    expect(screen.getByText(/restart shnkitd manually/i)).toBeDefined();
    expect(screen.queryByRole('button', { name: /^restart$/i })).toBeNull();
    expect(bridge.restartKit).not.toHaveBeenCalled();
  });
});

describe('App — BYO lane surfaces', () => {
  it('neither swap applied → exactly today\'s UI (regression pin): 8 seeded cards, no banners, no FreeFormPanel/WatchPanel', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({ ehr: null, davinci: null, ingress: null });

    await renderMain();
    await flush();

    expect(screen.getAllByTestId(/^card-uc0\d$/)).toHaveLength(8);
    expect(screen.queryByText(/free-form/i)).toBeNull();
    expect(screen.queryByText(/watch for your system/i)).toBeNull();
  });

  it('byo.ehr.applied → the ehr lane renders FreeFormPanel INSTEAD of the seeded cards, with a swap+restore banner; conformant lane stays UCCards', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: true },
      davinci: null,
      ingress: null,
    });
    vi.mocked(api.getBYOPatients).mockResolvedValue([]);

    await renderMain();
    await flush(); // getBYO resolves

    // Default lane is conformant — still the seeded UCCards, untouched.
    expect(screen.getAllByTestId(/^card-uc0\d$/)).toHaveLength(8);
    // ... plus the conformant-under-ehr-swap banner since the
    // SAME repointed SoR backs the conformant lane's member resolution too.
    expect(
      screen.getByText(/seeded scenarios resolve their members against your connected server/i),
    ).toBeDefined();
    expect(screen.getByText(/the demo personas are synthetic/i)).toBeDefined();
    expect(screen.getByText(/your server carries the demo personas/i)).toBeDefined();
    expect(screen.getByText(/download the conformant seed bundle from the.*bring your own.*panel/i)).toBeDefined();

    // Switch to the ehr lane: cards disappear, FreeFormPanel + swap banner appear.
    await act(async () => {
      fireEvent.click(screen.getByRole('tab', { name: /plain ehr/i }));
    });

    expect(screen.queryAllByTestId(/^card-uc0\d$/)).toHaveLength(0);
    expect(screen.getByRole('heading', { name: /free-form/i })).toBeDefined();
    expect(screen.getByText(/your ehr is connected/i)).toBeDefined();
    expect(screen.getByText(/restore demo data in bring your own/i)).toBeDefined();
  });

  it('demoPersonas:false renders "does not carry the demo personas yet"', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: false },
      davinci: null,
      ingress: null,
    });

    await renderMain();
    await flush();

    expect(screen.getByText(/your server does not carry the demo personas yet/i)).toBeDefined();
    expect(screen.queryByText(/your server carries the demo personas/i)).toBeNull();
  });

  it('demoPersonas:null renders neither presence sentence — shown, never assumed', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: null },
      davinci: null,
      ingress: null,
    });

    await renderMain();
    await flush();

    // The requirement/remedy + mandatory synthetic-data sentences still render (unconditional)...
    expect(
      screen.getByText(/seeded scenarios resolve their members against your connected server/i),
    ).toBeDefined();
    // ...but neither tri-state presence sentence does.
    expect(screen.queryByText(/your server carries the demo personas/i)).toBeNull();
    expect(screen.queryByText(/your server does not carry the demo personas yet/i)).toBeNull();
  });

  it('byo.davinci.applied → WatchPanel renders ALONGSIDE the seeded conformant cards, with a coexistence banner', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: null,
      davinci: { clientId: 'partner-1', alg: 'RS384', publicKeyPem: '-----BEGIN PUBLIC KEY-----', applied: true },
      ingress: {
        baseUrl: 'http://127.0.0.1:9000',
        tokenUrl: 'http://127.0.0.1:9000/oauth/token',
        smartConfigUrl: 'http://127.0.0.1:9000/.well-known/smart-configuration',
        endpoints: ['/cds-services'],
      },
    });

    await renderMain();
    await flush();

    // Still conformant lane by default — cards stay, WatchPanel joins them.
    expect(screen.getAllByTestId(/^card-uc0\d$/)).toHaveLength(8);
    expect(screen.getByText(/watch for your system/i)).toBeDefined();
    expect(screen.getByText(/registered as an inbound ingress client/i)).toBeDefined();
    expect(screen.getByText(/the seeded conformant scenarios keep running/i)).toBeDefined();
  });

  it('a watch in flight (run.started uc "external", no terminal) disables the Run buttons with the watch-specific copy', async () => {
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-watch-1', lane: 'conformant', uc: 'external' },
        ],
        activeRunId: 'run-watch-1',
      }),
    );

    await renderMain();

    // `Run uc0X` scoping avoids the "Run history" nav button (also /^run /).
    for (const b of screen.getAllByRole('button', { name: /^run uc0\d$/i })) {
      expect(b).toBeDisabled();
    }
    expect(screen.getByRole('alert').textContent).toMatch(
      /watching for incoming flows — stop watching to run scenarios/i,
    );
  });
});

describe('App — BYO provider label threading', () => {
  it('ehr-lane live run under an applied EHR swap: the inspector FlowMap shows "Your EHR (FHIR data source)"', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: true },
      davinci: null,
      ingress: null,
    });
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-ehr-live', lane: 'ehr', uc: 'uc01' },
        ],
        activeRunId: 'run-ehr-live',
      }),
    );

    await renderMain();
    await flush(); // getBYO resolves

    const providerNode = document.querySelector('.insp [data-node="provider"]');
    expect(providerNode?.textContent).toBe('Your EHR (FHIR data source)');
  });

  it('a WATCH run (uc "external") under an applied Da Vinci swap: the inspector FlowMap shows "Your Da Vinci system"', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: null,
      davinci: { clientId: 'partner-1', alg: 'RS384', publicKeyPem: '-----BEGIN PUBLIC KEY-----', applied: true },
      ingress: {
        baseUrl: 'http://127.0.0.1:9000',
        tokenUrl: 'http://127.0.0.1:9000/oauth/token',
        smartConfigUrl: 'http://127.0.0.1:9000/.well-known/smart-configuration',
        endpoints: ['/cds-services'],
      },
    });
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-conf-live', lane: 'conformant', uc: 'external' },
        ],
        activeRunId: 'run-conf-live',
      }),
    );

    await renderMain();
    await flush(); // getBYO resolves

    const providerNode = document.querySelector('.insp [data-node="provider"]');
    expect(providerNode?.textContent).toBe('Your Da Vinci system');
  });

  it('a SEEDED conformant run (uc03) under an applied Da Vinci swap keeps the default label — the partner label is watch-only, never claimed for a driven/seeded run', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: null,
      davinci: { clientId: 'partner-1', alg: 'RS384', publicKeyPem: '-----BEGIN PUBLIC KEY-----', applied: true },
      ingress: {
        baseUrl: 'http://127.0.0.1:9000',
        tokenUrl: 'http://127.0.0.1:9000/oauth/token',
        smartConfigUrl: 'http://127.0.0.1:9000/.well-known/smart-configuration',
        endpoints: ['/cds-services'],
      },
    });
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-conf-live', lane: 'conformant', uc: 'uc03' },
        ],
        activeRunId: 'run-conf-live',
      }),
    );

    await renderMain();
    await flush(); // getBYO resolves

    const providerNode = document.querySelector('.insp [data-node="provider"]');
    expect(providerNode?.textContent).toBe('Provider system');
  });

  it('a history-reopened run (not the current latest run) keeps the default label — never retroactively relabeled from today\'s byo state', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: true },
      davinci: null,
      ingress: null,
    });
    // The current latest/live run is a DIFFERENT run than the one about to
    // be reopened from history.
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-ehr-live', lane: 'ehr', uc: 'uc01' },
        ],
        activeRunId: 'run-ehr-live',
      }),
    );
    const historyEntry: HistorySummary = {
      runId: 'run-hist',
      lane: 'ehr',
      uc: 'uc02',
      branch: '',
      state: 'passed',
      detail: 'ok',
      time: '2026-07-03T10:00:00Z',
      eventCount: 1,
    };
    vi.mocked(api.getHistory).mockResolvedValue([historyEntry]);
    vi.mocked(api.getHistoryRecord).mockResolvedValue({
      ...historyEntry,
      events: [{ seq: 1, time: '2026-07-03T10:00:00Z', type: 'run.started', runId: 'run-hist', lane: 'ehr', uc: 'uc02' }],
    });

    await renderMain();
    await flush(); // getBYO + getHistory resolve; the row renders
    await clickNav(/^run history$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /open run-hist/i }));
    });
    await flush(); // getHistoryRecord resolves

    const providerNode = document.querySelector('.insp [data-node="provider"]');
    expect(providerNode?.textContent).toBe('Plain EHR (seeded data source)');
  });

  it('a history-reopened FREEFORM run gets "Your EHR (FHIR data source)" regardless of latestRunId or today\'s byo state — provenance is in the record (uc "freeform") by construction, not state-derived', async () => {
    // byo has NO applied EHR swap at all today, and the current latest/live
    // run is a different, non-freeform run — neither would grant the label
    // under the byo/latestRunId rule. The freeform run's label comes purely
    // from its own recorded uc.
    vi.mocked(api.getBYO).mockResolvedValue({ ehr: null, davinci: null, ingress: null });
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-other-live', lane: 'ehr', uc: 'uc01' },
        ],
        activeRunId: 'run-other-live',
      }),
    );
    const historyEntry: HistorySummary = {
      runId: 'run-hist-freeform',
      lane: 'ehr',
      uc: 'freeform',
      branch: '',
      state: 'passed',
      detail: 'ok',
      time: '2026-07-03T10:00:00Z',
      eventCount: 1,
    };
    vi.mocked(api.getHistory).mockResolvedValue([historyEntry]);
    vi.mocked(api.getHistoryRecord).mockResolvedValue({
      ...historyEntry,
      events: [
        { seq: 1, time: '2026-07-03T10:00:00Z', type: 'run.started', runId: 'run-hist-freeform', lane: 'ehr', uc: 'freeform' },
      ],
    });

    await renderMain();
    await flush(); // getBYO + getHistory resolve; the row renders
    await clickNav(/^run history$/i);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /open run-hist-freeform/i }));
    });
    await flush(); // getHistoryRecord resolves

    const providerNode = document.querySelector('.insp [data-node="provider"]');
    expect(providerNode?.textContent).toBe('Your EHR (FHIR data source)');
  });
});

describe('App — update banner', () => {
  it('status.update.available renders a banner in the TopBar linking via openExternal', async () => {
    await renderMain();
    vi.mocked(api.getStatus).mockImplementation(() =>
      Promise.resolve({
        ...statusReady(),
        update: { available: true, latest: 'v1.4.0', url: 'https://github.com/SmartHealthNetwork/shn-kit/releases/v1.4.0' },
      }),
    );
    await advance(STATUS_POLL_MS);

    expect(screen.getByText(/new version of the kit is available/i)).toBeDefined();
    expect(document.querySelector('.topbar .update-banner')).not.toBeNull();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /view release/i }));
    });
    expect(bridge.openExternal).toHaveBeenCalledWith(
      'https://github.com/SmartHealthNetwork/shn-kit/releases/v1.4.0',
    );
  });

  // Regression row: status.update absent (never checked / dev build / old
  // daemon) must never render a banner out of nothing.
  it('status.update absent renders no banner', async () => {
    await renderMain();
    expect(screen.queryByText(/new version of the kit is available/i)).toBeNull();
    expect(document.querySelector('.update-banner')).toBeNull();
  });

  it('status.update.available:false renders no banner', async () => {
    await renderMain();
    vi.mocked(api.getStatus).mockImplementation(() =>
      Promise.resolve({ ...statusReady(), update: { available: false, latest: '', url: '' } }),
    );
    await advance(STATUS_POLL_MS);
    expect(screen.queryByText(/new version of the kit is available/i)).toBeNull();
  });

  it('dismiss hides the banner for the rest of the session — it does not reappear on a later poll of the SAME still-available update', async () => {
    await renderMain();
    vi.mocked(api.getStatus).mockImplementation(() =>
      Promise.resolve({
        ...statusReady(),
        update: { available: true, latest: 'v1.4.0', url: 'https://example.org/v1.4.0' },
      }),
    );
    await advance(STATUS_POLL_MS);
    expect(screen.getByText(/new version of the kit is available/i)).toBeDefined();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^dismiss$/i }));
    });
    expect(screen.queryByText(/new version of the kit is available/i)).toBeNull();

    // Further polls, still reporting the SAME available update, must not
    // resurrect the banner this session.
    await advance(STATUS_POLL_MS);
    expect(screen.queryByText(/new version of the kit is available/i)).toBeNull();
  });
});

describe('App — latestRunId fold', () => {
  // deriveProviderLabel's state-derived branch (`runId === latestRunId`) is
  // honest only for the CURRENT live/latest run (its own doc comment). Before
  // this fold, latestRunId never cleared on terminal — reopening the SAME
  // now-finished run later would still match `latestRunId` and get
  // relabeled from TODAY's byo state, which is exactly the retroactive claim
  // the doc comment forbids. Pinned against the SAME fixture the existing
  // "ehr-lane live run" test uses.
  it('latestRunId clears once the active run reaches terminal: reopening that SAME run from history afterward no longer gets the state-derived label', async () => {
    vi.mocked(api.getBYO).mockResolvedValue({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: true },
      davinci: null,
      ingress: null,
    });
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-ehr-live', lane: 'ehr', uc: 'uc01' },
        ],
        activeRunId: 'run-ehr-live',
      }),
    );
    // Set up from the start (this test only cares about the label logic, not
    // the history-fetch re-trigger mechanics already covered
    // elsewhere) — the row is available to reopen once it terminates below.
    const historyEntry: HistorySummary = {
      runId: 'run-ehr-live',
      lane: 'ehr',
      uc: 'uc01',
      branch: '',
      state: 'passed',
      detail: 'ok',
      time: '2026-07-03T14:00:00Z',
      eventCount: 2,
    };
    vi.mocked(api.getHistory).mockResolvedValue([historyEntry]);

    await renderMain();
    await flush(); // getBYO + getHistory resolve

    let providerNode = document.querySelector('.insp [data-node="provider"]');
    expect(providerNode?.textContent).toBe('Your EHR (FHIR data source)');

    // The run reaches terminal (activeRunId clears).
    vi.mocked(useEvents).mockReturnValue(
      eventsView({
        all: [
          { seq: 1, time: '2026-07-03T14:00:00Z', type: 'run.started', runId: 'run-ehr-live', lane: 'ehr', uc: 'uc01' },
          { seq: 2, time: '2026-07-03T14:00:05Z', type: 'run.finished', runId: 'run-ehr-live' },
        ],
        activeRunId: undefined,
      }),
    );
    await advance(STATUS_POLL_MS);

    // Reopen the SAME run from history — its events are still in the ring
    // (this test's `all` array still carries them), so this exercises the
    // label logic alone, not a history-fetch fallback.
    await clickNav(/^run history$/i);
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /open run-ehr-live/i }));
    });

    providerNode = document.querySelector('.insp [data-node="provider"]');
    expect(providerNode?.textContent).toBe('Plain EHR (seeded data source)');
  });
});
