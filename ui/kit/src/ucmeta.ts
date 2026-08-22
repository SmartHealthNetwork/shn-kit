// ucmeta.ts — the eight UC cards' copy + branch pickers + provenance labels.
//
// Participant-facing copy. Each card carries TWO detail registers: `overview`
// (plain-language outcome for any reader) and `technical` (the Da Vinci
// mechanics an integrator wants). A single global RegisterSwitch flips them
// all at once. Both registers stay TRUE to the asserted OUTCOMES in
// kit/runner/rows_*.go (the row funcs are the executable spec) — they are a
// deliberate participant-facing rewrite of those outcomes, NOT a verbatim copy
// of the Go detail strings, and they avoid SHN-internal vocabulary (Mode A,
// br-provider, direct-mint, "gap-fill", deferral IDs) per the participant-
// facing writing rules. Da Vinci-domain terms (CRD/DTR/PAS/CDex, prior auth,
// coverage) and the product-facing Hub / Smart Gateway stay.
import type { Lane, Register } from './types';

// The two detail registers, in render order, and their toggle labels — the
// single source of truth the RegisterSwitch and any pinning test share.
export const REGISTERS: Register[] = ['overview', 'technical'];
export const REGISTER_LABELS: Record<Register, string> = {
  overview: 'Overview',
  technical: 'Technical',
};

export interface UCBranchOption {
  value: string;
  label: string;
}

export interface UCMeta {
  uc: string;
  title: string;
  // Per-register description: `overview` (plain) and `technical` (Da Vinci
  // mechanics). Same scenario, two reading levels — never diverging in truth.
  description: Record<Register, string>;
  branches?: Partial<Record<Lane, UCBranchOption[]>>; // absent ⇒ always branch ""
  // provenance label, rendered as a small tag — conformant lane + Technical
  // register only (it is an honest "this leg is a stand-in on this lane"
  // mechanics caveat, noise for the plain reader).
  provenance?: Partial<Record<Lane, string>>;
}

export const UC_METAS: UCMeta[] = [
  {
    uc: 'uc01',
    title: 'Eligibility check',
    description: {
      overview:
        "Is the patient's insurance active? The Smart Gateway checks with the payer across the Hub and reads back the answer — covered or not.",
      technical:
        "An eligibility check: the Smart Gateway asks the payer across the Hub whether the member's coverage is active, and reads back the verdict.",
    },
    branches: {
      ehr: [
        { value: 'covered', label: 'Member is covered' },
        { value: 'notcovered', label: 'Member is not covered' },
      ],
      conformant: [
        { value: 'covered', label: 'Member is covered' },
        { value: 'notcovered', label: 'Member is not covered' },
      ],
      'provider-data': [
        { value: 'covered', label: 'Member is covered' },
        { value: 'notcovered', label: 'Member is not covered' },
      ],
    },
    provenance: {
      conformant:
        "Eligibility isn't a Da Vinci prior-auth operation, so this lane runs the same coverage check as the plain-EHR lane.",
      'provider-data':
        "Eligibility reads the member's own Coverage on the Kit's data server: the covered branch finds an active one, the not-covered branch a terminated one.",
    },
  },
  {
    uc: 'uc02',
    title: 'No prior auth needed',
    description: {
      overview:
        'An order the insurer allows without prior approval — the coverage check comes back "covered, no prior authorization required."',
      technical:
        'The CRD coverage card returns "covered, no prior authorization required" — the order needs no PA.',
    },
    provenance: {
      'provider-data':
        'A hospital-bed order read from the chart; the reference payer answers covered, no prior authorization, no questionnaire.',
    },
  },
  {
    uc: 'uc03',
    title: 'Prior auth, approved',
    description: {
      overview:
        "The insurer requires approval before this order can proceed. The request is filled in from the patient's chart, submitted, and comes back approved with an authorization number.",
      technical:
        'CRD flags the order as needing prior authorization; the DTR questionnaire is completed from clinical data; the PAS submit returns approved with an auth number.',
    },
    provenance: {
      'provider-data':
        "A home-oxygen order read from the chart; the questionnaire's oxygen-saturation and blood-gas answers are computed from the patient's own observations by the clinical-logic engine, then approved.",
    },
  },
  {
    uc: 'uc04',
    title: 'Pend, then approve',
    description: {
      overview:
        "The first request is held for missing evidence — an operative report. Once the report is attached and resubmitted, it's approved.",
      technical:
        'The first PAS submit pends on missing evidence (an operative report); an amended re-submit carrying the operative report is approved.',
    },
    provenance: {
      'provider-data':
        'A home-health therapy order read from the chart; the adaptive questionnaire is driven group by group — the diagnosis, functional limitations and treatment goals all trace to the chart — and approved in one submission.',
    },
  },
  {
    uc: 'uc05',
    title: 'Federated evidence',
    description: {
      overview:
        "The missing evidence lives at another facility. With the patient's consent, it's fetched across the network and attached before the request is resubmitted.",
      technical:
        'The missing evidence lives at another facility; a consent-checked federated query (CDex) retrieves it across the network before the amended PAS re-submit.',
    },
    branches: {
      ehr: [
        { value: 'consent', label: 'Patient consent on file' },
        { value: 'noconsent', label: 'Consent denied — query blocked' },
      ],
      'provider-data': [
        { value: 'consent', label: 'Patient consent on file' },
        { value: 'noconsent', label: 'Consent denied — query blocked' },
      ],
    },
    provenance: {
      conformant:
        "On this lane the federated (CDex) query runs gateway-to-gateway, so the consent-denied branch isn't exercised here.",
      'provider-data':
        "The federated evidence is the patient's own operative report at the other facility; the reference payer resolves the held request after the amended re-submit.",
    },
  },
  {
    uc: 'uc06',
    title: 'Clinician attestation',
    description: {
      overview:
        "A request is held for an answer only a clinician can attest to. Once the clinician's attestation is added, it's approved.",
      technical:
        "A pended item that only a clinician's attested answer can satisfy; the attested amended re-submit is approved.",
    },
    provenance: {
      conformant:
        "The DTR questionnaire package is fetched through the real Da Vinci flow; the manual clinician-facing DTR app isn't part of this run.",
      'provider-data':
        "The clinician's functional-status attestation replaces the chart's value inside the questionnaire group before the amended re-submit.",
    },
  },
  {
    uc: 'uc07',
    title: 'Patient can see it',
    description: {
      overview:
        "An approved order shows up in the patient's Smart Health account, where they can see it.",
      technical:
        "An approved order is projected to the patient's Smart Health account; the HCPCS branch reads the approval back from that surface to confirm it.",
    },
    branches: {
      ehr: [
        { value: '', label: 'CPT order' },
        { value: 'hcpcs', label: 'HCPCS order + patient read-back' },
      ],
    },
    provenance: {
      conformant:
        "Also reads the approval back from the patient's Smart Health account, where that surface is reachable.",
      'provider-data':
        "The patient's attestation replaces the chart's value inside the questionnaire group; no separate patient read-back on this lane.",
    },
  },
  {
    uc: 'uc08',
    title: 'Denied',
    description: {
      overview:
        "A request the insurer denies — the documented conservative therapy falls short of what's required — and the reason for the denial is returned.",
      technical:
        "The PAS submit is denied — the conservative-therapy answers fall below the policy threshold — and the payer's denial rationale travels back.",
    },
    provenance: {
      'provider-data':
        'An excluded service read from the chart; the reference payer returns a formal not-certified denial with its reason.',
    },
  },
];

// The lanes, in the order they render across the app (TopBar's ModeSwitch
// and, historically, UCCards' own tablist). The provider-data lane renders
// only when the Kit reports its gateway child (App filters on
// status.providerDataUrl — see visibleLanes).
export const LANES: Lane[] = ['conformant', 'ehr', 'provider-data'];

// visibleLanes is the ModeSwitch's lane list for a given status: the
// provider-data lane exists iff the Kit booted its provider-data gateway
// child (key-presence of providerDataUrl — the packaged Java trio).
export function visibleLanes(providerDataUrl: string | undefined): Lane[] {
  return providerDataUrl ? LANES : LANES.filter((l) => l !== 'provider-data');
}

// `short` is a CONCISE chip label for the TopBar mode switch — it is NOT a
// paraphrase of `title`/`blurb`. The honest, fuller framing lives in `blurb`
// (rendered as the Scenarios caption), which itself carries both registers so
// it flips with the cards; the chip label never does.
export const LANE_LABELS: Record<
  Lane,
  { title: string; short: string; blurb: Record<Register, string> }
> = {
  ehr: {
    title: 'Plain EHR (non-conformant provider)',
    short: 'Plain EHR',
    blurb: {
      overview:
        'Every scenario runs off a plain FHIR data server — no Da Vinci support needed on the provider side. The Smart Gateway does all the Da Vinci work.',
      technical:
        'The Smart Gateway originates every scenario off a plain FHIR data server — no Da Vinci conformance required on the provider side.',
    },
  },
  conformant: {
    title: 'Da Vinci-conformant provider',
    short: 'Da Vinci provider',
    blurb: {
      overview:
        'A Da Vinci-conformant provider system sends each scenario straight into the Smart Gateway, the way a real EHR integration would.',
      technical:
        "A conformant Da Vinci client originates through the Smart Gateway's authenticated inbound endpoint. In this build the Kit itself acts as that client; the packaged Kit adds a real Da Vinci provider system in this role.",
    },
  },
  'provider-data': {
    title: 'Plain EHR, Da Vinci reference payer',
    short: 'Reference payer',
    blurb: {
      overview:
        "Every scenario originates off the Kit's own FHIR data — the order, the diagnosis and the clinical evidence are read from the chart, not scripted — and runs against the hosted Da Vinci reference payer instead of the built-in sandbox payer.",
      technical:
        'The Smart Gateway runs the provider-data origination profile: it reads each scenario\'s order from the seeded provider FHIR store, drives DTR itself (operated $populate, SDC adaptive $next-question) and PAS, routed through the Hub to the hosted conformance payer that fronts the Da Vinci reference payer.',
    },
  },
};
