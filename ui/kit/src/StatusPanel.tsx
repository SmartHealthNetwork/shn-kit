// StatusPanel.tsx — connectivity/system-status as first-class UI state.
// Renders SSE liveness, identity, child health, verify probes, the
// patient-app launcher, and the reset/restart affordances, laid out as the
// full-width diagnostics card grid SystemsPage mounts. Java child restarts
// respect runner admission; whole-Kit recovery remains available during a run.
import { useState } from 'react';
import type { JSX } from 'react';
import type { BootstrapResponse, Probe, StatusResponse } from './types';
import type { SSEState } from './useEvents';
import { canRestart, openExternal, restartKit, resolveToken } from './bridge';
import { ApiError, postChildRestart, postConformanceLevel, postReset, postVerify, supportBundleUrl } from './api';
import { AboutPanel } from './AboutPanel';

export interface StatusPanelProps {
  boot: BootstrapResponse;
  status?: StatusResponse;
  sseState: SSEState;
  // Only Java child restarts wait for runner finalization. Whole-Kit
  // recovery intentionally remains available while a run is pending.
  admissionPending?: boolean;
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

function ChildRestartControl({ name, admissionPending }: { name: string; admissionPending: boolean }): JSX.Element {
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
        disabled={admissionPending || state.kind === 'pending'}
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

// CONFORMANCE_LEVEL_LABELS names each level ConformanceLevelControl can
// offer. Which of them it actually offers comes from the daemon
// (StatusResponse.conformanceLevels: the levels this Kit's pinned gateway
// accepts), so this file keeps no list of its own. "" — left unset, the
// gateway's published default — is labelled "Observe (default)", and an
// explicitly saved "observe" (for example one migrated from an earlier Kit's
// "none") shows as that same option, since the two behave the same.
const CONFORMANCE_LEVEL_LABELS: Record<string, string> = {
  '': 'Observe (default)',
  structural: 'Structural',
  strict: 'Strict',
  none: 'None',
};

// conformanceLevelOptions lists the tabs for the levels the daemon offers:
// the default first, then each offered level other than observe (which the
// default already stands for), from least to most refusing.
function conformanceLevelOptions(levels: string[] | undefined): { value: string; label: string }[] {
  const offered = levels ?? ['none', 'strict'];
  const order = ['structural', 'strict', 'none'];
  return [
    { value: '', label: CONFORMANCE_LEVEL_LABELS[''] },
    ...order.filter((l) => offered.includes(l)).map((l) => ({ value: l, label: CONFORMANCE_LEVEL_LABELS[l] })),
  ];
}

type ConformanceLevelState = { kind: 'idle' } | { kind: 'pending' } | { kind: 'error'; message: string };

// ConformanceLevelControl is the operator-facing equivalent of
// --conformance-enforcement/kit.config.json's conformanceEnforcement for a
// PACKAGED, installed Kit (which can reach neither — see
// kitd.Config.ConformanceLevel's own doc). A mutually-exclusive selection,
// same role="tablist"/role="tab" idiom as ModeSwitch's lane switch — never
// aria-pressed, which is for independent toggles. `level` is the CURRENT
// server-reported value (StatusResponse.conformanceLevel); a click that
// repeats it is a no-op (no request), and every tab disables while a change
// is in flight so a second click can't race the first restart.
function ConformanceLevelControl({ level, levels }: { level: string; levels?: string[] }): JSX.Element {
  const [state, setState] = useState<ConformanceLevelState>({ kind: 'idle' });
  // applied is the locally-known current level: seeded from the prop,
  // advanced only by this control's OWN successful change — never
  // overwritten by a later prop update, so a slow/racing status poll can't
  // visually revert a change this control just confirmed. A genuinely
  // external change (e.g. another client's toggle) is picked up on the next
  // full StatusPanel remount, the same eventual-consistency posture
  // ChildRestartControl's own local state already has.
  const [applied, setApplied] = useState(level);
  const options = conformanceLevelOptions(levels);
  // A saved "observe" is the default's behavior: select the default tab.
  const shown = applied === 'observe' ? '' : applied;

  const handleSelect = async (value: string) => {
    // Compared with the saved value, not the shown tab: clicking "Observe
    // (default)" while an explicit "observe" is saved clears it back to the
    // default, so the level again follows the published default by absence.
    if (value === applied || state.kind === 'pending') return;
    setState({ kind: 'pending' });
    try {
      const res = await postConformanceLevel(value);
      setApplied(res.level);
      setState({ kind: 'idle' });
    } catch (err) {
      // 409 (a run or watch is in flight, a best-effort gate) gets the
      // SAME operator-actionable copy ChildRestartControl uses; every other
      // error surfaces the raw server detail.
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
    <div className="conformance-level-control">
      <div className="conformance-level-switch" role="tablist" aria-label="Conformance enforcement">
        {options.map((opt) => (
          <button
            key={opt.value}
            type="button"
            role="tab"
            aria-selected={shown === opt.value}
            // aria-current is what the stylesheet's selected-tab rule
            // actually keys on (mirroring ModeSwitch's own dual
            // aria-selected+aria-current — role="tab" alone is correct
            // ARIA but, without this, the CSS has nothing to render the
            // selection with).
            aria-current={shown === opt.value ? 'true' : undefined}
            disabled={state.kind === 'pending'}
            onClick={() => {
              void handleSelect(opt.value);
            }}
          >
            {opt.label}
          </button>
        ))}
      </div>
      {state.kind === 'error' && (
        <p role="alert" className="conformance-level-error">
          {state.message}
        </p>
      )}
    </div>
  );
}

export function StatusPanel({ boot, status, sseState, admissionPending = false, onResetComplete, onVerified }: StatusPanelProps): JSX.Element {
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
              {RESTARTABLE_CHILDREN.has(c.name) && <ChildRestartControl name={c.name} admissionPending={admissionPending} />}
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

      {/* status?.conformanceLevel !== undefined: key-presence, not truthiness
          — "" (the published default) is a genuine present value, never
          conflated with "this Kit build has no live level control at all"
          (StatusResponse.conformanceLevel's own doc). */}
      {status?.conformanceLevel !== undefined && (
        <section className="card systems-card conformance-level">
          <h2>Conformance enforcement</h2>
          {status.conformanceNotice && (
            <p role="status" className="conformance-level-notice">
              {status.conformanceNotice}
            </p>
          )}
          <p className="conformance-level-hint">
            “Observe (default)” runs every check, records each defect as a finding and relays the
            message as sent. “Structural” refuses a message whose structure or profile is broken,
            or with a defect it cannot classify, and records the rest. “Strict” refuses any message
            a supported check finds invalid, and any a check cannot run on. “None” runs no
            conformance checks and records nothing. At every level, the network rules and a payload
            this gateway itself translated between IG lines are refused. An answer the gateway
            cannot read is relayed at Observe and None, and refused at Structural and Strict.
          </p>
          <ConformanceLevelControl level={status.conformanceLevel} levels={status.conformanceLevels} />
        </section>
      )}

      <section className="card systems-card systems-card-wide about-card">
        <AboutPanel />
      </section>
    </div>
  );
}

export default StatusPanel;
