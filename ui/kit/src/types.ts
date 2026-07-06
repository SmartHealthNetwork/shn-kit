// types.ts — mirrors the kitd wire (kit/kitd/kitd.go + kit/bootstrap + kit/event + kit/supervisor + kit/runner)

export type BootstrapState = 'signin-required' | 'signing-in' | 'provisioning' | 'provisioned';

export interface Probe {
  name: string;
  ok: boolean;
  detail: string;
}

export interface BootstrapResponse {
  state: BootstrapState;
  email?: string;
  holderId?: string;
  authExpiry?: string;
  detail?: string;
  verify: Probe[];
}

export interface ChildStatus {
  name: string;
  state: string;
  detail: string;
  pid: number;
  restarts: number;
}

// validator/brProviderUrl/update all use key-presence semantics: omitted
// entirely (never a zero value) until kitd's Daemon.SetStackInfo/SetUpdate
// has actually been called, mirroring patientAppUrl's existing convention.
// An absent `validator` (old daemon, or a race before SetStackInfo) reads
// as the honest "stand-in" fallback — never assume "packaged".
export interface StatusResponse {
  children: ChildStatus[];
  patientAppUrl?: string;
  validator?: 'stand-in' | 'packaged';
  brProviderUrl?: string;
  update?: { available: boolean; latest: string; url: string };
}

// AboutManifest mirrors GET /api/about's body byte-for-byte — the
// package-time versions.json manifest tools/kitassets/manifest.sh writes.
// Field names/shape must track that script exactly; re-read it before
// changing this type.
export interface AboutManifest {
  kit: string;
  modules: {
    'shn-gateway': string;
    'shn-sdk': string;
  };
  brProvider: string;
  hapiImage: string;
  temurin: string;
  igsValidator: string[];
  igsData: string[];
  build: {
    timestamp: string;
    commit: string;
  };
}

export type Lane = 'ehr' | 'conformant';

export interface RunResult {
  runId: string;
  lane: Lane;
  uc: string;
  branch: string;
  state: 'passed' | 'failed';
  detail: string;
}

export interface KitEvent {
  seq: number;
  time: string;
  type: string;
  runId?: string;
  lane?: string;
  uc?: string;
  child?: string;
  detail?: string;
  observer?: unknown;
  audit?: unknown;
}

// History: GET /api/history returns HistorySummary[]; GET
// /api/history/{runId} returns the full HistoryRecord (summary + the run's
// stamped events, replayed through the same buildRunStory as a live run).
export interface HistorySummary {
  runId: string;
  lane: string;
  uc: string;
  branch: string;
  state: 'passed' | 'failed';
  detail: string;
  time: string;
  eventCount: number;
}

export interface HistoryRecord extends HistorySummary {
  events: KitEvent[];
}

// Bring-your-own systems — mirrors kitd.go's
// byoEHRResponse/byoDaVinciResponse/byoIngressResponse/byoGetResponse wire
// shapes exactly (json tags quoted per field below).

// byoEHRResponse (kitd.go): clientKeyPem is deliberately absent (the key
// is write-only and never echoed) — HasClientKey reports presence without
// ever carrying key bytes.
export interface BYOEhr {
  dataUrl: string; // json:"dataUrl"
  tokenUrl?: string; // json:"tokenUrl,omitempty"
  clientId?: string; // json:"clientId,omitempty"
  alg?: string; // json:"alg,omitempty"
  scope?: string; // json:"scope,omitempty"
  kid?: string; // json:"kid,omitempty"
  hasClientKey: boolean; // json:"hasClientKey"
  applied: boolean; // json:"applied"
  // Tri-state: true/false is a live sentinel result
  // (byo.Browser.HasPersona against the applied swap's connected server)
  // when the swap is applied THIS boot; null otherwise, or when the check
  // itself errors — "we don't know," never a guessed false. Explicit JSON
  // null (a Go *bool), not an omitted key.
  demoPersonas: boolean | null; // json:"demoPersonas"
}

// byoDaVinciResponse (kitd.go): unlike the EHR lane, publicKeyPem is public
// ingress-client registration material — echoing it back is not a
// key-hygiene concern.
export interface BYODaVinci {
  clientId: string; // json:"clientId"
  alg: string; // json:"alg"
  publicKeyPem: string; // json:"publicKeyPem"
  applied: boolean; // json:"applied"
}

// byoIngressResponse (kitd.go): null until this process has actually booted
// a gateway.
export interface BYOIngress {
  baseUrl: string; // json:"baseUrl"
  tokenUrl: string; // json:"tokenUrl"
  smartConfigUrl: string; // json:"smartConfigUrl"
  endpoints: string[]; // json:"endpoints"
}

// byoGetResponse (kitd.go): GET /api/byo's full body. A lane absent from the
// saved config renders as null (never an applied:false stand-in); loadError
// is omitted when clean.
export interface BYOStatus {
  ehr: BYOEhr | null; // json:"ehr"
  davinci: BYODaVinci | null; // json:"davinci"
  ingress: BYOIngress | null; // json:"ingress"
  loadError?: string; // json:"loadError,omitempty"
}

// PatientSummary/PatientContext mirror kit/byo/browse.go's wire shapes
// exactly (json tags quoted per field below) — GET /api/byo/patients and
// GET /api/byo/patients/{fhirId}/context, the free-form panel's browse
// reads.
export interface PatientSummary {
  fhirId: string; // json:"fhirId"
  memberId: string; // json:"memberId" — the urn:shn:member value the free-form run posts
  name: string; // json:"name"
  birthDate: string; // json:"birthDate"
}

// Order/Coverage are raw FHIR resource bytes (json.RawMessage null when
// absent) — the panel only ever needs presence (Run is disabled when
// order is null) and the plain-language Summary strings for display.
export interface PatientContext {
  order: unknown; // json:"order" — null when absent
  orderSummary: string; // json:"orderSummary"
  coverage: unknown; // json:"coverage" — null when absent
  coverageSummary: string; // json:"coverageSummary"
}
