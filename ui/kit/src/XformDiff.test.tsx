import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { XformDiff } from './XformDiff';
import { SHN_CARRIED_CONTENT_EXT_URL } from './xformclassify';
import type { BridgingLossReport } from './types';

describe('XformDiff', () => {
  it('renders the two panes labeled by beforeLabel/afterLabel and the four-swatch legend', () => {
    const value = { status: 'completed' };
    const raw = JSON.stringify(value);
    render(
      <XformDiff
        before={value}
        after={value}
        rawBefore={raw}
        rawAfter={raw}
        beforeLabel="Before — as built (pa.dtr 2.0)"
        afterLabel="After — as sent (pa.dtr 2.2)"
        lossReports={[]}
      />,
    );
    expect(screen.getByText('Before — as built (pa.dtr 2.0)')).toBeDefined();
    expect(screen.getByText('After — as sent (pa.dtr 2.2)')).toBeDefined();
    expect(screen.getByText('unchanged')).toBeDefined();
    expect(screen.getByText('carried')).toBeDefined();
    expect(screen.getByText('synthesized')).toBeDefined();
    expect(screen.getByText('rewritten')).toBeDefined();
  });

  it('identity pair: summary reads "0 regions differ — byte-identical"', () => {
    const value = { status: 'completed' };
    const raw = JSON.stringify(value);
    render(
      <XformDiff
        before={value}
        after={value}
        rawBefore={raw}
        rawAfter={raw}
        beforeLabel="Before"
        afterLabel="After"
        lossReports={[]}
      />,
    );
    expect(screen.getByText('0 regions differ — byte-identical')).toBeDefined();
  });

  it('two independent differing regions: summary reads "2 regions differ" and each region highlights the pane(s) its class calls for', () => {
    // Fixture shapes reused from xformclassify.test.ts's already-pinned
    // classification rows — a carried-content wrapper added on the after
    // side, and a `meta.profile` addition matched to a Synthesized entry —
    // combined so this test exercises XformDiff's WIRING (label/legend/
    // summary text, and routing DiffRegion.side into the right pane's
    // regions map), not classification correctness (xformclassify.test.ts's
    // job).
    const before = {
      item: [{ answer: [{ value: 1 }] }],
      resourceType: 'QuestionnaireResponse',
      meta: { versionId: '1' },
    };
    const after = {
      item: [
        {
          answer: [{ value: 1, extension: [{ url: SHN_CARRIED_CONTENT_EXT_URL, valueDecimal: 0.4 }] }],
        },
      ],
      resourceType: 'QuestionnaireResponse',
      meta: { versionId: '1', profile: ['http://smarthealth.network/fhir/StructureDefinition/dtr-qr|2.2.0'] },
    };
    const lossReports: BridgingLossReport[] = [
      {
        module: 'pa.dtr 2.1->2.2',
        source: '2.1',
        target: '2.2',
        carried: [{ path: 'QuestionnaireResponse.item.answer.extension' }],
        synthesized: [{ path: 'QuestionnaireResponse.meta.profile' }],
      },
    ];
    render(
      <XformDiff
        before={before}
        after={after}
        rawBefore={JSON.stringify(before)}
        rawAfter={JSON.stringify(after)}
        beforeLabel="Before"
        afterLabel="After"
        lossReports={lossReports}
      />,
    );
    expect(screen.getByText('2 regions differ')).toBeDefined();

    // Both regions only have a JSON node to highlight on the after pane
    // (neither key exists at that path on the before side) — one carried,
    // one synthesized, and nothing highlighted on the before pane.
    expect(document.querySelectorAll('.json-region-carried')).toHaveLength(1);
    expect(document.querySelectorAll('.json-region-synthesized')).toHaveLength(1);
    expect(document.querySelectorAll('.json-region')).toHaveLength(2);
  });
});
