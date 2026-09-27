// useRunEvents.ts — resolves a selected run's events ring-first, falling back
// to GET /api/history/{runId} when the ring no longer holds it (evicted, or
// the run predates this page load). One hook, so RunInspector renders a
// live run and a reopened historical run through the exact same path.
import { useEffect, useState } from 'react';
import type { KitEvent } from './types';
import type { EventsView } from './useEvents';
import { getHistoryRecord } from './api';

export type RunSource = 'live' | 'history' | 'loading' | 'missing';

export interface RunEventsResult {
  events: KitEvent[];
  source: RunSource;
}

export function useRunEvents(runId: string | undefined, events: EventsView): RunEventsResult {
  // Fetched history records, cached per runId so revisiting an already-fetched
  // historical run (e.g. via compare view) doesn't refetch. A result that lands
  // after the caller has moved to another run only fills the cache for its own
  // runId; it never shows for the run now selected.
  const [cache, setCache] = useState<ReadonlyMap<string, KitEvent[]>>(() => new Map());
  // The run whose history fetch failed. Selecting a run clears it while
  // rendering, so coming back to a run that failed fetches it again.
  const [missingFor, setMissingFor] = useState<string | undefined>(undefined);
  const [selectedRun, setSelectedRun] = useState(runId);
  if (selectedRun !== runId) {
    setSelectedRun(runId);
    setMissingFor(undefined);
  }

  const liveEvents = runId !== undefined ? events.byRun(runId) : [];
  const isLive = runId !== undefined && liveEvents.some((e) => e.type === 'run.started');
  const cached = runId !== undefined ? cache.get(runId) : undefined;
  const needsFetch = runId !== undefined && !isLive && cached === undefined;

  useEffect(() => {
    if (!needsFetch || runId === undefined) return;
    getHistoryRecord(runId).then(
      (record) => {
        const normalized = record.events ?? [];
        setCache((prev) => new Map(prev).set(runId, normalized));
        // A success supersedes an earlier failure for the same run (a 404 for a
        // run selected before its run.started, then a refetch once it left the
        // ring).
        setMissingFor((prev) => (prev === runId ? undefined : prev));
      },
      () => {
        // A 404 (run not in history) and any other fetch failure both
        // render the same honest "missing" state — RunSource has no
        // separate error variant, and a run this hook can't produce events
        // for is, from the UI's point of view, simply not available.
        setMissingFor(runId);
      },
    );
    // Only runId and whether it still needs a fetch drive this: byRun/isLive
    // are recomputed every render from the current props.
  }, [runId, needsFetch]);

  if (runId === undefined) return { events: [], source: 'missing' };
  if (isLive) return { events: liveEvents, source: 'live' };
  if (cached !== undefined) return { events: cached, source: 'history' };
  if (missingFor === runId) return { events: [], source: 'missing' };
  return { events: [], source: 'loading' };
}
