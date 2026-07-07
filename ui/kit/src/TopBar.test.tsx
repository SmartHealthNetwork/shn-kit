import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { TopBar } from './TopBar';
import { LANE_LABELS } from './ucmeta';

describe('TopBar', () => {
  it('renders the wordmark, a connection dot reflecting sseState, the embedded mode switch (active lane), and the signed-in identity', () => {
    render(
      <TopBar
        lane="conformant"
        onLane={vi.fn()}
        sseState="open"
        children={[]}
        identity={{ email: 'linda.johansson@example.org' }}
      />,
    );

    expect(screen.getByText('SHN Kit')).toBeDefined();

    const dot = document.querySelector('.connection-dot');
    expect(dot).not.toBeNull();
    expect(dot?.className).toContain('connection-open');

    // ModeSwitch is embedded — its tablist renders, the active lane
    // (conformant) carries aria-current.
    expect(screen.getByRole('tablist')).toBeDefined();
    expect(
      screen.getByRole('tab', { name: LANE_LABELS.conformant.short }).getAttribute('aria-current'),
    ).toBe('true');

    expect(screen.getByText(/linda\.johansson@example\.org/)).toBeDefined();
  });

  it('reflects a non-open sseState in the connection dot class', () => {
    render(
      <TopBar
        lane="ehr"
        onLane={vi.fn()}
        sseState="reconnecting"
        children={[]}
        identity={{}}
      />,
    );
    const dot = document.querySelector('.connection-dot');
    expect(dot?.className).toContain('connection-reconnecting');
  });

  it('renders the updateBanner slot when provided, and omits identity when no email is signed in', () => {
    render(
      <TopBar
        lane="ehr"
        onLane={vi.fn()}
        sseState="connecting"
        children={[]}
        identity={{}}
        updateBanner={<div className="update-banner">A new version of the Kit is available.</div>}
      />,
    );

    expect(screen.getByText('A new version of the Kit is available.')).toBeDefined();
    expect(document.querySelector('.who')).toBeNull();
  });
});
