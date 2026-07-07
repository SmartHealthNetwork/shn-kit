import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { BYOPanel } from './BYOPanel';
import type { BYOStatus } from './types';

// vi.mock factories are hoisted above the rest of the module, so ApiError
// must be created through vi.hoisted (mirrors App.test.tsx / UCCards.test.tsx).
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
  putBYOEhr: vi.fn(),
  deleteBYOEhr: vi.fn(),
  putBYODaVinci: vi.fn(),
  deleteBYODaVinci: vi.fn(),
  ApiError,
}));

import * as api from './api';

function byoStatus(overrides: Partial<BYOStatus> = {}): BYOStatus {
  return { ehr: null, davinci: null, ingress: null, ...overrides };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.putBYOEhr).mockResolvedValue({ restartRequired: true });
  vi.mocked(api.deleteBYOEhr).mockResolvedValue({ restartRequired: true });
  vi.mocked(api.putBYODaVinci).mockResolvedValue({ restartRequired: true });
  vi.mocked(api.deleteBYODaVinci).mockResolvedValue({ restartRequired: true });
});

describe('BYOPanel — EHR section', () => {
  it('dataUrl is required; an "authentication (optional)" fieldset carries the quad + key textarea', () => {
    render(<BYOPanel byo={byoStatus()} onSaved={vi.fn()} onRestart={vi.fn()} />);

    const ehrSection = within(screen.getByText('EHR (data source)').closest('section') as HTMLElement);
    expect(ehrSection.getByLabelText(/data url/i)).toBeRequired();
    expect(ehrSection.getByText(/authentication \(optional\)/i)).toBeDefined();
    expect(ehrSection.getByLabelText(/token url/i)).toBeDefined();
    expect(ehrSection.getByLabelText(/client id/i)).toBeDefined();
    expect(ehrSection.getByLabelText(/algorithm/i)).toBeDefined();
    expect(ehrSection.getByLabelText(/scope/i)).toBeDefined();
    expect(ehrSection.getByLabelText(/key id/i)).toBeDefined();
    expect(ehrSection.getByLabelText(/client key/i)).toBeDefined();
  });

  it('Save calls putBYOEhr with exactly the filled fields; success renders the restart-pending affordance', async () => {
    const onSaved = vi.fn();
    const onRestart = vi.fn();
    render(<BYOPanel byo={byoStatus()} onSaved={onSaved} onRestart={onRestart} />);

    const ehrSection = within(screen.getByText('EHR (data source)').closest('section') as HTMLElement);
    await userEvent.type(ehrSection.getByLabelText(/data url/i), 'https://ehr.example.org/fhir');
    await userEvent.type(ehrSection.getByLabelText(/client id/i), 'client-1');
    await userEvent.click(ehrSection.getByRole('button', { name: /^save$/i }));

    expect(api.putBYOEhr).toHaveBeenCalledWith({ dataUrl: 'https://ehr.example.org/fhir', clientId: 'client-1' });
    expect(await screen.findByText(/saved — restart the kit to apply/i)).toBeDefined();
    expect(onSaved).toHaveBeenCalledOnce();

    await userEvent.click(screen.getByRole('button', { name: /restart the kit now/i }));
    expect(onRestart).toHaveBeenCalledOnce();
  });

  it('422 renders the server detail verbatim; nothing else changes', async () => {
    vi.mocked(api.putBYOEhr).mockRejectedValue(new ApiError('dataUrl: probe failed: connection refused', 422));
    const onSaved = vi.fn();
    render(<BYOPanel byo={byoStatus()} onSaved={onSaved} onRestart={vi.fn()} />);

    const ehrSection = within(screen.getByText('EHR (data source)').closest('section') as HTMLElement);
    await userEvent.type(ehrSection.getByLabelText(/data url/i), 'https://ehr.example.org/fhir');
    await userEvent.click(ehrSection.getByRole('button', { name: /^save$/i }));

    expect(await screen.findByText(/dataUrl: probe failed: connection refused/i)).toBeDefined();
    expect(onSaved).not.toHaveBeenCalled();
    expect(screen.queryByText(/saved — restart the kit to apply/i)).toBeNull();
    expect((ehrSection.getByLabelText(/data url/i) as HTMLInputElement).value).toBe('https://ehr.example.org/fhir');
  });
});

describe('BYOPanel — EHR applied states', () => {
  it('applied:true shows the connected copy + Restore demo data -> deleteBYOEhr -> restart-pending affordance', async () => {
    const onSaved = vi.fn();
    const byo = byoStatus({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: true, demoPersonas: null },
    });
    render(<BYOPanel byo={byo} onSaved={onSaved} onRestart={vi.fn()} />);

    expect(screen.getByText(/connected — your ehr is the ehr lane's data source/i)).toBeDefined();

    await userEvent.click(screen.getByRole('button', { name: /restore demo data/i }));

    expect(api.deleteBYOEhr).toHaveBeenCalledOnce();
    expect(await screen.findByText(/saved — restart the kit to apply/i)).toBeDefined();
    expect(onSaved).toHaveBeenCalledOnce();
  });

  it('applied:false with saved config shows the restart-pending banner', () => {
    const byo = byoStatus({
      ehr: { dataUrl: 'https://ehr.example.org/fhir', hasClientKey: false, applied: false, demoPersonas: null },
    });
    render(<BYOPanel byo={byo} onSaved={vi.fn()} onRestart={vi.fn()} />);

    expect(
      screen.getByText(/saved, not yet applied — runs still use the demo data source/i),
    ).toBeDefined();
  });
});

describe('BYOPanel — Da Vinci section', () => {
  it('the form saves via putBYODaVinci; the ingress block + loopback sentence + awareness note render pinned exactly', async () => {
    const byo = byoStatus({
      ingress: {
        baseUrl: 'http://127.0.0.1:54321',
        tokenUrl: 'http://127.0.0.1:54321/oauth/token',
        smartConfigUrl: 'http://127.0.0.1:54321/.well-known/smart-configuration',
        endpoints: ['/cds-services', '/cds-services/{id}', '/Questionnaire/$questionnaire-package', '/Claim/$submit'],
      },
    });
    render(<BYOPanel byo={byo} onSaved={vi.fn()} onRestart={vi.fn()} />);

    const dvSection = within(screen.getByText('Da Vinci (inbound ingress)').closest('section') as HTMLElement);

    expect(dvSection.getByText('http://127.0.0.1:54321')).toBeDefined();
    expect(dvSection.getByText('http://127.0.0.1:54321/oauth/token')).toBeDefined();
    expect(dvSection.getByText('http://127.0.0.1:54321/.well-known/smart-configuration')).toBeDefined();
    expect(dvSection.getByText('/Claim/$submit')).toBeDefined();

    expect(
      dvSection.getByText('your system must run on this machine — the Kit does not open a remote listener'),
    ).toBeDefined();
    expect(
      dvSection.getByText('the inspector displays full payloads from your connected systems'),
    ).toBeDefined();

    await userEvent.type(dvSection.getByLabelText(/client id/i), 'partner-1');
    await userEvent.type(dvSection.getByLabelText(/algorithm/i), 'RS384');
    await userEvent.type(dvSection.getByLabelText(/public key/i), '-----BEGIN PUBLIC KEY-----');
    await userEvent.click(dvSection.getByRole('button', { name: /^save$/i }));

    expect(api.putBYODaVinci).toHaveBeenCalledWith({
      clientId: 'partner-1',
      alg: 'RS384',
      publicKeyPem: '-----BEGIN PUBLIC KEY-----',
    });
  });
});

describe('BYOPanel — loadError', () => {
  it('renders the corrupted-config banner with a Clear and reconfigure action that clears both lanes', async () => {
    const onSaved = vi.fn();
    const byo = byoStatus({ loadError: 'kit/byo: parse byo.json: unexpected EOF' });
    render(<BYOPanel byo={byo} onSaved={onSaved} onRestart={vi.fn()} />);

    expect(screen.getByText(/kit\/byo: parse byo\.json: unexpected eof/i)).toBeDefined();
    // The copy states BOTH lanes are cleared BEFORE the click
    // (it already did clear both — the copy must say so up front).
    expect(screen.getByText(/clear and reconfigure resets both lanes back to demo data/i)).toBeDefined();

    await userEvent.click(screen.getByRole('button', { name: /clear and reconfigure/i }));

    expect(api.deleteBYOEhr).toHaveBeenCalledOnce();
    expect(api.deleteBYODaVinci).toHaveBeenCalledOnce();
    expect(onSaved).toHaveBeenCalledOnce();
  });
});

describe('BYOPanel — key hygiene', () => {
  it('the key textarea is cleared after a successful save; hasClientKey:true renders "a client key is stored"; key material is never rendered', async () => {
    const byo = byoStatus({
      ehr: {
        dataUrl: 'https://ehr.example.org/fhir',
        hasClientKey: true,
        applied: true,
        demoPersonas: null,
      },
    });
    render(<BYOPanel byo={byo} onSaved={vi.fn()} onRestart={vi.fn()} />);

    const ehrSection = within(screen.getByText('EHR (data source)').closest('section') as HTMLElement);
    expect(ehrSection.getByText(/a client key is stored/i)).toBeDefined();

    const keyField = ehrSection.getByLabelText(/client key/i) as HTMLTextAreaElement;
    expect(keyField.value).toBe('');

    await userEvent.type(keyField, '-----BEGIN PRIVATE KEY-----typed-key-material');
    await userEvent.click(ehrSection.getByRole('button', { name: /^save$/i }));

    await screen.findByText(/saved — restart the kit to apply/i);
    // The document no longer contains the just-typed key material anywhere
    // (the restart-pending state replaces the form).
    expect(screen.queryByText(/typed-key-material/i)).toBeNull();
  });
});
