import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  FreeFormPanel,
  FREEFORM_PROVENANCE_LINE,
  MEMBER_REQUIREMENTS_NOTE,
} from './FreeFormPanel';
import type { EventsView } from './useEvents';
import type { PatientContext, PatientSummary, RunResult } from './types';

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
  getBYOPatients: vi.fn(),
  getBYOContext: vi.fn(),
  postRun: vi.fn(),
  ApiError,
}));

import * as api from './api';

function events(activeRunId?: string): EventsView {
  return {
    all: [],
    byRun: () => [],
    activeRunId,
    sseState: 'open',
  };
}

function patient(overrides: Partial<PatientSummary> = {}): PatientSummary {
  return {
    fhirId: 'pt-1',
    memberId: 'MBR-PD-UC03',
    name: 'Linda Johansson',
    birthDate: '1970-01-01',
    ...overrides,
  };
}

function context(overrides: Partial<PatientContext> = {}): PatientContext {
  return {
    order: { resourceType: 'DeviceRequest', id: 'order-1' },
    orderSummary: 'E0424 portable oxygen concentrator (active)',
    coverage: { resourceType: 'Coverage', id: 'cov-1' },
    coverageSummary: 'Acme Health (active)',
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.getBYOPatients).mockResolvedValue([patient()]);
  vi.mocked(api.getBYOContext).mockResolvedValue(context());
  vi.mocked(api.postRun).mockResolvedValue({ runId: 'run-freeform-1' });
});

describe('FreeFormPanel — patients + member requirements note', () => {
  it('loads patients on mount and renders the member list', async () => {
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={vi.fn()} />);

    expect(api.getBYOPatients).toHaveBeenCalledOnce();
    expect(await screen.findByText(/linda johansson/i)).toBeDefined();
    expect(screen.getByText(/MBR-PD-UC03/)).toBeDefined();
  });

  it('shows the run\'s provenance line', async () => {
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={vi.fn()} />);
    expect(screen.getByText(FREEFORM_PROVENANCE_LINE)).toBeDefined();
  });

  it("shows the urn:shn:member requirement copy (the honest onboarding note)", async () => {
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={vi.fn()} />);
    expect(screen.getByText(/urn:shn:member/)).toBeDefined();
    expect(screen.getByText(MEMBER_REQUIREMENTS_NOTE)).toBeDefined();
  });

  it('a getBYOPatients failure renders an alert instead of an unhandled rejection', async () => {
    vi.mocked(api.getBYOPatients).mockRejectedValue(new ApiError('connect your EHR and restart the Kit first', 409));
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={vi.fn()} />);

    expect(await screen.findByRole('alert')).toHaveTextContent(/connect your ehr/i);
  });
});

describe('FreeFormPanel — selecting a patient loads context', () => {
  it('selecting one loads context (order/coverage summaries) and enables Run', async () => {
    const user = userEvent.setup();
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={vi.fn()} />);

    await user.click(await screen.findByRole('button', { name: /linda johansson/i }));

    expect(api.getBYOContext).toHaveBeenCalledWith('pt-1');
    expect(await screen.findByText(/E0424 portable oxygen concentrator/i)).toBeDefined();
    expect(screen.getByText(/acme health/i)).toBeDefined();

    const runButton = screen.getByRole('button', { name: /^run$/i });
    expect(runButton).not.toBeDisabled();
  });

  it('the no-open-order message disables Run', async () => {
    vi.mocked(api.getBYOContext).mockResolvedValue(
      context({
        order: null,
        orderSummary: 'no open order found — the free-form run needs one active DeviceRequest or ServiceRequest',
      }),
    );
    const user = userEvent.setup();
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={vi.fn()} />);

    await user.click(await screen.findByRole('button', { name: /linda johansson/i }));

    expect(await screen.findByText(/no open order found/i)).toBeDefined();
    const runButton = screen.getByRole('button', { name: /^run$/i });
    expect(runButton).toBeDisabled();
  });
});

describe('FreeFormPanel — Run', () => {
  it("Run posts lane 'ehr' uc 'freeform' branch '' with the selected member, then calls onSelectRun", async () => {
    const onSelectRun = vi.fn();
    const user = userEvent.setup();
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={onSelectRun} />);

    await user.click(await screen.findByRole('button', { name: /linda johansson/i }));
    await screen.findByText(/E0424 portable oxygen concentrator/i);
    await user.click(screen.getByRole('button', { name: /^run$/i }));

    await waitFor(() => expect(api.postRun).toHaveBeenCalledWith('ehr', 'freeform', '', 'MBR-PD-UC03'));
    await waitFor(() => expect(onSelectRun).toHaveBeenCalledWith('run-freeform-1'));
  });

  it('a 409 from postRun renders an inline in-flight notice', async () => {
    vi.mocked(api.postRun).mockRejectedValue(new ApiError('run already active', 409));
    const user = userEvent.setup();
    render(<FreeFormPanel events={events()} results={[]} onSelectRun={vi.fn()} />);

    await user.click(await screen.findByRole('button', { name: /linda johansson/i }));
    await screen.findByText(/E0424 portable oxygen concentrator/i);
    await user.click(screen.getByRole('button', { name: /^run$/i }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/in flight/i);
  });
});

describe('FreeFormPanel — result rows', () => {
  it('a freeform result row renders passed/failed state', async () => {
    const results: RunResult[] = [
      { runId: 'run-a', lane: 'ehr', uc: 'freeform', branch: '', state: 'passed', detail: 'auth number AUTH-1' },
      { runId: 'run-b', lane: 'ehr', uc: 'freeform', branch: '', state: 'failed', detail: 'unknown member' },
      // a non-freeform row must not leak into the free-form results list.
      { runId: 'run-c', lane: 'ehr', uc: 'uc01', branch: 'covered', state: 'passed', detail: 'ok' },
    ];
    const onSelectRun = vi.fn();
    render(<FreeFormPanel events={events()} results={results} onSelectRun={onSelectRun} />);

    const list = within(screen.getByText(/auth number auth-1/i).closest('ul') as HTMLElement);
    expect(list.getByText('Passed')).toBeDefined();
    expect(list.getByText('Failed')).toBeDefined();
    expect(screen.queryByText('run-c')).toBeNull();

    await userEvent.setup().click(list.getAllByRole('button', { name: /view in inspector/i })[0]);
    expect(onSelectRun).toHaveBeenCalledWith('run-a');
  });
});
