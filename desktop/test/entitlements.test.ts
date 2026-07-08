import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

// The hardened-runtime entitlements are embedded by `codesign` at mac package
// time. codesign parses them with Apple's strict AMFIUnserializeXML parser,
// which — unlike lenient XML readers — rejects a literal double-hyphen inside a
// comment (invalid per the XML 1.0 spec). A malformed comment fails the ENTIRE
// mac signing step ("Failed to parse entitlements: AMFIUnserializeXML: syntax
// error"), and that failure is invisible on an unsigned/local build (entitlements
// are ignored there), so it only surfaces on the expensive signed mac packaging
// job. This hermetic guard is the fast feedback that keeps the plist codesign-safe.
const plist = readFileSync(new URL('../build/entitlements.mac.plist', import.meta.url), 'utf8');

describe('entitlements.mac.plist', () => {
  it('has no double-hyphen inside any XML comment (codesign/AMFI rejects it)', () => {
    const comments = [...plist.matchAll(/<!--([\s\S]*?)-->/g)].map((m) => m[1]);
    expect(comments.length).toBeGreaterThan(0);
    for (const body of comments) {
      expect(body, `an XML comment contains a literal "--":\n${body}`).not.toContain('--');
    }
  });

  it('declares the hardened-runtime entitlements the bundled JVM needs', () => {
    for (const key of [
      'com.apple.security.cs.allow-jit',
      'com.apple.security.cs.allow-unsigned-executable-memory',
      'com.apple.security.cs.disable-library-validation',
      'com.apple.security.cs.allow-dyld-environment-variables',
    ]) {
      expect(plist, `missing entitlement ${key}`).toContain(key);
    }
  });
});
