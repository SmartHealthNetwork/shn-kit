// pameta.ts — ALL copy for a prior-authorization DECISION outcome (the
// bridgingmeta.ts/ucmeta.ts idiom: participant-facing, no SHN-internal
// vocabulary in this file's exported strings; Da Vinci-domain terms — prior
// authorization, PAS, payer — stay). The strings below are PINNED: exported
// verbatim, asserted both rendered (UCCards, per string) and literal
// (pameta.test.ts), and swept against ucmeta.ts's BANNED_VOCAB.
//
// A payer answers a prior-authorization submission with one of three
// decisions. Nothing polls for a later one: a PENDED decision is continued by
// an explicit inquiry against a continuation — an opaque capability id the
// provider's gateway keeps for that pended decision.

export type PADecision = 'approved' | 'denied' | 'pended';

// The decision's own short label — the chip-sized word, never a paraphrase of
// the outcome sentence below.
export const PA_DECISION_LABELS: Record<PADecision, string> = {
  approved: 'Approved',
  denied: 'Denied',
  pended: 'Pended',
};

// The outcome sentence for each decision. Pinned exactly; do not paraphrase.
// "has not decided yet" is load-bearing on the pended row: a pended request is
// undecided, never a soft refusal.
export const PA_DECISION_OUTCOMES: Record<PADecision, string> = {
  approved: 'The payer approved this prior-authorization request.',
  denied: 'The payer denied this prior-authorization request.',
  pended: 'The payer has not decided this prior-authorization request yet.',
};

// The denial's own reason, as the payer stated it — the label only; the text
// beside it is always the payer's own words, never written on its behalf.
export const PA_RATIONALE_LABEL = 'Payer rationale';

// The continuation row: its label, and the caption saying what it is for.
// Pinned exactly; do not paraphrase.
export const PA_CONTINUATION_LABEL = 'Continuation';
export const PA_CONTINUATION_CAPTION =
  'Nothing polls for this decision — the payer is asked for it again with this continuation.';

// The non-durable DISCLOSURE — what this shape of the Kit does, not a warning
// about a defect: a continuation held in memory is gone after a restart, and
// the participant has two honest ways forward. Pinned exactly; do not
// paraphrase.
export const PA_CONTINUATION_NOT_DURABLE =
  'In this shape your Smart Gateway keeps a continuation in memory only, so it does not survive a restart. A continuation the gateway no longer holds can be replaced by submitting the run again, or the decision can be inquired from your own system.';

// Every pinned string in this file, in one list — what pameta.test.ts sweeps
// against the participant-facing vocabulary gate, so a new constant added
// here cannot slip past that gate by being forgotten in the test.
export const PA_PINNED_STRINGS: string[] = [
  ...Object.values(PA_DECISION_LABELS),
  ...Object.values(PA_DECISION_OUTCOMES),
  PA_RATIONALE_LABEL,
  PA_CONTINUATION_LABEL,
  PA_CONTINUATION_CAPTION,
  PA_CONTINUATION_NOT_DURABLE,
];
