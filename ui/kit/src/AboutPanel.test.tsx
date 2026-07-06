import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { AboutPanel } from './AboutPanel';
import type { AboutManifest } from './types';

vi.mock('./api', () => ({
  getAbout: vi.fn(),
}));

import * as api from './api';

function manifest(overrides: Partial<AboutManifest> = {}): AboutManifest {
  return {
    kit: '1.0.0',
    modules: { 'shn-gateway': 'v0.20.1', 'shn-sdk': 'v0.27.0' },
    brProvider: 'a8bece4',
    hapiImage: 'sha256:deadbeef',
    temurin: '21.0.4+7',
    igsValidator: [
      'hl7.fhir.us.core 6.1.0',
      'hl7.fhir.us.davinci-crd 2.0.1',
    ],
    igsData: ['hl7.fhir.us.davinci-pas 2.0.1'],
    build: { timestamp: '2026-07-04T00:00:00Z', commit: 'abc1234' },
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('AboutPanel', () => {
  it('renders every manifest field once getAbout() resolves', async () => {
    vi.mocked(api.getAbout).mockResolvedValue(manifest());
    render(<AboutPanel />);

    await waitFor(() => expect(screen.getByText('1.0.0')).toBeDefined());
    expect(screen.getByText('v0.20.1')).toBeDefined();
    expect(screen.getByText('v0.27.0')).toBeDefined();
    expect(screen.getByText('a8bece4')).toBeDefined();
    expect(screen.getByText('sha256:deadbeef')).toBeDefined();
    expect(screen.getByText('21.0.4+7')).toBeDefined();
    expect(screen.getByText(/abc1234/)).toBeDefined();
    expect(screen.getByText(/2026-07-04T00:00:00Z/)).toBeDefined();
    expect(screen.getByText(/Validator IGs \(2\)/)).toBeDefined();
    expect(screen.getByText(/Data server IGs \(1\)/)).toBeDefined();
    expect(screen.getByText('hl7.fhir.us.core 6.1.0')).toBeDefined();
    expect(screen.getByText('hl7.fhir.us.davinci-pas 2.0.1')).toBeDefined();
  });

  it('absent manifest (404, dev build) renders the honest "development build" note, not fabricated versions', async () => {
    vi.mocked(api.getAbout).mockRejectedValue(new Error('manifest not available (dev build)'));
    render(<AboutPanel />);

    await waitFor(() =>
      expect(screen.getByText('development build — no packaged manifest')).toBeDefined(),
    );
    expect(screen.queryByText('1.0.0')).toBeNull();
  });
});
