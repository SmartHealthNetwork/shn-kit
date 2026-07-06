// useEvents.ts — subscribes to kitd's SSE event stream (/events) and exposes
// it as reactive state. Browser-native EventSource reconnect carries
// Last-Event-ID automatically — no custom retry loop here.
import { useEffect, useState } from 'react';
import type { KitEvent } from './types';
import { eventsUrl } from './api';

export type SSEState = 'connecting' | 'open' | 'reconnecting';

export interface EventsView {
  all: KitEvent[];
  byRun(runId: string): KitEvent[];
  activeRunId?: string;
  sseState: SSEState;
}

const CAP = 2000;
const TERMINAL_TYPES = new Set(['run.finished', 'run.failed']);

export function useEvents(token: string | undefined): EventsView {
  const [all, setAll] = useState<KitEvent[]>([]);
  const [activeRunId, setActiveRunId] = useState<string | undefined>(undefined);
  const [sseState, setSseState] = useState<SSEState>('connecting');

  useEffect(() => {
    if (token === undefined) return;

    setAll([]);
    setActiveRunId(undefined);
    setSseState('connecting');

    const es = new EventSource(eventsUrl(token));

    es.onopen = () => setSseState('open');
    es.onerror = () => setSseState('reconnecting');
    es.onmessage = (ev: MessageEvent) => {
      const parsed = JSON.parse(ev.data as string) as KitEvent;

      setAll((prev) => {
        const next = [...prev, parsed];
        return next.length > CAP ? next.slice(next.length - CAP) : next;
      });

      if (parsed.type === 'run.started') {
        setActiveRunId(parsed.runId);
      } else if (TERMINAL_TYPES.has(parsed.type)) {
        setActiveRunId((prev) => (prev === parsed.runId ? undefined : prev));
      }
    };

    return () => {
      es.close();
    };
  }, [token]);

  return {
    all,
    byRun: (runId: string) => all.filter((e) => e.runId === runId),
    activeRunId,
    sseState,
  };
}
