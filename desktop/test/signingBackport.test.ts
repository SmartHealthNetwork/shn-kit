import { afterEach, describe, expect, it, vi } from 'vitest';
import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

const installedRequire = createRequire(resolve(__dirname, '../package.json'));
const builderUtil = installedRequire('builder-util');
const codesign = installedRequire('app-builder-lib/out/codeSign/codesign');
const { createKeychain } = installedRequire('app-builder-lib/out/codeSign/macCodeSign');
const childProcess = installedRequire('node:child_process');
const crypto = installedRequire('node:crypto');
const logging = installedRequire('builder-util/out/log');
const desktopRoot = resolve(__dirname, '..');
const temporaryRoots: string[] = [];

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllEnvs();
  for (const root of temporaryRoots.splice(0)) rmSync(root, { recursive: true, force: true });
});

interface Target {
  module: string;
  version: string;
  file: string;
  originalSha256: string;
  patchedSha256: string;
  replacements: { before: string; after: string }[];
}

const manifest: Target[] = installedRequire('./patches/signing-backport.json');
const hash = (bytes: string | Buffer) => createHash('sha256').update(bytes).digest('hex');
const targetPath = (root: string, target: Target) => join(root, 'node_modules', target.module, target.file);

function originalBytes(target: Target): string {
  let bytes = readFileSync(targetPath(desktopRoot, target), 'utf8');
  if (hash(bytes) === target.patchedSha256) {
    for (const replacement of [...target.replacements].reverse()) bytes = bytes.replace(replacement.after, replacement.before);
  }
  if (hash(bytes) !== target.originalSha256) throw new Error('Installed fixture source drift');
  return bytes;
}

function patchFixture(): string {
  const root = mkdtempSync(join(tmpdir(), 'signing-backport-test-'));
  temporaryRoots.push(root);
  for (const target of manifest) {
    const file = targetPath(root, target);
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(join(root, 'node_modules', target.module, 'package.json'), JSON.stringify({ version: target.version }));
    writeFileSync(file, originalBytes(target));
  }
  return root;
}

describe('install-time signing backport', () => {
  it('patches the exact original installed sources and is byte-idempotent', () => {
    const root = patchFixture();
    const { applyBackport } = installedRequire('./scripts/apply-signing-backport.cjs');
    applyBackport(root);
    for (const target of manifest) expect(hash(readFileSync(targetPath(root, target)))).toBe(target.patchedSha256);
    const writes = vi.spyOn(installedRequire('node:fs'), 'writeFileSync');
    applyBackport(root);
    expect(writes).not.toHaveBeenCalled();
  });

  const refusals = manifest.flatMap((target, index) => ['missing module', 'missing file', 'version drift', 'source drift', 'mixed state', 'partial file'].map(kind => ({ name: `${target.module}: ${kind}`, index, kind })));
  it.each(refusals)('refuses $name before changing any target', ({ index, kind }) => {
    const root = patchFixture();
    const target = manifest[index];
    const file = targetPath(root, target);
    const packageFile = join(root, 'node_modules', target.module, 'package.json');
    if (kind === 'missing module') rmSync(packageFile);
    if (kind === 'missing file') rmSync(file);
    if (kind === 'version drift') writeFileSync(packageFile, JSON.stringify({ version: '26.15.4' }));
    if (kind === 'source drift') writeFileSync(file, `${readFileSync(file, 'utf8')}\n// synthetic drift`);
    if (kind === 'mixed state' || kind === 'partial file') {
      let bytes = readFileSync(file, 'utf8');
      for (const replacement of kind === 'mixed state' ? target.replacements : target.replacements.slice(0, 1)) bytes = bytes.replace(replacement.before, replacement.after);
      writeFileSync(file, bytes);
    }
    const before = manifest.map(t => existsSync(targetPath(root, t)) ? readFileSync(targetPath(root, t), 'utf8') : null);
    const { applyBackport } = installedRequire('./scripts/apply-signing-backport.cjs');
    expect(() => applyBackport(root)).toThrow(/Signing backport/);
    expect(manifest.map(t => existsSync(targetPath(root, t)) ? readFileSync(targetPath(root, t), 'utf8') : null)).toEqual(before);
  });

  it('runs the package postinstall hook on a fresh dependency tree', () => {
    const root = patchFixture();
    for (const file of ['package.json', 'scripts/apply-signing-backport.cjs', 'patches/signing-backport.json']) {
      mkdirSync(dirname(join(root, file)), { recursive: true });
      copyFileSync(join(desktopRoot, file), join(root, file));
    }
    const result = childProcess.spawnSync(process.execPath, [process.env.npm_execpath!, 'run', 'postinstall'], { cwd: root, encoding: 'utf8', timeout: 30_000 });
    expect(result.status).toBe(0);
    for (const target of manifest) expect(hash(readFileSync(targetPath(root, target)))).toBe(target.patchedSha256);
  });

  it.each(['missing replacement', 'duplicate replacement', 'wrong patched hash', 'unexpected target', 'duplicate target'])('refuses %s in the last target before writing the first', kind => {
    const root = patchFixture();
    const changed: Target[] = structuredClone(manifest);
    const target = changed.at(-1)!;
    if (kind === 'missing replacement') target.replacements[0].before = 'not present in the installed module';
    if (kind === 'duplicate replacement') target.replacements[0].before = 'const ';
    if (kind === 'wrong patched hash') target.patchedSha256 = '0'.repeat(64);
    if (kind === 'unexpected target') target.file = '../outside.js';
    if (kind === 'duplicate target') changed[1] = changed[0];
    for (const directory of ['scripts', 'patches']) mkdirSync(join(root, directory));
    copyFileSync(join(desktopRoot, 'scripts/apply-signing-backport.cjs'), join(root, 'scripts/apply-signing-backport.cjs'));
    writeFileSync(join(root, 'patches/signing-backport.json'), JSON.stringify(changed));
    const before = manifest.map(t => hash(readFileSync(targetPath(root, t))));
    const result = childProcess.spawnSync(process.execPath, [join(root, 'scripts/apply-signing-backport.cjs')], { encoding: 'utf8', timeout: 30_000 });
    expect(result.status).toBe(1);
    expect(result.stderr).toMatch(/^Signing backport[^\n]+\n$/);
    expect(manifest.map(t => hash(readFileSync(targetPath(root, t))))).toEqual(before);
  });

  it('does not disclose malformed dependency metadata in CLI failure output', () => {
    const root = patchFixture();
    for (const directory of ['scripts', 'patches']) mkdirSync(join(root, directory));
    for (const file of ['scripts/apply-signing-backport.cjs', 'patches/signing-backport.json']) copyFileSync(join(desktopRoot, file), join(root, file));
    writeFileSync(join(root, 'node_modules', 'builder-util', 'package.json'), appPassword);
    const result = childProcess.spawnSync(process.execPath, [join(root, 'scripts/apply-signing-backport.cjs')], { encoding: 'utf8', timeout: 30_000 });
    expect(result.status).toBe(1);
    expect(`${result.stdout}${result.stderr}`.includes(appPassword)).toBe(false);
    for (const target of manifest) expect(hash(readFileSync(targetPath(root, target)))).toBe(target.originalSha256);
  });
});

// Synthetic inputs only. Boolean assertions keep credential values out of RED diagnostics.
const appPassword = 'synthetic-app-password';
const installerPassword = 'synthetic-installer-password';
const keychainBytes = Buffer.alloc(32, 42);
const keychainPassword = keychainBytes.toString('base64');
const keychainPath = '/synthetic/signing.keychain';
const partitionArgs = ['set-key-partition-list', '-S', 'apple-tool:,apple:', '-s', '-k', keychainPassword, keychainPath];

describe('installed signing password flow', () => {
  async function runSigning(passwords: string[], failure?: string) {
    const commands: string[][] = [];
    const refused = new Error('Synthetic security command refused');
    // Skip upstream's shared root-certificate cache; intercept every OS/network boundary.
    vi.stubEnv('TRAVIS', 'true');
    vi.spyOn(crypto, 'randomBytes').mockReturnValue(keychainBytes);
    vi.spyOn(codesign, 'importCertificate').mockImplementation(async (link: unknown) => link);
    vi.spyOn(builderUtil, 'exec').mockImplementation(async (file: unknown, args: unknown) => {
      if (file !== '/usr/bin/security' || !Array.isArray(args)) throw new Error('Unexpected executable');
      commands.push([...args]);
      if (args[0] === failure) throw refused;
      if (!['delete-keychain', 'list-keychains', 'create-keychain', 'unlock-keychain', 'set-keychain-settings', 'import', 'set-key-partition-list'].includes(args[0])) {
        throw new Error('Unexpected security command');
      }
      return '';
    });
    const result = createKeychain({
      tmpDir: {}, currentDir: '/synthetic/project', cscLink: '/synthetic/app.p12', cscKeyPassword: passwords[0],
      ...(passwords.length === 2 ? { cscILink: '/synthetic/installer.p12', cscIKeyPassword: passwords[1] } : {}),
    });
    return { result, commands, refused };
  }

  it.each([
    { name: 'application certificate', passwords: [appPassword] },
    { name: 'application and installer certificates', passwords: [appPassword, installerPassword] },
    { name: 'empty certificate passwords', passwords: ['', ''] },
  ])('uses the generated keychain password for $name', async ({ passwords }) => {
    const { result, commands } = await runSigning(passwords);
    const signed = await result;
    const generated = commands.find(c => c[0] === 'create-keychain')![2];
    const imports = commands.filter(c => c[0] === 'import');
    const partitions = commands.filter(c => c[0] === 'set-key-partition-list');
    expect(imports.length).toBe(passwords.length);
    expect(imports.every((c, i) => c[c.indexOf('-P') + 1] === passwords[i])).toBe(true);
    expect(partitions.length).toBe(passwords.length);
    expect(partitions.every(c => c[c.indexOf('-k') + 1] === generated)).toBe(true);
    expect(partitions.every(c => c.at(-1) === signed.keychainFile)).toBe(true);
  });

  it.each(['import', 'set-key-partition-list'])('propagates %s failure without proceeding to the installer', async failure => {
    const { result, commands, refused } = await runSigning([appPassword, installerPassword], failure);
    await expect(result).rejects.toBe(refused);
    expect(commands.filter(c => c[0] === failure).length).toBe(1);
    expect(commands.some(c => c[0] === 'import' && c[1] === '/synthetic/installer.p12')).toBe(false);
  });
});

describe('installed signing command logging', () => {
  it.each(['unquoted', 'single quoted', 'double quoted'])('scrubs partition credentials in %s debug and failure text', style => {
    const quote = style === 'single quoted' ? "'" : style === 'double quoted' ? '"' : '';
    const command = `set-key-partition-list -S apple-tool:,apple: -s -k ${quote}${keychainPassword}${quote} ${keychainPath}`;
    for (const input of [command, `Exit code: 1. Command failed: /usr/bin/security ${command}`]) {
      const output = builderUtil.removePassword(input);
      expect(output.includes(keychainPassword)).toBe(false);
      expect(output.includes(keychainPath)).toBe(true);
    }
  });

  it('preserves import keychain paths and existing sensitive flag formats', () => {
    const command = `security import /synthetic/app.p12 -k ${keychainPath} -P ${appPassword}`;
    const output = builderUtil.removePassword(command);
    expect(output.includes(appPassword)).toBe(false);
    expect(output.includes(`-k ${keychainPath}`)).toBe(true);
    for (const flag of ['-p', '--password', '/pass', 'pass:', '/b']) {
      const input = `${flag} ${appPassword}${flag === '/b' ? ' /c' : ''}`;
      expect(builderUtil.removePassword(input).includes(appPassword)).toBe(false);
    }
    expect(builderUtil.removePassword('-path /synthetic/path /p \\\\Mac\\Host\\file')).toBe('-path /synthetic/path /p \\\\Mac\\Host\\file');
  });

  it.each(['debug', 'error'])('keeps both passwords out of the real %s logging entry', async mode => {
    const output: string[] = [];
    const previousLog = logging.log;
    const previousEnabled = logging.debug.enabled;
    const previousDisabled = logging.shouldDisableNonErrorLoggingVitest;
    logging.debug.enabled = true;
    logging.shouldDisableNonErrorLoggingVitest = false;
    logging.log = new logging.Logger({ write: (text: string) => output.push(text) });
    vi.spyOn(childProcess, 'execFile').mockImplementation((_file: unknown, args: unknown, _options: unknown, callback: any) => {
      const command = `/usr/bin/security ${(args as string[]).join(' ')}`;
      if (mode === 'error') {
        callback(Object.assign(new Error(`Command failed: ${command}`), { code: 1 }), command, command);
      } else {
        callback(null, '', '');
      }
    });
    try {
      for (const args of [partitionArgs, ['import', '/synthetic/app.p12', '-k', keychainPath, '-P', appPassword]]) {
        const error = await builderUtil.exec('/usr/bin/security', args).then(() => null, (error: Error) => error);
        if (mode === 'error') {
          expect(error instanceof Error).toBe(true);
          logging.log.error(error);
        } else {
          expect(error === null).toBe(true);
        }
      }
      const rendered = output.join('');
      expect(rendered.includes(mode === 'debug' ? 'executing' : 'Exit code: 1')).toBe(true);
      expect(rendered.includes(keychainPassword)).toBe(false);
      expect(rendered.includes(appPassword)).toBe(false);
      expect(rendered.includes(keychainPath)).toBe(true);
    } finally {
      logging.log = previousLog;
      logging.debug.enabled = previousEnabled;
      logging.shouldDisableNonErrorLoggingVitest = previousDisabled;
    }
  });
});
