import { describe, expect, it, vi } from 'vitest';
import type { KitConfig } from '../src/config';
import {
  DaemonManager,
  SessionStore,
  buildArgs,
  handleRestartRequest,
  waitForSession,
  wireUnexpectedExit,
  type ChildLike,
  type Session,
} from '../src/daemon';

const cfgBase: KitConfig = {
  gatewayBin: '/opt/shn/gateway',
  discoveryUrl: 'http://127.0.0.1:9001/discovery',
  accountsUrl: 'http://127.0.0.1:9002',
  uiDir: '/opt/shn/ui',
  kitdBin: '/opt/shn/shnkitd',
};

describe('buildArgs', () => {
  it('maps every set field to its exact flag and omits unset optionals', () => {
    const args = buildArgs(cfgBase, '/state');
    expect(args).toEqual([
      '--gateway-bin', '/opt/shn/gateway',
      '--discovery-url', 'http://127.0.0.1:9001/discovery',
      '--accounts', 'http://127.0.0.1:9002',
      '--ui-dir', '/opt/shn/ui',
      '--api-addr', '127.0.0.1:0',
      '--state-dir', '/state',
    ]);
  });

  it('includes every optional flag when set, in the documented order', () => {
    const cfg: KitConfig = {
      ...cfgBase,
      auditUrl: 'http://a',
      phgUrl: 'http://p',
      consentUrl: 'http://c',
      fhirDataUrl: 'http://f',
      patientAppUrl: 'http://pa',
      javaAssets: '/opt/shn/java',
      jreDir: '/opt/shn/java/jre-darwin-arm64',
      manifest: '/opt/shn/versions.json',
      releasesUrl: 'https://api.github.com/repos/SmartHealthNetwork/shn-kit/releases/latest',
      apiAddr: '127.0.0.1:5555',
    };
    const args = buildArgs(cfg, '/state');
    expect(args).toEqual([
      '--gateway-bin', '/opt/shn/gateway',
      '--discovery-url', 'http://127.0.0.1:9001/discovery',
      '--accounts', 'http://127.0.0.1:9002',
      '--audit-url', 'http://a',
      '--phg-url', 'http://p',
      '--consent-url', 'http://c',
      '--fhir-data-url', 'http://f',
      '--patient-app-url', 'http://pa',
      '--java-assets', '/opt/shn/java',
      '--jre-dir', '/opt/shn/java/jre-darwin-arm64',
      '--manifest', '/opt/shn/versions.json',
      '--releases-url', 'https://api.github.com/repos/SmartHealthNetwork/shn-kit/releases/latest',
      '--ui-dir', '/opt/shn/ui',
      '--api-addr', '127.0.0.1:5555',
      '--state-dir', '/state',
    ]);
  });

  it('secretsDir set + accountsUrl unset -> no --accounts flag, --secrets instead', () => {
    const cfg: KitConfig = { ...cfgBase, accountsUrl: undefined, secretsDir: '/opt/shn/secrets' };
    const args = buildArgs(cfg, '/state');
    expect(args).not.toContain('--accounts');
    const idx = args.indexOf('--secrets');
    expect(idx).toBeGreaterThanOrEqual(0);
    expect(args[idx + 1]).toBe('/opt/shn/secrets');
  });

  it('pass-through: omits --java-assets/--jre-dir/--manifest/--releases-url when unset', () => {
    const args = buildArgs(cfgBase, '/state');
    expect(args).not.toContain('--java-assets');
    expect(args).not.toContain('--jre-dir');
    expect(args).not.toContain('--manifest');
    expect(args).not.toContain('--releases-url');
  });

  it('throws naming the field when gatewayBin/uiDir is missing (resolved upstream by main.ts, not defaulted here)', () => {
    expect(() => buildArgs({ ...cfgBase, gatewayBin: undefined }, '/state')).toThrowError(/gatewayBin/);
    expect(() => buildArgs({ ...cfgBase, uiDir: undefined }, '/state')).toThrowError(/uiDir/);
  });
});

// instantDelay never actually waits — every daemon test is hermetic/instant.
const instantDelay = (_ms: number): Promise<void> => Promise.resolve();

describe('waitForSession', () => {
  it('resolves once readFile stops throwing (ENOENT) and returns valid JSON', async () => {
    let calls = 0;
    const readFile = (_p: string): string => {
      calls += 1;
      if (calls < 3) {
        throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
      }
      return JSON.stringify({ api: 'http://127.0.0.1:4000', token: 'abc123' });
    };
    const session = await waitForSession('/state', { readFile, delay: instantDelay, deadlineMs: 5000 });
    expect(session).toEqual({ api: 'http://127.0.0.1:4000', token: 'abc123' });
    expect(calls).toBe(3);
  });

  it('keeps polling through malformed JSON until valid JSON appears', async () => {
    let calls = 0;
    const readFile = (_p: string): string => {
      calls += 1;
      if (calls < 3) {
        return '{not valid json';
      }
      return JSON.stringify({ api: 'http://127.0.0.1:4000', token: 'tok' });
    };
    const session = await waitForSession('/state', { readFile, delay: instantDelay, deadlineMs: 5000 });
    expect(session).toEqual({ api: 'http://127.0.0.1:4000', token: 'tok' });
  });

  it('rejects with a message naming session.json once the deadline is exceeded', async () => {
    const readFile = (_p: string): string => {
      throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
    };
    await expect(
      waitForSession('/state', { readFile, delay: instantDelay, deadlineMs: 100 }),
    ).rejects.toThrowError(/session\.json/);
  });
});

// FakeChild is the injectable process double. `exitOnSigterm`/`exitOnSigkill`
// control whether kill() synchronously fires the registered exit callback —
// how each test row models a cooperative vs. a stubborn child.
class FakeChild implements ChildLike {
  pid: number;
  killed: Array<'SIGTERM' | 'SIGKILL'> = [];
  private cb: ((code: number | null) => void) | null = null;
  exitOnSigterm = false;
  exitOnSigkill = true;

  constructor(pid: number) {
    this.pid = pid;
  }

  kill(sig: 'SIGTERM' | 'SIGKILL'): boolean {
    this.killed.push(sig);
    if (sig === 'SIGTERM' && this.exitOnSigterm) this.simulateExit(0);
    if (sig === 'SIGKILL' && this.exitOnSigkill) this.simulateExit(null);
    return true;
  }

  onExit(cb: (code: number | null) => void): void {
    this.cb = cb;
  }

  simulateExit(code: number | null): void {
    this.cb?.(code);
  }
}

interface Harness {
  manager: DaemonManager;
  calls: string[];
  children: FakeChild[];
  sessionFiles: Record<string, string>;
  removed: string[];
}

function makeHarness(): Harness {
  const calls: string[] = [];
  const children: FakeChild[] = [];
  const sessionFiles: Record<string, string> = {};
  const removed: string[] = [];
  let pidCounter = 100;

  const manager = new DaemonManager({
    spawn: (_cmd: string, _args: string[]): ChildLike => {
      calls.push('spawn');
      const child = new FakeChild(++pidCounter);
      children.push(child);
      // Simulate the real shnkitd process writing a fresh session.json
      // shortly after spawn (before serving) — every harness-driven test
      // that doesn't override spawn relies on this so waitForSession
      // resolves instead of polling to its deadline.
      sessionFiles['/state/session.json'] = JSON.stringify({
        api: `http://127.0.0.1:${4000 + pidCounter}`,
        token: `tok-${pidCounter}`,
      });
      return child;
    },
    readFile: (p: string): string => {
      if (!(p in sessionFiles)) {
        throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
      }
      return sessionFiles[p];
    },
    rm: (p: string): void => {
      calls.push('rm');
      removed.push(p);
      delete sessionFiles[p];
    },
    delay: instantDelay,
  });

  return { manager, calls, children, sessionFiles, removed };
}

const sessionPath = (stateDir: string) => `${stateDir}/session.json`;

describe('DaemonManager', () => {
  it('start() removes a stale session.json before spawn, then resolves the fresh session', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({ api: 'http://stale', token: 'stale-token' });

    // Once spawn() runs, the real shnkitd would overwrite session.json with a
    // fresh one; simulate that by writing the fresh contents synchronously
    // inside the spawn fake itself (spawn is called before rm's deletion is
    // observed by readFile — the rm-then-spawn ORDER is what row 6 is about).
    const manager = new DaemonManager({
      spawn: (_cmd, _args) => {
        h.calls.push('spawn');
        h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({ api: 'http://fresh:1234', token: 'fresh-token' });
        const child = new FakeChild(101);
        h.children.push(child);
        return child;
      },
      readFile: (p) => {
        if (!(p in h.sessionFiles)) throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
        return h.sessionFiles[p];
      },
      rm: (p) => {
        h.calls.push('rm');
        h.removed.push(p);
        delete h.sessionFiles[p];
      },
      delay: instantDelay,
    });

    const session = await manager.start(cfgBase, stateDir);

    expect(session).toEqual({ api: 'http://fresh:1234', token: 'fresh-token' });
    expect(h.calls.indexOf('rm')).toBeLessThan(h.calls.indexOf('spawn'));
    expect(h.removed).toContain(sessionPath(stateDir));
    expect(manager.running).toBe(true);
  });

  it('start(): if waitForSession rejects after spawn succeeds (session.json never appears), the spawned child is killed before start() rejects', async () => {
    const stateDir = '/state';
    let child: FakeChild | undefined;
    const manager = new DaemonManager({
      spawn: (_cmd, _args): ChildLike => {
        child = new FakeChild(500);
        child.exitOnSigterm = true; // cooperative: dies on SIGTERM, no SIGKILL needed
        return child;
      },
      readFile: (_p: string): string => {
        throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
      },
      rm: () => {},
      delay: instantDelay,
    });

    await expect(manager.start(cfgBase, stateDir)).rejects.toThrow(/session\.json/);

    expect(child).toBeDefined();
    expect(child!.killed).toContain('SIGTERM');
    expect(manager.running).toBe(false);
  });

  it('stop(): SIGTERM child that exits promptly -> no SIGKILL', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({ api: 'http://a', token: 't1' });
    await h.manager.start(cfgBase, stateDir);
    const child = h.children[0];
    child.exitOnSigterm = true;

    await h.manager.stop();

    expect(child.killed).toEqual(['SIGTERM']);
    expect(h.manager.running).toBe(false);
  });

  it('stop(): child that never exits on SIGTERM -> SIGKILL after the bounded wait', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({ api: 'http://a', token: 't1' });
    await h.manager.start(cfgBase, stateDir);
    const child = h.children[0];
    child.exitOnSigterm = false; // stubborn: ignores SIGTERM
    child.exitOnSigkill = true; // but SIGKILL does terminate it

    await h.manager.stop();

    expect(child.killed).toEqual(['SIGTERM', 'SIGKILL']);
    expect(h.manager.running).toBe(false);
  });

  it('stop() on an already-exited child resolves without signaling', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({ api: 'http://a', token: 't1' });
    await h.manager.start(cfgBase, stateDir);
    const child = h.children[0];
    child.simulateExit(1); // the child died on its own before stop() is ever called

    await h.manager.stop();

    expect(child.killed).toEqual([]);
  });

  it('restart() = stop then start; a second session with a different port/token is returned', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    let generation = 0;
    const manager = new DaemonManager({
      spawn: (_cmd, _args) => {
        generation += 1;
        h.calls.push('spawn');
        h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({
          api: `http://127.0.0.1:${4000 + generation}`,
          token: `token-${generation}`,
        });
        const child = new FakeChild(200 + generation);
        child.exitOnSigterm = true;
        h.children.push(child);
        return child;
      },
      readFile: (p) => {
        if (!(p in h.sessionFiles)) throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
        return h.sessionFiles[p];
      },
      rm: (p) => {
        h.calls.push('rm');
        delete h.sessionFiles[p];
      },
      delay: instantDelay,
    });

    const first = await manager.start(cfgBase, stateDir);
    const second = await manager.restart(cfgBase, stateDir);

    expect(first).toEqual({ api: 'http://127.0.0.1:4001', token: 'token-1' });
    expect(second).toEqual({ api: 'http://127.0.0.1:4002', token: 'token-2' });
    expect(second).not.toEqual(first);
  });

  it('deliberate-stop fencing: stop()/restart() exit does NOT fire onUnexpectedExit; an exit with no stop in progress DOES', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    let generation = 0;
    const manager = new DaemonManager({
      spawn: (_cmd, _args) => {
        generation += 1;
        h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({
          api: `http://127.0.0.1:${5000 + generation}`,
          token: `tok-${generation}`,
        });
        const child = new FakeChild(300 + generation);
        child.exitOnSigterm = true;
        h.children.push(child);
        return child;
      },
      readFile: (p) => {
        if (!(p in h.sessionFiles)) throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
        return h.sessionFiles[p];
      },
      rm: () => {},
      delay: instantDelay,
    });

    const unexpected = vi.fn();
    manager.onUnexpectedExit(unexpected);

    await manager.start(cfgBase, stateDir);
    await manager.stop(); // deliberate: must NOT fire onUnexpectedExit
    expect(unexpected).not.toHaveBeenCalled();

    await manager.start(cfgBase, stateDir);
    await manager.restart(cfgBase, stateDir); // deliberate stop-then-start: must NOT fire either
    expect(unexpected).not.toHaveBeenCalled();

    // Now an exit with no stop in progress: the CURRENT child dies on its own.
    const current = h.children[h.children.length - 1];
    current.simulateExit(137);
    expect(unexpected).toHaveBeenCalledWith(137);
    expect(unexpected).toHaveBeenCalledTimes(1);
  });

  it('restart() coalescing: two concurrent calls -> spawn called exactly twice total, both resolve to the same session', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    let generation = 0;
    const manager = new DaemonManager({
      spawn: (_cmd, _args) => {
        generation += 1;
        h.sessionFiles[sessionPath(stateDir)] = JSON.stringify({
          api: `http://127.0.0.1:${6000 + generation}`,
          token: `tok-${generation}`,
        });
        const child = new FakeChild(400 + generation);
        child.exitOnSigterm = true;
        h.children.push(child);
        return child;
      },
      readFile: (p) => {
        if (!(p in h.sessionFiles)) throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
        return h.sessionFiles[p];
      },
      rm: () => {},
      delay: instantDelay,
    });

    await manager.start(cfgBase, stateDir); // generation 1 (spawn #1)

    const p1 = manager.restart(cfgBase, stateDir);
    const p2 = manager.restart(cfgBase, stateDir);

    const [s1, s2] = await Promise.all([p1, p2]);

    expect(s1).toBe(s2); // literally the same resolved value/object
    expect(generation).toBe(2); // one initial spawn + exactly one coalesced restart spawn
  });

  // Stale-generation exit-callback guard.
  it('stale-generation guard: firing the FIRST generation\'s exit callback after a second start() has no effect', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    const unexpected = vi.fn();
    h.manager.onUnexpectedExit(unexpected);

    await h.manager.start(cfgBase, stateDir); // generation 1
    const firstChild = h.children[0];

    await h.manager.start(cfgBase, stateDir); // generation 2 supersedes generation 1
    expect(h.manager.running).toBe(true);

    // The superseded generation 1's process finally exits late (e.g. a slow
    // OS reap of an orphaned child) — its registered exit callback must be a
    // no-op: it must not clear `current` (which now tracks generation 2) and
    // must not fire onUnexpectedExit for a generation nobody tracks anymore.
    firstChild.simulateExit(1);

    expect(unexpected).not.toHaveBeenCalled();
    expect(h.manager.running).toBe(true);
  });

  // Coalesced-restart both-reject consistency.
  it('restart() coalescing failure: two concurrent restart() calls whose start() rejects both reject with the SAME error; state stays consistent; a later restart() works', async () => {
    const stateDir = '/state';
    const sessionFiles: Record<string, string> = {};
    const children: FakeChild[] = [];
    let spawnCalls = 0;
    const manager = new DaemonManager({
      spawn: (_cmd, _args) => {
        spawnCalls += 1;
        if (spawnCalls === 2) {
          // The binary vanishes between the initial start() and the restart
          // attempt (e.g. an install got corrupted mid-session).
          throw new Error('spawn ENOENT: shn-gateway missing');
        }
        const child = new FakeChild(700 + spawnCalls);
        child.exitOnSigterm = true;
        children.push(child);
        sessionFiles[sessionPath(stateDir)] = JSON.stringify({
          api: `http://127.0.0.1:${7000 + spawnCalls}`,
          token: `tok-${spawnCalls}`,
        });
        return child;
      },
      readFile: (p: string) => {
        if (!(p in sessionFiles)) throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
        return sessionFiles[p];
      },
      rm: (p: string) => {
        delete sessionFiles[p];
      },
      delay: instantDelay,
    });

    await manager.start(cfgBase, stateDir); // generation 1 (spawnCalls === 1)

    const p1 = manager.restart(cfgBase, stateDir);
    const p2 = manager.restart(cfgBase, stateDir);

    const err1 = await p1.catch((e: unknown) => e as Error);
    const err2 = await p2.catch((e: unknown) => e as Error);

    expect(err1.message).toMatch(/spawn ENOENT/);
    expect(err1).toBe(err2); // both callers coalesced onto the one restart() — the identical rejection
    expect(manager.running).toBe(false); // state is consistent, not stuck "running" on a dead generation

    // A subsequent restart() (spawn now succeeds, spawnCalls === 3) works.
    const session = await manager.restart(cfgBase, stateDir);
    expect(session).toEqual({ api: 'http://127.0.0.1:7003', token: 'tok-3' });
    expect(manager.running).toBe(true);
  });
});

// SessionStore + handleRestartRequest.
describe('SessionStore', () => {
  it('get() returns the current session; after markDead(), get() throws that error (renderer\'s next getSession rejects cleanly)', () => {
    const store = new SessionStore();
    const session: Session = { api: 'http://127.0.0.1:4000', token: 'tok' };
    store.set(session);
    expect(store.get()).toEqual(session);

    store.markDead(new Error('daemon crashed'));
    expect(() => store.get()).toThrowError(/daemon crashed/);
  });

  it('a later set() after markDead() revives the store (a successful restart clears the dead state)', () => {
    const store = new SessionStore();
    store.markDead(new Error('boom'));
    expect(() => store.get()).toThrow();

    const session: Session = { api: 'http://127.0.0.1:4000', token: 'tok' };
    store.set(session);
    expect(store.get()).toEqual(session);
  });
});

describe('handleRestartRequest', () => {
  it('on success: sets the store to the new session and returns it; reportError is never called', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    await h.manager.start(cfgBase, stateDir);
    const store = new SessionStore();
    store.set({ api: 'http://old', token: 'old' });
    const reported: Error[] = [];

    const session = await handleRestartRequest(h.manager, cfgBase, stateDir, store, (e) => reported.push(e));

    expect(session).toEqual(store.get());
    expect(reported).toEqual([]);
  });

  it('on rejection: marks the store dead, reports the error, and rethrows — a subsequent store.get() rejects cleanly, no stale-content limbo', async () => {
    const stateDir = '/state';
    const manager = new DaemonManager({
      spawn: () => {
        throw new Error('spawn ENOENT: shnkitd missing');
      },
      readFile: () => {
        throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
      },
      rm: () => {},
      delay: instantDelay,
    });
    const store = new SessionStore();
    const staleSession: Session = { api: 'http://stale', token: 'stale-token' };
    store.set(staleSession); // a previously-good session, now about to go stale

    const reported: Error[] = [];
    await expect(
      handleRestartRequest(manager, cfgBase, stateDir, store, (e) => reported.push(e)),
    ).rejects.toThrowError(/spawn ENOENT/);

    expect(reported).toHaveLength(1);
    expect(reported[0].message).toMatch(/spawn ENOENT/);
    // The store must NOT keep serving the stale (now-dead) session.
    expect(() => store.get()).toThrowError(/spawn ENOENT/);
  });
});

// wireUnexpectedExit's second-dialog latch.
describe('wireUnexpectedExit', () => {
  it('second-dialog latch: a second unexpected exit while the first dialog is still open coalesces — only one dialog is ever shown', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    await h.manager.start(cfgBase, stateDir); // generation 1

    let resolveDialog: ((v: { restart: boolean }) => void) | undefined;
    const showDialog = vi.fn(
      () =>
        new Promise<{ restart: boolean }>((resolve) => {
          resolveDialog = resolve;
        }),
    );
    const restart = vi.fn(async (): Promise<Session> => ({ api: 'http://new', token: 'new' }));
    const onRestarted = vi.fn();
    const showFatalError = vi.fn();
    const quit = vi.fn();

    wireUnexpectedExit(h.manager, { showDialog, restart, onRestarted, showFatalError, quit });

    h.children[0].simulateExit(1); // generation 1 crashes -> dialog opens (pending, unresolved)
    expect(showDialog).toHaveBeenCalledTimes(1);

    // A second generation exists and ALSO crashes before the first dialog's
    // promise has resolved (e.g. a race with an independent restart) — must
    // coalesce onto the already-open prompt, not stack a second dialog.
    await h.manager.start(cfgBase, stateDir); // generation 2
    h.children[1].simulateExit(1);

    expect(showDialog).toHaveBeenCalledTimes(1); // still just the one

    resolveDialog!({ restart: true });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(restart).toHaveBeenCalledTimes(1);
    expect(onRestarted).toHaveBeenCalledWith({ api: 'http://new', token: 'new' });
    expect(showFatalError).not.toHaveBeenCalled();
    expect(quit).not.toHaveBeenCalled();
  });

  it('Quit response: showDialog resolving {restart:false} calls quit(), never restart()', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    await h.manager.start(cfgBase, stateDir);

    const showDialog = vi.fn(async () => ({ restart: false }));
    const restart = vi.fn(async (): Promise<Session> => ({ api: 'http://new', token: 'new' }));
    const onRestarted = vi.fn();
    const showFatalError = vi.fn();
    const quit = vi.fn();

    wireUnexpectedExit(h.manager, { showDialog, restart, onRestarted, showFatalError, quit });

    h.children[0].simulateExit(1);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(restart).not.toHaveBeenCalled();
    expect(quit).toHaveBeenCalledTimes(1);
  });

  it('a restart-from-dialog failure surfaces a fatal error and quits', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    await h.manager.start(cfgBase, stateDir);

    const showDialog = vi.fn(async () => ({ restart: true }));
    const restart = vi.fn(async (): Promise<Session> => {
      throw new Error('restart failed: binary missing');
    });
    const onRestarted = vi.fn();
    const showFatalError = vi.fn();
    const quit = vi.fn();

    wireUnexpectedExit(h.manager, { showDialog, restart, onRestarted, showFatalError, quit });

    h.children[0].simulateExit(1);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(onRestarted).not.toHaveBeenCalled();
    expect(showFatalError).toHaveBeenCalledWith(expect.stringMatching(/restart failed/));
    expect(quit).toHaveBeenCalledTimes(1);
  });

  it('re-latches after the first dialog fully resolves: a THIRD, later crash opens a new dialog', async () => {
    const h = makeHarness();
    const stateDir = '/state';
    await h.manager.start(cfgBase, stateDir);

    const showDialog = vi.fn(async () => ({ restart: false }));
    const restart = vi.fn(async (): Promise<Session> => ({ api: 'http://new', token: 'new' }));
    const onRestarted = vi.fn();
    const showFatalError = vi.fn();
    const quit = vi.fn();

    wireUnexpectedExit(h.manager, { showDialog, restart, onRestarted, showFatalError, quit });

    h.children[0].simulateExit(1);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(showDialog).toHaveBeenCalledTimes(1);
    expect(quit).toHaveBeenCalledTimes(1);

    // A later, independent generation crashes after the first dialog fully
    // resolved — this is a NEW incident, not a coalesced one, so it gets its
    // own dialog.
    await h.manager.start(cfgBase, stateDir);
    h.children[1].simulateExit(1);
    await Promise.resolve();

    expect(showDialog).toHaveBeenCalledTimes(2);
  });
});
