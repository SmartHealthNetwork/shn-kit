// api.ts — kitd HTTP client. ALL paths are relative (same-origin in every
// mode: Electron loads kitd's own served UI, and the Vite dev server proxies
// /api and /events straight through to kitd).
import type {
  AboutManifest,
  BootstrapResponse,
  StatusResponse,
  RunResult,
  Lane,
  HistorySummary,
  HistoryRecord,
  Probe,
  BYOStatus,
  PatientContext,
  PatientSummary,
} from './types';
import { resolveToken } from './bridge';

export class ApiError extends Error {
  readonly status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function authed(path: string, init?: RequestInit): Promise<Response> {
  const token = await resolveToken();
  const headers = new Headers(init?.headers);
  headers.set('Authorization', `Bearer ${token}`);
  return fetch(path, { ...init, headers });
}

async function json<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await authed(path, init);
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const b = await res.json();
      if (b?.error) msg = b.error;
    } catch {
      /* ignore parse error */
    }
    throw new ApiError(msg, res.status);
  }
  return res.json() as Promise<T>;
}

function postJSON<T>(path: string, body?: unknown): Promise<T> {
  const headers = new Headers();
  headers.set('Content-Type', 'application/json');
  return json<T>(path, {
    method: 'POST',
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

function putJSON<T>(path: string, body: unknown): Promise<T> {
  const headers = new Headers();
  headers.set('Content-Type', 'application/json');
  return json<T>(path, { method: 'PUT', headers, body: JSON.stringify(body) });
}

function del<T>(path: string): Promise<T> {
  return json<T>(path, { method: 'DELETE' });
}

export function getBootstrap(): Promise<BootstrapResponse> {
  return json<BootstrapResponse>('/api/bootstrap');
}

export function postSignIn(): Promise<{ authorizeUrl: string }> {
  return postJSON<{ authorizeUrl: string }>('/api/bootstrap/signin');
}

export function postReset(): Promise<{ restartRequired: boolean }> {
  return postJSON<{ restartRequired: boolean }>('/api/bootstrap/reset');
}

export function getStatus(): Promise<StatusResponse> {
  return json<StatusResponse>('/api/status');
}

export function getRuns(): Promise<RunResult[]> {
  return json<RunResult[]>('/api/runs');
}

// member is populated only for uc "freeform" — the caller-named member id
// a free-form run is dispatched against. Undefined serializes as an
// omitted key (JSON.stringify drops undefined values), so every other
// caller's wire body is byte-identical to before this addition.
export function postRun(
  lane: Lane,
  uc: string,
  branch: string,
  member?: string,
): Promise<{ runId: string }> {
  return postJSON<{ runId: string }>('/api/runs', { lane, uc, branch, member });
}

export function eventsUrl(token: string): string {
  return `/events?token=${encodeURIComponent(token)}`;
}

export function getHistory(): Promise<HistorySummary[]> {
  return json<HistorySummary[]>('/api/history');
}

// postVerify serves POST /api/verify: re-runs the boot verify probes on
// demand and returns the fresh result — 409 if a re-probe is already in
// flight, 503 before boot has installed the verify closure.
export function postVerify(): Promise<Probe[]> {
  return postJSON<Probe[]>('/api/verify');
}

// getBYO/putBYOEhr/deleteBYOEhr/putBYODaVinci/deleteBYODaVinci — the
// bring-your-own systems config surface.
export function getBYO(): Promise<BYOStatus> {
  return json<BYOStatus>('/api/byo');
}

export function putBYOEhr(body: {
  dataUrl: string;
  tokenUrl?: string;
  clientId?: string;
  clientKeyPem?: string;
  alg?: string;
  scope?: string;
  kid?: string;
}): Promise<{ restartRequired: boolean }> {
  return putJSON<{ restartRequired: boolean }>('/api/byo/ehr', body);
}

export function deleteBYOEhr(): Promise<{ restartRequired: boolean }> {
  return del<{ restartRequired: boolean }>('/api/byo/ehr');
}

export function putBYODaVinci(body: {
  clientId: string;
  alg: string;
  publicKeyPem: string;
}): Promise<{ restartRequired: boolean }> {
  return putJSON<{ restartRequired: boolean }>('/api/byo/davinci', body);
}

export function deleteBYODaVinci(): Promise<{ restartRequired: boolean }> {
  return del<{ restartRequired: boolean }>('/api/byo/davinci');
}

// getBYOPatients/getBYOContext — the free-form panel's browse reads:
// proxies to kitd's kit/byo.Browser, which mirrors the SAME queries the
// gateway's fhirsor SoR makes at run time (shown-never-faked applied to
// previews — the preview shows what the origination will actually find).
export function getBYOPatients(): Promise<PatientSummary[]> {
  return json<PatientSummary[]>('/api/byo/patients');
}

export function getBYOContext(fhirId: string): Promise<PatientContext> {
  return json<PatientContext>(`/api/byo/patients/${encodeURIComponent(fhirId)}/context`);
}

// postWatch/deleteWatch — the conformant lane's bring-your-own Da Vinci
// watch session: opens/closes an attribution window over
// externally-originated (partner-driven) gateway traffic.
// deleteWatch's 200 body is the watch's final runner.Result — the exact
// same wire shape as any other completed run (RunResult).
export function postWatch(): Promise<{ runId: string }> {
  return postJSON<{ runId: string }>('/api/watch');
}

export function deleteWatch(): Promise<RunResult> {
  return del<RunResult>('/api/watch');
}

// getAbout serves GET /api/about: the package-time versions.json
// manifest's bytes, decoded — 404 (an honest JSON error) when
// Config.ManifestPath is unset (a dev checkout with no packaged manifest),
// which rejects as ApiError(404); AboutPanel renders its own "development
// build" fallback for that case rather than this module papering over it.
export function getAbout(): Promise<AboutManifest> {
  return json<AboutManifest>('/api/about');
}

// postChildRestart serves POST /api/children/{name}/restart: a deliberate
// stop-then-respawn of one supervised child
// (validator/data-server/br-provider) — distinct from the whole-Kit Restart
// action. 403 for "gateway" (kitd refuses — restarting it would invalidate
// its port/keypair/runner wiring), 409 when a run or watch is in flight, 404
// for an unknown child, 503 before the stack has started.
export function postChildRestart(name: string): Promise<{ restarted: string }> {
  return postJSON<{ restarted: string }>(`/api/children/${encodeURIComponent(name)}/restart`);
}

// supportBundleUrl returns GET /api/support-bundle's path (a sans-secrets
// contract) — a bare string, not a token-qualified URL:
// the route is Bearer-gated like every other /api/* route (kitd's
// authMiddleware), and fetch() (unlike an EventSource) CAN set headers, so
// the download goes through the SAME Authorization-header path as the rest
// of this module rather than the `?token=` query fallback authMiddleware
// only carries as an EventSource workaround (see authMiddleware's doc
// comment in kitd.go). Callers fetch this path with a Bearer header and
// save the response Blob — StatusPanel does exactly that (fetch-as-blob +
// object URL, the same pattern App.tsx's history export already uses),
// never a bare `<a href>` navigation, which cannot carry the header.
export function supportBundleUrl(): string {
  return '/api/support-bundle';
}

// seedBundleUrl returns GET /api/byo/seed-bundle/{lane}'s path — same
// Bearer-gated-route-as-Blob-download shape as supportBundleUrl() above:
// a bare string, not a token-qualified URL, fetched with an Authorization
// header (never a bare `<a href>` navigation, which can't carry it).
// SeedYourServerBlock (BYOPanel.tsx) does exactly that.
export function seedBundleUrl(lane: 'ehr' | 'conformant'): string {
  return `/api/byo/seed-bundle/${lane}`;
}

export function getHistoryRecord(runId: string): Promise<HistoryRecord> {
  return json<HistoryRecord>(`/api/history/${encodeURIComponent(runId)}`).then((record) => ({
    ...record,
    // wire tolerance: kitd serializes a zero-event Record's events as `null`
    // (Go's nil-slice JSON encoding) rather than `[]` — normalize here so
    // every consumer downstream (useRunEvents, buildRunStory) can assume an
    // array.
    events: record.events ?? [],
  }));
}
