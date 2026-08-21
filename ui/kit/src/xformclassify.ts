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

// VALUE_CHOICE_KEY: a FHIR value[x] choice key in either JSON encoding —
// `valueCoding` (a complex type, extensions on the value object) or
// `_valueInteger` (a primitive, extensions on the sibling object). The
// uppercase-led type name keeps real field names that merely begin with
// "value" (valueSet) out of the fold.
//
// HAND-MIRROR of tools/xmatrix/locus.go's valueChoiceKey + NormalizeDiffPath,
// including the scope condition. There is no CI tie across the language
// boundary; if you change one, change the other.
//
// ONE deliberate exception: the Go side ALSO folds QuestionnaireResponse's item
// recursion (item.item and item.answer.item both collapse onto item), and this
// side does not need to. LocusCovers matches a locus as a PREFIX, so extra
// nesting segments in the middle break it; suffixMatches compares a TAIL, so the
// same segments fall off the front and nested regions already match. A fold
// here would therefore be a harmless NO-OP rather than a fix: a recursion fold
// only removes redundant segments earlier in the chain, never the trailing
// segments suffixMatches actually compares, so the classification outcome is
// structurally invariant to whether the fold exists. (Confirmed directly: a
// scratch jsonKeysOf carrying the same fold left every case in
// xformclassify.test.ts, including the nested ones, unchanged.) Those nested
// cases pin the carried/rewritten classification and the dependency on
// suffixMatches — not the absence of a fold, which this comment records instead.
const VALUE_CHOICE_KEY = /^_?value[A-Z][a-zA-Z0-9]*$/;

// jsonKeysOf strips array indices from a region path, leaving only the
// object-key chain — array indices are always ignored for suffix matching —
// and folds an answer's value[x] key onto the class-level `value` segment so a
// LossEntry.Path naming ...item.answer.value.extension:<slice> can match a
// region whose actual JSON key is `_valueInteger` or `valueCoding` (the itemWeight
// extension is contexted to the answer's VALUE).
//
// The fold belongs HERE, on the observed region path, not in segmentsOf: the
// loss path already says `value`, and it is the region that carries the
// concrete key. Same side as the Go rule.
function jsonKeysOf(path: (string | number)[]): string[] {
  const keys = path.filter((p): p is string => typeof p === 'string');
  return keys.map((k, i) => (i > 0 && keys[i - 1] === 'answer' && VALUE_CHOICE_KEY.test(k) ? 'value' : k));
}

interface EnclosingResource {
  type: string;
  depth: number; // path prefix length at which the resource object sits
}

// enclosingResource: the NEAREST strict ancestor of `path` in `root` (the
// root itself included, at depth 0) that is a FHIR resource — a plain object
// carrying a string `resourceType`. A region's content belongs to that
// resource: the whole tree when `before`/`after` ARE the resource, the
// `entry[i].resource` when they are a Bundle, the contained resource when the
// region sits inside `contained[i]`. The walk stops at the first missing
// prefix, so an after-only region is resolved against the tree that has it.
function enclosingResource(root: unknown, path: (string | number)[]): EnclosingResource | undefined {
  let found: EnclosingResource | undefined;
  for (let i = 0; i < path.length; i++) {
    const at = getAtPath(root, path.slice(0, i));
    if (!at.exists) break;
    if (isPlainObject(at.value) && typeof at.value.resourceType === 'string') {
      found = { type: at.value.resourceType, depth: i };
    }
  }
  return found;
}

// suffixMatches anchors a loss entry to the region's enclosing resource and
// then requires the entry's remaining segments to be a TRUE suffix of the
// region's JSON-key chain RELATIVE to that resource.
//
// A LossEntry.Path is always resource-rooted, e.g.
// "QuestionnaireResponse.item.answer.extension": its first segment is a
// resource type, never a JSON key, and the rest name a location inside that
// resource. So the match has two halves, both load-bearing:
//
//  1. The resource type must equal the enclosing resource's `resourceType`,
//     in either tree. Without this, the stripped tail of a shallow entry —
//     `ClaimResponse.extension:<slice>` leaves the lone segment ['extension']
//     — claimed ANY region whose chain ended in `extension`, including a
//     QuestionnaireResponse's embedded in the same bundle (the mixed-bundle shape).
//     A tree with no `resourceType` anywhere has no enclosing resource and
//     is claimed by nothing; every real payload the inspector sees is a FHIR
//     resource and carries the key.
//  2. The chain from the resource down to the region must be at least as
//     long as the stripped entry, and the entry must line up with its tail
//     in order. The length floor stops a shallow region (a root-level
//     `extension`) from satisfying a deep entry ("...item.answer.extension")
//     on the strength of a short common tail. Measuring from the resource,
//     not the tree root, is what lets a Bundle-wrapped region still match —
//     the `entry.resource` prefix is outside the resource and outside the
//     compared chain — while keeping the floor honest for the resource's own
//     root-level keys.
function suffixMatches(path: (string | number)[], lossPath: string, before: unknown, after: unknown): boolean {
  const segments = segmentsOf(lossPath);
  const resourceType = segments[0];
  const want = segments.slice(1);
  if (want.length === 0) return false;
  for (const tree of [before, after]) {
    const enclosing = enclosingResource(tree, path);
    if (enclosing === undefined || enclosing.type !== resourceType) continue;
    const have = jsonKeysOf(path.slice(enclosing.depth));
    if (want.length > have.length) continue;
    const haveTail = have.slice(have.length - want.length);
    if (want.every((w, i) => w === haveTail[i])) return true;
  }
  return false;
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
  if (carriedPaths.some((p) => suffixMatches(path, p, before, after))) {
    return { cls: 'carried', side: 'both' };
  }

  const afterOnly = !beforeAt.exists && afterAt.exists;
  if (afterOnly) {
    const synthesizedPaths = lossReports.flatMap((r) => (r.synthesized ?? []).map((e) => e.path));
    if (synthesizedPaths.some((p) => suffixMatches(path, p, before, after))) {
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
