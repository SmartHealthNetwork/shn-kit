// useRunEvents.ts — resolves a selected run's events ring-first, falling back
// to GET /api/history/{runId} when the ring no longer holds it (evicted, or
// the run predates this page load). One hook, so RunInspector renders a
// live run and a reopened historical run through the exact same path.
import { useEffect, useRef, useState } from 'react';
import type { KitEvent } from './types';
import type { EventsView } from './useEvents';
import { ApiError, getHistoryRecord } from './api';

export type RunSource = 'live' | 'history' | 'loading' | 'missing';

export interface RunEventsResult {
  events: KitEvent[];
  source: RunSource;
}

type HistoryStatus = 'idle' | 'loading' | 'missing';

export function useRunEvents(runId: string | undefined, events: EventsView): RunEventsResult {
  // Cached per runId so revisiting an already-fetched historical run (e.g.
  // via compare view) doesn't refetch.
  const cacheRef = useRef(new Map<string, KitEvent[]>());
  // Guards against an out-of-order resolution applying to a runId the caller
  // has since navigated away from (switching runId mid-fetch).
  const inFlightRef = useRef<string | undefined>(undefined);

  const [historyEvents, setHistoryEvents] = useState<KitEvent[] | undefined>(undefined);
  const [historyStatus, setHistoryStatus] = useState<HistoryStatus>('idle');

  const liveEvents = runId !== undefined ? events.byRun(runId) : [];
  const isLive = runId !== undefined && liveEvents.some((e) => e.type === 'run.started');

  useEffect(() => {
    if (runId === undefined || isLive) {
      inFlightRef.current = undefined;
      setHistoryStatus('idle');
      setHistoryEvents(undefined);
      return;
    }

    const cached = cacheRef.current.get(runId);
    if (cached) {
      setHistoryEvents(cached);
      setHistoryStatus('idle');
      return;
    }

    inFlightRef.current = runId;
    setHistoryStatus('loading');
    setHistoryEvents(undefined);

    getHistoryRecord(runId).then(
      (record) => {
        if (inFlightRef.current !== runId) return; // stale — runId moved on
        const normalized = record.events ?? [];
        cacheRef.current.set(runId, normalized);
        setHistoryEvents(normalized);
        setHistoryStatus('idle');
      },
      (err: unknown) => {
        if (inFlightRef.current !== runId) return; // stale — runId moved on
        // A 404 (run not in history) and any other fetch failure both
        // render the same honest "missing" state — RunSource has no
        // separate error variant, and a run this hook can't produce events
        // for is, from the UI's point of view, simply not available.
        if (err instanceof ApiError && err.status === 404) {
          setHistoryStatus('missing');
          return;
        }
        setHistoryStatus('missing');
      },
    );
    // `events` is deliberately excluded from the dependency array: byRun/isLive
    // are recomputed every render from the current props, so the effect only
    // needs to (re)fetch when runId or the live/ring verdict changes.
  }, [runId, isLive]);

  if (runId === undefined) return { events: [], source: 'missing' };
  if (isLive) return { events: liveEvents, source: 'live' };
  if (historyStatus === 'missing') return { events: [], source: 'missing' };
  if (historyEvents !== undefined) return { events: historyEvents, source: 'history' };
  return { events: [], source: 'loading' };
}
