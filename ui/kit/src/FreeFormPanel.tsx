// FreeFormPanel.tsx — the ehr lane's bring-your-own surface: browses the
// connected partner EHR for a patient, previews their open order + coverage
// (the SAME reads the origination will make at run time, so the preview is
// honest about what it will find), and runs the data-derived order-dispatch
// PA spine (POST /api/runs lane "ehr" uc "freeform") against the selected
// patient's member id. This is the ehr lane's surface once an EHR swap is
// applied — the 8 seeded cards grey out in its favor (App.tsx wires that
// swap; this component itself has no BYO awareness, it just IS the
// free-form surface). Presentational + local fetch state (the same
// component idioms used throughout the renderer): no props beyond what the
// caller needs to drive selection/results.
import { useEffect, useState } from 'react';
import type { JSX } from 'react';
import type { PatientContext, PatientSummary, RunResult } from './types';
import type { EventsView } from './useEvents';
import { ApiError, getBYOContext, getBYOPatients, postRun } from './api';

export interface FreeFormPanelProps {
  events: EventsView;
  results: RunResult[];
  onSelectRun(runId: string): void;
}

// Pinned exactly — the free-form run's provenance line.
export const FREEFORM_PROVENANCE_LINE = "originated by the Smart Gateway off your EHR's data";

// The honest onboarding requirement every free-form patient must satisfy,
// stated here (not just in docs) so the panel names the constraint BEFORE
// a run ever fails on it. Identifier VALUES matter too, not just presence
// — the demo payer only covers the seeded ids, so an arbitrary member id a
// partner server carries is not automatically runnable.
export const MEMBER_REQUIREMENTS_NOTE =
  'Patients must carry a urn:shn:member identifier to be runnable, and its value must be a member id the payer counterparty covers — the demo payer covers the seeded ids.';

const IN_FLIGHT_NOTICE = 'A run is already in flight — wait for it to finish before starting another.';

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}

type ContextState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'loaded'; context: PatientContext }
  | { kind: 'error'; message: string };

export function FreeFormPanel({ events, results, onSelectRun }: FreeFormPanelProps): JSX.Element {
  const [patients, setPatients] = useState<PatientSummary[]>([]);
  const [patientsError, setPatientsError] = useState<string | undefined>(undefined);
  const [selected, setSelected] = useState<PatientSummary | undefined>(undefined);
  const [contextState, setContextState] = useState<ContextState>({ kind: 'idle' });
  const [posting, setPosting] = useState(false);
  const [runNotice, setRunNotice] = useState<string | undefined>(undefined);

  useEffect(() => {
    getBYOPatients()
      .then(setPatients)
      .catch((err: unknown) => setPatientsError(errorMessage(err)));
  }, []);

  const handleSelect = (p: PatientSummary) => {
    setSelected(p);
    setRunNotice(undefined);
    setContextState({ kind: 'loading' });
    getBYOContext(p.fhirId)
      .then((context) => setContextState({ kind: 'loaded', context }))
      .catch((err: unknown) => setContextState({ kind: 'error', message: errorMessage(err) }));
  };

  const hasOpenOrder = contextState.kind === 'loaded' && contextState.context.order != null;
  const sseInFlight = events.activeRunId !== undefined;
  const canRun = selected !== undefined && hasOpenOrder && !posting && !sseInFlight;

  const handleRun = async () => {
    if (!selected) return;
    setPosting(true);
    setRunNotice(undefined);
    try {
      const res = await postRun('ehr', 'freeform', '', selected.memberId);
      onSelectRun(res.runId);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setRunNotice(IN_FLIGHT_NOTICE);
      } else {
        setRunNotice(errorMessage(err));
      }
    } finally {
      setPosting(false);
    }
  };

  const freeformResults = results.filter((r) => r.lane === 'ehr' && r.uc === 'freeform');

  return (
    <div className="freeform-panel">
      <h3>Free-form (your EHR&apos;s data)</h3>
      <p className="freeform-provenance">{FREEFORM_PROVENANCE_LINE}</p>
      <p className="freeform-member-note">{MEMBER_REQUIREMENTS_NOTE}</p>

      {patientsError && (
        <p role="alert" className="freeform-error">
          {patientsError}
        </p>
      )}

      <ul className="freeform-patient-list">
        {patients.map((p) => (
          <li key={p.fhirId}>
            <button
              type="button"
              className="btn btn-link"
              aria-current={selected?.fhirId === p.fhirId ? 'true' : undefined}
              onClick={() => handleSelect(p)}
            >
              {p.name} ({p.memberId})
            </button>
          </li>
        ))}
      </ul>

      {selected && (
        <div className="freeform-context">
          <h4>{selected.name}</h4>
          {contextState.kind === 'loading' && <p>Loading…</p>}
          {contextState.kind === 'error' && (
            <p role="alert" className="freeform-error">
              {contextState.message}
            </p>
          )}
          {contextState.kind === 'loaded' && (
            <>
              <p className="freeform-order-summary">{contextState.context.orderSummary}</p>
              <p className="freeform-coverage-summary">{contextState.context.coverageSummary}</p>
            </>
          )}

          <button
            type="button"
            className="btn btn-primary"
            aria-label="Run"
            disabled={!canRun}
            onClick={() => {
              void handleRun();
            }}
          >
            Run
          </button>
          {runNotice && (
            <div role="alert" className="freeform-notice">
              {runNotice}
            </div>
          )}
        </div>
      )}

      <ul className="freeform-results">
        {freeformResults.map((r) => (
          <li key={r.runId} className={`result-badge result-${r.state}`}>
            <span className="badge-label">{r.state === 'passed' ? 'Passed' : 'Failed'}</span>
            <p className="result-detail">{r.detail}</p>
            <button type="button" className="btn btn-link" onClick={() => onSelectRun(r.runId)}>
              View in inspector
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

export default FreeFormPanel;
