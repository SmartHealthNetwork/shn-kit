// BYOPanel.tsx — bring-your-own systems settings: repoint the ehr lane at
// a partner EHR, or register a partner Da Vinci system as an inbound
// ingress client. Both swaps are per-lane, reversible ("restore demo
// data"), and take effect only on the next restart — every successful
// PUT/DELETE here answers {restartRequired:true}, which this panel always
// surfaces as a restart-pending affordance rather than pretending the
// change is live.
import { useState } from 'react';
import type { FormEvent, JSX } from 'react';
import type { BYODaVinci, BYOEhr, BYOIngress, BYOStatus } from './types';
import {
  ApiError,
  deleteBYODaVinci,
  deleteBYOEhr,
  putBYODaVinci,
  putBYOEhr,
} from './api';

export interface BYOPanelProps {
  byo: BYOStatus;
  onSaved: () => void; // App refetches getBYO()
  onRestart: () => void; // App: bridge restart w/ confirm
}

// The awareness note — pinned exactly, rendered on BOTH lanes: a
// bring-your-own swap means the inspector is now observing the partner's
// own data, not the Kit's synthetic personas.
const AWARENESS_NOTE = 'the inspector displays full payloads from your connected systems';

// The loopback sentence — pinned exactly: the Kit never opens a remote
// listener so a partner Da Vinci system can reach it; the partner system
// must run alongside the Kit itself.
const LOOPBACK_SENTENCE = 'your system must run on this machine — the Kit does not open a remote listener';

// Per-lane action state: idle/busy render the persistent state derived from
// props; a completed save/delete flips to restart-pending (transient,
// local — independent of whether/when the parent's onSaved()-triggered
// refetch lands) or error (server detail rendered verbatim, form untouched).
type LaneState =
  | { kind: 'idle' }
  | { kind: 'busy' }
  | { kind: 'restart-pending' }
  | { kind: 'error'; message: string };

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}

function RestartPendingNote({ onRestart }: { onRestart: () => void }): JSX.Element {
  return (
    <div className="byo-restart-pending">
      <p>Saved — restart the Kit to apply.</p>
      <button type="button" className="btn btn-primary" onClick={onRestart}>
        Restart the Kit now
      </button>
    </div>
  );
}

interface EHRSectionProps {
  ehr: BYOEhr | null;
  onSaved: () => void;
  onRestart: () => void;
}

function EHRSection({ ehr, onSaved, onRestart }: EHRSectionProps): JSX.Element {
  const [dataUrl, setDataUrl] = useState(ehr?.dataUrl ?? '');
  const [tokenUrl, setTokenUrl] = useState(ehr?.tokenUrl ?? '');
  const [clientId, setClientId] = useState(ehr?.clientId ?? '');
  const [clientKeyPem, setClientKeyPem] = useState('');
  const [alg, setAlg] = useState(ehr?.alg ?? '');
  const [scope, setScope] = useState(ehr?.scope ?? '');
  const [kid, setKid] = useState(ehr?.kid ?? '');
  const [state, setState] = useState<LaneState>({ kind: 'idle' });

  const handleSave = async (e: FormEvent) => {
    e.preventDefault();
    setState({ kind: 'busy' });
    try {
      const body: Parameters<typeof putBYOEhr>[0] = { dataUrl };
      if (tokenUrl) body.tokenUrl = tokenUrl;
      if (clientId) body.clientId = clientId;
      if (clientKeyPem) body.clientKeyPem = clientKeyPem;
      if (alg) body.alg = alg;
      if (scope) body.scope = scope;
      if (kid) body.kid = kid;
      await putBYOEhr(body);
      setClientKeyPem(''); // key hygiene: never keep the typed key in the field after a successful save
      setState({ kind: 'restart-pending' });
      onSaved();
    } catch (err) {
      setState({ kind: 'error', message: errorMessage(err) });
    }
  };

  const handleRestore = async () => {
    setState({ kind: 'busy' });
    try {
      await deleteBYOEhr();
      setState({ kind: 'restart-pending' });
      onSaved();
    } catch (err) {
      setState({ kind: 'error', message: errorMessage(err) });
    }
  };

  return (
    <section className="byo-section byo-ehr">
      <h3>EHR (data source)</h3>

      {state.kind === 'restart-pending' ? (
        <RestartPendingNote onRestart={onRestart} />
      ) : (
        <>
          {ehr?.applied && (
            <div className="byo-connected">
              <p>Connected — your EHR is the ehr lane&apos;s data source.</p>
              <button
                type="button"
                className="btn btn-secondary"
                disabled={state.kind === 'busy'}
                onClick={() => {
                  void handleRestore();
                }}
              >
                Restore demo data
              </button>
            </div>
          )}
          {ehr && !ehr.applied && (
            <p className="byo-restart-banner">
              saved, not yet applied — runs still use the demo data source
            </p>
          )}
        </>
      )}

      <form
        onSubmit={(e) => {
          void handleSave(e);
        }}
      >
        <label>
          Data URL
          <input
            type="text"
            required
            value={dataUrl}
            onChange={(e) => setDataUrl(e.target.value)}
          />
        </label>

        <fieldset>
          <legend>Authentication (optional)</legend>
          <label>
            Token URL
            <input type="text" value={tokenUrl} onChange={(e) => setTokenUrl(e.target.value)} />
          </label>
          <label>
            Client ID
            <input type="text" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </label>
          <label>
            Algorithm
            <input type="text" value={alg} onChange={(e) => setAlg(e.target.value)} />
          </label>
          <label>
            Scope
            <input type="text" value={scope} onChange={(e) => setScope(e.target.value)} />
          </label>
          <label>
            Key ID
            <input type="text" value={kid} onChange={(e) => setKid(e.target.value)} />
          </label>
          <label>
            Client key (PEM)
            <textarea value={clientKeyPem} onChange={(e) => setClientKeyPem(e.target.value)} />
          </label>
          {ehr?.hasClientKey && <p className="byo-key-hint">a client key is stored</p>}
        </fieldset>

        <p className="byo-awareness-note">{AWARENESS_NOTE}</p>

        <button type="submit" className="btn btn-primary" disabled={state.kind === 'busy'}>
          Save
        </button>
      </form>

      {state.kind === 'error' && (
        <p role="alert" className="byo-error">
          {state.message}
        </p>
      )}
    </section>
  );
}

interface DaVinciSectionProps {
  davinci: BYODaVinci | null;
  ingress: BYOIngress | null;
  onSaved: () => void;
  onRestart: () => void;
}

function DaVinciSection({ davinci, ingress, onSaved, onRestart }: DaVinciSectionProps): JSX.Element {
  const [clientId, setClientId] = useState(davinci?.clientId ?? '');
  const [alg, setAlg] = useState(davinci?.alg ?? '');
  const [publicKeyPem, setPublicKeyPem] = useState(davinci?.publicKeyPem ?? '');
  const [state, setState] = useState<LaneState>({ kind: 'idle' });

  const handleSave = async (e: FormEvent) => {
    e.preventDefault();
    setState({ kind: 'busy' });
    try {
      await putBYODaVinci({ clientId, alg, publicKeyPem });
      setState({ kind: 'restart-pending' });
      onSaved();
    } catch (err) {
      setState({ kind: 'error', message: errorMessage(err) });
    }
  };

  const handleRestore = async () => {
    setState({ kind: 'busy' });
    try {
      await deleteBYODaVinci();
      setState({ kind: 'restart-pending' });
      onSaved();
    } catch (err) {
      setState({ kind: 'error', message: errorMessage(err) });
    }
  };

  return (
    <section className="byo-section byo-davinci">
      <h3>Da Vinci (inbound ingress)</h3>

      {state.kind === 'restart-pending' ? (
        <RestartPendingNote onRestart={onRestart} />
      ) : (
        <>
          {davinci?.applied && (
            <div className="byo-connected">
              <p>Connected — your Da Vinci system is registered as an ingress client.</p>
              <button
                type="button"
                className="btn btn-secondary"
                disabled={state.kind === 'busy'}
                onClick={() => {
                  void handleRestore();
                }}
              >
                Restore demo data
              </button>
            </div>
          )}
          {davinci && !davinci.applied && (
            <p className="byo-restart-banner">
              saved, not yet applied — runs still use the demo data source
            </p>
          )}
        </>
      )}

      <form
        onSubmit={(e) => {
          void handleSave(e);
        }}
      >
        <label>
          Client ID
          <input type="text" required value={clientId} onChange={(e) => setClientId(e.target.value)} />
        </label>
        <label>
          Algorithm
          <input type="text" required value={alg} onChange={(e) => setAlg(e.target.value)} />
        </label>
        <label>
          Public key (PEM)
          <textarea required value={publicKeyPem} onChange={(e) => setPublicKeyPem(e.target.value)} />
        </label>

        {ingress && (
          <div className="byo-ingress">
            <h4>Ingress</h4>
            <dl>
              <dt>Base URL</dt>
              <dd>{ingress.baseUrl}</dd>
              <dt>Token URL</dt>
              <dd>{ingress.tokenUrl}</dd>
              <dt>SMART configuration</dt>
              <dd>{ingress.smartConfigUrl}</dd>
            </dl>
            <p>Endpoints:</p>
            <ul>
              {ingress.endpoints.map((ep) => (
                <li key={ep}>{ep}</li>
              ))}
            </ul>
          </div>
        )}

        <p className="byo-loopback-note">{LOOPBACK_SENTENCE}</p>
        <p className="byo-awareness-note">{AWARENESS_NOTE}</p>

        <button type="submit" className="btn btn-primary" disabled={state.kind === 'busy'}>
          Save
        </button>
      </form>

      {state.kind === 'error' && (
        <p role="alert" className="byo-error">
          {state.message}
        </p>
      )}
    </section>
  );
}

function LoadErrorBanner({ loadError, onSaved }: { loadError: string; onSaved: () => void }): JSX.Element {
  const [clearing, setClearing] = useState(false);
  const [error, setError] = useState<string | undefined>(undefined);

  const handleClear = async () => {
    setClearing(true);
    setError(undefined);
    try {
      // The saved config is a single file holding both lanes (kit/byo.Store)
      // — a corrupt file corrupts both, so recovering means clearing both
      // (each DELETE is independently safe: it re-persists a fresh, valid
      // file even when the other lane's clear has already run).
      await Promise.all([deleteBYOEhr(), deleteBYODaVinci()]);
      onSaved();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setClearing(false);
    }
  };

  return (
    <div className="byo-load-error-banner" role="alert">
      <p>Your bring-your-own configuration could not be read: {loadError}</p>
      <p>Clear and reconfigure resets BOTH lanes back to demo data.</p>
      <button
        type="button"
        className="btn btn-secondary"
        disabled={clearing}
        onClick={() => {
          void handleClear();
        }}
      >
        Clear and reconfigure
      </button>
      {error && <p className="byo-error">{error}</p>}
    </div>
  );
}

export function BYOPanel({ byo, onSaved, onRestart }: BYOPanelProps): JSX.Element {
  return (
    <div className="byo-panel">
      <h2>Bring your own</h2>
      {byo.loadError && <LoadErrorBanner loadError={byo.loadError} onSaved={onSaved} />}
      <EHRSection ehr={byo.ehr} onSaved={onSaved} onRestart={onRestart} />
      <DaVinciSection davinci={byo.davinci} ingress={byo.ingress} onSaved={onSaved} onRestart={onRestart} />
    </div>
  );
}

export default BYOPanel;
