// pameta.test.ts — literal pins for pameta.ts's prior-authorization decision
// copy, the bridgingmeta.test.ts idiom. Each string's RENDERED half of the
// double-assert lives in the component that actually shows it
// (UCCards.test.tsx); this file pins the literals and holds the same
// participant-facing vocabulary gate ucmeta.test.ts holds.
import { describe, it, expect } from 'vitest';
import {
  PA_CONTINUATION_CAPTION,
  PA_CONTINUATION_LABEL,
  PA_CONTINUATION_NOT_DURABLE,
  PA_DECISION_LABELS,
  PA_DECISION_OUTCOMES,
  PA_PINNED_STRINGS,
  PA_RATIONALE_LABEL,
} from './pameta';
import { BANNED_VOCAB } from './ucmeta';

describe('pameta — prior-authorization decision pinned strings (literal)', () => {
  it('PA_DECISION_LABELS covers exactly the three decisions a payer can give', () => {
    expect(PA_DECISION_LABELS).toEqual({
      approved: 'Approved',
      denied: 'Denied',
      pended: 'Pended',
    });
  });

  it('PA_DECISION_OUTCOMES — pended says the payer has NOT DECIDED, never that it refused', () => {
    expect(PA_DECISION_OUTCOMES.approved).toBe('The payer approved this prior-authorization request.');
    expect(PA_DECISION_OUTCOMES.denied).toBe('The payer denied this prior-authorization request.');
    expect(PA_DECISION_OUTCOMES.pended).toBe(
      'The payer has not decided this prior-authorization request yet.',
    );
  });

  it('PA_RATIONALE_LABEL', () => {
    expect(PA_RATIONALE_LABEL).toBe('Payer rationale');
  });

  it('PA_CONTINUATION_LABEL / PA_CONTINUATION_CAPTION', () => {
    expect(PA_CONTINUATION_LABEL).toBe('Continuation');
    expect(PA_CONTINUATION_CAPTION).toBe(
      'Nothing polls for this decision — the payer is asked for it again with this continuation.',
    );
  });

  it('PA_CONTINUATION_NOT_DURABLE — a disclosure: memory only, lost on restart, and the two ways forward', () => {
    expect(PA_CONTINUATION_NOT_DURABLE).toBe(
      'In this shape your Smart Gateway keeps a continuation in memory only, so it does not survive a restart. A continuation the gateway no longer holds can be replaced by submitting the run again, or the decision can be inquired from your own system.',
    );
  });

  it('every pinned string is swept by the participant-facing vocabulary gate', () => {
    // PA_PINNED_STRINGS is built FROM the exported constants, so a string
    // added to this file cannot dodge the gate by being left out here.
    for (const s of PA_PINNED_STRINGS) expect(s, s).not.toMatch(BANNED_VOCAB);
    for (const s of Object.values(PA_DECISION_OUTCOMES)) expect(PA_PINNED_STRINGS).toContain(s);
    for (const s of Object.values(PA_DECISION_LABELS)) expect(PA_PINNED_STRINGS).toContain(s);
    for (const s of [PA_RATIONALE_LABEL, PA_CONTINUATION_LABEL, PA_CONTINUATION_CAPTION, PA_CONTINUATION_NOT_DURABLE]) {
      expect(PA_PINNED_STRINGS).toContain(s);
    }
  });
});
