import { describe, expect, it } from 'vitest';
import { packagedDefaults, resolveConfig } from '../src/config';

// Fixed minimal valid JSON payload every test starts from and mutates —
// keeps each row's diff-from-baseline obvious.
const baseFields = {
  gatewayBin: '/opt/shn/gateway',
  discoveryUrl: 'http://127.0.0.1:9001/discovery',
  accountsUrl: 'http://127.0.0.1:9002',
  uiDir: '/opt/shn/ui',
};

function fakeReadFile(contents: Record<string, string>) {
  return (p: string): string => {
    if (!(p in contents)) {
      throw Object.assign(new Error(`ENOENT: no such file, open '${p}'`), { code: 'ENOENT' });
    }
    return contents[p];
  };
}

describe('resolveConfig', () => {
  it('resolves from defaultPath; SHN_KIT_CONFIG env wins', () => {
    const defaultPath = '/default/kit.config.json';
    const envPath = '/env/kit.config.json';
    const readFile = fakeReadFile({
      [defaultPath]: JSON.stringify(baseFields),
      [envPath]: JSON.stringify({ ...baseFields, gatewayBin: '/env/gateway' }),
    });

    // No env override: reads defaultPath.
    const cfg1 = resolveConfig(readFile, {}, defaultPath);
    expect(cfg1.gatewayBin).toBe('/opt/shn/gateway');

    // SHN_KIT_CONFIG set: wins over defaultPath.
    const cfg2 = resolveConfig(readFile, { SHN_KIT_CONFIG: envPath }, defaultPath);
    expect(cfg2.gatewayBin).toBe('/env/gateway');
  });

  it('errors naming the field when discoveryUrl is missing', () => {
    const path = '/kit.config.json';
    const fields: Record<string, unknown> = { ...baseFields };
    delete fields.discoveryUrl;
    const readFile = fakeReadFile({ [path]: JSON.stringify(fields) });
    expect(() => resolveConfig(readFile, {}, path)).toThrowError(/discoveryUrl/);
  });

  // gatewayBin/uiDir are packaged-path knobs — main.ts defaults them from
  // process.resourcesPath when packaged (packagedDefaults below), exactly
  // like kitdBin already does, so resolveConfig itself no longer requires
  // either in the JSON file.
  it('tolerates gatewayBin/uiDir missing (main.ts defaults them in packaged mode)', () => {
    const path = '/kit.config.json';
    const fields: Record<string, unknown> = { ...baseFields };
    delete fields.gatewayBin;
    delete fields.uiDir;
    const readFile = fakeReadFile({ [path]: JSON.stringify(fields) });
    const cfg = resolveConfig(readFile, {}, path);
    expect(cfg.gatewayBin).toBeUndefined();
    expect(cfg.uiDir).toBeUndefined();
  });

  it('errors stating the either-or when both accountsUrl and secretsDir are missing', () => {
    const path = '/kit.config.json';
    const fields: Record<string, unknown> = { ...baseFields };
    delete fields.accountsUrl;
    const readFile = fakeReadFile({ [path]: JSON.stringify(fields) });
    expect(() => resolveConfig(readFile, {}, path)).toThrowError(
      /accountsUrl.*required unless.*secretsDir.*pre-provisioned bundle/,
    );
  });

  it('accepts secretsDir in place of accountsUrl', () => {
    const path = '/kit.config.json';
    const fields: Record<string, unknown> = { ...baseFields };
    delete fields.accountsUrl;
    (fields as Record<string, string>).secretsDir = '/opt/shn/secrets';
    const readFile = fakeReadFile({ [path]: JSON.stringify(fields) });
    const cfg = resolveConfig(readFile, {}, path);
    expect(cfg.secretsDir).toBe('/opt/shn/secrets');
    expect(cfg.accountsUrl).toBeUndefined();
  });

  it('tolerates unknown keys (forward-compat)', () => {
    const path = '/kit.config.json';
    const fields = { ...baseFields, someFutureField: 'whatever', another: 42 };
    const readFile = fakeReadFile({ [path]: JSON.stringify(fields) });
    const cfg = resolveConfig(readFile, {}, path);
    expect(cfg.gatewayBin).toBe(baseFields.gatewayBin);
    expect(cfg.uiDir).toBe(baseFields.uiDir);
  });

  // The pass-through knobs parse like every other optional field.
  it('parses javaAssets/jreDir/manifest/releasesUrl when present', () => {
    const path = '/kit.config.json';
    const fields = {
      ...baseFields,
      javaAssets: 'java',
      jreDir: '/opt/shn/java/jre-darwin-arm64',
      manifest: '/opt/shn/versions.json',
      releasesUrl: 'https://api.github.com/repos/SmartHealthNetwork/shn-kit/releases/latest',
    };
    const readFile = fakeReadFile({ [path]: JSON.stringify(fields) });
    const cfg = resolveConfig(readFile, {}, path);
    expect(cfg.javaAssets).toBe('java');
    expect(cfg.jreDir).toBe('/opt/shn/java/jre-darwin-arm64');
    expect(cfg.manifest).toBe('/opt/shn/versions.json');
    expect(cfg.releasesUrl).toBe('https://api.github.com/repos/SmartHealthNetwork/shn-kit/releases/latest');
  });
});

// Packaged-mode defaults: gatewayBin resolves exactly as
// kitdBin does, and every other extraResources path (ui/ build, versions.json,
// the java/ trio dir) resolves the same way — all rooted at resourcesPath, a
// pure function so it needs no packaged Electron app to test.
describe('packagedDefaults', () => {
  it('resolves every packaged path from resourcesPath — gatewayBin exactly as kitdBin', () => {
    const resourcesPath = '/Applications/SHN Kit.app/Contents/Resources';
    const d = packagedDefaults(resourcesPath);
    expect(d.gatewayBin).toBe('/Applications/SHN Kit.app/Contents/Resources/shn-gateway');
    expect(d.kitdBin).toBe('/Applications/SHN Kit.app/Contents/Resources/shnkitd');
    expect(d.uiDir).toBe('/Applications/SHN Kit.app/Contents/Resources/ui');
    expect(d.manifest).toBe('/Applications/SHN Kit.app/Contents/Resources/versions.json');
    expect(d.javaAssets).toBe('/Applications/SHN Kit.app/Contents/Resources/java');
  });

  it('is a pure function of resourcesPath (no filesystem/electron access)', () => {
    const a = packagedDefaults('/a');
    const b = packagedDefaults('/b');
    expect(a.gatewayBin).toBe('/a/shn-gateway');
    expect(b.gatewayBin).toBe('/b/shn-gateway');
  });
});
