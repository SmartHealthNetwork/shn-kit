import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import SignIn from './SignIn';
import type { BootstrapResponse } from './types';

// vi.mock factories are hoisted above the rest of the module, so ApiError
// must be created through vi.hoisted rather than a plain top-level class.
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
  postSignIn: vi.fn(),
  ApiError,
}));

vi.mock('./bridge', () => ({
  openExternal: vi.fn(),
}));

import * as api from './api';
import * as bridge from './bridge';

function boot(overrides: Partial<BootstrapResponse> = {}): BootstrapResponse {
  return { state: 'signin-required', verify: [], ...overrides };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('SignIn', () => {
  it('renders the explainer and a Sign in button; click posts sign-in and shows the browser-opened copy with a fallback link that calls openExternal', async () => {
    vi.mocked(api.postSignIn).mockResolvedValue({ authorizeUrl: 'https://portal.example/authorize' });
    render(<SignIn boot={boot()} />);

    expect(screen.getByText(/developer portal/i)).toBeDefined();
    const button = screen.getByRole('button', { name: /sign in/i });

    fireEvent.click(button);
    expect(api.postSignIn).toHaveBeenCalledOnce();

    await waitFor(() => {
      expect(screen.getByText(/your browser should have opened/i)).toBeDefined();
    });

    const link = screen.getByRole('link');
    fireEvent.click(link);
    expect(bridge.openExternal).toHaveBeenCalledWith('https://portal.example/authorize');
  });

  it('state signing-in disables the button and shows waiting copy plus the bootstrap detail when present', () => {
    render(
      <SignIn
        boot={boot({ state: 'signing-in', detail: 'Waiting for the browser to complete the flow.' })}
      />,
    );

    const button = screen.getByRole('button', { name: /sign in/i });
    expect(button).toBeDisabled();
    expect(screen.getByText(/waiting for browser sign-in/i)).toBeDefined();
    expect(screen.getByText(/waiting for the browser to complete the flow\./i)).toBeDefined();
  });

  it('postSignIn rejecting with ApiError(409) renders the server message inline instead of crashing', async () => {
    vi.mocked(api.postSignIn).mockRejectedValue(new ApiError('sign-in already in progress', 409));
    render(<SignIn boot={boot()} />);

    fireEvent.click(screen.getByRole('button', { name: /sign in/i }));

    await waitFor(() => {
      expect(screen.getByText(/sign-in already in progress/i)).toBeDefined();
    });
  });

  it('empty authorizeUrl (token fast path) shows the resuming-session copy with no link', async () => {
    vi.mocked(api.postSignIn).mockResolvedValue({ authorizeUrl: '' });
    render(<SignIn boot={boot()} />);

    fireEvent.click(screen.getByRole('button', { name: /sign in/i }));

    await waitFor(() => {
      expect(screen.getByText(/resuming your session/i)).toBeDefined();
    });
    expect(screen.queryByRole('link')).toBeNull();
  });
});
