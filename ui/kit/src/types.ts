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
  // providerDataUrl: present iff the Kit booted its provider-data gateway
  // child (the packaged Java trio) — the signal the third lane exists. Same
  // key-presence contract as brProviderUrl.
  providerDataUrl?: string;
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

// 'provider-data' is the third lane: every scenario originated off the Kit's
// own FHIR data server and run against the hosted Da Vinci reference payer.
// It exists only when StatusResponse.providerDataUrl is present (the packaged
// Java trio); the ModeSwitch hides it otherwise.
export type Lane = 'ehr' | 'conformant' | 'provider-data';

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
  // demo carries the demo.exhibit event's demoRecord JSON — a
  // local-demonstration payload, never a wire observer/audit frame (the
  // "demo." type prefix keeps the two vocabularies disjoint on the wire; see
  // DemoRecord below). Raw `unknown` here for the same reason observer/audit
  // are: the SSE client JSON.parses the whole KitEvent, and this field is
  // shape-checked downstream (buildDemoStory), never trusted un-parsed.
  demo?: unknown;
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
  runId: string; // json:"runId" — the local-demonstration run this exhibit orchestrated; the panel's link into the inspector.
}

export interface BridgingExhibitRefusalResponse {
  kind: 'refusal'; // json:"kind"
  refusal: string; // json:"refusal" — the typed refusal text
  semanticChange: boolean; // json:"semanticChange"
  runId: string; // json:"runId" — the local-demonstration run this exhibit orchestrated; the panel's link into the inspector.
}

export type BridgingExhibitResponse = BridgingExhibitCarryResponse | BridgingExhibitRefusalResponse;

// Local-demonstration engine-exhibit payload — mirrors kit/kitd/bridging.go's
// demoRecord exactly (json tags quoted per field below). This is the
// demo.exhibit event's `demo` payload, never a wire observer/audit frame: a
// scripted, in-process walkthrough of a substrate property that never
// touches a real wire leg. `kind` discriminates the two demonstration
// species; the fields below it are a union of both species' payloads
// (refusal-only: refusal/semanticChange; carry-only: intermediate/output/
// restored/lossReports) rather than two separate response types, mirroring
// demoRecord's own single-struct shape on the wire.
export interface DemoChainHop {
  module: string; // json:"module"
  from: string; // json:"from"
  to: string; // json:"to"
  class: string; // json:"class"
}

export interface DemoRecord {
  kind: 'refusal-engine' | 'carry-engine'; // json:"kind"
  contract: string; // json:"contract"
  // chain is a Go slice with no `omitempty` on demoRecord — a nil Chain
  // (the refusal record's outcome; the carry record's chain is always
  // populated) marshals to JSON `null`, not `[]`. Parsing normalizes that
  // null to an empty array rather than propagating it, since this type is
  // declared non-nullable.
  chain: DemoChainHop[]; // json:"chain"
  input: unknown; // json:"input" — raw JSON, byte-faithful to the embedded fixture
  refusal?: string; // json:"refusal,omitempty" — refusal-engine only
  semanticChange?: boolean; // json:"semanticChange" (no omitempty — false is a real value, distinct from absent) — refusal-engine only
  intermediate?: unknown; // json:"intermediate,omitempty" — carry-engine only
  output?: unknown; // json:"output,omitempty" — carry-engine only
  restored?: boolean; // json:"restored,omitempty" — carry-engine only
  // BridgingLossReport ALREADY EXISTS above — it is the kitd wire mirror
  // ({module, source, target, carried?, synthesized?}) and is exactly what
  // demoRecord.lossReports carries. Used as-is here. (The observer-parsed
  // sibling is the already-exported ParsedLossReport, StepDetail.tsx — a
  // different shape for a different source; do not conflate.)
  lossReports?: BridgingLossReport[]; // json:"lossReports,omitempty" — carry-engine only
}

// BridgingCapture mirrors kitd's GET /api/bridging/capture/{correlationId}
// 200 body exactly (kit/kitd/bridging.go) — the gateway child's edge-capture
// passthrough for one bridged leg, kept in memory only while
// SHN_DEMO_EDGE_CAPTURE (the compatibility simulation) is on. `before`/
// `after` are the already-parsed payload the leg built vs. what actually
// left the gateway's edge — StepDetail.tsx's on-demand transformation
// expander feeds them straight into XformDiff.tsx alongside
// JSON.stringify'd raw strings for its byte-identical check. Both 404
// bodies this route can return (the flag is off; there is no capture for
// this leg) reject through api.ts's getBridgingCapture as ApiError with
// `.status === 404` — the two wire reasons are never distinguished
// client-side.
export interface BridgingCapture {
  correlationId: string; // json:"correlationId"
  legType: string; // json:"legType"
  contract: string; // json:"contract"
  from: string; // json:"from"
  to: string; // json:"to"
  chain: DemoChainHop[]; // json:"chain"
  lossReports: BridgingLossReport[]; // json:"lossReports"
  before: unknown; // json:"before"
  after: unknown; // json:"after"
  capturedAt: string; // json:"capturedAt"
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
