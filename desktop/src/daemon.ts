// daemon.ts is pure (no electron import): it owns shnkitd process-lifecycle
// logic (arg building, session-file polling, spawn/stop/restart) behind an
// injectable io seam (spawn/readFile/rm/delay), so it is unit-testable in a
// plain node environment with fakes — no real child process, no real clock.
import * as path from 'node:path';
import type { KitConfig } from './config';

export interface Session {
  api: string;
  token: string;
}

/** Maps every set KitConfig field to shnkitd's exact CLI flag
 * (kit/cmd/shnkitd/main.go), omitting unset optionals. `gatewayBin`/`uiDir`
 * are resolved (packaged-path defaults or a dev.config.json value) by
 * main.ts's resolve* helpers before this is ever called — buildArgs itself
 * just asserts that already happened, mirroring DaemonManager.start's own
 * `cfg.kitdBin` check below. */
export function buildArgs(cfg: KitConfig, stateDir: string): string[] {
  if (!cfg.gatewayBin) {
    throw new Error('buildArgs: cfg.gatewayBin is required');
  }
  if (!cfg.uiDir) {
    throw new Error('buildArgs: cfg.uiDir is required');
  }
  const args: string[] = ['--gateway-bin', cfg.gatewayBin, '--discovery-url', cfg.discoveryUrl];
  if (cfg.accountsUrl) args.push('--accounts', cfg.accountsUrl);
  if (cfg.secretsDir) args.push('--secrets', cfg.secretsDir);
  if (cfg.auditUrl) args.push('--audit-url', cfg.auditUrl);
  if (cfg.phgUrl) args.push('--phg-url', cfg.phgUrl);
  if (cfg.consentUrl) args.push('--consent-url', cfg.consentUrl);
  if (cfg.fhirDataUrl) args.push('--fhir-data-url', cfg.fhirDataUrl);
  if (cfg.patientAppUrl) args.push('--patient-app-url', cfg.patientAppUrl);
  // Packaged-mode pass-through rows: each mirrors shnkitd's own flag 1:1
  // (kit/cmd/shnkitd/main.go); omitted whenever unset, same as every
  // optional row above.
  if (cfg.javaAssets) args.push('--java-assets', cfg.javaAssets);
  if (cfg.jreDir) args.push('--jre-dir', cfg.jreDir);
  if (cfg.manifest) args.push('--manifest', cfg.manifest);
  if (cfg.releasesUrl) args.push('--releases-url', cfg.releasesUrl);
  args.push('--ui-dir', cfg.uiDir);
  args.push('--api-addr', cfg.apiAddr ?? '127.0.0.1:0');
  args.push('--state-dir', stateDir);
  return args;
}

const SESSION_POLL_INTERVAL_MS = 50;

/**
 * Polls {stateDir}/session.json until it parses to a valid Session, or
 * rejects once io.deadlineMs of (simulated) elapsed time has passed. Elapsed
 * time is tracked as the sum of the ms handed to io.delay — not the wall
 * clock — so this is hermetic under an instant fake delay in tests.
 */
export async function waitForSession(
  stateDir: string,
  io: { readFile(p: string): string; delay(ms: number): Promise<void>; deadlineMs: number },
): Promise<Session> {
  const sessionPath = path.join(stateDir, 'session.json');
  let elapsedMs = 0;
  for (;;) {
    try {
      const raw = io.readFile(sessionPath);
      try {
        const parsed = JSON.parse(raw) as Partial<Session>;
        if (typeof parsed.api === 'string' && typeof parsed.token === 'string') {
          return { api: parsed.api, token: parsed.token };
        }
        // Parsed but missing/wrong-typed fields: keep polling.
      } catch {
        // Malformed JSON (e.g. a partial write in progress): keep polling.
      }
    } catch {
      // readFile threw (ENOENT — not written yet): keep polling.
    }
    if (elapsedMs >= io.deadlineMs) {
      throw new Error(`waitForSession: ${sessionPath} did not appear/parse within ${io.deadlineMs}ms`);
    }
    await io.delay(SESSION_POLL_INTERVAL_MS);
    elapsedMs += SESSION_POLL_INTERVAL_MS;
  }
}

/** ChildLike is the injectable process handle DaemonManager spawns and
 * supervises — a thin seam over node's ChildProcess so daemon.ts stays
 * electron/node-child_process-free for testing. `pid` mirrors Node's own
 * `ChildProcess.pid` (possibly `undefined` if the spawn itself never
 * surfaced one) — killGeneration's Windows tree-kill path needs it to target
 * `taskkill`. */
export interface ChildLike {
  pid: number | undefined;
  kill(sig: 'SIGTERM' | 'SIGKILL'): boolean;
  /** Registers this generation's exit callback — one registration per spawned
   * child, not a constructor-level singleton, so a stale generation's
   * callback can never fire for a later child. */
  onExit(cb: (code: number | null) => void): void;
}

export interface DaemonIO {
  spawn(cmd: string, args: string[]): ChildLike;
  /** Runs a command to completion (stdout/stderr ignored) without supervising
   * it as a generation — the seam killGeneration's Windows path uses to run
   * `taskkill /T /F` (see below), kept injectable so tests never spawn a real
   * subprocess. */
  run(cmd: string, args: string[]): Promise<void>;
  readFile(p: string): string;
  rm(p: string): void;
  delay(ms: number): Promise<void>;
}

// The session.json wait budget (waitForSession's deadlineMs) — generous
// because it bounds shnkitd's own boot (BuildStack + every child's ready
// probe), not just its process spawn.
const SESSION_WAIT_DEADLINE_MS = 30_000;
// SIGTERM grace period before escalating to SIGKILL (stop()), macOS/Linux
// only (killGeneration's Windows path doesn't wait on this — see below). Must
// cover, in series, kitd.Serve's own bounded srv.Shutdown (up to ~5s) THEN
// sup.StopAll()'s sequential reap of the gateway + 3 Java children (each
// individually bounded, kit/supervisor/supervisor.go's Stop, up to ~3s
// apiece before it force-kills that one child and moves on). 10s left thin
// margin against that combined worst case; 15s is comfortable. This is only
// the MAX wait — a fast, cooperative quit still resolves as soon as
// shnkitd's own exit fires, never after the full grace.
const STOP_GRACE_MS = 15_000;

/** One spawned shnkitd generation: its ChildLike handle, a promise that
 * resolves once it has actually exited, and whether that exit is expected
 * (a deliberate stop()/restart(), which must NOT fire
 * onUnexpectedExit). */
interface Generation {
  child: ChildLike;
  exited: Promise<void>;
  deliberate: boolean;
}

/**
 * DaemonManager owns the shnkitd child-process lifecycle: start (rm stale
 * session.json, spawn, wait for the fresh one), stop (SIGTERM, bounded wait,
 * SIGKILL), and restart (stop then start, coalesced across concurrent
 * callers). Every method here is pure w.r.t. the injected io seam — no
 * timers or child_process calls of its own.
 */
export class DaemonManager {
  private readonly io: DaemonIO;
  private current: Generation | null = null;
  private unexpectedExitCb: ((code: number | null) => void) | null = null;
  private restartPromise: Promise<Session> | null = null;

  constructor(io: DaemonIO) {
    this.io = io;
  }

  get running(): boolean {
    return this.current !== null;
  }

  /** Fires only for an exit outside a deliberate stop()/restart() — a
   * genuine crash. Deliberate stops are fenced per generation. */
  onUnexpectedExit(cb: (code: number | null) => void): void {
    this.unexpectedExitCb = cb;
  }

  async start(cfg: KitConfig, stateDir: string): Promise<Session> {
    const sessionPath = path.join(stateDir, 'session.json');
    // Remove a stale session.json BEFORE spawn: shnkitd writes a fresh one
    // before serving, but a leftover file from a prior generation could
    // otherwise be misread as already-current during the race window between
    // spawn and the new write.
    try {
      this.io.rm(sessionPath);
    } catch {
      // Nothing to remove — fine.
    }

    if (!cfg.kitdBin) {
      throw new Error('DaemonManager.start: cfg.kitdBin is required');
    }
    const args = buildArgs(cfg, stateDir);
    const child = this.io.spawn(cfg.kitdBin, args);

    let markExited: () => void = () => {};
    const exited = new Promise<void>((resolve) => {
      markExited = resolve;
    });
    const generation: Generation = { child, exited, deliberate: false };
    this.current = generation;

    child.onExit((code) => {
      if (this.current !== generation) return; // already superseded/handled
      this.current = null;
      markExited();
      if (!generation.deliberate) {
        this.unexpectedExitCb?.(code);
      }
    });

    try {
      return await waitForSession(stateDir, {
        readFile: this.io.readFile,
        delay: this.io.delay,
        deadlineMs: SESSION_WAIT_DEADLINE_MS,
      });
    } catch (err) {
      // waitForSession rejected (deadline, or shnkitd wrote a session.json
      // that never parses) even though spawn() itself succeeded — without
      // this, the spawned process is orphaned:
      // start() throws but the child keeps running, unsupervised, holding
      // whatever ports/state it already claimed. Kill it (same bounded
      // SIGTERM->SIGKILL escalation as stop()) before rethrowing so a failed
      // start() never leaks a live child.
      if (this.current === generation) {
        generation.deliberate = true;
        await this.killGeneration(generation);
      }
      throw err;
    }
  }

  /** Shared shutdown path for both stop() and start()'s failure path
   * (finding 3) so the two don't drift — platform-aware since Windows has no
   * POSIX signal delivery to shnkitd. */
  private async killGeneration(generation: Generation): Promise<void> {
    if (process.platform === 'win32') {
      await this.killGenerationWindows(generation);
      return;
    }

    // macOS/Linux: a real SIGTERM reaches shnkitd's own signal.Notify handler,
    // which runs sup.StopAll() to reap the gateway + Java trio before it
    // exits — bounded-wait, then escalate to SIGKILL only if it doesn't.
    generation.child.kill('SIGTERM');

    const timedOut = Symbol('timeout');
    const result = await Promise.race([
      generation.exited.then(() => 'exited' as const),
      this.io.delay(STOP_GRACE_MS).then(() => timedOut),
    ]);
    if (result === timedOut && this.current === generation) {
      generation.child.kill('SIGKILL');
      await generation.exited;
    }
  }

  /** Windows has no POSIX signal delivery: `child.kill('SIGTERM')` maps to
   * `TerminateProcess`, which hard-kills shnkitd WITHOUT running its
   * signal.Notify handler — sup.StopAll() never runs, so the Java trio + the
   * gateway are orphaned and keep the validator's H2 file locked (the next
   * boot's validator then can't open it). There is also no process-group/Job
   * Object in this codebase to make a plain kill cascade to children.
   *
   * Instead, kill the whole shnkitd process TREE directly:
   * `taskkill /PID <pid> /T /F` (`/T` = terminate the tree, taking the Java
   * trio + gateway down with it; `/F` = force) — this releases the H2 lock
   * the same way a graceful SIGTERM-driven StopAll() does on macOS/Linux,
   * without ever needing shnkitd to run its own reap logic. */
  private async killGenerationWindows(generation: Generation): Promise<void> {
    const pid = generation.child.pid;
    if (typeof pid === 'number') {
      try {
        await this.io.run('taskkill', ['/PID', String(pid), '/T', '/F']);
      } catch {
        // taskkill itself failed to even run (e.g. missing from PATH) —
        // fall back to a direct hard-kill of shnkitd; it won't reap the
        // tree, but it's the best available fallback.
        generation.child.kill('SIGKILL');
      }
    } else {
      // No pid to target (shouldn't happen — the real adapter always
      // surfaces Node's ChildProcess.pid) — fall back to a direct hard-kill.
      generation.child.kill('SIGKILL');
    }
    await generation.exited;
  }

  async stop(): Promise<void> {
    const generation = this.current;
    if (!generation) return; // nothing running (or already exited) — no signal to send
    generation.deliberate = true;
    await this.killGeneration(generation);
  }

  /**
   * stop() then start(). Concurrent restart() calls while one is already in
   * flight coalesce onto the SAME in-flight promise — a double-clicked
   * Restart runs exactly one stop/start cycle, and every caller resolves to
   * the same session.
   */
  async restart(cfg: KitConfig, stateDir: string): Promise<Session> {
    if (this.restartPromise) return this.restartPromise;
    const p = (async () => {
      try {
        await this.stop();
        return await this.start(cfg, stateDir);
      } finally {
        this.restartPromise = null;
      }
    })();
    this.restartPromise = p;
    return p;
  }
}

/**
 * Holds the renderer-visible "current session" — extracted from main.ts so
 * it's electron-free/unit-testable. `get()` THROWS once the session has been
 * marked dead (a failed restart, fold c below) instead of silently handing
 * back the last-known-good session: the renderer's next `kit:session` IPC
 * call rejects cleanly rather than loading stale content against a daemon
 * that is no longer there (no stale-content limbo).
 */
export class SessionStore {
  private session: Session | null = null;
  private deadError: Error | null = null;

  set(session: Session): void {
    this.session = session;
    this.deadError = null;
  }

  markDead(err: Error): void {
    this.session = null;
    this.deadError = err;
  }

  get(): Session {
    if (!this.session) {
      throw this.deadError ?? new Error('SessionStore: no session');
    }
    return this.session;
  }
}

/**
 * Extracted from main.ts's `kit:restart` IPC handler (electron-free, so it's
 * unit-testable with fakes): on success, records the new session. On
 * rejection — the underlying manager.restart() throwing — marks the store
 * dead and reports the error via the injected `reportError` (main.ts wires
 * this to `dialog.showErrorBox`), then rethrows so the IPC call itself
 * rejects too. Without the markDead call, the store would keep serving the
 * last-known (now-stale) session forever; without the rethrow, the renderer
 * would believe the restart succeeded.
 */
export async function handleRestartRequest(
  manager: DaemonManager,
  cfg: KitConfig,
  stateDir: string,
  store: SessionStore,
  reportError: (err: Error) => void,
): Promise<Session> {
  try {
    const session = await manager.restart(cfg, stateDir);
    store.set(session);
    return session;
  } catch (err) {
    store.markDead(err as Error);
    reportError(err as Error);
    throw err;
  }
}

/** Injectable, electron-free dependencies for wireUnexpectedExit — main.ts
 * supplies real dialog.showMessageBox/app.quit; tests supply fakes. */
export interface UnexpectedExitDeps {
  showDialog(code: number | null): Promise<{ restart: boolean }>;
  restart(): Promise<Session>;
  onRestarted(session: Session): void;
  showFatalError(message: string): void;
  quit(): void;
}

/**
 * Wires DaemonManager.onUnexpectedExit to a Restart/Quit prompt. Latched
 * (fold d): a second unexpected-exit callback firing while the first
 * prompt's dialog is still open (awaiting the user) coalesces onto the one
 * already in flight rather than stacking a second dialog — the dialog
 * re-opens (or the app re-prompts) only after the current one has fully
 * resolved (its `finally` clears the latch).
 */
export function wireUnexpectedExit(manager: DaemonManager, deps: UnexpectedExitDeps): void {
  let dialogOpen = false;
  manager.onUnexpectedExit((code) => {
    if (dialogOpen) return; // latched: a prompt is already in flight — coalesce
    dialogOpen = true;
    void (async () => {
      try {
        const { restart } = await deps.showDialog(code);
        if (!restart) {
          deps.quit();
          return;
        }
        try {
          const session = await deps.restart();
          deps.onRestarted(session);
        } catch (err) {
          // A restart attempted FROM the unexpected-exit dialog can itself
          // fail — surface it and quit rather than fail silently with no
          // window and no prompt.
          deps.showFatalError((err as Error).message);
          deps.quit();
        }
      } finally {
        dialogOpen = false;
      }
    })();
  });
}

/** Minimal structural shape of Electron's 'before-quit' event — kept
 * electron-free (like the rest of this file) so wireGracefulQuit stays
 * unit-testable with a fake, no real Electron.Event needed. */
export interface QuitEvent {
  preventDefault(): void;
}

/**
 * Returns a 'before-quit' handler that stops the daemon on EVERY quit path
 * (Cmd+Q, dock quit, the crash-dialog Quit, window-all-closed, OS
 * SIGTERM/SIGINT — main.ts routes all of them through app.quit(), which
 * fires 'before-quit') BEFORE Electron actually exits. Without this,
 * shnkitd and its Java trio are orphaned on quit and keep the validator's H2
 * file locked, so the next launch's validator can't open it and the daemon
 * exits 1.
 *
 * Guarded against infinite recursion: the first call prevents the default
 * quit, stops the daemon, then calls deps.quit() again once stop() resolves
 * — which re-fires 'before-quit'. That second call sees `stopping` already
 * true and returns immediately (no preventDefault this time), so Electron's
 * quit actually proceeds. With no daemon running (e.g. an early quit before
 * the daemon ever started), manager.stop() is a safe no-op.
 */
export function wireGracefulQuit(manager: DaemonManager, deps: { quit(): void }): (event: QuitEvent) => void {
  let stopping = false;
  return (event) => {
    if (stopping) return; // second pass, after stop() resolved — let it quit
    stopping = true;
    event.preventDefault();
    void manager.stop().finally(() => deps.quit());
  };
}
