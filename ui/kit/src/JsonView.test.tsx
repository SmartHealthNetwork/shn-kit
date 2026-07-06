import { describe, it, expect } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { JsonView } from './JsonView';

describe('JsonView', () => {
  it('renders keys and primitive values of a small object', () => {
    render(<JsonView value={{ name: 'Linda', age: 42 }} />);
    expect(screen.getByText('name')).toBeDefined();
    expect(screen.getByText('Linda')).toBeDefined();
    expect(screen.getByText('age')).toBeDefined();
    expect(screen.getByText('42')).toBeDefined();
  });

  it('nodes deeper than defaultDepth render collapsed — child keys absent from the DOM', () => {
    const value = { nested: { deeper: { secretDeepKey: 'secret-deep-value' } } };
    render(<JsonView value={value} defaultDepth={2} />);
    expect(screen.queryByText('secretDeepKey')).toBeNull();
    expect(screen.queryByText('secret-deep-value')).toBeNull();
  });

  it('toggle button expands a collapsed node, then re-collapses it', () => {
    const value = { nested: { deeper: { secretDeepKey: 'secret-deep-value' } } };
    render(<JsonView value={value} defaultDepth={2} />);

    const toggles = screen.getAllByRole('button');
    const deeperToggle = toggles[toggles.length - 1];

    fireEvent.click(deeperToggle);
    expect(screen.getByText('secretDeepKey')).toBeDefined();
    expect(screen.getByText('secret-deep-value')).toBeDefined();

    fireEvent.click(deeperToggle);
    expect(screen.queryByText('secretDeepKey')).toBeNull();
    expect(screen.queryByText('secret-deep-value')).toBeNull();
  });

  it('search highlights the match, reports a count, and auto-expands the path beyond defaultDepth', () => {
    const value = { nested: { deeper: { patientName: 'Linda Johansson' } } };
    render(<JsonView value={value} defaultDepth={2} search="Johansson" />);

    const match = screen.getByText('Linda Johansson');
    expect(match.className).toContain('json-match');
    expect(screen.getByText('1 match')).toBeDefined();
  });

  it('search with 0 matches renders "no matches"', () => {
    render(<JsonView value={{ name: 'Linda' }} search="zzz-nonexistent" />);
    expect(screen.getByText('no matches')).toBeDefined();
  });

  it('a non-object value (string) renders plainly — no keys, brackets, or toggle', () => {
    render(<JsonView value="just a string" />);
    expect(screen.getByText('just a string')).toBeDefined();
    expect(screen.queryAllByRole('button')).toHaveLength(0);
  });

  it('a non-object value (number) renders plainly', () => {
    render(<JsonView value={42} />);
    expect(screen.getByText('42')).toBeDefined();
  });

  it('null and undefined render safely without throwing', () => {
    expect(() => render(<JsonView value={null} />)).not.toThrow();
    expect(() => render(<JsonView value={undefined} />)).not.toThrow();
  });

  // Finding 2: nodeOwnMatch used to short-circuit on the key hit, so a node
  // whose key AND value both match only counted once even though BOTH render
  // a <mark> highlight. The count must equal the number of visible marks.
  it('counts a key-hit and a value-hit on the same node independently', () => {
    render(<JsonView value={{ Linda: 'Linda' }} search="Linda" />);
    expect(screen.getByText('2 matches')).toBeDefined();
    expect(document.querySelectorAll('mark.json-match')).toHaveLength(2);
  });

  // Finding 5 (accepted, take-or-leave): array rendering with nested null +
  // numbers — indices render as labels, no throw.
  it('renders an array of objects with nested null and numbers, using indices as labels, without throwing', () => {
    const value = [
      { id: 1, note: null },
      { id: 2, note: 'ok' },
    ];
    expect(() => render(<JsonView value={value} />)).not.toThrow();
    expect(document.querySelector('[data-path="0"]')).not.toBeNull();
    expect(document.querySelector('[data-path="1"]')).not.toBeNull();
    expect(document.querySelector('[data-path="0.id"]')).not.toBeNull();
    expect(document.querySelector('[data-path="1.note"]')).not.toBeNull();
    expect(screen.getByText('null')).toBeDefined();
  });
});
