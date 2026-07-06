import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

function makeFetch(ok: boolean, status: number, body: unknown) {
  return vi.fn().mockResolvedValue({
    ok,
    status,
    json: () => Promise.resolve(body),
  });
}

// bridge.ts caches the resolved token at module scope, so each test that
// exercises token resolution needs a fresh module instance.
async function freshApi() {
  vi.resetModules();
  return import('./api');
}
async function freshBridge() {
  vi.resetModules();
  return import('./bridge');
}

beforeEach(() => {
  sessionStorage.clear();
  delete (window as unknown as { kit?: unknown }).kit;
  window.history.replaceState({}, '', '/');
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('getBootstrap', () => {
  it('GETs /api/bootstrap with the resolved bearer token and parses {state, verify}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, {
      state: 'provisioned',
      verify: [{ name: 'hub', ok: true, detail: 'reachable' }],
    });
    vi.stubGlobal('fetch', stub);

    const { getBootstrap } = await freshApi();
    const result = await getBootstrap();

    expect(stub).toHaveBeenCalledOnce();
    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/bootstrap');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result.state).toBe('provisioned');
    expect(result.verify).toHaveLength(1);
  });
});

describe('postSignIn', () => {
  it('POSTs /api/bootstrap/signin and parses {authorizeUrl}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { authorizeUrl: 'https://payer.example/authorize' });
    vi.stubGlobal('fetch', stub);

    const { postSignIn } = await freshApi();
    const result = await postSignIn();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/bootstrap/signin');
    expect(init.method).toBe('POST');
    expect(result.authorizeUrl).toBe('https://payer.example/authorize');
  });

  it('409 rejects with ApiError(status 409) carrying the server error message', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(false, 409, { error: 'sign-in already in progress' });
    vi.stubGlobal('fetch', stub);

    const { postSignIn, ApiError } = await freshApi();

    try {
      await postSignIn();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(409);
      expect((e as Error).message).toBe('sign-in already in progress');
    }
  });
});

describe('postRun', () => {
  it('POSTs /api/runs with body exactly {lane,uc,branch}; 202 -> {runId}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 202, { runId: 'run-1' });
    vi.stubGlobal('fetch', stub);

    const { postRun } = await freshApi();
    const result = await postRun('ehr', 'uc01', 'covered');

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/runs');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ lane: 'ehr', uc: 'uc01', branch: 'covered' });
    expect(result.runId).toBe('run-1');
  });

  it('409 -> ApiError(409)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 409, { error: 'run already active' }));

    const { postRun, ApiError } = await freshApi();
    try {
      await postRun('ehr', 'uc01', 'covered');
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(409);
      expect((e as Error).message).toBe('run already active');
    }
  });

  it('503 -> ApiError(503)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 503, { error: 'substrate not ready' }));

    const { postRun, ApiError } = await freshApi();
    try {
      await postRun('conformant', 'uc08', 'deny');
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(503);
    }
  });
});

describe('postRun (freeform member)', () => {
  it('POSTs /api/runs with body {lane,uc,branch,member} when member is passed', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 202, { runId: 'run-2' });
    vi.stubGlobal('fetch', stub);

    const { postRun } = await freshApi();
    const result = await postRun('ehr', 'freeform', '', 'MBR-1');

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/runs');
    expect(JSON.parse(init.body as string)).toEqual({
      lane: 'ehr',
      uc: 'freeform',
      branch: '',
      member: 'MBR-1',
    });
    expect(result.runId).toBe('run-2');
  });

  it('omits the member key entirely when not passed (existing callers unaffected)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 202, { runId: 'run-1' });
    vi.stubGlobal('fetch', stub);

    const { postRun } = await freshApi();
    await postRun('ehr', 'uc01', 'covered');

    const [, init] = stub.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect('member' in body).toBe(false);
  });
});

describe('getBYOPatients', () => {
  it('GETs /api/byo/patients with the resolved bearer token and parses PatientSummary[]', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const patients = [
      { fhirId: 'pt-1', memberId: 'MBR-1', name: 'Linda Johansson', birthDate: '1970-01-01' },
    ];
    const stub = makeFetch(true, 200, patients);
    vi.stubGlobal('fetch', stub);

    const { getBYOPatients } = await freshApi();
    const result = await getBYOPatients();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/byo/patients');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result).toEqual(patients);
  });

  it('409 -> ApiError(409) ("connect your EHR and restart the Kit first")', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal(
      'fetch',
      makeFetch(false, 409, { error: 'connect your EHR and restart the Kit first' }),
    );

    const { getBYOPatients, ApiError } = await freshApi();
    try {
      await getBYOPatients();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(409);
    }
  });
});

describe('getBYOContext', () => {
  it('GETs /api/byo/patients/{fhirId}/context and parses PatientContext', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const context = {
      order: { resourceType: 'DeviceRequest' },
      orderSummary: 'E0424 (active)',
      coverage: { resourceType: 'Coverage' },
      coverageSummary: 'Acme Health (active)',
    };
    const stub = makeFetch(true, 200, context);
    vi.stubGlobal('fetch', stub);

    const { getBYOContext } = await freshApi();
    const result = await getBYOContext('pt-1');

    const [url] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/byo/patients/pt-1/context');
    expect(result).toEqual(context);
  });
});

describe('postWatch', () => {
  it('POSTs /api/watch with bearer; 202 -> {runId}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 202, { runId: 'run-watch-1' });
    vi.stubGlobal('fetch', stub);

    const { postWatch } = await freshApi();
    const result = await postWatch();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/watch');
    expect(init.method).toBe('POST');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result.runId).toBe('run-watch-1');
  });

  it('409 (a run or watch already in flight) -> ApiError(409)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 409, { error: 'run in flight' }));

    const { postWatch, ApiError } = await freshApi();
    try {
      await postWatch();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(409);
    }
  });
});

describe('deleteWatch', () => {
  it('DELETEs /api/watch with bearer; 200 -> RunResult', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const result = {
      runId: 'run-watch-1',
      lane: 'conformant',
      uc: 'external',
      branch: '',
      state: 'passed',
      detail: 'external activity window closed',
    };
    const stub = makeFetch(true, 200, result);
    vi.stubGlobal('fetch', stub);

    const { deleteWatch } = await freshApi();
    const res = await deleteWatch();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/watch');
    expect(init.method).toBe('DELETE');
    expect(res).toEqual(result);
  });

  it('404 (no watch in progress) -> ApiError(404)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 404, { error: 'no watch in progress' }));

    const { deleteWatch, ApiError } = await freshApi();
    try {
      await deleteWatch();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(404);
    }
  });
});

describe('getRuns', () => {
  it('GETs /api/runs and returns RunResult[] with lowercase keys', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const runs = [
      { runId: 'run-1', lane: 'ehr', uc: 'uc01', branch: 'covered', state: 'passed', detail: 'ok' },
    ];
    vi.stubGlobal('fetch', makeFetch(true, 200, runs));

    const { getRuns } = await freshApi();
    const result = await getRuns();

    expect(result).toEqual(runs);
    expect(result[0].runId).toBe('run-1');
    expect(result[0].state).toBe('passed');
  });
});

describe('postReset', () => {
  it('POSTs /api/bootstrap/reset and returns {restartRequired:true}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { restartRequired: true });
    vi.stubGlobal('fetch', stub);

    const { postReset } = await freshApi();
    const result = await postReset();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/bootstrap/reset');
    expect(init.method).toBe('POST');
    expect(result.restartRequired).toBe(true);
  });
});

describe('postVerify', () => {
  it('POSTs /api/verify with bearer; 200 -> Probe[]', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const probes = [
      { name: 'discovery', ok: true, detail: 'reachable' },
      { name: 'registration', ok: false, detail: 'holder not found in registrar feed' },
    ];
    const stub = makeFetch(true, 200, probes);
    vi.stubGlobal('fetch', stub);

    const { postVerify } = await freshApi();
    const result = await postVerify();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/verify');
    expect(init.method).toBe('POST');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result).toEqual(probes);
  });

  it('409 (already in flight) -> ApiError(409)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 409, { error: 'verify already in flight' }));

    const { postVerify, ApiError } = await freshApi();
    try {
      await postVerify();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(409);
      expect((e as Error).message).toBe('verify already in flight');
    }
  });

  it('503 (not available until boot completes) -> ApiError(503)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 503, { error: 'verify not available until boot completes' }));

    const { postVerify, ApiError } = await freshApi();
    try {
      await postVerify();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(503);
    }
  });
});

describe('getBYO', () => {
  it('GETs /api/byo with the resolved bearer token and parses BYOStatus', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const byoStatus = {
      ehr: {
        dataUrl: 'https://ehr.example.org/fhir',
        hasClientKey: false,
        applied: true,
        demoPersonas: true,
      },
      davinci: null,
      ingress: null,
    };
    const stub = makeFetch(true, 200, byoStatus);
    vi.stubGlobal('fetch', stub);

    const { getBYO } = await freshApi();
    const result = await getBYO();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/byo');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result).toEqual(byoStatus);
  });
});

describe('putBYOEhr', () => {
  it('PUTs /api/byo/ehr with bearer + the JSON body; 200 -> {restartRequired}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { restartRequired: true });
    vi.stubGlobal('fetch', stub);

    const { putBYOEhr } = await freshApi();
    const result = await putBYOEhr({ dataUrl: 'https://ehr.example.org/fhir', clientId: 'c1' });

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/byo/ehr');
    expect(init.method).toBe('PUT');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(JSON.parse(init.body as string)).toEqual({
      dataUrl: 'https://ehr.example.org/fhir',
      clientId: 'c1',
    });
    expect(result.restartRequired).toBe(true);
  });

  it('422 rejects with ApiError(422) whose message carries the server detail', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 422, { error: 'dataUrl: probe failed: connection refused' }));

    const { putBYOEhr, ApiError } = await freshApi();
    try {
      await putBYOEhr({ dataUrl: 'https://ehr.example.org/fhir' });
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(422);
      expect((e as Error).message).toBe('dataUrl: probe failed: connection refused');
    }
  });
});

describe('deleteBYOEhr', () => {
  it('DELETEs /api/byo/ehr with bearer; 200 -> {restartRequired}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { restartRequired: true });
    vi.stubGlobal('fetch', stub);

    const { deleteBYOEhr } = await freshApi();
    const result = await deleteBYOEhr();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/byo/ehr');
    expect(init.method).toBe('DELETE');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result.restartRequired).toBe(true);
  });
});

describe('putBYODaVinci', () => {
  it('PUTs /api/byo/davinci with bearer + the JSON body; 200 -> {restartRequired}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { restartRequired: true });
    vi.stubGlobal('fetch', stub);

    const { putBYODaVinci } = await freshApi();
    const result = await putBYODaVinci({ clientId: 'partner-1', alg: 'RS384', publicKeyPem: '-----BEGIN PUBLIC KEY-----' });

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/byo/davinci');
    expect(init.method).toBe('PUT');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(JSON.parse(init.body as string)).toEqual({
      clientId: 'partner-1',
      alg: 'RS384',
      publicKeyPem: '-----BEGIN PUBLIC KEY-----',
    });
    expect(result.restartRequired).toBe(true);
  });

  it('422 rejects with ApiError(422) carrying the server detail', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 422, { error: 'alg: unsupported' }));

    const { putBYODaVinci, ApiError } = await freshApi();
    try {
      await putBYODaVinci({ clientId: 'partner-1', alg: 'bogus', publicKeyPem: 'x' });
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(422);
      expect((e as Error).message).toBe('alg: unsupported');
    }
  });
});

describe('deleteBYODaVinci', () => {
  it('DELETEs /api/byo/davinci with bearer; 200 -> {restartRequired}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { restartRequired: true });
    vi.stubGlobal('fetch', stub);

    const { deleteBYODaVinci } = await freshApi();
    const result = await deleteBYODaVinci();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/byo/davinci');
    expect(init.method).toBe('DELETE');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result.restartRequired).toBe(true);
  });
});

describe('eventsUrl', () => {
  it('encodes the token as a query param', async () => {
    const { eventsUrl } = await freshApi();
    expect(eventsUrl('a b')).toBe('/events?token=a%20b');
  });
});

describe('getHistory', () => {
  it('GETs /api/history with the resolved bearer token and parses HistorySummary[]', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const summaries = [
      {
        runId: 'run-1',
        lane: 'ehr',
        uc: 'uc03',
        branch: 'covered',
        state: 'passed',
        detail: 'approved',
        time: '2026-07-03T23:20:25-04:00',
        eventCount: 26,
      },
    ];
    const stub = makeFetch(true, 200, summaries);
    vi.stubGlobal('fetch', stub);

    const { getHistory } = await freshApi();
    const result = await getHistory();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/history');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result).toEqual(summaries);
  });
});

describe('getHistoryRecord', () => {
  it("GETs /api/history/{runId} and parses the full HistoryRecord", async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const record = {
      runId: 'run-x',
      lane: 'ehr',
      uc: 'uc03',
      branch: 'covered',
      state: 'passed',
      detail: 'approved',
      time: '2026-07-03T23:20:25-04:00',
      eventCount: 1,
      events: [{ seq: 1, time: '2026-07-03T23:20:25-04:00', type: 'run.started', runId: 'run-x' }],
    };
    const stub = makeFetch(true, 200, record);
    vi.stubGlobal('fetch', stub);

    const { getHistoryRecord } = await freshApi();
    const result = await getHistoryRecord('run-x');

    const [url] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/history/run-x');
    expect(result).toEqual(record);
  });

  it('normalizes a null events field to [] (kitd wire tolerance: a zero-event Record serializes events as null)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const record = {
      runId: 'run-empty',
      lane: 'ehr',
      uc: 'uc03',
      branch: 'covered',
      state: 'passed',
      detail: 'approved',
      time: '2026-07-03T23:20:25-04:00',
      eventCount: 0,
      events: null,
    };
    vi.stubGlobal('fetch', makeFetch(true, 200, record));

    const { getHistoryRecord } = await freshApi();
    const result = await getHistoryRecord('run-empty');

    expect(result.events).toEqual([]);
  });

  it('404 -> ApiError(404)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 404, { error: 'run not found' }));

    const { getHistoryRecord, ApiError } = await freshApi();
    try {
      await getHistoryRecord('run-missing');
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(404);
    }
  });
});

describe('getAbout', () => {
  it('GETs /api/about with the resolved bearer token and parses AboutManifest', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const manifest = {
      kit: '1.0.0',
      modules: { 'shn-gateway': 'v0.20.1', 'shn-sdk': 'v0.27.0' },
      brProvider: 'a8bece4',
      hapiImage: 'sha256:deadbeef',
      temurin: '21.0.4+7',
      igsValidator: ['hl7.fhir.us.core 6.1.0'],
      igsData: ['hl7.fhir.us.core 6.1.0'],
      build: { timestamp: '2026-07-04T00:00:00Z', commit: 'abc1234' },
    };
    const stub = makeFetch(true, 200, manifest);
    vi.stubGlobal('fetch', stub);

    const { getAbout } = await freshApi();
    const result = await getAbout();

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/about');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result).toEqual(manifest);
  });

  it('404 (dev build, no packaged manifest) -> ApiError(404) carrying the server detail', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 404, { error: 'manifest not available (dev build)' }));

    const { getAbout, ApiError } = await freshApi();
    try {
      await getAbout();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(404);
      expect((e as Error).message).toBe('manifest not available (dev build)');
    }
  });
});

describe('postChildRestart', () => {
  it('POSTs /api/children/{name}/restart with bearer; 200 -> {restarted}', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { restarted: 'validator' });
    vi.stubGlobal('fetch', stub);

    const { postChildRestart } = await freshApi();
    const result = await postChildRestart('validator');

    const [url, init] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/children/validator/restart');
    expect(init.method).toBe('POST');
    const headers = init.headers as Headers;
    expect(headers.get('Authorization')).toBe('Bearer t-1');
    expect(result.restarted).toBe('validator');
  });

  it('encodes the child name in the path', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = makeFetch(true, 200, { restarted: 'data-server' });
    vi.stubGlobal('fetch', stub);

    const { postChildRestart } = await freshApi();
    await postChildRestart('data-server');

    const [url] = stub.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/children/data-server/restart');
  });

  it('403 (gateway refused) -> ApiError(403) carrying the server detail', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 403, { error: 'restarting gateway would invalidate its port, driver keypair, and runner wiring' }));

    const { postChildRestart, ApiError } = await freshApi();
    try {
      await postChildRestart('gateway');
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(403);
    }
  });

  it('409 (a run or watch is in flight) -> ApiError(409) carrying the raw server detail', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 409, { error: 'a run or watch is in flight' }));

    const { postChildRestart, ApiError } = await freshApi();
    try {
      await postChildRestart('validator');
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(409);
      expect((e as Error).message).toBe('a run or watch is in flight');
    }
  });

  it('404 (unknown child) -> ApiError(404)', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    vi.stubGlobal('fetch', makeFetch(false, 404, { error: 'unknown child' }));

    const { postChildRestart, ApiError } = await freshApi();
    try {
      await postChildRestart('bogus');
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(404);
    }
  });
});

describe('supportBundleUrl', () => {
  it('returns the support-bundle API path', async () => {
    const { supportBundleUrl } = await freshApi();
    expect(supportBundleUrl()).toBe('/api/support-bundle');
  });
});

describe('resolveToken', () => {
  it('prefers window.kit.getSession()', async () => {
    (window as unknown as { kit: unknown }).kit = {
      getSession: vi.fn().mockResolvedValue({ api: 'http://x', token: 'bridge-tok' }),
      restart: vi.fn(),
      openExternal: vi.fn(),
    };

    const { resolveToken } = await freshBridge();
    await expect(resolveToken()).resolves.toBe('bridge-tok');
  });

  it('falls back to ?token= query: stores in sessionStorage + strips via history.replaceState', async () => {
    window.history.replaceState({}, '', '/?token=q-tok&x=1');
    const replaceSpy = vi.spyOn(window.history, 'replaceState');

    const { resolveToken } = await freshBridge();
    const token = await resolveToken();

    expect(token).toBe('q-tok');
    expect(sessionStorage.getItem('kitToken')).toBe('q-tok');
    expect(replaceSpy).toHaveBeenCalled();
    expect(window.location.search).not.toContain('token=q-tok');
    expect(window.location.search).toContain('x=1');
  });

  it('falls back to sessionStorage when no bridge and no query token', async () => {
    sessionStorage.setItem('kitToken', 'stored-tok');

    const { resolveToken } = await freshBridge();
    await expect(resolveToken()).resolves.toBe('stored-tok');
  });

  it("resolves '' when nothing is available", async () => {
    const { resolveToken } = await freshBridge();
    await expect(resolveToken()).resolves.toBe('');
  });

  it('bridge wins over ?token= when both are present: getSession is used and the query token is not persisted', async () => {
    window.history.replaceState({}, '', '/?token=q-tok');
    const getSession = vi.fn().mockResolvedValue({ api: 'http://x', token: 'bridge-tok' });
    (window as unknown as { kit: unknown }).kit = { getSession, restart: vi.fn(), openExternal: vi.fn() };

    const { resolveToken } = await freshBridge();
    const token = await resolveToken();

    expect(getSession).toHaveBeenCalledOnce();
    expect(token).toBe('bridge-tok');
    expect(sessionStorage.getItem('kitToken')).not.toBe('q-tok');
  });

  it('caches the resolved token: getSession is invoked exactly once across two resolveToken() calls', async () => {
    const getSession = vi.fn().mockResolvedValue({ api: 'http://x', token: 'bridge-tok' });
    (window as unknown as { kit: unknown }).kit = { getSession, restart: vi.fn(), openExternal: vi.fn() };

    const { resolveToken } = await freshBridge();
    const first = await resolveToken();
    const second = await resolveToken();

    expect(first).toBe('bridge-tok');
    expect(second).toBe('bridge-tok');
    expect(getSession).toHaveBeenCalledOnce();
  });

  it('caches the PROMISE, not the value: two concurrent resolveToken() calls invoke getSession exactly once and both resolve to the token', async () => {
    let releaseGetSession: (() => void) | undefined;
    const getSession = vi.fn().mockImplementation(
      () =>
        new Promise((resolve) => {
          releaseGetSession = () => resolve({ api: 'http://x', token: 'bridge-tok' });
        }),
    );
    (window as unknown as { kit: unknown }).kit = { getSession, restart: vi.fn(), openExternal: vi.fn() };

    const { resolveToken } = await freshBridge();
    const first = resolveToken();
    const second = resolveToken();
    expect(getSession).toHaveBeenCalledOnce();

    releaseGetSession?.();
    const [a, b] = await Promise.all([first, second]);

    expect(a).toBe('bridge-tok');
    expect(b).toBe('bridge-tok');
    expect(getSession).toHaveBeenCalledOnce();
  });

  it('a rejected resolve clears the cache so a retry re-invokes getSession', async () => {
    const getSession = vi
      .fn()
      .mockRejectedValueOnce(new Error('bridge unavailable'))
      .mockResolvedValueOnce({ api: 'http://x', token: 'bridge-tok' });
    (window as unknown as { kit: unknown }).kit = { getSession, restart: vi.fn(), openExternal: vi.fn() };

    const { resolveToken } = await freshBridge();

    await expect(resolveToken()).rejects.toThrow('bridge unavailable');
    expect(getSession).toHaveBeenCalledOnce();

    await expect(resolveToken()).resolves.toBe('bridge-tok');
    expect(getSession).toHaveBeenCalledTimes(2);
  });
});

describe('ApiError body-parse fallback', () => {
  it('falls back to "HTTP 500" when the error response body fails to parse as JSON', async () => {
    sessionStorage.setItem('kitToken', 't-1');
    const stub = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: () => Promise.reject(new Error('body is not JSON')),
    });
    vi.stubGlobal('fetch', stub);

    const { getBootstrap, ApiError } = await freshApi();

    try {
      await getBootstrap();
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as Error).message).toBe('HTTP 500');
      expect((e as InstanceType<typeof ApiError>).status).toBe(500);
    }
  });
});

describe('canRestart / restartKit', () => {
  it('canRestart is true only when window.kit is present', async () => {
    const { canRestart } = await freshBridge();
    expect(canRestart()).toBe(false);

    (window as unknown as { kit: unknown }).kit = {
      getSession: vi.fn(),
      restart: vi.fn(),
      openExternal: vi.fn(),
    };
    const { canRestart: canRestart2 } = await freshBridge();
    expect(canRestart2()).toBe(true);
  });

  it('restartKit calls bridge.restart() when present', async () => {
    const restart = vi.fn().mockResolvedValue(undefined);
    (window as unknown as { kit: unknown }).kit = { getSession: vi.fn(), restart, openExternal: vi.fn() };

    const { restartKit } = await freshBridge();
    await restartKit();

    expect(restart).toHaveBeenCalledOnce();
  });

  it('restartKit rejects in browser mode (no window.kit)', async () => {
    const { restartKit } = await freshBridge();
    await expect(restartKit()).rejects.toThrow();
  });
});

describe('openExternal', () => {
  it('uses window.kit.openExternal when present', async () => {
    const openExternalFn = vi.fn().mockResolvedValue(undefined);
    (window as unknown as { kit: unknown }).kit = { getSession: vi.fn(), restart: vi.fn(), openExternal: openExternalFn };

    const { openExternal } = await freshBridge();
    openExternal('https://example.com');

    expect(openExternalFn).toHaveBeenCalledWith('https://example.com');
  });

  it('falls back to window.open(url, "_blank") in browser mode', async () => {
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(null);

    const { openExternal } = await freshBridge();
    openExternal('https://example.com');

    expect(openSpy).toHaveBeenCalledWith('https://example.com', '_blank');
  });
});
