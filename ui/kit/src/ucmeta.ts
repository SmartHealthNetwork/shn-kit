// ucmeta.ts — the eight UC cards' copy + branch pickers + provenance labels.
// The table below is VERBATIM, grounded in kit/runner/rows_*.go (the row
// funcs' asserted outcomes) — do not paraphrase title/description/
// branch-label/provenance strings here.
import type { Lane } from './types';

export interface UCBranchOption {
  value: string;
  label: string;
}

export interface UCMeta {
  uc: string;
  title: string;
  description: string;
  branches?: Partial<Record<Lane, UCBranchOption[]>>; // absent ⇒ always branch ""
  provenance?: Partial<Record<Lane, string>>; // provenance label, rendered as a small tag
}

export const UC_METAS: UCMeta[] = [
  {
    uc: 'uc01',
    title: 'Eligibility check',
    description:
      "Is the member's coverage active? The Smart Gateway asks the payer across the Hub and reads back the coverage verdict.",
    branches: {
      ehr: [
        { value: 'covered', label: 'Member is covered' },
        { value: 'notcovered', label: 'Member is not covered' },
      ],
      conformant: [
        { value: 'covered', label: 'Member is covered' },
        { value: 'notcovered', label: 'Member is not covered' },
      ],
    },
    provenance: {
      conformant: "SHN-originated gap-fill — eligibility isn't a Da Vinci ingress operation",
    },
  },
  {
    uc: 'uc02',
    title: 'No prior auth needed',
    description:
      'An order the payer waves through: the CRD card comes back "covered, no prior authorization required".',
  },
  {
    uc: 'uc03',
    title: 'Prior auth, approved',
    description:
      'CRD flags the order as needing prior auth, the DTR questionnaire is completed from clinical data, and the PAS submit returns approved with an auth number.',
  },
  {
    uc: 'uc04',
    title: 'Pend, then approve',
    description:
      'The first submit pends on missing evidence (an operative report). An amended re-submit carrying the evidence is approved.',
  },
  {
    uc: 'uc05',
    title: 'Federated evidence',
    description:
      'The missing evidence lives at another facility. A consent-checked federated query (CDex) fetches it across the network before the amended submit.',
    branches: {
      ehr: [
        { value: 'consent', label: 'Patient consent on file' },
        { value: 'noconsent', label: 'Consent denied — query blocked' },
      ],
    },
    provenance: {
      conformant:
        'CDex evidence carried on the amended re-POST; the federated-query leg is SHN-bracketed, no consent branch (CXL-D11)',
    },
  },
  {
    uc: 'uc06',
    title: 'Clinician attestation',
    description:
      "A pended item only a clinician's attested answer can complete; the attested amendment is approved.",
    provenance: {
      conformant: 'Real $questionnaire-package via the ingress; manual DTR SPA deferred (D-2RI-1)',
    },
  },
  {
    uc: 'uc07',
    title: 'Patient can see it',
    description: "An approved order projected to the patient's Smart Health account.",
    branches: {
      ehr: [
        { value: '', label: 'CPT order' },
        { value: 'hcpcs', label: 'HCPCS order + patient read-back' },
      ],
    },
    provenance: {
      conformant: 'Hybrid patient-surface read-back (D-2RI-6 analog)',
    },
  },
  {
    uc: 'uc08',
    title: 'Denied',
    description:
      'A request the payer denies — the conservative-therapy answers fall below the threshold — with the denial rationale travelling back.',
  },
];

// The two lanes, in the order they render across the app (TopBar's
// ModeSwitch and, historically, UCCards' own tablist).
export const LANES: Lane[] = ['conformant', 'ehr'];

// `short` is a CONCISE chip label for the TopBar mode switch — it is NOT a
// paraphrase of `title`/`blurb` (those stay verbatim, grounded copy per the
// file header) but a new, separately-authored label. The honest, fuller
// framing lives in `blurb` (rendered as the Scenarios caption), never in
// the chip.
export const LANE_LABELS: Record<Lane, { title: string; short: string; blurb: string }> = {
  ehr: {
    title: 'Plain EHR (non-conformant provider)',
    short: 'Plain EHR',
    blurb:
      'The Smart Gateway originates every scenario off a plain FHIR data server — no Da Vinci conformance required on the provider side (Mode A).',
  },
  conformant: {
    title: 'Da Vinci-conformant provider',
    short: 'Da Vinci provider',
    blurb:
      "A conformant Da Vinci client originates through the Smart Gateway's ingress (UDAP B2B). In this build the Kit itself acts as the conformant client (direct-mint); the packaged Kit adds the real br-provider system in this role.",
  },
};
