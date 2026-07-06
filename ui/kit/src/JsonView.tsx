// JsonView.tsx — hand-rolled collapsible/searchable JSON tree (no new npm
// dependency). Renders any `unknown` value as a tree: containers
// (objects/arrays) collapse beyond `defaultDepth`, primitives render plainly.
// Search is a case-insensitive substring match over keys + primitive values;
// matches are wrapped in <mark className="json-match"> and every ancestor
// path of a match is force-expanded, even past defaultDepth, so a hit is
// always visible without the caller hand-driving toggles.
import { useMemo, useState } from 'react';
import type { JSX } from 'react';

export interface JsonViewProps {
  value: unknown;
  search?: string;
  defaultDepth?: number;
}

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
}

function JsonNode({ label, value, depth, path, defaultDepth, expandPaths, search }: JsonNodeProps): JSX.Element {
  // Always called (rules of hooks) — unused on the leaf branch below, but a
  // node's container/leaf-ness is stable for a given tree position so this
  // never actually toggles branches across re-renders in practice.
  const [manualOpen, setManualOpen] = useState<boolean | undefined>(undefined);
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
  const defaultOpen = depth < defaultDepth;
  const open = manualOpen !== undefined ? manualOpen || forcedOpen : defaultOpen || forcedOpen;
  const isArray = Array.isArray(value);
  const entries = containerEntries(value);

  return (
    <div className="json-node json-container" data-path={path}>
      <button
        type="button"
        className="json-toggle"
        aria-expanded={open}
        aria-label={open ? `collapse ${label ?? 'root'}` : `expand ${label ?? 'root'}`}
        onClick={() => setManualOpen(!open)}
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

  return (
    <div className="json-view">
      {trimmedSearch !== '' && (
        <div className="json-search-summary">
          {count > 0 ? `${count} match${count === 1 ? '' : 'es'}` : 'no matches'}
        </div>
      )}
      <JsonNode
        value={value}
        depth={0}
        path=""
        defaultDepth={defaultDepth}
        expandPaths={expandPaths}
        search={search}
      />
    </div>
  );
}
