import { describe, expect, it } from 'vitest';
import { BANNED_VOCAB, LANES, LANE_LABELS, UC_METAS, visibleLanes } from './ucmeta';
import { FREEFORM_PROVENANCE_LINE } from './FreeFormPanel';

describe('ucmeta vocabulary gate', () => {
  it('no participant-facing string names the payer implementation or internal vocabulary', () => {
    const strings: string[] = [];
    for (const lane of LANES) {
      const l = LANE_LABELS[lane];
      strings.push(l.title, l.short, l.blurb.overview, l.blurb.technical);
    }
    for (const m of UC_METAS) {
      strings.push(m.title, m.description.overview, m.description.technical);
      for (const opts of Object.values(m.branches ?? {})) for (const o of opts ?? []) strings.push(o.label);
      strings.push(...Object.values(m.provenance ?? {}));
    }
    strings.push(FREEFORM_PROVENANCE_LINE);
    for (const s of strings) expect(s, s).not.toMatch(BANNED_VOCAB);
  });
  it('two lanes; the Plain EHR lane exists iff the Kit reports its second gateway child', () => {
    expect(LANES).toEqual(['conformant', 'ehr']);
    expect(visibleLanes(undefined)).toEqual(['conformant']);
    expect(visibleLanes('')).toEqual(['conformant']);
    expect(visibleLanes('http://127.0.0.1:9095')).toEqual(['conformant', 'ehr']);
  });
  it('every lane blurb names the hosted Da Vinci reference payer; uc07 takes no branch on either lane; uc05 branches on ehr only', () => {
    for (const lane of LANES) expect(LANE_LABELS[lane].blurb.overview).toMatch(/reference payer/i);
    const uc07 = UC_METAS.find((m) => m.uc === 'uc07')!;
    expect(uc07.branches).toBeUndefined();
    const uc05 = UC_METAS.find((m) => m.uc === 'uc05')!;
    expect(Object.keys(uc05.branches ?? {})).toEqual(['ehr']);
    expect(uc05.provenance?.conformant).toMatch(/consent-denied branch isn't exercised here/);
  });
});
