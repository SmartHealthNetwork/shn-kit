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
  // bridging mirrors kitd.go's bridgingStatus: the KEY ITSELF
  // is present iff Config.BridgingDemo is configured at all — an absent key
  // means "this Kit build has no bridging demo mode," never "off" (that's
  // demoMode:false). peer/refusePeer are each further-optional (present iff
  // the matching bootstrap.Verify BridgeProbes holder id was configured) —
  // never a fabricated red for a probe that didn't run.
  bridging?: { demoMode: boolean; peer?: Probe; refusePeer?: Probe };
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
  // igLines/igSizeNote are ADDITIVE — an older manifest written before these
  // fields existed (or by a future manifest.sh that dropped them) simply
  // omits them, and the About panel degrades to showing only the flat
  // igsValidator/igsData fields (line 2.0's, unchanged meaning). Keyed by
  // contract line ("2.0" | "2.1" | "2.2" today, but not narrowed to that
  // union — a manifest line the UI doesn't know about yet must still render).
  igLines?: Record<string, { igsValidator: string[]; igsData: string[] }>;
  igSizeNote?: string;
  build: {
    timestamp: string;
    commit: string;
  };
}

export type Lane = 'ehr' | 'conformant';

// The scenario-card detail level: 'overview' is the plain-language outcome for
// any reader; 'technical' is the Da Vinci-mechanics register for integrators.
// A single global choice (RegisterSwitch) flips every card + lane blurb at once.
export type Register = 'overview' | 'technical';

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
  branch?: string;
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

// Bridging engine-exhibit wire shapes — mirror kit/kitd/bridging.go's
// bridgingLossEntry/bridgingLossReport/bridgingExhibitCarryResponse/
// bridgingExhibitRefusalResponse exactly (json tags quoted per field below).
// POST /api/bridging/exhibit's body is {"kind":"carry"|"refusal"}; the 200
// response is one of the two shapes below, discriminated by `kind`.
export interface BridgingLossEntry {
  path: string; // json:"path"
  detail?: string; // json:"detail,omitempty"
}

export interface BridgingLossReport {
  module: string; // json:"module" — e.g. "pa.dtr 2.1->2.2"
  source: string; // json:"source"
  target: string; // json:"target"
  carried?: BridgingLossEntry[]; // json:"carried,omitempty"
  synthesized?: BridgingLossEntry[]; // json:"synthesized,omitempty"
}

export interface BridgingExhibitCarryResponse {
  kind: 'carry'; // json:"kind"
  lossReports: BridgingLossReport[]; // json:"lossReports"
  restored: boolean; // json:"restored"
}

export interface BridgingExhibitRefusalResponse {
  kind: 'refusal'; // json:"kind"
  refusal: string; // json:"refusal" — the typed refusal text
  semanticChange: boolean; // json:"semanticChange"
}

export type BridgingExhibitResponse = BridgingExhibitCarryResponse | BridgingExhibitRefusalResponse;

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
