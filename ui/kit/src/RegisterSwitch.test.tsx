import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RegisterSwitch } from './RegisterSwitch';

describe('RegisterSwitch', () => {
  it('renders Overview + Technical as a labeled GROUP of toggle buttons (aria-pressed), never a tablist', () => {
    render(<RegisterSwitch register="overview" onRegister={vi.fn()} />);

    // A group, NOT a tablist/tab. The lane switch (ModeSwitch) owns tablist
    // semantics; this is an in-place view-density toggle, so it must not add a
    // tablist/tab inside the scenarios column (UCCards guards against that).
    expect(screen.getByRole('group')).toBeDefined();
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(screen.queryByRole('tab')).toBeNull();

    const overview = screen.getByRole('button', { name: 'Overview' });
    const technical = screen.getByRole('button', { name: 'Technical' });
    expect(overview.getAttribute('aria-pressed')).toBe('true');
    expect(technical.getAttribute('aria-pressed')).toBe('false');
  });

  it('clicking the inactive register calls onRegister with its value', async () => {
    const onRegister = vi.fn();
    render(<RegisterSwitch register="overview" onRegister={onRegister} />);

    await userEvent.click(screen.getByRole('button', { name: 'Technical' }));
    expect(onRegister).toHaveBeenCalledWith('technical');
  });

  it('reflects the selected register when technical is active', () => {
    render(<RegisterSwitch register="technical" onRegister={vi.fn()} />);

    expect(screen.getByRole('button', { name: 'Technical' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: 'Overview' }).getAttribute('aria-pressed')).toBe('false');
  });
});
