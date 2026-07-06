// RunHistory.tsx — run-history panel (list/reopen/compare/export).
// Presentational + dispatch only — App owns the `history` fetch,
// `compareRunId` state, and the export byte-plumbing, so this component
// can be tested standalone (mirrors UCCards' split).
import type { JSX } from 'react';
import type { HistorySummary } from './types';

export interface RunHistoryProps {
  history: HistorySummary[];
  selectedRunId?: string;
  compareRunId?: string;
  onOpen(runId: string): void; // App: manual-pin selection (manualPickRef)
  onCompare(runId: string): void; // toggles the compare pane (same id again => close)
  onExport(runId: string): void;
}

export function RunHistory({
  history,
  selectedRunId,
  compareRunId,
  onOpen,
  onCompare,
  onExport,
}: RunHistoryProps): JSX.Element {
  if (history.length === 0) {
    return (
      <div className="run-history empty-state">
        <p>No runs yet.</p>
      </div>
    );
  }

  return (
    <div className="run-history">
      <h2>Run history</h2>
      <ul className="run-history-list">
        {history.map((h) => {
          const isSelected = h.runId === selectedRunId;
          const isComparing = h.runId === compareRunId;
          const rowClass = ['run-history-row', isSelected && 'selected', isComparing && 'comparing']
            .filter(Boolean)
            .join(' ');

          return (
            <li key={h.runId} data-testid={`history-row-${h.runId}`} className={rowClass}>
              <div className="run-history-facts">
                <span className="run-history-lane-uc">{`${h.lane}/${h.uc}`}</span>
                {h.branch && (
                  <span className="run-history-branch" data-testid={`history-branch-${h.runId}`}>
                    {h.branch}
                  </span>
                )}
                <span className={`result-badge result-${h.state}`}>
                  {h.state === 'passed' ? 'Passed' : 'Failed'}
                </span>
                <span className="run-history-time">{new Date(h.time).toLocaleTimeString()}</span>
                {isComparing && <span className="run-history-comparing-marker">comparing</span>}
              </div>
              <div className="run-history-actions">
                <button
                  type="button"
                  className="btn btn-link"
                  aria-label={`Open ${h.runId}`}
                  onClick={() => onOpen(h.runId)}
                >
                  Open
                </button>
                <button
                  type="button"
                  className="btn btn-link"
                  aria-label={`Compare ${h.runId}`}
                  onClick={() => onCompare(h.runId)}
                >
                  Compare
                </button>
                <button
                  type="button"
                  className="btn btn-link"
                  aria-label={`Export ${h.runId}`}
                  onClick={() => onExport(h.runId)}
                >
                  Export
                </button>
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

export default RunHistory;
