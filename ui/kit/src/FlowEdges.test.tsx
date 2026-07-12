import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/react';
import { createRef } from 'react';
import { FlowEdges } from './FlowEdges';
import type { EdgeStates } from './FlowMap';

const railRef = { current: document.createElement('div') };

function edgePath(edge: string, dir: string): SVGPathElement | null {
  return document.querySelector(`path[data-edge="${edge}"][data-dir="${dir}"]`);
}

describe('FlowEdges', () => {
  it('renders paired out/back paths per edge, lit independently', () => {
    const edges: EdgeStates = {
      src: { out: true, back: false },
      val: { out: true, back: true },
      leg: { out: true, back: false },
    };
    render(<FlowEdges edges={edges} railRef={railRef} />);
    expect(edgePath('src', 'out')?.classList.contains('lit')).toBe(true);
    expect(edgePath('src', 'back')?.classList.contains('lit')).toBe(false);
    expect(edgePath('leg', 'out')?.classList.contains('lit')).toBe(true);
    expect(edgePath('leg', 'back')?.classList.contains('lit')).toBe(false);
    expect(edgePath('val', 'back')?.classList.contains('lit')).toBe(true);
  });

  it('ehr static fallback renders ONE dashed static path + the honesty caption', () => {
    const edges: EdgeStates = { src: 'static', val: { out: false, back: false }, leg: { out: false, back: false } };
    render(<FlowEdges edges={edges} railRef={railRef} />);
    expect(edgePath('src', 'static')?.classList.contains('static')).toBe(true);
    expect(edgePath('src', 'out')).toBeNull();
    expect(document.querySelector('.src-label')?.textContent).toBe('seeded read — not observed');
  });

  it('selectedEdge gets sel, every other path gets dim (static edge included)', () => {
    const edges: EdgeStates = { src: { out: true, back: true }, val: { out: true, back: true }, leg: { out: true, back: true } };
    render(<FlowEdges edges={edges} selectedEdge="leg" railRef={railRef} />);
    expect(edgePath('leg', 'out')?.classList.contains('sel')).toBe(true);
    expect(edgePath('leg', 'back')?.classList.contains('sel')).toBe(true);
    expect(edgePath('val', 'out')?.classList.contains('dim')).toBe(true);
    expect(edgePath('src', 'back')?.classList.contains('dim')).toBe(true);
  });

  it('the static (unobserved) dashed edge never takes sel, even when it IS the selected edge', () => {
    const edges: EdgeStates = { src: 'static', val: { out: false, back: false }, leg: { out: false, back: false } };
    render(<FlowEdges edges={edges} selectedEdge="src" railRef={railRef} />);
    expect(edgePath('src', 'static')?.classList.contains('sel')).toBe(false);
  });

  it('pulse() resolves without error in jsdom (no layout)', async () => {
    const ref = createRef<{ pulse(e: 'src' | 'val' | 'leg', d: 'out' | 'back'): Promise<void> }>();
    const edges: EdgeStates = { src: { out: true, back: true }, val: { out: true, back: true }, leg: { out: true, back: true } };
    render(<FlowEdges ref={ref} edges={edges} railRef={railRef} />);
    await ref.current?.pulse('leg', 'out');
  });
});
