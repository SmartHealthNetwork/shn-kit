import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { useRunEvents } from './useRunEvents';
import type { EventsView } from './useEvents';
import type { KitEvent } from './types';

const { getHistoryRecordMock } = vi.hoisted(() => ({ getHistoryRecordMock: vi.fn() }));

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>();
  return { ...actual, getHistoryRecord: getHistoryRecordMock };
});

// Real ApiError comes through `actual` above (not mocked), so `instanceof`
// checks inside useRunEvents keep working against errors this test rejects with.
import { ApiError } from './api';

function evt(partial: Partial<KitEvent> & { seq: number; type: string; runId: string }): KitEvent {
  return { time: '2026-07-03T00:00:00Z', ...partial };
}

function fakeEventsView(rows: KitEvent[]): EventsView {
  return {
    all: rows,
    byRun: (runId: string) => rows.filter((e) => e.runId === runId),
    activeRunId: undefined,
    sseState: 'open',
  };
}

beforeEach(() => {
  getHistoryRecordMock.mockReset();
});

describe('useRunEvents', () => {
  it('ring holds the run: source "live", events from the ring, getHistoryRecord is not called', () => {
    const rows = [
      evt({ seq: 1, type: 'run.started', runId: 'run-1' }),
      evt({ seq: 2, type: 'run.finished', runId: 'run-1' }),
    ];
    const events = fakeEventsView(rows);

    const { result } = renderHook(() => useRunEvents('run-1', events));

    expect(result.current.source).toBe('live');
    expect(result.current.events).toEqual(rows);
    expect(getHistoryRecordMock).not.toHaveBeenCalled();
  });

  it('ring lacks the run: "loading" then "history" with the fetched record’s events', async () => {
    const historyEvents = [
      evt({ seq: 1, type: 'run.started', runId: 'run-2' }),
      evt({ seq: 2, type: 'run.finished', runId: 'run-2' }),
    ];
    let resolveFetch: (() => void) | undefined;
    getHistoryRecordMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveFetch = () =>
            resolve({
              runId: 'run-2',
              lane: 'ehr',
              uc: 'uc03',
              branch: 'covered',
              state: 'passed',
              detail: 'ok',
              time: '2026-07-03T00:00:00Z',
              eventCount: historyEvents.length,
              events: historyEvents,
            });
        }),
    );

    const events = fakeEventsView([]);
    const { result } = renderHook(() => useRunEvents('run-2', events));

    expect(result.current.source).toBe('loading');
    expect(getHistoryRecordMock).toHaveBeenCalledWith('run-2');

    resolveFetch?.();
    await waitFor(() => expect(result.current.source).toBe('history'));
    expect(result.current.events).toEqual(historyEvents);
  });

  it('a 404 resolves to source "missing"', async () => {
    getHistoryRecordMock.mockRejectedValue(new ApiError('run not found', 404));

    const events = fakeEventsView([]);
    const { result } = renderHook(() => useRunEvents('run-3', events));

    expect(result.current.source).toBe('loading');
    await waitFor(() => expect(result.current.source).toBe('missing'));
    expect(result.current.events).toEqual([]);
  });

  it('runId undefined: source "missing", no fetch', () => {
    const events = fakeEventsView([]);
    const { result } = renderHook(() => useRunEvents(undefined, events));

    expect(result.current.source).toBe('missing');
    expect(result.current.events).toEqual([]);
    expect(getHistoryRecordMock).not.toHaveBeenCalled();
  });

  it('switching runId cancels stale application of an out-of-order resolution', async () => {
    let resolveFirst: (() => void) | undefined;
    const firstEvents = [evt({ seq: 1, type: 'run.started', runId: 'run-a' })];
    const secondEvents = [evt({ seq: 1, type: 'run.started', runId: 'run-b' })];

    getHistoryRecordMock.mockImplementation((runId: string) => {
      if (runId === 'run-a') {
        return new Promise((resolve) => {
          resolveFirst = () =>
            resolve({
              runId: 'run-a',
              lane: 'ehr',
              uc: 'uc03',
              branch: 'covered',
              state: 'passed',
              detail: 'ok',
              time: '2026-07-03T00:00:00Z',
              eventCount: firstEvents.length,
              events: firstEvents,
            });
        });
      }
      return Promise.resolve({
        runId: 'run-b',
        lane: 'ehr',
        uc: 'uc03',
        branch: 'covered',
        state: 'passed',
        detail: 'ok',
        time: '2026-07-03T00:00:00Z',
        eventCount: secondEvents.length,
        events: secondEvents,
      });
    });

    const events = fakeEventsView([]);
    const { result, rerender } = renderHook(({ runId }) => useRunEvents(runId, events), {
      initialProps: { runId: 'run-a' as string | undefined },
    });

    expect(result.current.source).toBe('loading');

    // Navigate to run-b before run-a's fetch resolves.
    rerender({ runId: 'run-b' });
    await waitFor(() => expect(result.current.source).toBe('history'));
    expect(result.current.events).toEqual(secondEvents);

    // Now let the stale run-a resolution land — it must NOT clobber run-b's
    // already-applied events (the ref-guarded out-of-order check).
    resolveFirst?.();
    await new Promise((r) => setTimeout(r, 0));

    expect(result.current.source).toBe('history');
    expect(result.current.events).toEqual(secondEvents);
  });
});
