import { useState } from 'react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { BridgingPanel } from './BridgingPanel';
import {
  BRIDGING_REMOTE_CAPTION,
  CONTRACT_LINE_EXPLAINER,
  DEMO_MODE_BADGE,
  ENGINE_EXHIBIT_FRAMING,
  REFUSAL_EXHIBIT_FRAMING,
} from './bridgingmeta';
import type { EventsView } from './useEvents';
import type { KitEvent, Probe, Register, RunResult, StatusResponse } from './types';

// vi.mock factories are hoisted above the rest of the module, so ApiError
// must be created through vi.hoisted (mirrors UCCards.test.tsx/WatchPanel.test.tsx).
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
  postBridgingDemo: vi.fn(),
  postBridgingExhibit: vi.fn(),
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

function okProbe(name: string): Probe {
  return { name, ok: true, detail: `${name}: ok` };
}
function redProbe(name: string, detail: string): Probe {
  return { name, ok: false, detail };
}

function statusWith(bridging?: StatusResponse['bridging']): StatusResponse {
  return { children: [], bridging };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.postRun).mockResolvedValue({ runId: 'run-1' });
  vi.mocked(api.postBridgingDemo).mockResolvedValue({ demoMode: true });
  vi.mocked(api.postBridgingExhibit).mockResolvedValue({
    kind: 'carry',
    lossReports: [{ module: 'pa.dtr 2.2->2.1', source: '2.2', target: '2.1', carried: [{ path: 'item.answer.extension:itemWeight', detail: 'carried' }] }],
    restored: true,
  });
});

function renderPanel(overrides: Partial<Parameters<typeof BridgingPanel>[0]> = {}) {
  const onSelectRun = vi.fn();
  const utils = render(
    <BridgingPanel
      status={statusWith({ demoMode: false, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') })}
      events={events()}
      results={[]}
      onSelectRun={onSelectRun}
      {...overrides}
    />,
  );
  return { ...utils, onSelectRun };
}

describe('BridgingPanel — feature absent', () => {
  it('renders a feature-unavailable state when status.bridging is absent, and nothing else', () => {
    render(<BridgingPanel status={statusWith(undefined)} events={events()} results={[]} onSelectRun={vi.fn()} />);
    expect(screen.getByText(/isn't available on this kit build/i)).toBeDefined();
    expect(screen.queryByText('Run bridged exchange')).toBeNull();
    expect(screen.queryByText(DEMO_MODE_BADGE)).toBeNull();
  });

  it('also shows feature-unavailable when status itself is undefined', () => {
    render(<BridgingPanel status={undefined} events={events()} results={[]} onSelectRun={vi.fn()} />);
    expect(screen.getByText(/isn't available on this kit build/i)).toBeDefined();
  });
});

describe('BridgingPanel — explainer cards', () => {
  it('renders the contract-line explainer and all three step classes', () => {
    renderPanel();
    expect(screen.getByText(CONTRACT_LINE_EXPLAINER.overview)).toBeDefined();
    expect(screen.getByText('Full')).toBeDefined();
    expect(screen.getByText('Carry')).toBeDefined();
    expect(screen.getByText('Gated')).toBeDefined();
  });

  it('register flip changes the explainer copy', async () => {
    // BridgingPanel is fully controlled (App owns `register`) — a local
    // stateful wrapper is required to observe the flip, mirroring how App
    // itself wires RegisterSwitch through onRegister.
    function Wrapper() {
      const [register, setRegister] = useState<Register>('overview');
      return (
        <BridgingPanel
          status={statusWith({ demoMode: false, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') })}
          register={register}
          onRegister={setRegister}
          events={events()}
          results={[]}
          onSelectRun={vi.fn()}
        />
      );
    }
    const user = userEvent.setup();
    render(<Wrapper />);
    expect(screen.getByText(CONTRACT_LINE_EXPLAINER.overview)).toBeDefined();
    expect(screen.queryByText(CONTRACT_LINE_EXPLAINER.technical)).toBeNull();

    await user.click(screen.getByRole('button', { name: /technical/i }));

    expect(screen.getByText(CONTRACT_LINE_EXPLAINER.technical)).toBeDefined();
    expect(screen.queryByText(CONTRACT_LINE_EXPLAINER.overview)).toBeNull();
  });
});

describe('BridgingPanel — probe states', () => {
  it('shows a red probe with its Detail text', () => {
    renderPanel({
      status: statusWith({
        demoMode: false,
        peer: redProbe('bridge-demo-payer', 'holder "bridge-demo" not found in registrar feed'),
      }),
    });
    expect(screen.getByText('holder "bridge-demo" not found in registrar feed')).toBeDefined();
  });

  it('shows "not configured" honestly when a probe is absent (never a fabricated red)', () => {
    renderPanel({ status: statusWith({ demoMode: false }) });
    expect(screen.getAllByText('not configured for this Kit')).toHaveLength(2);
  });
});

describe('BridgingPanel — demo mode off', () => {
  it('does not show the badge', () => {
    renderPanel({ status: statusWith({ demoMode: false, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }) });
    expect(screen.queryByText(DEMO_MODE_BADGE)).toBeNull();
  });

  it('disables "Run bridged exchange" with honest copy naming demo mode as the missing precondition', () => {
    renderPanel({ status: statusWith({ demoMode: false, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }) });
    const btn = screen.getByRole('button', { name: 'Run bridged exchange' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('Turn on the compatibility simulation below to run this.')).toBeDefined();
  });

  it('the toggle calls postBridgingDemo(true) with an explicit body when turned on', async () => {
    const user = userEvent.setup();
    renderPanel({ status: statusWith({ demoMode: false, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }) });
    await user.click(screen.getByRole('checkbox', { name: /compatibility simulation/i }));
    expect(api.postBridgingDemo).toHaveBeenCalledWith(true);
  });
});

describe('BridgingPanel — demo mode on', () => {
  function onStatus(): StatusResponse {
    return statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') });
  }

  it('shows the pinned DEMO_MODE_BADGE verbatim', () => {
    renderPanel({ status: onStatus() });
    expect(DEMO_MODE_BADGE).toBe(
      'Compatibility simulation active — this gateway routes as a build that predates the newer contract lines. Your registration and normal scenarios are unaffected.',
    );
    expect(screen.getByText(DEMO_MODE_BADGE)).toBeDefined();
  });

  it('shows the pinned BRIDGING_REMOTE_CAPTION verbatim', () => {
    renderPanel({ status: onStatus() });
    expect(BRIDGING_REMOTE_CAPTION).toBe(
      'the demo counterparty is a Smart Health Network-operated gateway on the preview network — this traffic is real and crosses the network',
    );
    expect(screen.getByText(BRIDGING_REMOTE_CAPTION)).toBeDefined();
  });

  it('with NO demo counterparty configured (no peer probe), the remote caption does not render — "this traffic is real" has no referent', () => {
    renderPanel({ status: statusWith({ demoMode: true, refusePeer: okProbe('bridge-demo-refuse') }) });
    expect(screen.queryByText(BRIDGING_REMOTE_CAPTION)).toBeNull();
  });

  it('enables "Run bridged exchange" once demo mode is on and the peer probe is green', () => {
    renderPanel({ status: onStatus() });
    const btn = screen.getByRole('button', { name: 'Run bridged exchange' });
    expect((btn as HTMLButtonElement).disabled).toBe(false);
  });

  it('clicking "Run bridged exchange" posts conformant/uc03/bridge-demo and selects the new run', async () => {
    const user = userEvent.setup();
    const { onSelectRun } = renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run bridged exchange' }));
    expect(api.postRun).toHaveBeenCalledWith('conformant', 'uc03', 'bridge-demo');
    expect(onSelectRun).toHaveBeenCalledWith('run-1');
  });

  it('the toggle calls postBridgingDemo(false) with an explicit body when turned off', async () => {
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('checkbox', { name: /compatibility simulation/i }));
    expect(api.postBridgingDemo).toHaveBeenCalledWith(false);
  });

  it('a 409 from the toggle surfaces the UCCards in-flight-notice idiom', async () => {
    vi.mocked(api.postBridgingDemo).mockRejectedValueOnce(new ApiError('conflict', 409));
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('checkbox', { name: /compatibility simulation/i }));
    expect(
      await screen.findByText('A run is already in flight — wait for it to finish before starting another.'),
    ).toBeDefined();
  });

  it('a 409 from the wire run posts the in-flight notice', async () => {
    vi.mocked(api.postRun).mockRejectedValueOnce(new ApiError('conflict', 409));
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run bridged exchange' }));
    expect(
      await screen.findByText('A run is already in flight — wait for it to finish before starting another.'),
    ).toBeDefined();
  });
});

describe('BridgingPanel — App-computed disabledReason gates the wire actions', () => {
  it('disables "Run bridged exchange" with App\'s own disabledReason even when demo mode is on and the probe is green', () => {
    renderPanel({
      status: statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }),
      disabledReason: 'A run is in flight — wait for it to finish before starting another.',
    });
    const btn = screen.getByRole('button', { name: 'Run bridged exchange' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getAllByText('A run is in flight — wait for it to finish before starting another.').length).toBeGreaterThan(0);
  });
});

describe('BridgingPanel — gating truth table (demo mode on, disabledReason clear)', () => {
  it('(a) peer ABSENT gates "Run bridged exchange" with the "not configured" copy — never a fabricated red', () => {
    renderPanel({
      status: statusWith({ demoMode: true, refusePeer: okProbe('bridge-demo-refuse') }),
    });
    const btn = screen.getByRole('button', { name: 'Run bridged exchange' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('No demo counterparty is configured for this Kit.')).toBeDefined();
  });

  it('(b) peer RED gates "Run bridged exchange" with its Detail named in the disabled copy', () => {
    renderPanel({
      status: statusWith({
        demoMode: true,
        peer: redProbe('bridge-demo-payer', 'holder "bridge-demo" declares no contract-version line beyond the 2.0 baseline'),
        refusePeer: okProbe('bridge-demo-refuse'),
      }),
    });
    const btn = screen.getByRole('button', { name: 'Run bridged exchange' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(
      screen.getByText(
        'Demo counterparty unavailable: holder "bridge-demo" declares no contract-version line beyond the 2.0 baseline',
      ),
    ).toBeDefined();
  });

  it('(c) refusePeer ABSENT gates the refusal wire exhibit with the "not configured" copy — never a fabricated red', () => {
    renderPanel({
      status: statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer') }),
    });
    const btn = screen.getByRole('button', { name: 'Run refusal wire exhibit' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('No refusal-demo counterparty is configured for this Kit.')).toBeDefined();
  });
});

describe('BridgingPanel — refusal exhibit', () => {
  function onStatus(): StatusResponse {
    return statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') });
  }

  it('shows the pinned REFUSAL_EXHIBIT_FRAMING before running anything', () => {
    renderPanel({ status: onStatus() });
    expect(REFUSAL_EXHIBIT_FRAMING).toBe(
      'a refusal is the successful outcome here: the network refuses loudly rather than guessing at clinical content, and nothing is sent',
    );
    expect(screen.getByText(REFUSAL_EXHIBIT_FRAMING)).toBeDefined();
  });

  it('disables the refusal wire part when the refusePeer probe is red, naming the detail', () => {
    renderPanel({
      status: statusWith({
        demoMode: true,
        peer: okProbe('bridge-demo-payer'),
        refusePeer: redProbe('bridge-demo-refuse', 'holder "bridge-demo-refuse" declares no pa.pas line beyond the 2.0 baseline'),
      }),
    });
    const btn = screen.getByRole('button', { name: 'Run refusal wire exhibit' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(
      screen.getByText(/refusal-demo counterparty unavailable: holder "bridge-demo-refuse" declares no pa.pas line/i),
    ).toBeDefined();
  });

  it('clicking the wire part posts ehr/uc03/bridge-refuse', async () => {
    const user = userEvent.setup();
    const { onSelectRun } = renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run refusal wire exhibit' }));
    expect(api.postRun).toHaveBeenCalledWith('ehr', 'uc03', 'bridge-refuse');
    expect(onSelectRun).toHaveBeenCalledWith('run-1');
  });

  it('the engine part is available even when demo mode is off (not gated on it)', () => {
    renderPanel({ status: statusWith({ demoMode: false, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }) });
    const btn = screen.getByRole('button', { name: 'Run refusal engine exhibit' });
    expect((btn as HTMLButtonElement).disabled).toBe(false);
  });

  it('running the engine part posts kind "refusal" and renders the typed text + semanticChange, framed by ENGINE_EXHIBIT_FRAMING', async () => {
    vi.mocked(api.postBridgingExhibit).mockResolvedValueOnce({
      kind: 'refusal',
      refusal: 'semantic-change refusal: certificationType/requestType/location[x]/relationship',
      semanticChange: true,
    });
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run refusal engine exhibit' }));

    expect(api.postBridgingExhibit).toHaveBeenCalledWith('refusal');
    expect(ENGINE_EXHIBIT_FRAMING).toBe(
      'engine demonstration over frozen reference content — the same modules your live legs route through',
    );
    expect(await screen.findByText(ENGINE_EXHIBIT_FRAMING)).toBeDefined();
    expect(
      screen.getByText('semantic-change refusal: certificationType/requestType/location[x]/relationship'),
    ).toBeDefined();
    expect(screen.getByText(/semantic change: yes/i)).toBeDefined();
  });

  it('running the carry exhibit posts kind "carry" and renders the Carried paths + restored, framed by ENGINE_EXHIBIT_FRAMING', async () => {
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run carry content exhibit' }));

    expect(api.postBridgingExhibit).toHaveBeenCalledWith('carry');
    expect(await screen.findByText(ENGINE_EXHIBIT_FRAMING)).toBeDefined();
    expect(screen.getByText(/item\.answer\.extension:itemWeight/)).toBeDefined();
    expect(screen.getByText(/restored: yes/i)).toBeDefined();
  });

  // Both exhibits are documented to answer 500 (never a 200) when kitd's own
  // fixtures don't behave as promised (bridging.go), so restored:false /
  // semanticChange:false should be unreachable via the real endpoint — this
  // pins the defensive UI-side visual differentiation anyway, rather than
  // rendering an unexpected false with the same neutral styling as the
  // expected-true case.
  it('an unexpected restored:false gets the visual-differentiation class, not neutral styling', async () => {
    vi.mocked(api.postBridgingExhibit).mockResolvedValueOnce({
      kind: 'carry',
      lossReports: [],
      restored: false,
    });
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run carry content exhibit' }));

    const el = await screen.findByText(/restored: no/i);
    expect(el.className).toContain('bridging-exhibit-unexpected');
  });

  it('an unexpected semanticChange:false gets the visual-differentiation class, not neutral styling', async () => {
    vi.mocked(api.postBridgingExhibit).mockResolvedValueOnce({
      kind: 'refusal',
      refusal: 'accepted (unexpected)',
      semanticChange: false,
    });
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run refusal engine exhibit' }));

    const el = await screen.findByText(/semantic change: no/i);
    expect(el.className).toContain('bridging-exhibit-unexpected');
  });

  it('the expected-true cases get NEITHER the unexpected class (regression pin against over-flagging)', async () => {
    const user = userEvent.setup();
    renderPanel({ status: onStatus() });
    await user.click(screen.getByRole('button', { name: 'Run carry content exhibit' }));
    const el = await screen.findByText(/restored: yes/i);
    expect(el.className).not.toContain('bridging-exhibit-unexpected');
  });
});

describe('BridgingPanel — static route-refusal grammar', () => {
  it('renders the exact grammar example text as static copy', () => {
    renderPanel();
    expect(
      screen.getByText(
        (_, el) =>
          el?.tagName.toLowerCase() === 'pre' &&
          (el.textContent ?? '').includes('no shared contract line for pa.pas'),
      ),
    ).toBeDefined();
  });
});

describe('BridgingPanel — view in inspector', () => {
  it('shows "View in inspector" for a completed bridged-exchange run and calls onSelectRun', async () => {
    const user = userEvent.setup();
    const results: RunResult[] = [
      { runId: 'run-completed', lane: 'conformant', uc: 'uc03', branch: 'bridge-demo', state: 'passed', detail: 'ok' },
    ];
    const { onSelectRun } = renderPanel({
      status: statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }),
      results,
    });
    const row = screen.getByText('Run bridged exchange').closest('.bridging-action-row') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: /view in inspector/i }));
    expect(onSelectRun).toHaveBeenCalledWith('run-completed');
  });
});

// run.started carries branch (additive). These prove the spinner attributes
// off it, not off the coarser lane+uc a plain UCCards uc03 row (branch "")
// can ALSO produce for either lane.
describe('BridgingPanel — spinner attribution keys on branch', () => {
  function runStarted(runId: string, lane: string, uc: string, branch: string): KitEvent {
    return { seq: 1, time: 't', type: 'run.started', runId, lane, uc, branch };
  }

  // getByRole('heading', ...), not getByText — the WireRunAction title also
  // doubles as its idle-state button label (runLabel), so a plain text
  // match is ambiguous whenever no `latest` result exists for the row.
  function spinnerRow(title: string): HTMLElement {
    return screen.getByRole('heading', { name: title, level: 3 }).closest('.bridging-action-row') as HTMLElement;
  }

  it('branch "bridge-demo" active lights only the bridged-exchange spinner', () => {
    renderPanel({
      status: statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }),
      events: events('run-a', [runStarted('run-a', 'conformant', 'uc03', 'bridge-demo')]),
    });
    expect(within(spinnerRow('Run bridged exchange')).getByText('Running…')).toBeDefined();
    expect(within(spinnerRow('Wire part — refuse before forward')).queryByText('Running…')).toBeNull();
  });

  it('branch "bridge-refuse" active lights only the refusal-wire spinner', () => {
    renderPanel({
      status: statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }),
      events: events('run-b', [runStarted('run-b', 'ehr', 'uc03', 'bridge-refuse')]),
    });
    expect(within(spinnerRow('Wire part — refuse before forward')).getByText('Running…')).toBeDefined();
    expect(within(spinnerRow('Run bridged exchange')).queryByText('Running…')).toBeNull();
  });

  it('two concurrent-ish runs sharing lane+uc: the spinner attributes to the branch-matching exhibit only', () => {
    // Both events below share lane "ehr" + uc "uc03" (the same pair a
    // plain UCCards uc03 row's branch-"" run can ALSO produce on either
    // lane) — a collision the pre-rider lane+uc-only derivation could not
    // resolve: it would have shown ONLY the refusal-wire spinner here,
    // since lane "ehr" matched its check and "conformant" never matched
    // the bridged-exchange one. Branch now decides instead: the ACTIVE run
    // (run-a, branch "bridge-demo") lights the bridged-exchange spinner
    // despite its lane also matching the refusal-wire row's lane check.
    const all: KitEvent[] = [
      runStarted('run-a', 'ehr', 'uc03', 'bridge-demo'),
      runStarted('run-b', 'ehr', 'uc03', 'bridge-refuse'),
    ];
    renderPanel({
      status: statusWith({ demoMode: true, peer: okProbe('bridge-demo-payer'), refusePeer: okProbe('bridge-demo-refuse') }),
      events: events('run-a', all),
    });
    expect(within(spinnerRow('Run bridged exchange')).getByText('Running…')).toBeDefined();
    expect(within(spinnerRow('Wire part — refuse before forward')).queryByText('Running…')).toBeNull();
  });
});
