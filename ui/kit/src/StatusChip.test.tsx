import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StatusChip } from './StatusChip';

describe('StatusChip', () => {
  it('passed renders the "Passed" text, the pass chip class, and the tick icon', () => {
    render(<StatusChip state="passed" />);

    const chip = screen.getByText('Passed');
    expect(chip.className).toMatch(/\bchip\b/);
    expect(chip.className).toMatch(/\bpass\b/);
    expect(chip.querySelector('svg.ic.tick')).not.toBeNull();
    expect(chip.querySelector('svg.ic.cross')).toBeNull();
  });

  it('failed renders the "Failed" text, the fail chip class, and the cross icon', () => {
    render(<StatusChip state="failed" />);

    const chip = screen.getByText('Failed');
    expect(chip.className).toMatch(/\bchip\b/);
    expect(chip.className).toMatch(/\bfail\b/);
    expect(chip.querySelector('svg.ic.cross')).not.toBeNull();
    expect(chip.querySelector('svg.ic.tick')).toBeNull();
  });
});
