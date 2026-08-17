// xformclassify.ts — the loss-report-keyed transformation diff: pure
// classification over two already-parsed JSON trees (the payload a bridged
// leg sent vs. what it built before the transform), keyed to the same loss
// reports TransformCard already renders (StepDetail.tsx's
// parseLossReports). No fetching, no React — computeXformDiff is a pure
// function so the classification rules can be pinned by table-driven unit
// tests independent of rendering.
import type { BridgingLossReport } from './types';

// SHN_CARRIED_CONTENT_EXT_URL mirrors sdk/carry.go's CarriedContentExtURL
// byte-for-byte. Same posture as StepDetail.tsx's SHN_LOSS_REPORT_EXT_URL —
// ui/kit is a separate module pinned against published shn-gateway/shn-sdk
// releases (kit/go.mod) and cannot import the Go sdk to read the constant
// live, so this is a literal copy: if sdk/carry.go's CarriedContentExtURL
// ever changes, this string goes stale silently (no cross-module CI tie),
// and wrapper detection below just stops matching — it never throws.
export const SHN_CARRIED_CONTENT_EXT_URL =
  'http://smarthealth.network/fhir/StructureDefinition/shn-carried-content';

export type RegionClass = 'carried' | 'synthesized' | 'rewritten';

export interface DiffRegion {
  path: (string | number)[];
  side: 'before' | 'after' | 'both';
  cls: RegionClass;
}

export interface XformDiffResult {
  // byteIdentical: string equality of rawBefore/rawAfter — the strongest
  // claim this function can make, but a claim about the CALLER's two
  // JSON.stringify outputs, not about wire bytes: no caller here ever holds
  // onto the original captured/sent text past JSON.parse, so `raw` is
  // always a re-stringification, never the bytes that actually crossed
  // (or, for a demonstration, never crossed) the network.
  byteIdentical: boolean;
  regions: DiffRegion[]; // maximal differing subtrees, classified
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) return false;
    return a.every((v, i) => deepEqual(v, b[i]));
  }
  if (isPlainObject(a) && isPlainObject(b)) {
    const aKeys = Object.keys(a);
    const bKeys = Object.keys(b);
    if (aKeys.length !== bKeys.length) return false;
    return aKeys.every((k) => Object.prototype.hasOwnProperty.call(b, k) && deepEqual(a[k], b[k]));
  }
  return false;
}

// diffPaths walks two aligned JSON trees and returns the MAXIMAL differing
// region paths — not one region per leaf. Object/array children are
// recursed into (and so get their own, more specific, region) only when
// both sides are the same alignable container shape (both plain objects,
// or both arrays of equal length) AND the key/index exists on both sides;
// anything else — a key present on only one side, a type mismatch, an
// array whose length changed (an insertion/deletion shifts every
// subsequent index, so element-wise alignment past that point is
// meaningless), or two differing primitives — collapses to a single region
// covering the whole subtree at that path.
function diffPaths(a: unknown, b: unknown, path: (string | number)[]): (string | number)[][] {
  if (deepEqual(a, b)) return [];

  if (isPlainObject(a) && isPlainObject(b)) {
    const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
    const regions: (string | number)[][] = [];
    for (const k of keys) {
      const inA = Object.prototype.hasOwnProperty.call(a, k);
      const inB = Object.prototype.hasOwnProperty.call(b, k);
      if (!inA || !inB) {
        if (!deepEqual(a[k], b[k])) regions.push([...path, k]);
        continue;
      }
      regions.push(...diffPaths(a[k], b[k], [...path, k]));
    }
    return regions;
  }

  if (Array.isArray(a) && Array.isArray(b) && a.length === b.length) {
    const regions: (string | number)[][] = [];
    for (let i = 0; i < a.length; i++) {
      regions.push(...diffPaths(a[i], b[i], [...path, i]));
    }
    return regions;
  }

  // Type mismatch, array-length mismatch, or two differing primitives —
  // the whole subtree at this path is one maximal region.
  return [path];
}

interface PathLookup {
  exists: boolean;
  value: unknown;
}

function getAtPath(root: unknown, path: (string | number)[]): PathLookup {
  let cur: unknown = root;
  for (const seg of path) {
    if (typeof seg === 'number') {
      if (!Array.isArray(cur) || seg < 0 || seg >= cur.length) return { exists: false, value: undefined };
      cur = cur[seg];
    } else {
      if (!isPlainObject(cur) || !Object.prototype.hasOwnProperty.call(cur, seg)) {
        return { exists: false, value: undefined };
      }
      cur = cur[seg];
    }
  }
  return { exists: true, value: cur };
}

// containsCarriedExtension: true if `v` IS an extension object whose url is
// the shn-carried-content wrapper, or contains one anywhere in its subtree.
function containsCarriedExtension(v: unknown): boolean {
  if (isPlainObject(v)) {
    if (v.url === SHN_CARRIED_CONTENT_EXT_URL) return true;
    return Object.values(v).some(containsCarriedExtension);
  }
  if (Array.isArray(v)) return v.some(containsCarriedExtension);
  return false;
}

// isCarriedWrapper: true if `v` IS an extension object whose url is the
// shn-carried-content wrapper — a single-level check, deliberately never
// recursive (unlike containsCarriedExtension below): it is used to walk
// STRICT ANCESTORS of a region's path, and recursing into an ancestor's
// full subtree there would risk finding an unrelated wrapper sitting on
// some sibling branch and misclassifying this region as carried on that
// basis alone.
function isCarriedWrapper(v: unknown): boolean {
  return isPlainObject(v) && v.url === SHN_CARRIED_CONTENT_EXT_URL;
}

// ancestorHasCarriedWrapper: true if any STRICT ancestor of `path` (i.e. any
// proper prefix, not the path itself) IS the carried-content wrapper object.
//
// Exists because diffPaths' aligned-object recursion has no special case for
// the wrapper shape: when the "before" side holds a plain FHIR extension and
// the "after" side holds the wrapper at the SAME array index, both are plain
// objects, so the walk recurses INTO the wrapper instead of collapsing the
// whole index into one region — the wrapper's own `url` field then ends up
// as the VALUE of a child region (a bare string, not an object
// containsCarriedExtension can recognize) rather than sitting at or below
// the region's own root. containsCarriedExtension only ever looks at a
// region's own value and its descendants; this walks the other direction —
// up the path — to catch exactly that case, while the true-suffix loss-path
// rule below still carries the non-wrapper cases (e.g. the restored side,
// where the content lands back in its native, non-wrapped slot).
function ancestorHasCarriedWrapper(root: unknown, path: (string | number)[]): boolean {
  for (let i = 0; i < path.length; i++) {
    const at = getAtPath(root, path.slice(0, i));
    if (isCarriedWrapper(at.value)) return true;
  }
  return false;
}

// segmentsOf splits a LossEntry.Path on '.', reducing a trailing
// `extension:<sliceName>` segment to the bare JSON key `extension` — slice
// names never appear as JSON keys, so this is a deliberate
// over-approximation: it matches the WHOLE extension array element, not the
// specific sliced entry.
function segmentsOf(lossPath: string): string[] {
  return lossPath.split('.').map((seg) => {
    const colon = seg.indexOf(':');
    return colon === -1 ? seg : seg.slice(0, colon);
  });
}

// jsonKeysOf strips array indices from a region path, leaving only the
// object-key chain — array indices are always ignored for suffix matching.
function jsonKeysOf(path: (string | number)[]): string[] {
  return path.filter((p): p is string => typeof p === 'string');
}

// suffixMatches drops the loss entry's leading resource-type segment — a
// LossEntry.Path is always resource-rooted, e.g.
// "QuestionnaireResponse.item.answer.extension", but `before`/`after` here
// ARE the resource itself, so the resource-type name never appears as an
// actual JSON key — and then requires what's left to be a TRUE suffix of
// the region's JSON-key chain: the region path must be AT LEAST as long as
// the (stripped) loss path, and every remaining loss segment must line up
// with the region path's tail in order.
//
// The length floor is load-bearing, not incidental: without it, a shallow
// region (e.g. a root-level `extension` key) would satisfy a deep loss
// entry (e.g. "...item.answer.extension") merely because SOME short common
// tail exists — silently mislabeling an unrelated root-level rewrite as
// carried. Requiring the full stripped loss path to fit inside the region
// path rejects that, while still matching a Bundle-wrapped resource whose
// region path is DEEPER than the loss path — the extra `entry.resource`-
// style prefix segments simply fall outside the compared tail.
function suffixMatches(path: (string | number)[], lossPath: string): boolean {
  const want = segmentsOf(lossPath).slice(1);
  if (want.length === 0) return false;
  const have = jsonKeysOf(path);
  if (want.length > have.length) return false;
  const haveTail = have.slice(have.length - want.length);
  return want.every((w, i) => w === haveTail[i]);
}

function classify(
  path: (string | number)[],
  before: unknown,
  after: unknown,
  lossReports: BridgingLossReport[],
): { cls: RegionClass; side: DiffRegion['side'] } {
  const beforeAt = getAtPath(before, path);
  const afterAt = getAtPath(after, path);

  if (
    containsCarriedExtension(beforeAt.value) ||
    containsCarriedExtension(afterAt.value) ||
    ancestorHasCarriedWrapper(before, path) ||
    ancestorHasCarriedWrapper(after, path)
  ) {
    return { cls: 'carried', side: 'both' };
  }

  const carriedPaths = lossReports.flatMap((r) => (r.carried ?? []).map((e) => e.path));
  if (carriedPaths.some((p) => suffixMatches(path, p))) {
    return { cls: 'carried', side: 'both' };
  }

  const afterOnly = !beforeAt.exists && afterAt.exists;
  if (afterOnly) {
    const synthesizedPaths = lossReports.flatMap((r) => (r.synthesized ?? []).map((e) => e.path));
    if (synthesizedPaths.some((p) => suffixMatches(path, p))) {
      return { cls: 'synthesized', side: 'after' };
    }
  }

  return { cls: 'rewritten', side: 'both' };
}

// computeXformDiff is pure and UI-side: `before`/`after` are the already-
// parsed payloads (the pair a bridged leg built vs. sent), `rawBefore`/
// `rawAfter` are JSON.stringify of that SAME pair — every caller re-derives
// them from the parsed value, never from bytes it independently held onto —
// used for the strongest identity claim this function can honestly make
// (string equality, not a structural walk that could be fooled by e.g.
// key-order-insensitive comparison), and `lossReports` the same
// BridgingLossReport[] TransformCard already parses from the leg's
// Provenance.
export function computeXformDiff(
  before: unknown,
  after: unknown,
  rawBefore: string,
  rawAfter: string,
  lossReports: BridgingLossReport[],
): XformDiffResult {
  if (rawBefore === rawAfter) return { byteIdentical: true, regions: [] };

  const regions = diffPaths(before, after, []).map((path) => {
    const { cls, side } = classify(path, before, after, lossReports);
    return { path, side, cls };
  });
  return { byteIdentical: false, regions };
}
