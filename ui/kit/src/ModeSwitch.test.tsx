import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ModeSwitch } from './ModeSwitch';
import { LANE_LABELS, LANES, visibleLanes } from './ucmeta';

describe('ModeSwitch', () => {
  it('renders both lanes with the concise labels; the active lane carries both aria-selected and aria-current; clicking the other lane calls onLane', async () => {
    const onLane = vi.fn();
    render(<ModeSwitch lane="ehr" onLane={onLane} />);

    expect(screen.getByRole('tablist')).toBeDefined();

    const ehrTab = screen.getByRole('tab', { name: LANE_LABELS.ehr.short });
    const conformantTab = screen.getByRole('tab', { name: LANE_LABELS.conformant.short });

    expect(ehrTab.getAttribute('aria-selected')).toBe('true');
    expect(ehrTab.getAttribute('aria-current')).toBe('true');
    expect(conformantTab.getAttribute('aria-selected')).toBe('false');
    expect(conformantTab.getAttribute('aria-current')).toBeNull();

    await userEvent.click(conformantTab);
    expect(onLane).toHaveBeenCalledWith('conformant');
  });

  it('labels are the concise short forms, not the verbose lane titles', () => {
    render(<ModeSwitch lane="conformant" onLane={vi.fn()} />);

    expect(screen.getByText(LANE_LABELS.conformant.short)).toBeDefined();
    expect(screen.getByText(LANE_LABELS.ehr.short)).toBeDefined();
    expect(screen.queryByText(LANE_LABELS.conformant.title)).toBeNull();
    expect(screen.queryByText(LANE_LABELS.ehr.title)).toBeNull();
  });

  it('lane tabs use aria-current (selection semantics), not aria-pressed (toggle semantics) — parked aria-current-vs-pressed nit, moved here from UCCards', () => {
    render(<ModeSwitch lane="conformant" onLane={vi.fn()} />);

    const conformantTab = screen.getByRole('tab', { name: LANE_LABELS.conformant.short });
    const ehrTab = screen.getByRole('tab', { name: LANE_LABELS.ehr.short });

    expect(conformantTab.hasAttribute('aria-pressed')).toBe(false);
    expect(ehrTab.hasAttribute('aria-pressed')).toBe(false);
  });
});

describe('ModeSwitch lanes (the provider-data lane is status-gated)', () => {
  it('renders only the lanes it is given; the default is every lane, provider-data included', () => {
    const { rerender } = render(
      <ModeSwitch lane="conformant" onLane={vi.fn()} lanes={visibleLanes(undefined)} />,
    );
    expect(screen.getAllByRole('tab')).toHaveLength(2);
    expect(screen.queryByRole('tab', { name: LANE_LABELS['provider-data'].short })).toBeNull();

    rerender(
      <ModeSwitch lane="conformant" onLane={vi.fn()} lanes={visibleLanes('http://127.0.0.1:9095')} />,
    );
    expect(screen.getAllByRole('tab')).toHaveLength(3);
    expect(screen.getByRole('tab', { name: LANE_LABELS['provider-data'].short })).toBeDefined();

    rerender(<ModeSwitch lane="conformant" onLane={vi.fn()} />);
    expect(screen.getAllByRole('tab')).toHaveLength(LANES.length);
  });

  it('visibleLanes: the provider-data lane exists iff the Kit reports its gateway child; order is the render order', () => {
    expect(visibleLanes(undefined)).toEqual(['conformant', 'ehr']);
    expect(visibleLanes('')).toEqual(['conformant', 'ehr']);
    expect(visibleLanes('http://127.0.0.1:9095')).toEqual(['conformant', 'ehr', 'provider-data']);
  });

  it('the provider-data lane copy names the reference payer and the chart, never internal vocabulary', () => {
    const pd = LANE_LABELS['provider-data'];
    expect(pd.short).toBe('Reference payer');
    expect(pd.blurb.overview).toMatch(/reference payer/i);
    expect(pd.blurb.overview).toMatch(/chart/i);
    for (const text of [pd.title, pd.short, pd.blurb.overview, pd.blurb.technical]) {
      expect(text).not.toMatch(/substrate|Mode A|br-payer|gap-fill/);
    }
  });
});
