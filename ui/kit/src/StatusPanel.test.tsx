import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StatusPanel } from './StatusPanel';
import type { BootstrapResponse, StatusResponse } from './types';

// vi.mock factories are hoisted above the rest of the module, so ApiError
// must be created through vi.hoisted rather than a plain top-level class
// (App.test.tsx's same precedent) — StatusPanel.tsx's ChildRestartControl
// checks `err instanceof ApiError`, so the mock must export a real class.
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

vi.mock('./bridge', () => ({
  canRestart: vi.fn(),
  restartKit: vi.fn(),
  openExternal: vi.fn(),
  resolveToken: vi.fn(() => Promise.resolve('t-1')),
}));

vi.mock('./api', () => ({
  postReset: vi.fn(),
  postVerify: vi.fn(),
  postChildRestart: vi.fn(),
  getAbout: vi.fn(() => new Promise(() => {})),
  supportBundleUrl: vi.fn(() => '/api/support-bundle'),
  ApiError,
}));

import * as bridge from './bridge';
import * as api from './api';

function boot(overrides: Partial<BootstrapResponse> = {}): BootstrapResponse {
  return { state: 'provisioned', verify: [], ...overrides };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(bridge.canRestart).mockReturnValue(false);
  vi.mocked(bridge.resolveToken).mockResolvedValue('t-1');
  vi.mocked(api.getAbout).mockImplementation(() => new Promise(() => {}));
  vi.mocked(api.supportBundleUrl).mockReturnValue('/api/support-bundle');
});

describe('StatusPanel — children', () => {
  it('renders children name/state (lowercase wire keys) with a state-colored dot; restarts>0 shows the count', () => {
    const status: StatusResponse = {
      children: [{ name: 'gateway', state: 'ready', detail: 'ok', pid: 1, restarts: 2 }],
    };
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    expect(screen.getByText('gateway')).toBeDefined();
    expect(screen.getByText('ready')).toBeDefined();
    expect(document.querySelector('.state-dot-ready')).not.toBeNull();
    expect(screen.getByText(/restarts: 2/)).toBeDefined();
  });

  it('a failed child shows its detail + the one Restart action when canRestart() is true', () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    const status: StatusResponse = {
      children: [{ name: 'gateway', state: 'failed', detail: 'crashed: exit status 1', pid: 1, restarts: 0 }],
    };
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    expect(screen.getByText('crashed: exit status 1')).toBeDefined();
    expect(screen.getByRole('button', { name: /restart/i })).toBeDefined();
  });

  it('a failed child shows no Restart action when canRestart() is false', () => {
    vi.mocked(bridge.canRestart).mockReturnValue(false);
    const status: StatusResponse = {
      children: [{ name: 'gateway', state: 'failed', detail: 'crashed: exit status 1', pid: 1, restarts: 0 }],
    };
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);
    expect(screen.queryByRole('button', { name: /restart/i })).toBeNull();
  });
});

describe('StatusPanel — verify probes', () => {
  it('renders ok/failed probes with detail', () => {
    const b = boot({
      verify: [
        { name: 'discovery', ok: true, detail: 'reachable' },
        { name: 'registration', ok: false, detail: 'holder not found in registrar feed' },
        { name: 'hosted-payer', ok: true, detail: 'ok' },
      ],
    });
    render(<StatusPanel boot={b} sseState="open" />);

    expect(screen.getByText('discovery')).toBeDefined();
    expect(screen.getByText('reachable')).toBeDefined();
    expect(screen.getByText('holder not found in registrar feed')).toBeDefined();
  });

  it('the three-skipped-probes shape renders as one "verify skipped" info line, not three red errors', () => {
    const b = boot({
      verify: [
        { name: 'discovery', ok: false, detail: 'skipped: reset raced the boot window' },
        { name: 'registration', ok: false, detail: 'skipped: reset raced the boot window' },
        { name: 'hosted-payer', ok: false, detail: 'skipped: reset raced the boot window' },
      ],
    });
    render(<StatusPanel boot={b} sseState="open" />);

    expect(screen.getByText(/verify skipped/i)).toBeDefined();
    expect(screen.queryByText('discovery')).toBeNull();
    expect(screen.queryAllByText(/skipped: reset raced the boot window/).length).toBe(1);
  });
});

describe('StatusPanel — verify re-check', () => {
  it('Re-check calls postVerify(), disables + reads "checking…" while pending, then calls onVerified with the fresh probes', async () => {
    let resolvePromise: ((probes: { name: string; ok: boolean; detail: string }[]) => void) | undefined;
    vi.mocked(api.postVerify).mockReturnValue(
      new Promise((resolve) => {
        resolvePromise = resolve;
      }),
    );
    const onVerified = vi.fn();
    const b = boot({ verify: [{ name: 'discovery', ok: true, detail: 'reachable' }] });
    render(<StatusPanel boot={b} sseState="open" onVerified={onVerified} />);

    const button = screen.getByRole('button', { name: /re-check/i });
    await userEvent.click(button);

    expect(api.postVerify).toHaveBeenCalledOnce();
    expect(screen.getByRole('button', { name: /checking…/i })).toBeDisabled();

    const freshProbes = [{ name: 'discovery', ok: false, detail: 'unreachable' }];
    resolvePromise?.(freshProbes);
    await waitFor(() => expect(onVerified).toHaveBeenCalledWith(freshProbes));

    expect(screen.getByRole('button', { name: /^re-check$/i })).not.toBeDisabled();
  });

  it('rejection renders an inline error and re-enables the button', async () => {
    vi.mocked(api.postVerify).mockRejectedValue(new Error('verify already in flight'));
    render(<StatusPanel boot={boot()} sseState="open" />);

    await userEvent.click(screen.getByRole('button', { name: /re-check/i }));

    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/verify already in flight/i));
    expect(screen.getByRole('button', { name: /^re-check$/i })).not.toBeDisabled();
  });
});

describe('StatusPanel — SSE indicator', () => {
  it('open renders "live"', () => {
    render(<StatusPanel boot={boot()} sseState="open" />);
    expect(screen.getByText('live')).toBeDefined();
  });

  it('reconnecting renders "reconnecting…"', () => {
    render(<StatusPanel boot={boot()} sseState="reconnecting" />);
    expect(screen.getByText('reconnecting…')).toBeDefined();
  });
});

describe('StatusPanel — identity', () => {
  it('renders email + holderId; authExpiry absent means no session-expires line', () => {
    const b = boot({ email: 'linda@example.org', holderId: 'holder-42' });
    render(<StatusPanel boot={b} sseState="open" />);

    expect(screen.getByText('linda@example.org')).toBeDefined();
    expect(screen.getByText('holder-42')).toBeDefined();
    expect(screen.queryByText(/session expires/i)).toBeNull();
  });

  it('authExpiry present renders the session-expires line', () => {
    const b = boot({ email: 'linda@example.org', holderId: 'holder-42', authExpiry: '2026-07-04T00:00:00Z' });
    render(<StatusPanel boot={b} sseState="open" />);
    expect(screen.getByText(/session expires/i)).toBeDefined();
  });
});

describe('StatusPanel — patient app launcher', () => {
  it('patientAppUrl present shows the button and calls openExternal', async () => {
    const status: StatusResponse = { children: [], patientAppUrl: 'http://127.0.0.1:8084/' };
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    const button = screen.getByRole('button', { name: /open the smart health account app/i });
    await userEvent.click(button);
    expect(bridge.openExternal).toHaveBeenCalledWith('http://127.0.0.1:8084/');
  });

  it('patientAppUrl absent hides the button', () => {
    const status: StatusResponse = { children: [] };
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);
    expect(screen.queryByRole('button', { name: /open the smart health account app/i })).toBeNull();
  });
});

describe('StatusPanel — reset flow', () => {
  it('Reset -> confirm affordance -> Confirm reset calls postReset()', async () => {
    vi.mocked(api.postReset).mockResolvedValue({ restartRequired: false });
    render(<StatusPanel boot={boot()} sseState="open" />);

    await userEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    expect(screen.getByRole('button', { name: /confirm reset/i })).toBeDefined();

    await userEvent.click(screen.getByRole('button', { name: /confirm reset/i }));
    await waitFor(() => expect(api.postReset).toHaveBeenCalledOnce());
  });

  it('Cancel returns to the idle Reset button without calling postReset()', async () => {
    render(<StatusPanel boot={boot()} sseState="open" />);
    await userEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    await userEvent.click(screen.getByRole('button', { name: /cancel/i }));

    expect(screen.getByRole('button', { name: /^reset$/i })).toBeDefined();
    expect(api.postReset).not.toHaveBeenCalled();
  });

  it('restartRequired:true renders "Restart the Kit to finish the reset" with a Restart button when canRestart(); the reset note is present', async () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    vi.mocked(api.postReset).mockResolvedValue({ restartRequired: true });
    render(<StatusPanel boot={boot()} sseState="open" />);

    await userEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    await userEvent.click(screen.getByRole('button', { name: /confirm reset/i }));

    await waitFor(() => expect(screen.getByText(/restart the kit to finish the reset/i)).toBeDefined());
    expect(screen.getByText(/runs in progress were reset/i)).toBeDefined();
    expect(screen.getByRole('button', { name: /^restart$/i })).toBeDefined();
  });

  it('restartRequired:true with canRestart() false renders the manual-restart sentence instead of a button', async () => {
    vi.mocked(bridge.canRestart).mockReturnValue(false);
    vi.mocked(api.postReset).mockResolvedValue({ restartRequired: true });
    render(<StatusPanel boot={boot()} sseState="open" />);

    await userEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    await userEvent.click(screen.getByRole('button', { name: /confirm reset/i }));

    await waitFor(() => expect(screen.getByText(/restart shnkitd manually/i)).toBeDefined());
    expect(screen.queryByRole('button', { name: /^restart$/i })).toBeNull();
  });

  it('the Restart button in the post-reset flow calls restartKit() (mock bridge)', async () => {
    vi.mocked(bridge.canRestart).mockReturnValue(true);
    vi.mocked(api.postReset).mockResolvedValue({ restartRequired: true });
    render(<StatusPanel boot={boot()} sseState="open" />);

    await userEvent.click(screen.getByRole('button', { name: /^reset$/i }));
    await userEvent.click(screen.getByRole('button', { name: /confirm reset/i }));
    await waitFor(() => screen.getByRole('button', { name: /^restart$/i }));

    await userEvent.click(screen.getByRole('button', { name: /^restart$/i }));
    expect(bridge.restartKit).toHaveBeenCalledOnce();
  });
});

describe('StatusPanel — per-child restart', () => {
  function statusWithChildren(names: string[]): StatusResponse {
    return {
      children: names.map((name) => ({ name, state: 'ready', detail: 'ok', pid: 1, restarts: 0 })),
    };
  }

  it('renders a Restart button on validator/data-server/br-provider rows but NOT on the gateway row', () => {
    const status = statusWithChildren(['gateway', 'validator', 'data-server', 'br-provider']);
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    const gatewayRow = within(screen.getByText('gateway').closest('li') as HTMLElement);
    expect(gatewayRow.queryByRole('button', { name: /^restart$/i })).toBeNull();

    for (const name of ['validator', 'data-server', 'br-provider']) {
      const row = within(screen.getByText(name).closest('li') as HTMLElement);
      expect(row.getByRole('button', { name: /^restart$/i })).toBeDefined();
    }
  });

  it('clicking Restart calls postChildRestart(name), disables + reads "restarting…" while pending, then re-enables on success', async () => {
    let resolvePromise: ((v: { restarted: string }) => void) | undefined;
    vi.mocked(api.postChildRestart).mockReturnValue(
      new Promise((resolve) => {
        resolvePromise = resolve;
      }),
    );
    const status = statusWithChildren(['validator']);
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    const row = within(screen.getByText('validator').closest('li') as HTMLElement);
    await userEvent.click(row.getByRole('button', { name: /^restart$/i }));

    expect(api.postChildRestart).toHaveBeenCalledWith('validator');
    expect(row.getByRole('button', { name: /restarting…/i })).toBeDisabled();

    resolvePromise?.({ restarted: 'validator' });
    await waitFor(() => expect(row.getByRole('button', { name: /^restart$/i })).not.toBeDisabled());
  });

  it('409 (a run or watch is in flight) renders "finish or stop the current run first" inline, and re-enables the button', async () => {
    vi.mocked(api.postChildRestart).mockRejectedValue(new ApiError('a run or watch is in flight', 409));
    const status = statusWithChildren(['data-server']);
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    const row = within(screen.getByText('data-server').closest('li') as HTMLElement);
    await userEvent.click(row.getByRole('button', { name: /^restart$/i }));

    await waitFor(() =>
      expect(row.getByRole('alert').textContent).toMatch(/finish or stop the current run first/i),
    );
    expect(row.getByRole('button', { name: /^restart$/i })).not.toBeDisabled();
  });

  it('a non-409 error (e.g. 404 unknown child) renders the raw server message inline', async () => {
    vi.mocked(api.postChildRestart).mockRejectedValue(new ApiError('unknown child', 404));
    const status = statusWithChildren(['br-provider']);
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    const row = within(screen.getByText('br-provider').closest('li') as HTMLElement);
    await userEvent.click(row.getByRole('button', { name: /^restart$/i }));

    await waitFor(() => expect(row.getByRole('alert').textContent).toMatch(/unknown child/i));
  });

  it('each child row tracks its own restart state independently', async () => {
    let resolveValidator: ((v: { restarted: string }) => void) | undefined;
    vi.mocked(api.postChildRestart).mockImplementation(
      (name: string) =>
        new Promise((resolve) => {
          if (name === 'validator') resolveValidator = resolve;
        }),
    );
    const status = statusWithChildren(['validator', 'data-server']);
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    const validatorRow = within(screen.getByText('validator').closest('li') as HTMLElement);
    const dataServerRow = within(screen.getByText('data-server').closest('li') as HTMLElement);

    await userEvent.click(validatorRow.getByRole('button', { name: /^restart$/i }));
    expect(validatorRow.getByRole('button', { name: /restarting…/i })).toBeDisabled();
    expect(dataServerRow.getByRole('button', { name: /^restart$/i })).not.toBeDisabled();

    resolveValidator?.({ restarted: 'validator' });
    await waitFor(() =>
      expect(validatorRow.getByRole('button', { name: /^restart$/i })).not.toBeDisabled(),
    );
  });
});

describe('StatusPanel — provider system launcher', () => {
  it('brProviderUrl present shows the framed copy + button, calls openExternal', async () => {
    const status: StatusResponse = { children: [], brProviderUrl: 'http://127.0.0.1:9100/' };
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);

    expect(screen.getByText('a third-party Da Vinci system (br-provider)')).toBeDefined();
    const button = screen.getByRole('button', { name: /open the provider system/i });
    await userEvent.click(button);
    expect(bridge.openExternal).toHaveBeenCalledWith('http://127.0.0.1:9100/');
  });

  it('brProviderUrl absent hides the button and the framing copy', () => {
    const status: StatusResponse = { children: [] };
    render(<StatusPanel boot={boot()} status={status} sseState="open" />);
    expect(screen.queryByRole('button', { name: /open the provider system/i })).toBeNull();
    expect(screen.queryByText('a third-party Da Vinci system (br-provider)')).toBeNull();
  });
});

describe('StatusPanel — download support bundle', () => {
  it('clicking the anchor fetches supportBundleUrl() with a Bearer header and downloads the response as a blob', async () => {
    const blob = new Blob(['zip-bytes']);
    const fetchStub = vi.fn().mockResolvedValue({ ok: true, blob: () => Promise.resolve(blob) });
    vi.stubGlobal('fetch', fetchStub);

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
      render(<StatusPanel boot={boot()} sseState="open" />);
      await userEvent.click(screen.getByRole('link', { name: /download support bundle/i }));

      await waitFor(() => expect(fetchStub).toHaveBeenCalledOnce());
      const [url, init] = fetchStub.mock.calls[0] as [string, RequestInit];
      expect(url).toBe('/api/support-bundle');
      expect((init.headers as Record<string, string>).Authorization).toBe('Bearer t-1');

      await waitFor(() => expect(createObjectURL).toHaveBeenCalledWith(blob));
      expect(anchor.download).toBe('shn-kit-support-bundle.zip');
      expect(clickSpy).toHaveBeenCalledTimes(1);
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:mock-url');
    } finally {
      createElementSpy.mockRestore();
      URL.createObjectURL = originalCreateObjectURL;
      URL.revokeObjectURL = originalRevokeObjectURL;
      vi.unstubAllGlobals();
    }
  });

  it('a fetch failure renders a visible inline error instead of an unhandled rejection', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500 }));
    try {
      render(<StatusPanel boot={boot()} sseState="open" />);
      await userEvent.click(screen.getByRole('link', { name: /download support bundle/i }));

      await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/http 500/i));
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe('StatusPanel — About mount', () => {
  it('mounts the About section', () => {
    render(<StatusPanel boot={boot()} sseState="open" />);
    expect(screen.getByRole('heading', { name: /^about$/i })).toBeDefined();
  });
});
