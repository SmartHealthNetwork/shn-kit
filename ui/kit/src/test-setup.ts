import '@testing-library/jest-dom';
import { vi } from 'vitest';

// jsdom does not implement matchMedia. FlowEdges/FlowMap read
// `window.matchMedia?.('(prefers-reduced-motion: reduce)')` to decide the
// instant (no-rAF) animation path; a bare `?.()` on `undefined` already
// short-circuits safely (existing FlowEdges tests pass without this), but a
// real stub lets individual tests flip `matches` to force the reduced-motion
// path deterministically (`vi.spyOn(window, 'matchMedia').mockReturnValue(...)`
// requires the property to already exist as a function). Defaults to
// `matches: false` — same as the unstubbed `undefined` behavior — so this is
// a no-op for every test that doesn't opt in.
if (!window.matchMedia) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
}
