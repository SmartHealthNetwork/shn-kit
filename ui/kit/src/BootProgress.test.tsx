import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import BootProgress from './BootProgress';
import type { BootstrapResponse, ChildStatus, StatusResponse } from './types';

vi.mock('./bridge', () => ({
  canRestart: vi.fn(),
  restartKit: vi.fn(),
}));

import * as bridge from './bridge';

function boot(overrides: Partial<BootstrapResponse> = {}): BootstrapResponse {
  return { state: 'provisioning', verify: [], ...overrides };
}

function statusWith(childState: string, extra: Partial<ChildStatus> = {}): StatusResponse {
  return {
    children: [{ name: 'gateway', state: childState, detail: '', pid: 1, restarts: 0, ...extra }],
  };
}

// A multi-child fixture — statusWith above only ever builds a single
// `gateway` child, which can't exercise the FHIR-servers stage (gateway +
// the validator/data-server/br-provider trio at independent states).
function statusWithChildren(
  children: Array<Pick<ChildStatus, 'name' | 'state'> & Partial<ChildStatus>>,
  extra: Partial<StatusResponse> = {},
): StatusResponse {
  return {
    children: children.map((c, i) => ({ detail: '', pid: i + 1, restarts: 0, ...c })),
    ...extra,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(bridge.canRestart).mockReturnValue(false);
});

describe('BootProgress stage rows', () => {
  it('renders exactly the five stage labels', () => {
    render(<BootProgress boot={boot()} runsLive={false} />);
    for (const label of [
      'Sign in',
      'Provision on the Hub',
      'Start the Smart Gateway',
      'Verify the network',
      'Ready',
    ]) {
      expect(screen.getByText(label)).toBeDefined();
    }
  });

  it('Sign in is done once state has moved past signin-required/signing-in', () => {
    const { rerender } = render(<BootProgress boot={boot({ state: 'provisioning' })} runsLive={false} />);
    expect(screen.getByTestId('stage-signin').className).toContain('stage-done');

    rerender(<BootProgress boot={boot({ state: 'signing-in' })} runsLive={false} />);
    expect(screen.getByTestId('stage-signin').className).not.toContain('stage-done');
  });

  it('Provision on the Hub is done once provisioned, and shows the canonical subtitle', () => {
    render(<BootProgress boot={boot({ state: 'provisioning' })} runsLive={false} />);
    expect(
      screen.getByText(/Register this Kit as a provider participant on the preview Hub/),
    ).toBeDefined();
    expect(screen.getByTestId('stage-provision').className).not.toContain('stage-done');
  });

  it('Provision on the Hub is done once boot.state is provisioned', () => {
    render(<BootProgress boot={boot({ state: 'provisioned' })} runsLive={false} />);
    expect(screen.getByTestId('stage-provision').className).toContain('stage-done');
  });

  it('Start the Smart Gateway: done on ready, active on starting/restarting, failed style + detail on failed', () => {
    const { rerender } = render(
      <BootProgress boot={boot({ state: 'provisioned' })} status={statusWith('ready')} runsLive={false} />,
    );
    expect(screen.getByTestId('stage-gateway').className).toContain('stage-done');

    rerender(
      <BootProgress boot={boot({ state: 'provisioned' })} status={statusWith('starting')} runsLive={false} />,
    );
    expect(screen.getByTestId('stage-gateway').className).toContain('stage-active');

    rerender(
      <BootProgress boot={boot({ state: 'provisioned' })} status={statusWith('restarting')} runsLive={false} />,
    );
    expect(screen.getByTestId('stage-gateway').className).toContain('stage-active');

    rerender(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWith('failed', { detail: 'crashed: exit status 1' })}
        runsLive={false}
      />,
    );
    expect(screen.getByTestId('stage-gateway').className).toContain('stage-failed');
    expect(screen.getByText(/crashed: exit status 1/)).toBeDefined();
  });

  it('Verify the network: pending while empty, done once all 3 probes ok, failed naming the failing probe otherwise', () => {
    const { rerender } = render(
      <BootProgress boot={boot({ state: 'provisioned', verify: [] })} runsLive={false} />,
    );
    expect(screen.getByTestId('stage-verify').className).not.toContain('stage-failed');
    expect(screen.getByTestId('stage-verify').className).not.toContain('stage-done');

    rerender(
      <BootProgress
        boot={boot({
          state: 'provisioned',
          verify: [
            { name: 'discovery', ok: true, detail: 'reachable' },
            { name: 'registration', ok: true, detail: 'found in registrar feed' },
            { name: 'hosted-payer', ok: true, detail: 'ok' },
          ],
        })}
        runsLive={false}
      />,
    );
    expect(screen.getByTestId('stage-verify').className).toContain('stage-done');

    rerender(
      <BootProgress
        boot={boot({
          state: 'provisioned',
          verify: [
            { name: 'discovery', ok: true, detail: 'reachable' },
            { name: 'registration', ok: false, detail: 'holder not found in registrar feed' },
            { name: 'hosted-payer', ok: true, detail: 'ok' },
          ],
        })}
        runsLive={false}
      />,
    );
    const verifyStage = screen.getByTestId('stage-verify');
    expect(verifyStage.className).toContain('stage-failed');
    expect(verifyStage.textContent).toMatch(/registration: holder not found in registrar feed/);
  });

  it('Ready is done once runsLive', () => {
    const { rerender } = render(<BootProgress boot={boot({ state: 'provisioned' })} runsLive={false} />);
    expect(screen.getByTestId('stage-ready').className).not.toContain('stage-done');

    rerender(<BootProgress boot={boot({ state: 'provisioned' })} runsLive={true} />);
    expect(screen.getByTestId('stage-ready').className).toContain('stage-done');
  });

  it('the active stage renders a spinner; done/pending stages do not', () => {
    render(
      <BootProgress boot={boot({ state: 'provisioned' })} status={statusWith('starting')} runsLive={false} />,
    );

    const gatewayStage = screen.getByTestId('stage-gateway');
    expect(gatewayStage.className).toContain('stage-active');
    expect(within(gatewayStage).queryByTestId('stage-spinner')).not.toBeNull();

    const signinStage = screen.getByTestId('stage-signin');
    expect(signinStage.className).toContain('stage-done');
    expect(within(signinStage).queryByTestId('stage-spinner')).toBeNull();

    const readyStage = screen.getByTestId('stage-ready');
    expect(readyStage.className).toContain('stage-pending');
    expect(within(readyStage).queryByTestId('stage-spinner')).toBeNull();
  });

  it('shows the slow-boot hint while a stage is active, and hides it once Ready', () => {
    const { rerender } = render(
      <BootProgress boot={boot({ state: 'provisioned' })} status={statusWith('starting')} runsLive={false} />,
    );
    expect(screen.getByText(/first launch starts the local servers/i)).toBeDefined();

    rerender(
      <BootProgress
        boot={boot({
          state: 'provisioned',
          verify: [
            { name: 'discovery', ok: true, detail: 'reachable' },
            { name: 'registration', ok: true, detail: 'found in registrar feed' },
            { name: 'hosted-payer', ok: true, detail: 'ok' },
          ],
        })}
        status={statusWith('ready')}
        runsLive={true}
      />,
    );
    expect(screen.queryByText(/first launch starts the local servers/i)).toBeNull();
  });

  it('does not show the slow-boot hint on a failed boot', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWith('failed', { detail: 'crashed: exit status 1' })}
        runsLive={false}
      />,
    );
    expect(screen.queryByText(/first launch starts the local servers/i)).toBeNull();
  });
});

describe('BootProgress gateway failure action', () => {
  it('shows a Restart the Kit button when canRestart() is true', () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWith('failed', { detail: 'crashed: exit status 1' })}
        runsLive={false}
      />,
    );
    expect(screen.getByRole('button', { name: /restart the kit/i })).toBeDefined();
  });

  it('shows the manual-restart sentence when canRestart() is false', () => {
    vi.mocked(bridge.canRestart).mockReturnValue(false);
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWith('failed', { detail: 'crashed: exit status 1' })}
        runsLive={false}
      />,
    );
    expect(screen.queryByRole('button', { name: /restart the kit/i })).toBeNull();
    expect(screen.getByText(/restart shnkitd/i)).toBeDefined();
  });

  it('no restart action is shown when the gateway child has not failed', () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    render(
      <BootProgress boot={boot({ state: 'provisioned' })} status={statusWith('ready')} runsLive={false} />,
    );
    expect(screen.queryByRole('button', { name: /restart the kit/i })).toBeNull();
    expect(screen.queryByText(/restart shnkitd/i)).toBeNull();
  });
});

describe('BootProgress FHIR-servers stage (FHIR-servers attribution)', () => {
  it('the Smart Gateway stage is done, and a distinct FHIR-servers stage shows serial per-server progress', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([
          { name: 'gateway', state: 'ready' },
          { name: 'validator', state: 'ready' },
          { name: 'data-server', state: 'starting' },
          { name: 'br-provider', state: 'starting' },
        ])}
        runsLive={false}
      />,
    );

    // The gateway itself is a fast Go binary — it checks off done well
    // before the trio finishes.
    expect(screen.getByTestId('stage-gateway').className).toContain('stage-done');

    const fhirStage = screen.getByTestId('stage-fhirservers');
    expect(within(fhirStage).getByText('Starting the FHIR servers')).toBeDefined();

    // Order-based derivation: the trio boots one at a time, so among the
    // two non-ready children only the FIRST in canonical order
    // (validator, data-server, br-provider) is genuinely in flight.
    const validatorRow = within(fhirStage).getByTestId('stage-sub-validator');
    expect(validatorRow.className).toContain('stage-sub-done');
    expect(validatorRow.textContent).toContain('Validator');

    const dataServerRow = within(fhirStage).getByTestId('stage-sub-data-server');
    expect(dataServerRow.className).toContain('stage-sub-active');
    expect(dataServerRow.textContent).toContain('Data server');

    const brProviderRow = within(fhirStage).getByTestId('stage-sub-br-provider');
    expect(brProviderRow.className).toContain('stage-sub-waiting');
    expect(brProviderRow.textContent).toContain('Provider system');
  });

  it('renders all three canonical sub-rows at the real first-boot moment — only validator present-and-starting, the other two ABSENT — as [active, waiting, waiting]', () => {
    // The trio boots serially, so status.children only carries the members
    // the supervisor has already reached. Early in the wait only the
    // validator exists; data-server + br-provider are genuinely queued
    // (absent), and must still render as waiting sub-rows so the picture is
    // a static three rows, not a list that grows 1→2→3.
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([
          { name: 'gateway', state: 'ready' },
          { name: 'validator', state: 'starting' },
        ])}
        runsLive={false}
      />,
    );

    const fhirStage = screen.getByTestId('stage-fhirservers');
    expect(within(fhirStage).getByTestId('stage-sub-validator').className).toContain(
      'stage-sub-active',
    );
    // Absent members render as waiting rows (queued in the serial boot).
    const dataServerRow = within(fhirStage).getByTestId('stage-sub-data-server');
    expect(dataServerRow.className).toContain('stage-sub-waiting');
    expect(dataServerRow.textContent).toContain('Data server');
    const brProviderRow = within(fhirStage).getByTestId('stage-sub-br-provider');
    expect(brProviderRow.className).toContain('stage-sub-waiting');
    expect(brProviderRow.textContent).toContain('Provider system');
  });

  it('does not double-show the generic boot-hint while the FHIR-servers stage is active (its own note carries the message)', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([
          { name: 'gateway', state: 'ready' },
          { name: 'validator', state: 'starting' },
        ])}
        runsLive={false}
      />,
    );
    // The fhirservers stage is the active one — its own subtitle + note are
    // the single "why is this slow" message; the generic boot-hint is
    // suppressed to avoid two overlapping messages.
    expect(screen.queryByTestId('boot-hint')).toBeNull();
    expect(
      screen.getByText(/first launch builds their databases/i),
    ).toBeDefined();
    expect(
      screen.getByText(/you only wait this long the first time/i),
    ).toBeDefined();
  });

  it('renders no FHIR-servers stage for a stand-in validator (dev build without the packaged Java assets)', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([{ name: 'gateway', state: 'ready' }], { validator: 'stand-in' })}
        runsLive={false}
      />,
    );
    expect(screen.queryByTestId('stage-fhirservers')).toBeNull();
  });

  it('renders no FHIR-servers stage before any status has been fetched', () => {
    render(<BootProgress boot={boot({ state: 'provisioned' })} runsLive={false} />);
    expect(screen.queryByTestId('stage-fhirservers')).toBeNull();
  });
});

describe('BootProgress FHIR-servers stage appears up front (no pop-in)', () => {
  it('shows the FHIR-servers stage the moment the trio is known to be coming — driven by the packaged validator posture, while the gateway is still starting and before any trio child exists', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([{ name: 'gateway', state: 'starting' }], { validator: 'packaged' })}
        runsLive={false}
      />,
    );

    // The gateway is the active stage...
    expect(screen.getByTestId('stage-gateway').className).toContain('stage-active');

    // ...and the FHIR-servers stage is ALREADY present (no later pop-in),
    // rendered as a pending/upcoming step — not active, since the trio boots
    // only after the gateway is ready.
    const fhirStage = screen.getByTestId('stage-fhirservers');
    expect(fhirStage.className).toContain('stage-pending');

    // All three servers read 'waiting' — none has been launched yet.
    for (const name of ['validator', 'data-server', 'br-provider']) {
      const row = within(fhirStage).getByTestId(`stage-sub-${name}`);
      expect(row.className).toContain('stage-sub-waiting');
    }
  });

  it('flips the FHIR-servers stage to active with the first server in flight once the gateway is done', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren(
          [
            { name: 'gateway', state: 'ready' },
            { name: 'validator', state: 'starting' },
          ],
          { validator: 'packaged' },
        )}
        runsLive={false}
      />,
    );

    const fhirStage = screen.getByTestId('stage-fhirservers');
    expect(fhirStage.className).toContain('stage-active');
    expect(within(fhirStage).getByTestId('stage-sub-validator').className).toContain(
      'stage-sub-active',
    );
  });
});

describe('BootProgress FHIR-servers terminal failure', () => {
  it('a terminally-failed trio child fails the stage + its sub-row, still derives the others, names the failure, and surfaces a restart affordance', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([
          { name: 'gateway', state: 'ready' },
          { name: 'validator', state: 'failed', detail: 'not ready within 3m0s' },
          { name: 'data-server', state: 'starting' },
          { name: 'br-provider', state: 'starting' },
        ])}
        runsLive={false}
      />,
    );

    // The gateway can be perfectly ready — the wedge is on a dead trio child.
    expect(screen.getByTestId('stage-gateway').className).toContain('stage-done');

    // The FHIR-servers stage is no longer wedged 'active' forever: a dead
    // child terminally fails it.
    const fhirStage = screen.getByTestId('stage-fhirservers');
    expect(fhirStage.className).toContain('stage-failed');

    // The dead child shows FAILED — never a false "starting" spinner.
    const validatorRow = within(fhirStage).getByTestId('stage-sub-validator');
    expect(validatorRow.className).toContain('stage-sub-failed');
    expect(validatorRow.className).not.toContain('stage-sub-active');
    expect(validatorRow.textContent).not.toContain('starting');

    // The OTHER non-ready children still derive normally (serial order).
    expect(within(fhirStage).getByTestId('stage-sub-data-server').className).toContain(
      'stage-sub-active',
    );
    expect(within(fhirStage).getByTestId('stage-sub-br-provider').className).toContain(
      'stage-sub-waiting',
    );

    // The failed FHIR server is named, like the gateway-failed detail line.
    expect(fhirStage.textContent).toMatch(/not ready within 3m0s/);

    // A restart affordance now renders where today NONE would (canRestart is
    // false by default → the manual sentence).
    expect(screen.getByText(/restart shnkitd/i)).toBeDefined();
  });

  it('offers the Restart the Kit button on a trio failure when canRestart()', () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([
          { name: 'gateway', state: 'ready' },
          { name: 'validator', state: 'failed', detail: 'not ready within 3m0s' },
          { name: 'data-server', state: 'starting' },
          { name: 'br-provider', state: 'starting' },
        ])}
        runsLive={false}
      />,
    );
    expect(screen.getByRole('button', { name: /restart the kit/i })).toBeDefined();
  });

  it('treats an exited trio child as a terminal failure too', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([
          { name: 'gateway', state: 'ready' },
          { name: 'validator', state: 'exited', detail: 'exit status 1' },
          { name: 'data-server', state: 'starting' },
          { name: 'br-provider', state: 'starting' },
        ])}
        runsLive={false}
      />,
    );
    expect(screen.getByTestId('stage-fhirservers').className).toContain('stage-failed');
    expect(screen.getByTestId('stage-sub-validator').className).toContain('stage-sub-failed');
    expect(screen.getByText(/restart shnkitd/i)).toBeDefined();
  });

  it('a restarting trio child is still trying — active, not failed, and offers no restart affordance', () => {
    render(
      <BootProgress
        boot={boot({ state: 'provisioned' })}
        status={statusWithChildren([
          { name: 'gateway', state: 'ready' },
          { name: 'validator', state: 'restarting' },
          { name: 'data-server', state: 'starting' },
          { name: 'br-provider', state: 'starting' },
        ])}
        runsLive={false}
      />,
    );
    const fhirStage = screen.getByTestId('stage-fhirservers');
    expect(fhirStage.className).not.toContain('stage-failed');
    expect(fhirStage.className).toContain('stage-active');

    const validatorRow = within(fhirStage).getByTestId('stage-sub-validator');
    expect(validatorRow.className).toContain('stage-sub-active');
    expect(validatorRow.className).not.toContain('stage-sub-failed');

    // A merely-restarting child is not a terminal failure → no restart path.
    expect(screen.queryByText(/restart shnkitd/i)).toBeNull();
    expect(screen.queryByRole('button', { name: /restart the kit/i })).toBeNull();
  });
});
