// FlowEdges.tsx — the SVG directional edge overlay for FlowMap's node rail.
// Ported from the approved interactive mockup
// (`buildEdges`/`pulseAlong`) into React. Shown-never-faked: every drawn
// out/back path pair lights independently off `EdgeStates` (FlowMap's
// edgeStatesFor pure derivation) — an open leg shows an outbound arrow and
// nothing back. The ehr lane's un-instrumented-gateway fallback
// (`src: 'static'`) draws ONE dashed path plus the `.src-label` honesty
// caption instead of a lying lit pair. Arrows are PERSISTENT (Bo's decision
// 2 in the mockup notes) — always
// attached via CSS `marker-end`, not only on selection.
import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type JSX,
  type RefObject,
} from 'react';
import type { EdgeKey, EdgeStates } from './FlowMap';

export interface FlowEdgesHandle {
  pulse(edge: EdgeKey, dir: 'out' | 'back'): Promise<void>;
}

export interface FlowEdgesProps {
  edges: EdgeStates;
  selectedEdge?: EdgeKey; // sel/dim treatment; set by FlowMap when a step is selected
  railRef: RefObject<HTMLDivElement | null>; // the .flow container to measure
}

const NS = 'http://www.w3.org/2000/svg';

// A drawn path key: 'src.out' | 'src.back' | 'src.static' | 'val.out' | …
type PathKey = `${EdgeKey}.${'out' | 'back' | 'static'}`;

interface Geometry {
  paths: Partial<Record<PathKey, string>>; // key -> `d` attribute
  viewBox: string;
  srcLabel: { left: number; top: number } | null; // null when not the static ehr fallback
}

const EMPTY_GEOMETRY: Geometry = { paths: {}, viewBox: '0 0 0 0', srcLabel: null };

function rectRelativeTo(rail: DOMRect, el: Element | null) {
  const r = el?.getBoundingClientRect() ?? new DOMRect(0, 0, 0, 0);
  const x = r.left - rail.left;
  const y = r.top - rail.top;
  return { x, y, w: r.width, h: r.height, cy: y + r.height / 2, bottom: y + r.height };
}

// buildGeometry ports the mockup's buildEdges() path-string math exactly:
// the adjacency pair sits at x = provider.left + 78 (out) / +90 (back); the
// leg pair routes down the left gutter (x = 16 out / 8 back) from the
// gateway's left edge into the hub's left edge.
function buildGeometry(rail: HTMLDivElement, isStaticSrc: boolean): Geometry {
  const railRect = rail.getBoundingClientRect();
  const prov = rectRelativeTo(railRect, rail.querySelector('[data-node="provider"]'));
  const gw = rectRelativeTo(railRect, rail.querySelector('[data-node="gateway"]'));
  const val = rectRelativeTo(railRect, rail.querySelector('[data-node="validator"]'));
  const hub = rectRelativeTo(railRect, rail.querySelector('[data-node="hub"]'));

  const ax = prov.x + 78;
  const bx = ax + 12;

  const paths: Partial<Record<PathKey, string>> = {};
  let srcLabel: Geometry['srcLabel'] = null;

  if (isStaticSrc) {
    paths['src.static'] = `M ${ax + 6} ${prov.bottom + 2} V ${gw.y - 2}`;
    srcLabel = {
      left: ax + 18,
      top: prov.bottom + (gw.y - prov.bottom) / 2 - 10,
    };
  } else {
    paths['src.out'] = `M ${ax} ${prov.bottom + 2} V ${gw.y - 2}`;
    paths['src.back'] = `M ${bx} ${gw.y - 2} V ${prov.bottom + 2}`;
  }

  paths['val.out'] = `M ${ax} ${gw.bottom + 2} V ${val.y - 2}`;
  paths['val.back'] = `M ${bx} ${val.y - 2} V ${gw.bottom + 2}`;

  paths['leg.out'] = `M ${gw.x} ${gw.cy - 5} H 16 V ${hub.cy - 5} H ${hub.x - 2}`;
  paths['leg.back'] = `M ${hub.x - 2} ${hub.cy + 5} H 8 V ${gw.cy + 5} H ${gw.x - 2}`;

  return { paths, viewBox: `0 0 ${railRect.width} ${railRect.height}`, srcLabel };
}

function classNames(...parts: Array<string | false | undefined>): string {
  return parts.filter((p): p is string => Boolean(p)).join(' ');
}

function prefersReducedMotion(): boolean {
  return Boolean(window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches);
}

export const FlowEdges = forwardRef<FlowEdgesHandle, FlowEdgesProps>(function FlowEdges(
  { edges, selectedEdge, railRef },
  ref,
): JSX.Element {
  const [geometry, setGeometry] = useState<Geometry>(EMPTY_GEOMETRY);
  const pulseLayerRef = useRef<SVGGElement | null>(null);
  const pathRefs = useRef<Partial<Record<PathKey, SVGPathElement | null>>>({});
  // mountedRef bounds pulse()'s rAF walk on unmount: the walk is already
  // bounded + detached-DOM-safe (getPointAtLength on a removed-but-not-
  // destroyed SVGPathElement doesn't throw), so this isn't a
  // correctness fix — it just stops the tick loop from spinning frames after
  // the component's gone and resolves the promise so no caller is left
  // awaiting forever.
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const isStaticSrc = edges.src === 'static';

  const recompute = useCallback(() => {
    const rail = railRef.current;
    if (!rail) return;
    setGeometry(buildGeometry(rail, isStaticSrc));
  }, [railRef, isStaticSrc]);

  // railRef is owned by an ANCESTOR host node (FlowMap's `.flow` div wraps
  // this component) — React attaches a host ref to its fiber only after
  // that fiber's whole subtree has run its own layout effects, so on the
  // very first commit `railRef.current` is still null here (confirmed live:
  // null across both React 18 StrictMode dev double-invocations, becoming
  // non-null only once the browser's next frame runs). jsdom's unit tests
  // never hit this because they hand-set `railRef.current` to a detached
  // div BEFORE calling render(), sidestepping real ref attachment — hence
  // green tests alongside an empty overlay in a real browser. Retry on the
  // next animation frame until the rail is actually attached, then do the
  // one-time recompute + ResizeObserver setup exactly as before.
  useLayoutEffect(() => {
    let ro: ResizeObserver | undefined;
    let rafId: number | undefined;
    let cancelled = false;

    const attach = () => {
      const rail = railRef.current;
      if (!rail) {
        if (!cancelled) rafId = requestAnimationFrame(attach);
        return;
      }
      recompute();
      if (typeof ResizeObserver === 'undefined') return;
      ro = new ResizeObserver(() => recompute());
      ro.observe(rail);
    };

    attach();

    return () => {
      cancelled = true;
      if (rafId !== undefined) cancelAnimationFrame(rafId);
      ro?.disconnect();
    };
  }, [recompute, railRef]);

  const pulse = useCallback((edge: EdgeKey, dir: 'out' | 'back'): Promise<void> => {
    if (prefersReducedMotion()) return Promise.resolve();
    const key: PathKey = `${edge}.${dir}`;
    const path = pathRefs.current[key];
    const pulseLayer = pulseLayerRef.current;
    if (!path || !pulseLayer || typeof path.getTotalLength !== 'function') return Promise.resolve();
    const len = path.getTotalLength();
    if (!len) return Promise.resolve();

    const dur = Math.max(420, len * 2.2);
    const fadeOut = edge === 'leg' && dir === 'out';
    const fadeIn = edge === 'leg' && dir === 'back';

    return new Promise((resolve) => {
      const halo = document.createElementNS(NS, 'circle');
      halo.setAttribute('r', '8');
      halo.setAttribute('class', 'pulse-halo');
      const dot = document.createElementNS(NS, 'circle');
      dot.setAttribute('r', '4');
      dot.setAttribute('class', 'pulse');
      pulseLayer.appendChild(halo);
      pulseLayer.appendChild(dot);

      const t0 = performance.now();
      const tick = (t: number) => {
        if (!mountedRef.current) {
          halo.remove();
          dot.remove();
          resolve();
          return;
        }
        const p = Math.min(1, (t - t0) / dur);
        const pt = path.getPointAtLength(p * len);
        let op = 1;
        if (fadeOut && p > 0.82) op = (1 - p) / 0.18;
        if (fadeIn && p < 0.18) op = p / 0.18;
        for (const el of [halo, dot]) {
          el.setAttribute('cx', String(pt.x));
          el.setAttribute('cy', String(pt.y));
          el.setAttribute('opacity', String(op));
        }
        if (p < 1) {
          requestAnimationFrame(tick);
        } else {
          halo.remove();
          dot.remove();
          resolve();
        }
      };
      requestAnimationFrame(tick);
    });
  }, []);

  useImperativeHandle(ref, () => ({ pulse }), [pulse]);

  const edgeClass = (edge: EdgeKey, dir: 'out' | 'back' | 'static'): string => {
    const state = edges[edge];
    const lit = state !== 'static' && (dir === 'out' ? state.out : dir === 'back' ? state.back : false);
    // The static (explicitly-unobserved) dashed fallback never takes the
    // `sel` treatment: it isn't a real observed edge, so highlighting it as
    // "selected" would dress up a seeded-read placeholder as a live selection.
    const isSel = selectedEdge === edge && dir !== 'static';
    const isDim = selectedEdge !== undefined && selectedEdge !== edge;
    return classNames('edge', dir === 'static' && 'static', lit && 'lit', isSel && 'sel', isDim && 'dim');
  };

  const setPathRef = (key: PathKey) => (el: SVGPathElement | null) => {
    pathRefs.current[key] = el;
  };

  // Always mounts the path element, even before the rail's first real
  // recompute() lands (see the useLayoutEffect comment above: on the very
  // first commit railRef.current is still null, so geometry starts empty and
  // only fills in on a later animation frame). An un-measured path just
  // draws nothing (`d=""`, a no-op path) for that one interval — the sel/dim
  // selection classes below are driven by the `selectedEdge` prop directly,
  // not by geometry, so they must be selectable in the DOM immediately.
  const renderPath = (edge: EdgeKey, dir: 'out' | 'back' | 'static') => {
    const key: PathKey = `${edge}.${dir}`;
    const d = geometry.paths[key] ?? '';
    return (
      <path
        key={key}
        ref={setPathRef(key)}
        className={edgeClass(edge, dir)}
        data-edge={edge}
        data-dir={dir}
        d={d}
      />
    );
  };

  return (
    <>
      <svg className="flow-edges" aria-hidden="true" viewBox={geometry.viewBox}>
        <defs>
          <marker id="arr-base" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
            <path d="M0,0 L8,4 L0,8 z" fill="var(--edge)" />
          </marker>
          <marker id="arr-lit" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
            <path d="M0,0 L8,4 L0,8 z" fill="var(--edge-lit)" />
          </marker>
          <marker id="arr-sel" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
            <path d="M0,0 L8,4 L0,8 z" fill="var(--accent)" />
          </marker>
        </defs>
        <g>
          {isStaticSrc ? renderPath('src', 'static') : (
            <>
              {renderPath('src', 'out')}
              {renderPath('src', 'back')}
            </>
          )}
          {renderPath('val', 'out')}
          {renderPath('val', 'back')}
          {renderPath('leg', 'out')}
          {renderPath('leg', 'back')}
        </g>
        <g ref={pulseLayerRef} />
      </svg>
      {isStaticSrc && geometry.srcLabel && (
        <div
          className="src-label"
          style={{ left: geometry.srcLabel.left, top: geometry.srcLabel.top }}
        >
          seeded read — not observed
        </div>
      )}
    </>
  );
});

export default FlowEdges;
