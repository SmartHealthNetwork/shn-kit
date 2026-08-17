// XformDiff.tsx — the side-by-side transformation panes: renders the
// before/after payload pair through JsonView, with computeXformDiff's
// regions painted in as node highlights. Feed-agnostic — a live leg's
// capture fetch and the carry demonstration's `demo.exhibit` record both
// hand this component the same four inputs; it has no fetch of its own and
// no opinion about where the payloads came from.
import { useMemo, type JSX } from 'react';
import { JsonView } from './JsonView';
import { computeXformDiff } from './xformclassify';
import type { RegionClass } from './xformclassify';
import type { BridgingLossReport } from './types';
import { DEMO_RESTORED_VERDICT } from './bridgingmeta';

export interface XformDiffProps {
  before: unknown;
  after: unknown;
  rawBefore: string;
  rawAfter: string;
  beforeLabel: string; // e.g. 'Before — as built (pa.dtr 2.0)'
  afterLabel: string; // e.g. 'After — as sent (pa.dtr 2.2)'
  lossReports: BridgingLossReport[];
}

// JsonView's own default (2) is tuned for the general-purpose payload
// viewer, where a deep resource would otherwise dump the whole tree into
// view. A diff pane's entire reason to exist is showing WHERE the two sides
// differ, so start a little deeper than that default — but NOT fully
// expanded: JsonView's `regions` prop now force-expands a differing
// region's own ancestor chain regardless of depth (the same force-open
// precedent a search hit already gets), so every region stays guaranteed
// visible without paying to walk/render a resource up to 2 MiB fully open.
// Panes are independently scrollable, so a large resource still stays
// navigable past this depth.
const XFORM_PANE_DEFAULT_DEPTH = 4;

// The legend's fourth swatch, `unchanged`, has no RegionClass counterpart —
// computeXformDiff's regions array only ever holds DIFFERING subtrees, so
// "unchanged" here means "whatever's left over, painted with no highlight
// at all." It stays in the legend as the baseline every other swatch reads
// against — the carried/synthesized/rewritten swatches only make sense next
// to it.
const LEGEND: Array<{ cls: string; label: string }> = [
  { cls: 'unchanged', label: 'unchanged' },
  { cls: 'carried', label: 'carried' },
  { cls: 'synthesized', label: 'synthesized' },
  { cls: 'rewritten', label: 'rewritten' },
];

export function XformDiff({
  before,
  after,
  rawBefore,
  rawAfter,
  beforeLabel,
  afterLabel,
  lossReports,
}: XformDiffProps): JSX.Element {
  // Memoized on its five inputs — before/after/rawBefore/rawAfter/
  // lossReports — so a re-render this component's own props didn't
  // actually change (e.g. a sibling search box's keystroke re-rendering a
  // shared parent) never re-walks a payload up to 2 MiB.
  const { byteIdentical, regions } = useMemo(
    () => computeXformDiff(before, after, rawBefore, rawAfter, lossReports),
    [before, after, rawBefore, rawAfter, lossReports],
  );

  // Split the classified regions into per-pane path→class maps — JsonView's
  // `regions` prop is keyed to ONE tree, so `both`-side classes (carried,
  // rewritten) go into both maps while `after`-side-only classes
  // (synthesized) go into the after map alone.
  const beforeRegions = new Map<string, RegionClass>();
  const afterRegions = new Map<string, RegionClass>();
  for (const region of regions) {
    const key = region.path.join('.');
    if (region.side === 'before' || region.side === 'both') beforeRegions.set(key, region.cls);
    if (region.side === 'after' || region.side === 'both') afterRegions.set(key, region.cls);
  }

  const summary = byteIdentical ? DEMO_RESTORED_VERDICT : `${regions.length} regions differ`;

  return (
    <div className="xform-diff">
      <div className="xform-diff-head">
        <span className="xform-diff-summary">{summary}</span>
        <div className="xform-legend">
          {LEGEND.map((entry) => (
            <span key={entry.cls} className={`xform-legend-item xform-legend-${entry.cls}`}>
              <span className="xform-legend-swatch" />
              {entry.label}
            </span>
          ))}
        </div>
      </div>
      <div className="xform-panes">
        <div className="xform-pane">
          <div className="xform-pane-label">{beforeLabel}</div>
          <JsonView value={before} regions={beforeRegions} defaultDepth={XFORM_PANE_DEFAULT_DEPTH} />
        </div>
        <div className="xform-pane">
          <div className="xform-pane-label">{afterLabel}</div>
          <JsonView value={after} regions={afterRegions} defaultDepth={XFORM_PANE_DEFAULT_DEPTH} />
        </div>
      </div>
    </div>
  );
}
