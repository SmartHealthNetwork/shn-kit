import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  WatchPanel,
  OUTSIDE_WINDOW_NOTE,
  WATCHING_NARRATION_NOTE,
  WATCH_PROVENANCE_LINE,
} from './WatchPanel';
import type { EventsView } from './useEvents';

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
  postWatch: vi.fn(),
  deleteWatch: vi.fn(),
  ApiError,
}));

import * as api from './api';

function events(activeRunId?: string): EventsView {
  return {
    all: [],
    byRun: () => [],
    activeRunId,
    sseState: 'open',
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.postWatch).mockResolvedValue({ runId: 'run-watch-1' });
  vi.mocked(api.deleteWatch).mockResolvedValue({
    runId: 'run-watch-1',
    lane: 'conformant',
    uc: 'external',
    branch: '',
    state: 'passed',
    detail: 'external activity window closed',
  });
});

describe('WatchPanel — idle state', () => {
  it('renders "Start watching" and the outside-window honesty sentence (pinned)', () => {
    render(<WatchPanel events={events()} onSelectRun={vi.fn()} />);

    expect(screen.getByRole('button', { name: /start watching/i })).toBeDefined();
    expect(screen.getByText(OUTSIDE_WINDOW_NOTE)).toBeDefined();
    expect(OUTSIDE_WINDOW_NOTE.toLowerCase()).toContain('start watching before your system sends');
  });

  it('shows the watch provenance line', () => {
    render(<WatchPanel events={events()} onSelectRun={vi.fn()} />);
    expect(screen.getByText(WATCH_PROVENANCE_LINE)).toBeDefined();
  });
});

describe('WatchPanel — start', () => {
  it('start calls postWatch, enters watching state (runId shown, narration note), and calls onSelectRun', async () => {
    const onSelectRun = vi.fn();
    const user = userEvent.setup();
    render(<WatchPanel events={events()} onSelectRun={onSelectRun} />);

    await user.click(screen.getByRole('button', { name: /start watching/i }));

    expect(api.postWatch).toHaveBeenCalledOnce();
    expect(await screen.findByText(/run-watch-1/)).toBeDefined();
    expect(screen.getByText(WATCHING_NARRATION_NOTE)).toBeDefined();
    expect(onSelectRun).toHaveBeenCalledWith('run-watch-1');
    expect(screen.getByRole('button', { name: /stop watching/i })).toBeDefined();
  });

  it('postWatch 409 renders an inline "a run is in flight" notice — not an error state (the S5 idiom)', async () => {
    vi.mocked(api.postWatch).mockRejectedValue(new ApiError('run in flight', 409));
    const user = userEvent.setup();
    render(<WatchPanel events={events()} onSelectRun={vi.fn()} />);

    await user.click(screen.getByRole('button', { name: /start watching/i }));

    const notice = await screen.findByRole('alert');
    expect(notice.textContent).toMatch(/a run is in flight/i);
    // still idle — Start watching is available again, not replaced by an error card.
    expect(screen.getByRole('button', { name: /start watching/i })).toBeDefined();
  });
});

describe('WatchPanel — stop', () => {
  it('stop calls deleteWatch and renders the result summary', async () => {
    const user = userEvent.setup();
    render(<WatchPanel events={events()} onSelectRun={vi.fn()} />);

    await user.click(screen.getByRole('button', { name: /start watching/i }));
    await screen.findByRole('button', { name: /stop watching/i });

    await user.click(screen.getByRole('button', { name: /stop watching/i }));

    expect(api.deleteWatch).toHaveBeenCalledOnce();
    expect(await screen.findByText('Passed')).toBeDefined();
    expect(screen.getByText(/external activity window closed/i)).toBeDefined();
  });

  it("clicking the result's View in inspector calls onSelectRun with the stopped watch's runId", async () => {
    const onSelectRun = vi.fn();
    const user = userEvent.setup();
    render(<WatchPanel events={events()} onSelectRun={onSelectRun} />);

    await user.click(screen.getByRole('button', { name: /start watching/i }));
    await screen.findByRole('button', { name: /stop watching/i });
    await user.click(screen.getByRole('button', { name: /stop watching/i }));

    await user.click(await screen.findByRole('button', { name: /view in inspector/i }));
    expect(onSelectRun).toHaveBeenLastCalledWith('run-watch-1');
  });
});

describe('WatchPanel — driven-run in flight', () => {
  it('disables Start watching while a driven run is already in flight (events.activeRunId set)', () => {
    render(<WatchPanel events={events('run-driven-1')} onSelectRun={vi.fn()} />);
    expect(screen.getByRole('button', { name: /start watching/i })).toBeDisabled();
  });
});
