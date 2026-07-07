// StatusPanel.tsx — connectivity/system-status as first-class UI state.
// Renders SSE liveness, identity, child health, verify probes, the
// patient-app launcher, and the reset/restart affordances, laid out as the
// full-width diagnostics card grid SystemsPage mounts. Markup
// is grouped into cards; every handler and pinned string below is
// unchanged from the pre-restyle version — only the surrounding structure
// and class names moved.
import { useState } from 'react';
import type { JSX } from 'react';
import type { BootstrapResponse, Probe, StatusResponse } from './types';
import type { SSEState } from './useEvents';
import { canRestart, openExternal, restartKit, resolveToken } from './bridge';
import { ApiError, postChildRestart, postReset, postVerify, supportBundleUrl } from './api';
import { AboutPanel } from './AboutPanel';

export interface StatusPanelProps {
  boot: BootstrapResponse;
  status?: StatusResponse;
  sseState: SSEState;
  // Fired once postReset() resolves with restartRequired:true, so App can
  // hoist "restart required" into phase-router-level state — this panel
  // unmounts on the next bootstrap poll flip (signin-required), and the
  // restart affordance must survive that. Not called for
  // restartRequired:false (nothing to hoist).
  onResetComplete?: () => void;
  // Fired with the fresh Probe[] once a POST /api/verify re-check resolves
  // — App applies them to its own boot.verify state immediately, rather
  // than waiting for the next 2s bootstrap poll.
  onVerified?: (probes: Probe[]) => void;
}

type RecheckState = { kind: 'idle' } | { kind: 'pending' } | { kind: 'error'; message: string };

type ResetState =
  | { kind: 'idle' }
  | { kind: 'confirming' }
  | { kind: 'resetting' }
  | { kind: 'done'; restartRequired: boolean }
  | { kind: 'error'; message: string };

// main.go publishes exactly this 3-skipped-probes shape when Reset raced
// the boot window (Bundle() not ok) — render it as one info line, not
// three red probe-failure rows.
function allSkipped(probes: Probe[]): boolean {
  return probes.length > 0 && probes.every((p) => p.detail.startsWith('skipped:'));
}

function RestartButton(): JSX.Element {
  return (
    <button
      type="button"
      className="btn btn-primary"
      onClick={() => {
        void restartKit();
      }}
    >
      Restart
    </button>
  );
}

// The per-child restart seam is for the Java trio
// (validator/data-server/br-provider) only — gateway keeps the existing
// whole-Kit RestartButton above (restarting it would invalidate its port/
// driver keypair/runner wiring, kitd's own 403 doc comment).
const RESTARTABLE_CHILDREN = new Set(['validator', 'data-server', 'br-provider']);

type ChildRestartState = { kind: 'idle' } | { kind: 'pending' } | { kind: 'error'; message: string };

function ChildRestartControl({ name }: { name: string }): JSX.Element {
  const [state, setState] = useState<ChildRestartState>({ kind: 'idle' });

  const handleClick = async () => {
    setState({ kind: 'pending' });
    try {
      await postChildRestart(name);
      setState({ kind: 'idle' });
    } catch (err) {
      // 409 (a run or watch is in flight, a best-effort gate) gets the
      // operator-actionable UI copy; every other error surfaces the raw
      // server detail.
      const message =
        err instanceof ApiError && err.status === 409
          ? 'finish or stop the current run first'
          : err instanceof Error
            ? err.message
            : String(err);
      setState({ kind: 'error', message });
    }
  };

  return (
    <div className="child-restart">
      <button
        type="button"
        className="btn ghost"
        disabled={state.kind === 'pending'}
        onClick={() => {
          void handleClick();
        }}
      >
        {state.kind === 'pending' ? 'restarting…' : 'Restart'}
      </button>
      {state.kind === 'error' && (
        <p role="alert" className="child-restart-error">
          {state.message}
        </p>
      )}
    </div>
  );
}

export function StatusPanel({ boot, status, sseState, onResetComplete, onVerified }: StatusPanelProps): JSX.Element {
  const [reset, setReset] = useState<ResetState>({ kind: 'idle' });
  const [recheck, setRecheck] = useState<RecheckState>({ kind: 'idle' });
  const [bundleError, setBundleError] = useState<string | undefined>(undefined);

  // GET /api/support-bundle is Bearer-gated like every other /api/* route
  // — a bare `<a href>` navigation can't carry the header, so this fetches
  // the zip as a Blob with the SAME Authorization-header path api.ts's
  // other calls use (not the `?token=` query fallback, which
  // authMiddleware carries only as an EventSource workaround), then
  // downloads it via an object URL — the same pattern App.tsx's history
  // export already uses.
  const handleDownloadBundle = async () => {
    setBundleError(undefined);
    try {
      const token = await resolveToken();
      const res = await fetch(supportBundleUrl(), {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'shn-kit-support-bundle.zip';
      try {
        a.click();
      } finally {
        URL.revokeObjectURL(url);
      }
    } catch (err) {
      setBundleError(err instanceof Error ? err.message : String(err));
    }
  };

  const handleRecheck = async () => {
    setRecheck({ kind: 'pending' });
    try {
      const probes = await postVerify();
      setRecheck({ kind: 'idle' });
      onVerified?.(probes);
    } catch (err) {
      setRecheck({ kind: 'error', message: err instanceof Error ? err.message : String(err) });
    }
  };

  const handleConfirmReset = async () => {
    setReset({ kind: 'resetting' });
    try {
      const res = await postReset();
      setReset({ kind: 'done', restartRequired: res.restartRequired });
      if (res.restartRequired) onResetComplete?.();
    } catch (err) {
      setReset({ kind: 'error', message: err instanceof Error ? err.message : String(err) });
    }
  };

  return (
    <div className="systems-page">
      <section className="card systems-card connectivity">
        <h2>Connectivity</h2>
        <p className={`sse-indicator sse-${sseState}`}>
          {sseState === 'open' ? 'live' : sseState === 'reconnecting' ? 'reconnecting…' : 'connecting…'}
        </p>
      </section>

      <section className="card systems-card identity">
        <h2>Identity</h2>
        <div className="identity-facts">
          {boot.email && <p className="identity-email">{boot.email}</p>}
          {boot.holderId && <p className="identity-holder-id">{boot.holderId}</p>}
          {boot.authExpiry && <p className="identity-expiry">Session expires {boot.authExpiry}</p>}
        </div>
      </section>

      <section className="card systems-card systems-card-wide children">
        <h2>Children</h2>
        <ul className="child-list">
          {(status?.children ?? []).map((c) => (
            <li key={c.name} className={`child-row child-${c.state}`}>
              <div className="child-summary">
                <span className={`state-dot state-dot-${c.state}`} aria-hidden="true" />
                <span className="child-name">{c.name}</span>
                <span className="child-state">{c.state}</span>
                {c.restarts > 0 && <span className="child-restarts">restarts: {c.restarts}</span>}
              </div>
              {c.state === 'failed' && (
                <div className="child-failure">
                  <p className="child-detail">{c.detail}</p>
                  {canRestart() && <RestartButton />}
                </div>
              )}
              {RESTARTABLE_CHILDREN.has(c.name) && <ChildRestartControl name={c.name} />}
            </li>
          ))}
        </ul>
      </section>

      <section className="card systems-card systems-card-wide verify">
        <h2>Verify the network</h2>
        {allSkipped(boot.verify) ? (
          <p className="verify-skipped" role="status">
            verify skipped — {boot.verify[0]?.detail}
          </p>
        ) : (
          <ul className="verify-list">
            {boot.verify.map((p) => (
              <li key={p.name} className={`verify-probe verify-${p.ok ? 'ok' : 'failed'}`}>
                <span className="probe-name">{p.name}</span>
                <span className="probe-detail">{p.detail}</span>
              </li>
            ))}
          </ul>
        )}
        <div className="verify-actions">
          <button
            type="button"
            className="btn ghost"
            disabled={recheck.kind === 'pending'}
            onClick={() => {
              void handleRecheck();
            }}
          >
            {recheck.kind === 'pending' ? 'checking…' : 'Re-check'}
          </button>
          {recheck.kind === 'error' && (
            <p role="alert" className="verify-recheck-error">
              {recheck.message}
            </p>
          )}
        </div>
      </section>

      {(status?.patientAppUrl || status?.brProviderUrl) && (
        <section className="card systems-card launchers">
          <h2>Launch</h2>
          {status?.patientAppUrl && (
            <div className="launcher-row">
              <button
                type="button"
                className="btn btn-link"
                onClick={() => openExternal(status.patientAppUrl as string)}
              >
                Open the Smart Health account app
              </button>
            </div>
          )}

          {status?.brProviderUrl && (
            <div className="launcher-row">
              <p className="launcher-caption">a third-party Da Vinci system (br-provider)</p>
              <button
                type="button"
                className="btn btn-link"
                onClick={() => openExternal(status.brProviderUrl as string)}
              >
                Open the provider system
              </button>
            </div>
          )}
        </section>
      )}

      <section className="card systems-card support-bundle">
        <h2>Support bundle</h2>
        <a
          href={supportBundleUrl()}
          className="btn btn-link"
          onClick={(e) => {
            e.preventDefault();
            void handleDownloadBundle();
          }}
        >
          Download support bundle
        </a>
        {bundleError && (
          <p role="alert" className="support-bundle-error">
            {bundleError}
          </p>
        )}
      </section>

      <section className="card systems-card reset-panel">
        <h2>Reset</h2>

        {reset.kind === 'idle' && (
          <button type="button" className="btn ghost" onClick={() => setReset({ kind: 'confirming' })}>
            Reset
          </button>
        )}

        {reset.kind === 'confirming' && (
          <div className="reset-confirm">
            <p>This clears sign-in and provisioning state. Any runs in progress will be reset.</p>
            <div className="reset-confirm-actions">
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  void handleConfirmReset();
                }}
              >
                Confirm reset
              </button>
              <button type="button" className="btn btn-link" onClick={() => setReset({ kind: 'idle' })}>
                Cancel
              </button>
            </div>
          </div>
        )}

        {reset.kind === 'resetting' && <p className="reset-status">Resetting…</p>}

        {reset.kind === 'done' && (
          <div className="reset-done">
            <p>Reset complete. Runs in progress were reset.</p>
            {reset.restartRequired &&
              (canRestart() ? (
                <div className="reset-restart-action">
                  <p>Restart the Kit to finish the reset.</p>
                  <RestartButton />
                </div>
              ) : (
                <p>Restart shnkitd manually to finish the reset.</p>
              ))}
          </div>
        )}

        {reset.kind === 'error' && (
          <p role="alert" className="reset-error">
            {reset.message}
          </p>
        )}
      </section>

      <section className="card systems-card systems-card-wide about-card">
        <AboutPanel />
      </section>
    </div>
  );
}

export default StatusPanel;
