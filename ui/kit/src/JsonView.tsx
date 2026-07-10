// JsonView.tsx — hand-rolled collapsible/searchable JSON tree (no new npm
// dependency). Renders any `unknown` value as a tree: containers
// (objects/arrays) collapse beyond `defaultDepth`, primitives render plainly.
// Search is a case-insensitive substring match over keys + primitive values;
// matches are wrapped in <mark className="json-match"> and every ancestor
// path of a match is force-expanded, even past defaultDepth, so a hit is
// always visible without the caller hand-driving toggles.
//
// Open-state is CENTRALIZED here (not per-node): a global `mode`
// (default | all | none) sets the baseline every node reads, and per-node
// `overrides` (keyed by path) record explicit clicks. This is what lets
// "Expand all" / "Collapse all" flip the whole tree at once — a per-node
// useState can't be reached from the top — while an individual toggle still
// wins over the global baseline, and a search hit force-expands over both.
import { useMemo, useState } from 'react';
import type { JSX } from 'react';

export interface JsonViewProps {
  value: unknown;
  search?: string;
  defaultDepth?: number;
}

// The global open baseline. 'default' = open while depth < defaultDepth (the
// original behavior); 'all' = every container open; 'none' = only the root
// open (descendants collapsed) so "Collapse all" still shows the top-level
// structure rather than a bare `{…}`.
type OpenMode = 'default' | 'all' | 'none';

function isContainer(value: unknown): value is Record<string, unknown> | unknown[] {
  return typeof value === 'object' && value !== null;
}

function containerEntries(value: Record<string, unknown> | unknown[]): Array<[string, unknown]> {
  if (Array.isArray(value)) return value.map((v, i) => [String(i), v] as [string, unknown]);
  return Object.entries(value);
}

function formatPrimitive(value: unknown): string {
  if (value === undefined) return 'undefined';
  if (value === null) return 'null';
  if (typeof value === 'string') return value;
  return String(value);
}

// baselineOpen is the open state a node takes from the global `mode` alone,
// before per-node overrides or search force-expansion are applied.
function baselineOpen(mode: OpenMode, depth: number, defaultDepth: number): boolean {
  if (mode === 'all') return true;
  if (mode === 'none') return depth === 0;
  return depth < defaultDepth;
}

interface MatchInfo {
  // expandPaths: every container path that has a match somewhere at or below
  // it — forces that container open regardless of depth/manual-collapse.
  expandPaths: Set<string>;
  count: number;
}

// nodeOwnMatchCount counts key-hit and value-hit INDEPENDENTLY: a leaf
// whose key AND value both match the needle renders two <mark>
// highlights (json-key-match + json-value-match — see JsonNode), so the
// count must be 2, not short-circuit to 1.
function nodeOwnMatchCount(value: unknown, key: string | undefined, needle: string): number {
  let hits = 0;
  if (key !== undefined && key.toLowerCase().includes(needle)) hits += 1;
  if (!isContainer(value) && formatPrimitive(value).toLowerCase().includes(needle)) hits += 1;
  return hits;
}

// computeMatches walks the whole tree ONCE per (value, search) — callers
// memoize on that pair — and returns the ancestor paths that need
// force-expanding plus a total hit count for the search-summary line.
function computeMatches(value: unknown, search: string): MatchInfo {
  const expandPaths = new Set<string>();
  let count = 0;
  const needle = search.trim().toLowerCase();
  if (!needle) return { expandPaths, count };

  function walk(v: unknown, path: string, key: string | undefined): boolean {
    const ownHits = nodeOwnMatchCount(v, key, needle);
    count += ownHits;
    const selfHit = ownHits > 0;
    let descendantHit = false;
    if (isContainer(v)) {
      for (const [k, cv] of containerEntries(v)) {
        const childPath = path === '' ? k : `${path}.${k}`;
        if (walk(cv, childPath, k)) descendantHit = true;
      }
    }
    if (descendantHit) expandPaths.add(path);
    return selfHit || descendantHit;
  }

  walk(value, '', undefined);
  return { expandPaths, count };
}

interface JsonNodeProps {
  label?: string;
  value: unknown;
  depth: number;
  path: string;
  defaultDepth: number;
  expandPaths: Set<string>;
  search: string;
  mode: OpenMode;
  overrides: Record<string, boolean>;
  onToggle(path: string, nextOpen: boolean): void;
}

function JsonNode({
  label,
  value,
  depth,
  path,
  defaultDepth,
  expandPaths,
  search,
  mode,
  overrides,
  onToggle,
}: JsonNodeProps): JSX.Element {
  const needle = search.trim().toLowerCase();
  const keyMatches = needle !== '' && label !== undefined && label.toLowerCase().includes(needle);

  if (!isContainer(value)) {
    const valueStr = formatPrimitive(value);
    const valueMatches = needle !== '' && valueStr.toLowerCase().includes(needle);
    return (
      <div className="json-node json-leaf" data-path={path}>
        {label !== undefined &&
          (keyMatches ? (
            <mark className="json-match json-key-match">{label}</mark>
          ) : (
            <span className="json-key">{label}</span>
          ))}
        {label !== undefined && ': '}
        {valueMatches ? (
          <mark className="json-match json-value-match">{valueStr}</mark>
        ) : (
          <span className="json-value">{valueStr}</span>
        )}
      </div>
    );
  }

  const forcedOpen = expandPaths.has(path);
  // Precedence: a search hit force-opens; else an explicit per-node click;
  // else the global mode's baseline.
  const open = forcedOpen
    ? true
    : path in overrides
      ? overrides[path]
      : baselineOpen(mode, depth, defaultDepth);
  const isArray = Array.isArray(value);
  const entries = containerEntries(value);

  return (
    <div className="json-node json-container" data-path={path}>
      <button
        type="button"
        className="json-toggle"
        aria-expanded={open}
        aria-label={open ? `collapse ${label ?? 'root'}` : `expand ${label ?? 'root'}`}
        onClick={() => onToggle(path, !open)}
      >
        {open ? '▾' : '▸'}
      </button>
      {label !== undefined &&
        (keyMatches ? (
          <mark className="json-match json-key-match">{label}</mark>
        ) : (
          <span className="json-key">{label}</span>
        ))}
      {label !== undefined && ': '}
      <span className="json-bracket">{isArray ? '[' : '{'}</span>
      {open ? (
        <div className="json-children">
          {entries.map(([k, v]) => (
            <JsonNode
              key={k}
              label={k}
              value={v}
              depth={depth + 1}
              path={path === '' ? k : `${path}.${k}`}
              defaultDepth={defaultDepth}
              expandPaths={expandPaths}
              search={search}
              mode={mode}
              overrides={overrides}
              onToggle={onToggle}
            />
          ))}
        </div>
      ) : (
        <span className="json-collapsed-summary">…</span>
      )}
      <span className="json-bracket">{isArray ? ']' : '}'}</span>
    </div>
  );
}

export function JsonView({ value, search = '', defaultDepth = 2 }: JsonViewProps): JSX.Element {
  const { expandPaths, count } = useMemo(() => computeMatches(value, search), [value, search]);
  const trimmedSearch = search.trim();

  // Global open baseline + per-node explicit toggles. "Expand/Collapse all"
  // flips the baseline and clears the overrides (so a previously hand-collapsed
  // node re-opens under Expand all); a single toggle records an override that
  // wins over the baseline until the next Expand/Collapse all.
  const [mode, setMode] = useState<OpenMode>('default');
  const [overrides, setOverrides] = useState<Record<string, boolean>>({});
  const handleToggle = (path: string, nextOpen: boolean) =>
    setOverrides((prev) => ({ ...prev, [path]: nextOpen }));
  const setAll = (next: OpenMode) => {
    setMode(next);
    setOverrides({});
  };

  const showControls = isContainer(value); // nothing to expand/collapse on a primitive root
  const showSummary = trimmedSearch !== '';

  return (
    <div className="json-view">
      {(showControls || showSummary) && (
        <div className="json-controls">
          {showControls && (
            <>
              <button type="button" className="json-control" onClick={() => setAll('all')}>
                Expand all
              </button>
              <button type="button" className="json-control" onClick={() => setAll('none')}>
                Collapse all
              </button>
            </>
          )}
          {showSummary && (
            <span className="json-search-summary">
              {count > 0 ? `${count} match${count === 1 ? '' : 'es'}` : 'no matches'}
            </span>
          )}
        </div>
      )}
      <JsonNode
        value={value}
        depth={0}
        path=""
        defaultDepth={defaultDepth}
        expandPaths={expandPaths}
        search={search}
        mode={mode}
        overrides={overrides}
        onToggle={handleToggle}
      />
    </div>
  );
}
