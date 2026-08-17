// DemoChips.test.tsx — LocalDemoChip (species marker) + DemoResultChip
// (kind-keyed verdict). Double-assert idiom's rendered half for
// LOCAL_DEMO_CHIP/DEMO_RESULT_REFUSAL/DEMO_RESULT_CARRY — the literal half
// lives in bridgingmeta.test.ts.
import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DemoResultChip, LocalDemoChip } from './DemoChips';
import { DEMO_RESULT_CARRY, DEMO_RESULT_REFUSAL, LOCAL_DEMO_CHIP } from './bridgingmeta';

describe('LocalDemoChip', () => {
  it('renders the pinned LOCAL_DEMO_CHIP text as a dashed, neutral chip', () => {
    render(<LocalDemoChip />);
    const chip = screen.getByText(LOCAL_DEMO_CHIP);
    expect(chip.className).toMatch(/\bchip\b/);
    expect(chip.className).toMatch(/\blocal-demo\b/);
    // Neither the pass nor fail treatment — this chip marks the species,
    // never an outcome.
    expect(chip.className).not.toMatch(/\bpass\b/);
    expect(chip.className).not.toMatch(/\bfail\b/);
  });
});

describe('DemoResultChip', () => {
  it('kind="refusal-engine" renders the pinned DEMO_RESULT_REFUSAL text on the pass-green chip treatment', () => {
    render(<DemoResultChip kind="refusal-engine" />);
    const chip = screen.getByText(DEMO_RESULT_REFUSAL);
    expect(chip.className).toMatch(/\bchip\b/);
    expect(chip.className).toMatch(/\bpass\b/);
  });

  it('kind="carry-engine" renders the pinned DEMO_RESULT_CARRY text', () => {
    render(<DemoResultChip kind="carry-engine" />);
    expect(screen.getByText(DEMO_RESULT_CARRY)).toBeDefined();
  });
});
