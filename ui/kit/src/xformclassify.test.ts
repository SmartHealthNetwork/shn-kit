import { describe, it, expect } from 'vitest';
import { computeXformDiff, SHN_CARRIED_CONTENT_EXT_URL } from './xformclassify';
import { buildDemoStory } from './inspect';
import type { BridgingLossReport, KitEvent } from './types';
import runDemoCarry from './fixtures/run-demo-carry.json';

// Table-driven classification tests: each row is a (before,
// after) pair plus the loss reports a real TransformCard would have parsed
// from the leg's Provenance, and the exact region set computeXformDiff must
// produce. rawBefore/rawAfter are JSON.stringify of the pair by default
// (distinct strings unless the row is specifically pinning identity).
describe('computeXformDiff', () => {
  it('identity pair: byte-identical, no regions', () => {
    const value = { resourceType: 'QuestionnaireResponse', status: 'completed' };
    const raw = JSON.stringify(value);
    const result = computeXformDiff(value, value, raw, raw, []);
    expect(result).toEqual({ byteIdentical: true, regions: [] });
  });

  it('carried-content wrapper on the after side + a matching Carried entry ⇒ one carried both-sides region', () => {
    const before = { item: [{ answer: [{ value: 1 }] }] };
    const after = {
      item: [
        {
          answer: [
            {
              value: 1,
              extension: [{ url: SHN_CARRIED_CONTENT_EXT_URL, valueDecimal: 0.4 }],
            },
          ],
        },
      ],
    };
    const lossReports: BridgingLossReport[] = [
      {
        module: 'pa.dtr 2.1->2.2',
        source: '2.1',
        target: '2.2',
        carried: [{ path: 'QuestionnaireResponse.item.answer.extension' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.byteIdentical).toBe(false);
    expect(result.regions).toEqual([
      { path: ['item', 0, 'answer', 0, 'extension'], side: 'both', cls: 'carried' },
    ]);
  });

  it('carried-content wrapper present with NO loss reports at all ⇒ still carried — isolates the wrapper-URL clause', () => {
    // Every other wrapper-URL fixture in this file also ships a
    // suffix-matching Carried entry, so a regression that deleted the
    // wrapper-detection clause entirely would still pass them via the
    // suffix-match fallback. This row has NO loss reports — only the
    // wrapper-URL clause can classify it.
    const before = { item: [{ answer: [{ value: 1 }] }] };
    const after = {
      item: [
        {
          answer: [
            {
              value: 1,
              extension: [{ url: SHN_CARRIED_CONTENT_EXT_URL, valueDecimal: 0.4 }],
            },
          ],
        },
      ],
    };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), []);
    expect(result.regions).toEqual([
      { path: ['item', 0, 'answer', 0, 'extension'], side: 'both', cls: 'carried' },
    ]);
  });

  it('a non-matching Carried entry present elsewhere ⇒ does NOT claim an unrelated region — rewritten', () => {
    // A Carried entry exists in the loss reports, but its path doesn't
    // suffix-match THIS region's path — merely having some Carried entry
    // present must not be enough to claim an unrelated differing region.
    const before = { item: [{ answer: [{ value: 1 }] }] };
    const after = { item: [{ answer: [{ value: 2 }] }] };
    const lossReports: BridgingLossReport[] = [
      {
        module: 'pa.dtr 2.1->2.2',
        source: '2.1',
        target: '2.2',
        carried: [{ path: 'QuestionnaireResponse.item.answer.extension' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([{ path: ['item', 0, 'answer', 0, 'value'], side: 'both', cls: 'rewritten' }]);
  });

  it('a shallow root-level `extension` region does NOT match a deep "...answer.extension" Carried entry — rewritten (suffix-match length floor)', () => {
    // Pins the boundary the suffix-match length floor enforces: a
    // Carried entry naming a DEEP path (item.answer.extension) must not
    // claim a SHALLOW, unrelated region (a root-level `extension` key)
    // just because the region's lone segment happens to also read
    // "extension" — the two are different JSON locations entirely.
    const before = { item: [{ answer: [{ value: 1 }] }] };
    const after = {
      item: [{ answer: [{ value: 1 }] }],
      extension: [{ url: 'urn:shn:root-note', value: 1 }],
    };
    const lossReports: BridgingLossReport[] = [
      {
        module: 'pa.dtr 2.1->2.2',
        source: '2.1',
        target: '2.2',
        carried: [{ path: 'QuestionnaireResponse.item.answer.extension' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([{ path: ['extension'], side: 'both', cls: 'rewritten' }]);
  });

  it('restored-side counterpart matched via a Carried path ending extension:<sliceName> — no wrapper URL present', () => {
    // The restored-to-its-native-slot form: a PLAIN FHIR extension (not the
    // shn-carried-content wrapper) added at the JSON key `extension`. Only
    // the slice-segment suffix match's deliberate over-approximation —
    // extension:itemWeight matches JSON key `extension` — can classify this
    // one, since the wrapper URL is absent.
    const before = { item: [{ answer: [{ value: 2 }] }] };
    const after = {
      item: [
        {
          answer: [
            {
              value: 2,
              extension: [{ url: 'http://example.org/fhir/StructureDefinition/itemWeight', valueDecimal: 0.4 }],
            },
          ],
        },
      ],
    };
    const lossReports: BridgingLossReport[] = [
      {
        module: 'pa.dtr 2.1->2.2',
        source: '2.1',
        target: '2.2',
        carried: [{ path: 'QuestionnaireResponse.item.answer.extension:itemWeight' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([
      { path: ['item', 0, 'answer', 0, 'extension'], side: 'both', cls: 'carried' },
    ]);
  });

  it('added meta.profile + a matching Synthesized entry ⇒ synthesized, after-side only', () => {
    // `meta` itself already exists on both sides (the ordinary FHIR shape) —
    // only its `profile` child is new, so the aligned-object recursion
    // reaches down to that exact key rather than collapsing at `meta`.
    const before = { resourceType: 'QuestionnaireResponse', status: 'completed', meta: { versionId: '1' } };
    const after = {
      resourceType: 'QuestionnaireResponse',
      status: 'completed',
      meta: {
        versionId: '1',
        profile: ['http://smarthealth.network/fhir/StructureDefinition/dtr-qr|2.2.0'],
      },
    };
    const lossReports: BridgingLossReport[] = [
      {
        module: 'pa.dtr 2.1->2.2',
        source: '2.1',
        target: '2.2',
        synthesized: [{ path: 'QuestionnaireResponse.meta.profile', detail: 'target-version profile pin' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([{ path: ['meta', 'profile'], side: 'after', cls: 'synthesized' }]);
  });

  it('relocated extension URL with no loss entry ⇒ rewritten, both sides', () => {
    const before = { item: [{ extension: [{ url: 'urn:shn:old-slot', value: 1 }] }] };
    const after = { item: [{}], extension: [{ url: 'urn:shn:new-slot', value: 1 }] };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), []);
    // Two independent structural edits (removed from item[0], added at root)
    // — neither an extension wrapper nor loss-report-matched, so both are
    // real, unexplained changes: rewritten.
    expect(result.regions).toEqual(
      expect.arrayContaining([
        { path: ['item', 0, 'extension'], side: 'both', cls: 'rewritten' },
        { path: ['extension'], side: 'both', cls: 'rewritten' },
      ]),
    );
    expect(result.regions).toHaveLength(2);
  });

  it('a wholesale-added nested block with several leaves ⇒ ONE region, not one per leaf (maximal subtree)', () => {
    const before = { patient: { name: 'Linda' } };
    const after = { patient: { name: 'Linda' }, extra: { x: 1, y: { z: 2, w: 3 } } };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), []);
    expect(result.regions).toEqual([{ path: ['extra'], side: 'both', cls: 'rewritten' }]);
  });

  it('array-length change (insertion) ⇒ ONE region for the whole array, not one per shifted index', () => {
    const before = { list: [1, 2, 3] };
    const after = { list: [1, 2, 3, 4] };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), []);
    expect(result.regions).toEqual([{ path: ['list'], side: 'both', cls: 'rewritten' }]);
  });
});

// The captured carry demonstration fixture, driven through the same
// buildDemoStory pipeline StepDetail's DemoStepDetail actually consumes —
// not a hand-built shape. On this fixture, the input's itemWeight extension
// and the intermediate's shn-carried-content wrapper both sit at the SAME
// array index (item[0].answer[0].extension[1]) and are both plain objects,
// so the aligned-object recursion rule walks INTO the wrapper instead of
// collapsing at it: the wrapper's own `url` field (the one place its
// identity as a carried-content wrapper is visible) ends up as the VALUE of
// a child region rather than as an ancestor of one, and the suffix-match
// fallback falls one JSON-key short of the loss entry's declared path. Every
// region this pair produces must be genuinely explained: three sit inside
// the wrapper subtree (carried), the rest are real, unrelated content
// differences the down-then-up round trip introduces on this fixture — a
// contract-line vocabulary rewrite, a coding-system placeholder, and three
// provenance-origin rewrites — none of which any loss report names, so
// `rewritten` is the honest call for them.
describe('computeXformDiff — captured carry demonstration fixture', () => {
  it('classifies the itemWeight carry as carried, not rewritten', () => {
    const events = runDemoCarry as unknown as KitEvent[];
    const runId = events[0].runId as string;
    const story = buildDemoStory(runId, events);
    if (story === undefined || story.record.intermediate === undefined) {
      throw new Error('fixture no longer carries a carry-engine demonstration with an intermediate stage');
    }
    const { input, intermediate, lossReports } = story.record;

    const result = computeXformDiff(
      input,
      intermediate,
      JSON.stringify(input),
      JSON.stringify(intermediate),
      lossReports ?? [],
    );

    expect(result.byteIdentical).toBe(false);
    const carried = result.regions.filter((r) => r.cls === 'carried');
    const synthesized = result.regions.filter((r) => r.cls === 'synthesized');
    // The non-negotiable line: the feature's headline claim (a carried
    // region renders amber) must actually be true of the real fixture.
    expect(carried.length).toBeGreaterThan(0);
    expect(synthesized).toEqual([]);
    // The itemWeight carry's three child regions (the wrapper's `url`,
    // the input's now-vanished `valueDecimal`, and the wrapper's own inner
    // `extension` array) all land inside the shn-carried-content wrapper's
    // subtree — every one of them must classify carried, none rewritten.
    expect(result.regions).toEqual(
      expect.arrayContaining([
        { path: ['item', 0, 'answer', 0, 'extension', 1, 'url'], side: 'both', cls: 'carried' },
        { path: ['item', 0, 'answer', 0, 'extension', 1, 'valueDecimal'], side: 'both', cls: 'carried' },
        { path: ['item', 0, 'answer', 0, 'extension', 1, 'extension'], side: 'both', cls: 'carried' },
      ]),
    );
    expect(carried).toHaveLength(3);
    // The remaining five regions are genuine, unrelated content differences
    // this fixture's down-then-up round trip introduces — none declared in
    // lossReports, so rewritten (neutral) is the honest, non-amber call.
    expect(result.regions).toEqual(
      expect.arrayContaining([
        { path: ['extension', 0, 'url'], side: 'both', cls: 'rewritten' },
        { path: ['extension', 2, 'valueCodeableConcept', 'coding', 0, 'system'], side: 'both', cls: 'rewritten' },
        { path: ['item', 0, 'answer', 0, 'extension', 0, 'extension', 0, 'valueCode'], side: 'both', cls: 'rewritten' },
        { path: ['item', 1, 'answer', 0, 'extension', 0, 'extension', 0, 'valueCode'], side: 'both', cls: 'rewritten' },
        { path: ['item', 2, 'answer', 0, 'extension', 0, 'extension', 0, 'valueCode'], side: 'both', cls: 'rewritten' },
      ]),
    );
    expect(result.regions).toHaveLength(8);
  });
});
