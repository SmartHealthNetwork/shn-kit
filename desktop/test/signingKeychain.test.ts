import { describe, expect, it } from 'vitest';
import { createRequire } from 'node:module';
import { join, resolve } from 'node:path';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { spawn } from 'node:child_process';

const require = createRequire(resolve(__dirname, '../package.json'));
const loadSupervisor = () => require('./scripts/test-signing-keychain.cjs');

describe('real keychain test supervisor', () => {
  it.each(['success', 'rejection', 'timeout', 'restore failure', 'delete failure', 'combined failure'])('cleans up after %s and rejects cleanup failures', async mode => {
    const { withKeychainCleanup } = loadSupervisor();
    const calls: string[][] = [];
    const keychain = '/synthetic/owned.keychain';
    const searchList = ['/synthetic/first.keychain', '/synthetic/second with spaces.keychain'];
    const security = (args: string[]) => {
      calls.push(args);
      if (args[0] === 'delete-keychain' && ['delete failure', 'combined failure'].includes(mode)) throw new Error('synthetic delete refusal');
      if (args[0] === 'list-keychains' && args.includes('-s') && ['restore failure', 'combined failure'].includes(mode)) throw new Error('synthetic restore refusal');
      return searchList.map(p => `    "${p}"`).join('\n') + '\n';
    };
    const result = withKeychainCleanup({ keychain, searchList, security, exists: () => true }, async () => {
      if (['rejection', 'timeout'].includes(mode)) throw new Error('synthetic worker refusal');
      return 'passed';
    });
    if (mode === 'success') await expect(result).resolves.toBe('passed');
    else await expect(result).rejects.toThrow(/keychain regression/);
    expect(calls).toContainEqual(['delete-keychain', keychain]);
    expect(calls).toContainEqual(['list-keychains', '-d', 'user', '-s', ...searchList]);
    expect(calls.some(c => c[0] === 'lock-keychain' || c[0] === 'unlock-keychain')).toBe(false);
  });

  it('restores the search list even when the worker fails before creating a keychain', async () => {
    const { withKeychainCleanup } = loadSupervisor();
    const calls: string[][] = [];
    await expect(withKeychainCleanup({ keychain: '/synthetic/owned.keychain', searchList: [], exists: () => false, security: (args: string[]) => { calls.push(args); return ''; } }, async () => { throw new Error('synthetic pre-create failure'); })).rejects.toThrow(/keychain regression/);
    expect(calls).toEqual([['list-keychains', '-d', 'user', '-s'], ['list-keychains', '-d', 'user']]);
  });

  it('rejects a restoration that silently changes search-list order', async () => {
    const { withKeychainCleanup } = loadSupervisor();
    await expect(withKeychainCleanup({ keychain: '/synthetic/owned.keychain', searchList: ['/a', '/b'], exists: () => false, security: () => '"/b"\n"/a"\n' }, async () => null)).rejects.toThrow(/cleanup failed/);
  });

  it('bounds a worker, reaps its process group and withholds raw failure output', async () => {
    const { runWorker } = loadSupervisor();
    await expect(runWorker(process.execPath, ['-e', 'console.error("synthetic hidden output"); setInterval(() => {}, 1000)'], { timeout: 100, env: {} })).rejects.toThrow('keychain regression worker timed out');
    await expect(runWorker(process.execPath, ['-e', 'console.error("synthetic hidden output"); process.exit(2)'], { timeout: 1000, env: {} })).rejects.toThrow('keychain regression worker rejected');
  });

  it.skipIf(process.platform === 'win32')('terminates a real worker and descendant before cleanup starts', async () => {
    const { runWorker, withKeychainCleanup } = loadSupervisor();
    const root = mkdtempSync(join(tmpdir(), 'keychain-worker-test-'));
    const marker = join(root, 'pids.json');
    let cleanupStarted = false;
    try {
      const body = () => runWorker(process.execPath, ['-e', `
        const child = require('node:child_process').spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
        require('node:fs').writeFileSync(process.argv[1], JSON.stringify([process.pid, child.pid]));
        setInterval(() => {}, 1000);
      `, marker], { timeout: 500, env: {} });
      await expect(withKeychainCleanup({ keychain: '/synthetic/owned.keychain', searchList: [], exists: () => false, security: () => {
        cleanupStarted = true;
        const pids = JSON.parse(readFileSync(marker, 'utf8')) as number[];
        expect(pids.length).toBe(2);
        for (const pid of pids) expect(() => process.kill(pid, 0)).toThrow();
        return '';
      } }, body)).rejects.toThrow('keychain regression scenario failed');
      expect(cleanupStarted).toBe(true);
    } finally { rmSync(root, { recursive: true, force: true }); }
  });

  it.skipIf(process.platform === 'win32')('retains owned material if worker termination cannot establish quiescence', async () => {
    const { runWorker, withKeychainCleanup } = loadSupervisor();
    let workerPID: number | undefined;
    const cleanup: string[][] = [];
    try {
      await expect(withKeychainCleanup({keychain: '/synthetic/owned.keychain', searchList: [], exists: () => true, security: (args: string[]) => { cleanup.push(args); return ''; }}, () => runWorker(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { timeout: 100, env: {}, terminateGroup: (pid: number) => { workerPID = pid; throw Object.assign(new Error('synthetic termination refusal'), { code: 'EPERM' }); } }))).rejects.toThrow('quiescence unverified');
      expect(cleanup).toEqual([]);
    } finally { if (workerPID) process.kill(-workerPID, 'SIGKILL'); }
  });

  it.skipIf(process.platform === 'win32')('retains owned material if the group remains visible after the leader exits', async () => {
    const { runWorker, withKeychainCleanup } = loadSupervisor();
    const cleanup: string[][] = [];
    await expect(withKeychainCleanup({keychain: '/synthetic/owned.keychain', searchList: [], exists: () => true, security: (args: string[]) => { cleanup.push(args); return ''; }}, () => runWorker(process.execPath, ['-e', 'process.exit(0)'], { env: {}, groupExists: () => true, quiescenceTimeout: 25 }))).rejects.toThrow('quiescence unverified');
    expect(cleanup).toEqual([]);
  });

  it.skipIf(process.platform === 'win32').each(['SIGINT', 'SIGTERM'] as const)('cleans up after %s without starting another worker', async signal => {
    const root = mkdtempSync(join(tmpdir(), 'keychain-signal-test-'));
    const marker = join(root, 'pids.json');
    const supervisorScript = require.resolve('./scripts/test-signing-keychain.cjs');
    const supervisor = spawn(process.execPath, ['-e', `
      const fs = require('node:fs');
      const { createCancellation, withKeychainCleanup, runWorker } = require(process.argv[1]);
      const cancellation = createCancellation();
      let cleanup = [];
      const workerScript = "const child = require('node:child_process').spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], {stdio: 'ignore'}); require('node:fs').writeFileSync(process.argv[1], JSON.stringify([process.pid, child.pid])); setInterval(() => {}, 1000)";
      (async () => {
        try {
          await withKeychainCleanup({keychain: '/synthetic/owned.keychain', searchList: [], exists: () => true, security: args => {
            const stopped = JSON.parse(fs.readFileSync(process.argv[2], 'utf8')).every(pid => { try { process.kill(pid, 0); return false; } catch { return true; } });
            if (!stopped) throw new Error('worker survived');
            cleanup.push(args[0]); return '';
          }}, () => runWorker(process.execPath, ['-e', workerScript, process.argv[2]], {timeout: 4000, env: {}, signal: cancellation.signal}));
        } catch {}
        let nextRejected = false;
        try { await runWorker(process.execPath, ['-e', 'process.exit(0)'], {signal: cancellation.signal, env: {}}); } catch { nextRejected = true; }
        console.log(JSON.stringify({cleanup, nextRejected}));
        cancellation.dispose();
        process.exitCode = cancellation.exitCode;
      })();
    `, supervisorScript, marker], { stdio: ['ignore', 'pipe', 'pipe'], env: {} });
    let output = '';
    supervisor.stdout.on('data', chunk => { output += chunk; });
    supervisor.stderr.resume();
    const closed = new Promise<number | null>(resolve => supervisor.on('close', resolve));
    try {
      await expect.poll(() => existsSync(marker), { timeout: 3000 }).toBe(true);
      supervisor.kill(signal);
      expect(await closed).toBe(signal === 'SIGINT' ? 130 : 143);
      expect(JSON.parse(output)).toEqual({ cleanup: ['delete-keychain', 'list-keychains', 'list-keychains'], nextRejected: true });
    } finally {
      supervisor.kill('SIGKILL');
      if (existsSync(marker)) {
        const [workerPID] = JSON.parse(readFileSync(marker, 'utf8')) as number[];
        try { process.kill(-workerPID, 'SIGKILL'); } catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ESRCH') throw error; }
      }
      rmSync(root, { recursive: true, force: true });
    }
  });
});
