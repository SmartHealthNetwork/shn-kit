// config.ts is pure (no electron import) so its logic is unit-testable in a
// plain node environment (vitest, `environment: 'node'`). It resolves the
// desktop shell's KitConfig from a JSON file, mirroring shnkitd's own
// required-field checks (kit/cmd/shnkitd/main.go) so a misconfigured Kit
// fails the same way whether the mistake is a missing CLI flag or a missing
// JSON field.
import * as path from 'node:path';

/** The desktop shell's resolved configuration — the JSON shape read from
 * kit.config.json (packaged) / dev.config.json (dev), plus what main.ts fills
 * in before handing it to the DaemonManager (stateDir/kitdBin/gatewayBin/
 * uiDir/manifest/javaAssets packaged-path defaults, packagedDefaults below). */
export interface KitConfig {
  gatewayBin?: string; // spawn target for the gateway child; default <resourcesPath>/shn-gateway when packaged
  discoveryUrl: string;
  accountsUrl?: string;
  secretsDir?: string; // pre-provisioned seam
  auditUrl?: string;
  phgUrl?: string;
  consentUrl?: string;
  fhirDataUrl?: string;
  patientAppUrl?: string;
  uiDir?: string; // built renderer; default <resourcesPath>/ui when packaged
  stateDir?: string; // default injected by main: app.getPath('userData') + '/kit'
  apiAddr?: string; // default '127.0.0.1:0'
  kitdBin?: string; // spawn target (not a shnkitd flag); default <resourcesPath>/shnkitd when packaged
  javaAssets?: string; // Java trio assets dir (--java-assets); packaged kit.config.json carries this as a RELATIVE marker, not an absolute path — main.ts resolves the real <resourcesPath>-rooted dir; "" / unset => no trio
  jreDir?: string; // --jre-dir; "" / unset => shnkitd's own per-arch default ({java-assets}/jre-{GOOS}-{GOARCH})
  manifest?: string; // --manifest (versions.json path); default <resourcesPath>/versions.json when packaged
  releasesUrl?: string; // --releases-url; "" / unset => shnkitd's own default feed
}

const REQUIRED_STRING_FIELDS = ['discoveryUrl'] as const;

const OPTIONAL_STRING_FIELDS = [
  'gatewayBin',
  'accountsUrl',
  'secretsDir',
  'auditUrl',
  'phgUrl',
  'consentUrl',
  'fhirDataUrl',
  'patientAppUrl',
  'uiDir',
  'stateDir',
  'apiAddr',
  'kitdBin',
  'javaAssets',
  'jreDir',
  'manifest',
  'releasesUrl',
] as const;

/** Every packaged-only path the app ships under Resources (electron-builder.yml's
 * `extraResources`: the gateway/kitd binaries, the built UI, the versions.json
 * manifest, and the java/ trio assets dir) resolved from Electron's
 * `process.resourcesPath` — a pure function (no electron import) so it's
 * unit-testable without a packaged app. `gatewayBin` resolves exactly as
 * `kitdBin` does: kit.config.json never bakes an absolute install path
 * for any of these, since that path varies per machine/install
 * location and is only known at runtime. */
export interface PackagedDefaults {
  gatewayBin: string;
  kitdBin: string;
  uiDir: string;
  manifest: string;
  javaAssets: string;
}

export function packagedDefaults(resourcesPath: string): PackagedDefaults {
  return {
    gatewayBin: path.join(resourcesPath, 'shn-gateway'),
    kitdBin: path.join(resourcesPath, 'shnkitd'),
    uiDir: path.join(resourcesPath, 'ui'),
    manifest: path.join(resourcesPath, 'versions.json'),
    javaAssets: path.join(resourcesPath, 'java'),
  };
}

function asNonEmptyString(v: unknown): string | undefined {
  return typeof v === 'string' && v !== '' ? v : undefined;
}

/**
 * Resolves KitConfig from a JSON file. `SHN_KIT_CONFIG` (env) overrides
 * `defaultPath` when set. Validates required fields with named errors;
 * unknown JSON keys are tolerated (forward-compat).
 */
export function resolveConfig(
  readFile: (p: string) => string,
  env: Record<string, string | undefined>,
  defaultPath: string,
): KitConfig {
  const path = env.SHN_KIT_CONFIG || defaultPath;

  let raw: string;
  try {
    raw = readFile(path);
  } catch (err) {
    throw new Error(`resolveConfig: read ${path}: ${(err as Error).message}`);
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    throw new Error(`resolveConfig: parse ${path}: ${(err as Error).message}`);
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error(`resolveConfig: ${path}: expected a JSON object`);
  }
  const raw2 = parsed as Record<string, unknown>;

  for (const field of REQUIRED_STRING_FIELDS) {
    if (asNonEmptyString(raw2[field]) === undefined) {
      throw new Error(`resolveConfig: ${path}: missing required field "${field}"`);
    }
  }

  const accountsUrl = asNonEmptyString(raw2.accountsUrl);
  const secretsDir = asNonEmptyString(raw2.secretsDir);
  if (accountsUrl === undefined && secretsDir === undefined) {
    // Mirrors shnkitd's own stderr sentence shape (kit/cmd/shnkitd/main.go):
    // "--accounts is required unless --secrets provides a pre-provisioned bundle".
    throw new Error(
      `resolveConfig: ${path}: accountsUrl is required unless secretsDir provides a pre-provisioned bundle`,
    );
  }

  const cfg: Record<string, string> = {
    discoveryUrl: raw2.discoveryUrl as string,
  };
  for (const field of OPTIONAL_STRING_FIELDS) {
    const v = asNonEmptyString(raw2[field]);
    if (v !== undefined) {
      cfg[field] = v;
    }
  }
  return cfg as unknown as KitConfig;
}
