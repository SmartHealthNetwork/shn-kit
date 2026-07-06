// RunHistory.test.tsx — the presentational run-history panel. App owns the
// history state/fetch and passes it in.
import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RunHistory } from './RunHistory';
import type { HistorySummary } from './types';

function summary(overrides: Partial<HistorySummary> = {}): HistorySummary {
  return {
    runId: 'run-1',
    lane: 'ehr',
    uc: 'uc03',
    branch: '',
    state: 'passed',
    detail: 'approved, auth #A1',
    time: '2026-07-03T14:00:00Z',
    eventCount: 5,
    ...overrides,
  };
}

describe('RunHistory — empty state', () => {
  it('renders "No runs yet." when history is empty', () => {
    render(<RunHistory history={[]} onOpen={vi.fn()} onCompare={vi.fn()} onExport={vi.fn()} />);
    expect(screen.getByText('No runs yet.')).toBeDefined();
    expect(screen.queryAllByRole('listitem')).toHaveLength(0);
  });
});

describe('RunHistory — rows', () => {
  it('renders one row per summary: lane/uc, branch when non-empty, state badge, local time', () => {
    const withBranch = summary({ runId: 'run-1', lane: 'ehr', uc: 'uc01', branch: 'covered', state: 'passed' });
    const noBranch = summary({
      runId: 'run-2',
      lane: 'conformant',
      uc: 'uc03',
      branch: '',
      state: 'failed',
      detail: 'denied',
      time: '2026-07-03T15:30:00Z',
    });
    render(<RunHistory history={[withBranch, noBranch]} onOpen={vi.fn()} onCompare={vi.fn()} onExport={vi.fn()} />);

    expect(screen.getByText('ehr/uc01')).toBeDefined();
    expect(screen.getByText('covered')).toBeDefined();
    expect(screen.getByText('conformant/uc03')).toBeDefined();
    expect(screen.getByText(new Date(withBranch.time).toLocaleTimeString())).toBeDefined();
    expect(screen.getByText('Passed')).toBeDefined();
    expect(screen.getByText('Failed')).toBeDefined();
  });

  it('does not render a branch element for a branchless row', () => {
    render(<RunHistory history={[summary({ runId: 'run-1', branch: '' })]} onOpen={vi.fn()} onCompare={vi.fn()} onExport={vi.fn()} />);
    expect(screen.queryByTestId('history-branch-run-1')).toBeNull();
  });

  it('Open calls onOpen(runId)', async () => {
    const onOpen = vi.fn();
    render(<RunHistory history={[summary({ runId: 'run-1' })]} onOpen={onOpen} onCompare={vi.fn()} onExport={vi.fn()} />);
    await userEvent.click(screen.getByRole('button', { name: /open run-1/i }));
    expect(onOpen).toHaveBeenCalledWith('run-1');
  });

  it('Compare calls onCompare(runId) and the compared row shows a "comparing" marker', async () => {
    const onCompare = vi.fn();
    const { rerender } = render(
      <RunHistory history={[summary({ runId: 'run-1' })]} onOpen={vi.fn()} onCompare={onCompare} onExport={vi.fn()} />,
    );
    expect(screen.queryByText(/comparing/i)).toBeNull();

    await userEvent.click(screen.getByRole('button', { name: /compare run-1/i }));
    expect(onCompare).toHaveBeenCalledWith('run-1');

    // App owns compareRunId state; simulate it flowing back in as a prop.
    rerender(
      <RunHistory
        history={[summary({ runId: 'run-1' })]}
        compareRunId="run-1"
        onOpen={vi.fn()}
        onCompare={onCompare}
        onExport={vi.fn()}
      />,
    );
    expect(screen.getByText(/comparing/i)).toBeDefined();
  });

  it('Export calls onExport(runId)', async () => {
    const onExport = vi.fn();
    render(<RunHistory history={[summary({ runId: 'run-1' })]} onOpen={vi.fn()} onCompare={vi.fn()} onExport={onExport} />);
    await userEvent.click(screen.getByRole('button', { name: /export run-1/i }));
    expect(onExport).toHaveBeenCalledWith('run-1');
  });

  it('the selected row is marked; other rows are not', () => {
    render(
      <RunHistory
        history={[summary({ runId: 'run-1' }), summary({ runId: 'run-2' })]}
        selectedRunId="run-2"
        onOpen={vi.fn()}
        onCompare={vi.fn()}
        onExport={vi.fn()}
      />,
    );
    const row2 = screen.getByTestId('history-row-run-2');
    const row1 = screen.getByTestId('history-row-run-1');
    expect(row2.className).toMatch(/selected/);
    expect(row1.className).not.toMatch(/selected/);
  });
});
