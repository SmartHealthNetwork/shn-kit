// SignIn.tsx — the "Sign in" stage: system-browser authorization-code +
// PKCE. The Kit never sees a password — this screen only kicks off
// postSignIn() and reflects the resulting authorizeUrl (or the bootstrap
// Machine's own signing-in state once the daemon reports it).
import { useState } from 'react';
import type { BootstrapResponse } from './types';
import { postSignIn, ApiError } from './api';
import { openExternal } from './bridge';

export interface SignInProps {
  boot: BootstrapResponse;
}

type LocalState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'authorize'; authorizeUrl: string }
  | { kind: 'resuming' }
  | { kind: 'error'; message: string };

export default function SignIn({ boot }: SignInProps) {
  const [local, setLocal] = useState<LocalState>({ kind: 'idle' });
  const signingIn = boot.state === 'signing-in';

  const handleSignIn = async () => {
    setLocal({ kind: 'submitting' });
    try {
      const res = await postSignIn();
      if (res.authorizeUrl) {
        setLocal({ kind: 'authorize', authorizeUrl: res.authorizeUrl });
      } else {
        setLocal({ kind: 'resuming' });
      }
    } catch (err) {
      const message =
        err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err);
      setLocal({ kind: 'error', message });
    }
  };

  const disabled = signingIn || local.kind === 'submitting';

  return (
    <div className="phase-card signin-card">
      <h1>Sign in to the Smart Health Network</h1>
      <p className="explainer">
        Sign in through the developer portal in your system browser. The Kit never sees your
        password.
      </p>

      <button
        type="button"
        className="btn btn-primary"
        onClick={() => {
          void handleSignIn();
        }}
        disabled={disabled}
      >
        Sign in
      </button>

      {signingIn && (
        <div className="signin-status">
          <p>Waiting for browser sign-in…</p>
          {boot.detail && <p className="detail">{boot.detail}</p>}
        </div>
      )}

      {!signingIn && local.kind === 'authorize' && (
        <p className="signin-status">
          Your browser should have opened to continue signing in. If it didn't,{' '}
          <a
            href={local.authorizeUrl}
            onClick={(e) => {
              e.preventDefault();
              openExternal(local.authorizeUrl);
            }}
          >
            open it manually
          </a>
          .
        </p>
      )}

      {!signingIn && local.kind === 'resuming' && (
        <p className="signin-status">Resuming your session…</p>
      )}

      {local.kind === 'error' && (
        <p className="signin-status error" role="alert">
          {local.message}
        </p>
      )}
    </div>
  );
}
