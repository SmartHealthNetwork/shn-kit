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
    const before = { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ value: 1 }] }] };
    const after = { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ value: 2 }] }] };
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
    const before = { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ value: 1 }] }] };
    const after = {
      resourceType: 'QuestionnaireResponse',
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
    // extension:<sliceName> matches JSON key `extension` — can classify this
    // one, since the wrapper URL is absent.
    //
    // The slice is information-origin, not itemWeight: this fixture puts the
    // extension on the ANSWER, and itemWeight cannot legally sit
    // there (its SD contexts it to the answer's value). information-origin is
    // a real DTR slice that does live at answer.extension, so the test keeps
    // pinning the over-approximation against a locus that actually exists.
    const before = { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ value: 2 }] }] };
    const after = {
      resourceType: 'QuestionnaireResponse',
      item: [
        {
          answer: [
            {
              value: 2,
              extension: [
                {
                  url: 'http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/information-origin',
                  extension: [{ url: 'source', valueCode: 'auto' }],
                },
              ],
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
        carried: [{ path: 'QuestionnaireResponse.item.answer.extension:information-origin' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([
      { path: ['item', 0, 'answer', 0, 'extension'], side: 'both', cls: 'carried' },
    ]);
  });

  it('restored itemWeight under _value[x] classifies carried via the suffix fallback', () => {
    // The fallback path: NEITHER tree holds an shn-carried-content wrapper, so
    // containsCarriedExtension and ancestorHasCarriedWrapper both decline and
    // suffixMatches is the only rule that can classify this region. That is the
    // same case the "restored-side counterpart … no wrapper URL present" test
    // above pins — and with the locus at the answer's value, it stops covering itemWeight
    // unless jsonKeysOf folds the value[x] key.
    //
    // The `_valueInteger` container exists on BOTH sides on purpose. If it were
    // new on the after side, diffPaths would collapse the region at
    // `_valueInteger` itself, giving a 3-key chain against a 4-segment loss
    // path, and the length floor would reject it fold or no fold.
    const before = {
      resourceType: 'QuestionnaireResponse',
      item: [{ answer: [{ valueInteger: 6, _valueInteger: { id: 'a1' } }] }],
    };
    const after = {
      resourceType: 'QuestionnaireResponse',
      item: [
        {
          answer: [
            {
              valueInteger: 6,
              _valueInteger: {
                id: 'a1',
                extension: [{ url: 'http://hl7.org/fhir/StructureDefinition/itemWeight', valueDecimal: 0.5 }],
              },
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
        carried: [{ path: 'QuestionnaireResponse.item.answer.value.extension:itemWeight' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([
      { path: ['item', 0, 'answer', 0, '_valueInteger', 'extension'], side: 'both', cls: 'carried' },
    ]);
  });

  it('restored itemWeight on a Coding value classifies carried via the suffix fallback', () => {
    // The complex-value half: extensions live on the value object itself, so
    // the JSON key is `valueCoding`. It exists on both sides for the same
    // length-floor reason as the primitive case above.
    const before = { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ valueCoding: { code: 'N' } }] }] };
    const after = {
      resourceType: 'QuestionnaireResponse',
      item: [
        {
          answer: [
            {
              valueCoding: {
                code: 'N',
                extension: [{ url: 'http://hl7.org/fhir/StructureDefinition/itemWeight', valueDecimal: 0.5 }],
              },
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
        carried: [{ path: 'QuestionnaireResponse.item.answer.value.extension:itemWeight' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([
      { path: ['item', 0, 'answer', 0, 'valueCoding', 'extension'], side: 'both', cls: 'carried' },
    ]);
  });

  it('does NOT fold a value[x]-shaped key that is not under an answer', () => {
    // THE SCOPE GUARD. It must be deep enough to clear the length floor, or the
    // floor decides the result and the test pins nothing about scoping:
    //
    //   scoped fold (correct) -> no fold applies, keys stay ['item','valueCoding','extension'] -> rewritten
    //   UNSCOPED fold (bug)   -> keys become    ['item','value','extension']                   -> carried
    //   no fold at all        -> rewritten
    //
    // So this test fails loudly if anyone drops the `answer` condition, which is
    // the whole reason the Go rule is scoped: ^_?value[A-Z][a-zA-Z0-9]*$ also
    // matches real, non-choice FHIR field names.
    //
    // The loss path is a plausible-but-foreign one — the point is that a region
    // outside `answer` must not be claimed by it. The tree is typed as the
    // loss path's own resource so the resource anchor is satisfied and
    // cannot be what decides this row.
    const before = { resourceType: 'Questionnaire', item: [{ valueCoding: { code: 'a' } }] };
    const after = {
      resourceType: 'Questionnaire',
      item: [
        {
          valueCoding: {
            code: 'a',
            extension: [{ url: 'http://hl7.org/fhir/StructureDefinition/itemWeight', valueDecimal: 0.5 }],
          },
        },
      ],
    };
    const lossReports: BridgingLossReport[] = [
      {
        module: 'pa.dtr 2.1->2.2',
        source: '2.1',
        target: '2.2',
        carried: [{ path: 'Questionnaire.item.value.extension:itemWeight' }],
      },
    ];
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), lossReports);
    expect(result.regions).toEqual([
      { path: ['item', 0, 'valueCoding', 'extension'], side: 'both', cls: 'rewritten' },
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
    //
    // The subtree is `_valueInteger.extension[0]`, not `answer.extension[1]`:
    // the carry fires at the answer's VALUE, so the wrapper replaces
    // the itemWeight element inside the primitive's sibling object, where it is
    // the only entry. (What colors these carried is ancestorHasCarriedWrapper —
    // the wrapper is right there in the intermediate tree — not the value[x]
    // fold, which serves the case where neither tree holds a wrapper.)
    expect(result.regions).toEqual(
      expect.arrayContaining([
        { path: ['item', 0, 'answer', 0, '_valueInteger', 'extension', 0, 'url'], side: 'both', cls: 'carried' },
        { path: ['item', 0, 'answer', 0, '_valueInteger', 'extension', 0, 'valueDecimal'], side: 'both', cls: 'carried' },
        { path: ['item', 0, 'answer', 0, '_valueInteger', 'extension', 0, 'extension'], side: 'both', cls: 'carried' },
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

  // These three lock in the carried/rewritten classification of nested QR
  // items and, via the emptied-lossReports half below, the dependency on
  // suffixMatches. They do NOT pin the absence of a fold on this side.
  //
  // suffixMatches is a SUFFIX rule, so extra nesting segments fall off the front
  // of the compared tail — the same tolerance the resource anchor's comment
  // claims for Bundle-wrapped resources. The Go mirror (LocusCovers) breaks on
  // nesting because it is a PREFIX rule. That asymmetry is why NormalizeDiffPath
  // needed a fold and jsonKeysOf did not — but a fold added here would be a
  // harmless NO-OP, not a fix: a recursion fold only removes redundant segments
  // earlier in the chain, never the trailing segments suffixMatches compares,
  // so these cases would still pass unchanged with or without one. See the
  // mirror comment above VALUE_CHOICE_KEY in xformclassify.ts.
  //
  // Each case asserts THREE halves: carried with the QR loss report present,
  // rewritten with lossReports emptied, and rewritten with only a
  // ClaimResponse loss entry present. lossReports is the only input
  // suffixMatches consumes, and the wrapper pre-checks that run before it
  // consume none — so the emptied half is what proves the carried verdict
  // traces to suffixMatches rather than to a wrapper pre-check alone. The
  // ClaimResponse half is the resource-anchor rejection: before the resource anchor, a
  // one-segment ['extension'] loss path matched any region ending in
  // `extension`, across resource types, and all three of these shapes came
  // back carried on the strength of a ClaimResponse entry.
  const nestedAnswerBefore = { valueInteger: 6, _valueInteger: { id: 'a1' } };
  const nestedAnswerAfter = {
    valueInteger: 6,
    _valueInteger: {
      id: 'a1',
      extension: [{ url: 'http://hl7.org/fhir/StructureDefinition/itemWeight', valueDecimal: 0.5 }],
    },
  };
  const nestedLossReports: BridgingLossReport[] = [
    {
      module: 'pa.dtr 2.1->2.2',
      source: '2.1',
      target: '2.2',
      carried: [{ path: 'QuestionnaireResponse.item.answer.value.extension:itemWeight' }],
    },
  ];
  // The foreign entry: a pa.pas leg's loss report meeting an embedded QR in the
  // same bundle is what makes this reachable. Its stripped tail is the lone
  // segment ['extension'].
  const claimResponseLossReports: BridgingLossReport[] = [
    {
      module: 'pa.pas 2.0->2.1',
      source: '2.0',
      target: '2.1',
      carried: [{ path: 'ClaimResponse.extension:claimResponseReviewer' }],
    },
  ];

  it.each([
    [
      'item.item axis',
      { resourceType: 'QuestionnaireResponse', item: [{ linkId: '1', item: [{ linkId: '1.1', answer: [nestedAnswerBefore] }] }] },
      { resourceType: 'QuestionnaireResponse', item: [{ linkId: '1', item: [{ linkId: '1.1', answer: [nestedAnswerAfter] }] }] },
      ['item', 0, 'item', 0, 'answer', 0, '_valueInteger', 'extension'],
    ],
    [
      'item.answer.item axis',
      {
        resourceType: 'QuestionnaireResponse',
        item: [{ linkId: '1', answer: [{ valueBoolean: true, item: [{ linkId: '1.1', answer: [nestedAnswerBefore] }] }] }],
      },
      {
        resourceType: 'QuestionnaireResponse',
        item: [{ linkId: '1', answer: [{ valueBoolean: true, item: [{ linkId: '1.1', answer: [nestedAnswerAfter] }] }] }],
      },
      ['item', 0, 'answer', 0, 'item', 0, 'answer', 0, '_valueInteger', 'extension'],
    ],
    [
      'depth 3',
      { resourceType: 'QuestionnaireResponse', item: [{ item: [{ item: [{ answer: [nestedAnswerBefore] }] }] }] },
      { resourceType: 'QuestionnaireResponse', item: [{ item: [{ item: [{ answer: [nestedAnswerAfter] }] }] }] },
      ['item', 0, 'item', 0, 'item', 0, 'answer', 0, '_valueInteger', 'extension'],
    ],
  ])('nested carry classifies via the suffix fallback: %s', (_name, before, after, path) => {
    const withLoss = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), nestedLossReports);
    expect(withLoss.regions).toEqual([{ path, side: 'both', cls: 'carried' }]);

    // The discrimination: lossReports is the ONLY input suffixMatches consumes.
    const withoutLoss = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), []);
    expect(withoutLoss.regions).toEqual([{ path, side: 'both', cls: 'rewritten' }]);

    // Resource anchor: a ClaimResponse loss entry cannot explain a QuestionnaireResponse
    // region, however its tail reads.
    const foreign = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), claimResponseLossReports);
    expect(foreign.regions).toEqual([{ path, side: 'both', cls: 'rewritten' }]);
  });
});

// suffixMatches anchors a loss entry to the region's ENCLOSING RESOURCE —
// the nearest ancestor (root included) carrying a resourceType — and compares
// the tail against the key chain relative to that resource. These rows pin
// the three properties the anchor must hold at once: a Bundle-wrapped region
// still matches its own resource's entry; a deep entry still cannot claim a
// shallow root-level region; an entry for one resource type cannot claim a
// region inside another, whether the two sit side by side in a bundle or one
// is contained in the other.
describe('computeXformDiff — loss entries are anchored to the region\'s enclosing resource', () => {
  const qrCarried: BridgingLossReport[] = [
    {
      module: 'pa.dtr 2.1->2.2',
      source: '2.1',
      target: '2.2',
      carried: [{ path: 'QuestionnaireResponse.item.answer.extension:information-origin' }],
    },
  ];
  const crCarried: BridgingLossReport[] = [
    {
      module: 'pa.pas 2.0->2.1',
      source: '2.0',
      target: '2.1',
      carried: [{ path: 'ClaimResponse.extension:claimResponseReviewer' }],
    },
  ];
  const reviewerExt = { url: 'http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction', valueString: 'x' };
  const originExt = {
    url: 'http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/information-origin',
    extension: [{ url: 'source', valueCode: 'auto' }],
  };

  it('Bundle-wrapped resource: the `entry.resource` prefix falls outside the compared tail — carried', () => {
    // The property the old length floor's comment claimed but nothing pinned.
    const before = {
      resourceType: 'Bundle',
      entry: [{ resource: { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ valueInteger: 2 }] }] } }],
    };
    const after = {
      resourceType: 'Bundle',
      entry: [
        { resource: { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ valueInteger: 2, extension: [originExt] }] }] } },
      ],
    };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), qrCarried);
    expect(result.regions).toEqual([
      { path: ['entry', 0, 'resource', 'item', 0, 'answer', 0, 'extension'], side: 'both', cls: 'carried' },
    ]);
  });

  it('Bundle-wrapped resource: a deep entry still cannot claim the resource\'s root-level `extension` — rewritten', () => {
    // The length floor now applies to the chain RELATIVE to the enclosing
    // resource: ['extension'] is one key, the entry's tail is three.
    const before = {
      resourceType: 'Bundle',
      entry: [{ resource: { resourceType: 'QuestionnaireResponse', item: [{ answer: [{ valueInteger: 2 }] }] } }],
    };
    const after = {
      resourceType: 'Bundle',
      entry: [
        {
          resource: {
            resourceType: 'QuestionnaireResponse',
            item: [{ answer: [{ valueInteger: 2 }] }],
            extension: [{ url: 'urn:shn:root-note', valueInteger: 1 }],
          },
        },
      ],
    };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), qrCarried);
    expect(result.regions).toEqual([{ path: ['entry', 0, 'resource', 'extension'], side: 'both', cls: 'rewritten' }]);
  });

  it('mixed bundle: a ClaimResponse entry labels the ClaimResponse region, not the embedded QR\'s', () => {
    // Both resources gain an `extension` in the same transform. The one loss
    // report present names ClaimResponse.extension:<slice>; only the
    // ClaimResponse's region is explained by it.
    const before = {
      resourceType: 'Bundle',
      entry: [
        { resource: { resourceType: 'ClaimResponse', status: 'active' } },
        { resource: { resourceType: 'QuestionnaireResponse', status: 'completed' } },
      ],
    };
    const after = {
      resourceType: 'Bundle',
      entry: [
        { resource: { resourceType: 'ClaimResponse', status: 'active', extension: [reviewerExt] } },
        { resource: { resourceType: 'QuestionnaireResponse', status: 'completed', extension: [originExt] } },
      ],
    };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), crCarried);
    expect(result.regions).toEqual(
      expect.arrayContaining([
        { path: ['entry', 0, 'resource', 'extension'], side: 'both', cls: 'carried' },
        { path: ['entry', 1, 'resource', 'extension'], side: 'both', cls: 'rewritten' },
      ]),
    );
    expect(result.regions).toHaveLength(2);
  });

  it('contained resource: the nearest enclosing resource is the contained one, not the container — rewritten', () => {
    // A Patient contained in a QuestionnaireResponse gains an extension. The
    // QR's own root-level entry ends in the same `extension` key and the
    // absolute chain ['contained', 'extension'] has it as a suffix — but the
    // region belongs to the Patient, so the QR entry does not explain it.
    const qrRoot: BridgingLossReport[] = [
      { module: 'pa.dtr 2.1->2.2', source: '2.1', target: '2.2', carried: [{ path: 'QuestionnaireResponse.extension:context' }] },
    ];
    const before = { resourceType: 'QuestionnaireResponse', contained: [{ resourceType: 'Patient', id: 'p1' }] };
    const after = {
      resourceType: 'QuestionnaireResponse',
      contained: [{ resourceType: 'Patient', id: 'p1', extension: [{ url: 'urn:shn:x', valueString: 'y' }] }],
    };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), qrRoot);
    expect(result.regions).toEqual([{ path: ['contained', 0, 'extension'], side: 'both', cls: 'rewritten' }]);
  });

  it('a tree with no resourceType anywhere has no enclosing resource, so no loss entry can claim it — rewritten', () => {
    // Pins the design decision, not an accident: a LossEntry.Path is
    // resource-rooted, so a region that cannot be attributed to a resource is
    // a region the entry does not describe. Every real payload the inspector
    // sees is a FHIR resource and carries the key; the permissive alternative
    // (match when untyped) would let a fixture pass the suffix rule for a
    // reason unrelated to what it measures.
    const before = { item: [{ answer: [{ valueInteger: 2 }] }] };
    const after = { item: [{ answer: [{ valueInteger: 2, extension: [originExt] }] }] };
    const result = computeXformDiff(before, after, JSON.stringify(before), JSON.stringify(after), qrCarried);
    expect(result.regions).toEqual([{ path: ['item', 0, 'answer', 0, 'extension'], side: 'both', cls: 'rewritten' }]);
  });
});
