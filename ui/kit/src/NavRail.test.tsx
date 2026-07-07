import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { NavRail } from './NavRail';
import type { ChildStatus } from './types';

function children(): ChildStatus[] {
  return [
    { name: 'gateway', state: 'ready', detail: 'ok', pid: 1, restarts: 0 },
    { name: 'validator', state: 'ready', detail: 'ok', pid: 2, restarts: 0 },
  ];
}

describe('NavRail', () => {
  it('renders the four destinations', () => {
    render(<NavRail nav="scenarios" onNav={() => {}} children={children()} />);
    expect(screen.getByRole('button', { name: /scenarios/i })).toBeDefined();
    expect(screen.getByRole('button', { name: /run history/i })).toBeDefined();
    expect(screen.getByRole('button', { name: /bring your own/i })).toBeDefined();
    expect(screen.getByRole('button', { name: /^systems$/i })).toBeDefined();
  });

  it('marks the active destination with aria-current="true", and non-active items carry none', () => {
    render(<NavRail nav="scenarios" onNav={() => {}} children={children()} />);
    expect(screen.getByRole('button', { name: /scenarios/i }).getAttribute('aria-current')).toBe(
      'true',
    );
    expect(
      screen.getByRole('button', { name: /^systems$/i }).getAttribute('aria-current'),
    ).toBeNull();
  });

  it('fires onNav with the clicked destination', () => {
    const onNav = vi.fn();
    render(<NavRail nav="scenarios" onNav={onNav} children={children()} />);
    fireEvent.click(screen.getByRole('button', { name: /^systems$/i }));
    expect(onNav).toHaveBeenCalledWith('systems');
  });

  it('shows the health line derived from the child statuses in the foot', () => {
    // Two children, both ready → deriveHealth → "All systems ready".
    render(<NavRail nav="scenarios" onNav={() => {}} children={children()} />);
    expect(screen.getByText('All systems ready')).toBeDefined();
  });

  it('reflects a degraded child in the foot health line', () => {
    const degraded: ChildStatus[] = [
      { name: 'gateway', state: 'ready', detail: 'ok', pid: 1, restarts: 0 },
      { name: 'validator', state: 'exited', detail: 'crashed', pid: 0, restarts: 2 },
    ];
    render(<NavRail nav="systems" onNav={() => {}} children={degraded} />);
    expect(screen.getByText(/degraded/i)).toBeDefined();
  });

  it('with no child statuses at all, the foot reads "No status" with a neutral (unknown) dot — not the ready dot', () => {
    render(<NavRail nav="scenarios" onNav={() => {}} children={[]} />);
    expect(screen.getByText('No status')).toBeDefined();
    const dot = document.querySelector('.nav .foot .dot');
    expect(dot?.getAttribute('data-level')).toBe('unknown');
    expect(dot?.getAttribute('data-level')).not.toBe('ready');
  });
});
