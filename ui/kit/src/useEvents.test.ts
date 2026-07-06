import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useEvents } from './useEvents';
import { eventsUrl } from './api';
import type { KitEvent } from './types';

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  closed = false;
  closeCalls = 0;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
    this.closeCalls += 1;
  }

  emit(evt: KitEvent) {
    this.onmessage?.({ data: JSON.stringify(evt) } as MessageEvent);
  }
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal('EventSource', FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function evt(partial: Partial<KitEvent> & { seq: number; type: string }): KitEvent {
  return { time: '2026-07-03T00:00:00Z', ...partial };
}

describe('useEvents', () => {
  it('connects to eventsUrl(token) and accumulates events ordered by seq', () => {
    const { result } = renderHook(() => useEvents('tok-1'));
    const source = FakeEventSource.instances[0];
    expect(source.url).toBe(eventsUrl('tok-1'));

    act(() => {
      source.emit(evt({ seq: 1, type: 'child.state' }));
      source.emit(evt({ seq: 2, type: 'child.state' }));
    });

    expect(result.current.all.map((e) => e.seq)).toEqual([1, 2]);
  });

  it('tracks activeRunId from run.started to run.finished', () => {
    const { result } = renderHook(() => useEvents('tok-1'));
    const source = FakeEventSource.instances[0];

    act(() => {
      source.emit(evt({ seq: 1, type: 'run.started', runId: 'run-3' }));
    });
    expect(result.current.activeRunId).toBe('run-3');

    act(() => {
      source.emit(evt({ seq: 2, type: 'run.finished', runId: 'run-3' }));
    });
    expect(result.current.activeRunId).toBeUndefined();
  });

  it('run.failed is also terminal', () => {
    const { result } = renderHook(() => useEvents('tok-1'));
    const source = FakeEventSource.instances[0];

    act(() => {
      source.emit(evt({ seq: 1, type: 'run.started', runId: 'run-9' }));
    });
    expect(result.current.activeRunId).toBe('run-9');

    act(() => {
      source.emit(evt({ seq: 2, type: 'run.failed', runId: 'run-9' }));
    });
    expect(result.current.activeRunId).toBeUndefined();
  });

  it('byRun returns only events stamped with the given runId', () => {
    const { result } = renderHook(() => useEvents('tok-1'));
    const source = FakeEventSource.instances[0];

    act(() => {
      source.emit(evt({ seq: 1, type: 'run.started', runId: 'run-3' }));
      source.emit(evt({ seq: 2, type: 'child.state', child: 'gateway' }));
      source.emit(evt({ seq: 3, type: 'run.finished', runId: 'run-3' }));
    });

    const forRun3 = result.current.byRun('run-3');
    expect(forRun3.map((e) => e.seq)).toEqual([1, 3]);
  });

  it('onerror -> reconnecting; subsequent onopen -> open', () => {
    const { result } = renderHook(() => useEvents('tok-1'));
    const source = FakeEventSource.instances[0];

    expect(result.current.sseState).toBe('connecting');

    act(() => {
      source.onerror?.();
    });
    expect(result.current.sseState).toBe('reconnecting');

    act(() => {
      source.onopen?.();
    });
    expect(result.current.sseState).toBe('open');
  });

  it('caps all at the last 2000 events, dropping the oldest', () => {
    const { result } = renderHook(() => useEvents('tok-1'));
    const source = FakeEventSource.instances[0];

    act(() => {
      for (let i = 1; i <= 2005; i++) {
        source.emit(evt({ seq: i, type: 'child.state' }));
      }
    });

    expect(result.current.all.length).toBe(2000);
    expect(result.current.all[0].seq).toBe(6);
    expect(result.current.all[result.current.all.length - 1].seq).toBe(2005);
  });

  it('does not construct an EventSource when token is undefined', () => {
    renderHook(() => useEvents(undefined));
    expect(FakeEventSource.instances.length).toBe(0);
  });

  it('unmount closes the EventSource', () => {
    const { unmount } = renderHook(() => useEvents('tok-1'));
    const source = FakeEventSource.instances[0];
    expect(source.closed).toBe(false);

    unmount();

    expect(source.closed).toBe(true);
    expect(source.closeCalls).toBe(1);
  });

  it('a token change closes the old EventSource and opens a new one with the new token in its URL', () => {
    const { rerender } = renderHook(({ token }) => useEvents(token), {
      initialProps: { token: 'tok-1' },
    });

    const first = FakeEventSource.instances[0];
    expect(first.url).toBe(eventsUrl('tok-1'));
    expect(first.closed).toBe(false);

    rerender({ token: 'tok-2' });

    expect(first.closed).toBe(true);
    expect(FakeEventSource.instances.length).toBe(2);
    const second = FakeEventSource.instances[1];
    expect(second.url).toBe(eventsUrl('tok-2'));
  });
});
