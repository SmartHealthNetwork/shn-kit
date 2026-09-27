// Types jest-dom's matchers (toBeInTheDocument, toHaveTextContent, …) on
// Vitest 5's `expect`.
//
// Only the TYPES need this: src/test-setup.ts still registers the matchers at
// runtime through `expect.extend`, exactly as before. Vitest 5 stopped reading
// custom matcher types from the global `jest.Matchers` interface, which is the
// only thing the bare '@testing-library/jest-dom' entry augments. The package's
// '/vitest' entry is no substitute in 7.0.1: it still maps its matchers onto
// Vitest 4's single-parameter `Assertion<T>`, and Vitest 5's `Assertion` takes
// the return type first (`Assertion<R, T>`), so under Vitest 5 their return and
// received types come out wrong. A matcher returns the received value instead
// of `void`, `.resolves` / `.rejects` return that value instead of
// `Promise<void>`, and `expect.extend` accepts matchers with the wrong
// parameter types.
//
// This is Vitest 5's documented extension point (migration guide, "Assertion
// Types Expose Return and Received Types"; guide "Extending Matchers"):
// augment `Matchers`, which types `expect(x).*`, `expect.*` and `expect.extend`
// together. Its `R` is `void` for a plain assertion and `Promise<void>` under
// `.resolves` / `.rejects`, and it is jest-dom's own return-type parameter.
// Vitest's second parameter (the received type) has a default and jest-dom does
// not use it, so the augmentation declares `R` alone.
//
// Temporary seam, tracked upstream: jest-dom 7.0.1 does not type Vitest 5
// (https://github.com/testing-library/jest-dom/issues/738, with a fix proposed
// in https://github.com/testing-library/jest-dom/pull/742). Once a jest-dom
// release types Vitest 5 itself, delete this file and import
// '@testing-library/jest-dom/vitest' in src/test-setup.ts.
import 'vitest';
import type { TestingLibraryMatchers } from '@testing-library/jest-dom/matchers';

declare module 'vitest' {
  /* eslint-disable-next-line @typescript-eslint/no-empty-object-type --
     A declaration merge, not a new type: its whole body is the extends clause. */
  interface Matchers<R> extends TestingLibraryMatchers<unknown, R> {}
}
