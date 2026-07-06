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
