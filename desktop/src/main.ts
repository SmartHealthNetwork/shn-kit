// main.ts is thin wiring (Electron imports only here, keeping to the
// electron-free-testable-logic split): every step below is one
// small function, each delegating real logic to config.ts/daemon.ts, which
// carry their own unit tests. This file's own proof is the manual dev-run
// smoke (desktop/README.md), not a unit test.
import { app, BrowserWindow, dialog, ipcMain, shell } from 'electron';
import { spawn as nodeSpawn, type ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { packagedDefaults, resolveConfig, type KitConfig } from './config';
import {
  DaemonManager,
  SessionStore,
  handleRestartRequest,
  wireGracefulQuit,
  wireUnexpectedExit,
  type ChildLike,
  type Session,
} from './daemon';

// --- Step 1: locate kit.config.json (packaged) / dev.config.json (dev) ---
function defaultConfigPath(): string {
  if (app.isPackaged) {
    return path.join(process.resourcesPath, 'kit.config.json');
  }
  return path.join(__dirname, '..', 'dev.config.json');
}

// --- Step 2: resolve + create the state dir ---
function resolveStateDir(cfg: KitConfig): string {
  const dir = cfg.stateDir ?? path.join(app.getPath('userData'), 'kit');
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

// --- Step 2b: packaged-mode path resolution. Every path
// electron-builder.yml ships under Resources (extraResources: the
// gateway/kitd binaries, the built UI, versions.json, the java/ trio dir)
// resolves from process.resourcesPath here, exactly like kitdBin already
// did — kit.config.json never bakes an absolute install path for any of
// these; that path varies per machine/install location and is only known at
// runtime. Dev mode still requires each explicitly in dev.config.json (there
// is no packaged resourcesPath to default from). ---
function resolveGatewayBin(cfg: KitConfig): string {
  if (cfg.gatewayBin) return cfg.gatewayBin;
  if (app.isPackaged) return packagedDefaults(process.resourcesPath).gatewayBin;
  throw new Error('main: "gatewayBin" is required in dev.config.json (dev has no packaged resourcesPath to default from)');
}

function resolveKitdBin(cfg: KitConfig): string {
  if (cfg.kitdBin) return cfg.kitdBin;
  if (app.isPackaged) return packagedDefaults(process.resourcesPath).kitdBin;
  throw new Error('main: "kitdBin" is required in dev.config.json (dev has no packaged resourcesPath to default from)');
}

function resolveUiDir(cfg: KitConfig): string {
  if (cfg.uiDir) return cfg.uiDir;
  if (app.isPackaged) return packagedDefaults(process.resourcesPath).uiDir;
  throw new Error('main: "uiDir" is required in dev.config.json (dev has no packaged resourcesPath to default from)');
}

// manifest is genuinely optional even packaged (the "" => 404-with-body
// dev posture) but the packaged build always ships versions.json, so packaged
// mode fills it in unless the config file already overrides it.
function resolveManifest(cfg: KitConfig): string | undefined {
  if (cfg.manifest) return cfg.manifest;
  if (app.isPackaged) return packagedDefaults(process.resourcesPath).manifest;
  return undefined;
}

// javaAssets in a packaged kit.config.json is a RELATIVE marker (the
// packaged config "always sets" --java-assets), not an absolute path — the
// real, resourcesPath-rooted directory is resolved here, same as every other
// packaged path above. Unset/empty means no trio (dev/CI default, matching
// shnkitd's own "" => no trio behavior).
function resolveJavaAssets(cfg: KitConfig): string | undefined {
  if (!cfg.javaAssets) return undefined;
  if (app.isPackaged) return packagedDefaults(process.resourcesPath).javaAssets;
  return cfg.javaAssets; // dev: an explicit absolute path a developer points at locally
}

// realSpawn/realRun/realDelay are the only place this module touches a real
// OS process/timer — everything else routes through DaemonManager's
// injectable io seam, exactly as daemon.test.ts fakes it.
function realSpawn(cmd: string, args: string[]): ChildLike {
  const child: ChildProcess = nodeSpawn(cmd, args, { stdio: 'ignore' });
  return {
    pid: child.pid,
    kill(sig: 'SIGTERM' | 'SIGKILL'): boolean {
      return child.kill(sig);
    },
    onExit(cb: (code: number | null) => void): void {
      child.once('exit', (code) => cb(code));
    },
  };
}

// Runs a command to completion without supervising it as a generation — used
// only for killGeneration's Windows tree-kill (`taskkill /T /F`). Resolves
// once the subprocess itself exits (regardless of its exit code — the caller
// only cares that it ran); rejects only if the subprocess never started at
// all (e.g. `taskkill` missing from PATH).
function realRun(cmd: string, args: string[]): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = nodeSpawn(cmd, args, { stdio: 'ignore' });
    child.once('error', reject);
    child.once('exit', () => resolve());
  });
}

function realDelay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function rmIfExists(p: string): void {
  try {
    fs.rmSync(p);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code !== 'ENOENT') throw err;
  }
}

const manager = new DaemonManager({
  spawn: realSpawn,
  run: realRun,
  readFile: (p: string) => fs.readFileSync(p, 'utf8'),
  rm: rmIfExists,
  delay: realDelay,
});

// sessionStore replaces the old bare `let currentSession`: it throws once
// marked dead instead of quietly serving a stale session, so the
// renderer's next `kit:session` IPC call rejects cleanly.
const sessionStore = new SessionStore();

let win: BrowserWindow | null = null;

function currentOrigin(): string | null {
  try {
    return new URL(sessionStore.get().api).origin;
  } catch {
    return null; // no session yet, or the store was marked dead
  }
}

// --- Step 4 (navigation hardening): deny second privileged windows, route
// http(s) target="_blank" out to the OS browser, and confine in-window
// navigation to the current session's own origin. ---
function hardenNavigation(w: BrowserWindow): void {
  w.webContents.setWindowOpenHandler(({ url }) => {
    // A malformed url (new URL() throws) must be treated as deny, not an
    // uncaught exception inside the handler.
    try {
      const u = new URL(url);
      if (u.protocol === 'http:' || u.protocol === 'https:') void shell.openExternal(url);
    } catch {
      // Malformed — nothing to open externally; falls through to deny below.
    }
    return { action: 'deny' }; // never a second privileged window
  });
  w.webContents.on('will-navigate', (e, url) => {
    const origin = currentOrigin();
    try {
      if (!origin || new URL(url).origin !== origin) e.preventDefault();
    } catch {
      e.preventDefault(); // malformed url — deny
    }
  });
}

function createWindow(session: Session): BrowserWindow {
  const w = new BrowserWindow({
    width: 1200,
    height: 820,
    minWidth: 960,
    minHeight: 640,
    title: 'SHN Kit',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  hardenNavigation(w);
  void w.loadURL(session.api + '/ui/');
  return w;
}

// --- Step 5: IPC surface the preload bridge calls into. ---
function wireIPC(cfg: KitConfig, stateDir: string): void {
  ipcMain.handle('kit:session', () => sessionStore.get());

  ipcMain.handle('kit:restart', async () => {
    // handleRestartRequest catches manager.restart() rejection, shows the
    // error dialog, and marks the session store dead (so this handler's
    // own rejection AND every later kit:session call reject cleanly — no
    // stale-content limbo) before rethrowing.
    const session = await handleRestartRequest(manager, cfg, stateDir, sessionStore, (err) =>
      dialog.showErrorBox('SHN Kit', err.message),
    );
    if (win) void win.loadURL(session.api + '/ui/');
    return session;
  });

  ipcMain.handle('kit:open-external', async (_event, url: string) => {
    const u = new URL(url);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') {
      throw new Error(`kit:open-external: refused non-http(s) scheme "${u.protocol}"`);
    }
    await shell.openExternal(url);
  });
}

// --- Step 6: an unexpected daemon exit (never fired during a deliberate
// stop/restart — DaemonManager's per-generation fencing) surfaces a
// Restart/Quit dialog. Latched: a second unexpected exit while the first
// dialog is still open coalesces rather than stacking a second dialog —
// see daemon.ts's wireUnexpectedExit. No auto-respawn loop. ---
function wireCrashDialog(cfg: KitConfig, stateDir: string): void {
  wireUnexpectedExit(manager, {
    showDialog: async (code) => {
      const { response } = await dialog.showMessageBox({
        type: 'error',
        message: 'The Kit daemon stopped unexpectedly',
        detail: code !== null ? `Exit code ${code}.` : 'The process was terminated.',
        buttons: ['Restart', 'Quit'],
        defaultId: 0,
        cancelId: 1,
      });
      return { restart: response === 0 };
    },
    restart: () => manager.restart(cfg, stateDir),
    onRestarted: (session) => {
      sessionStore.set(session);
      if (win) void win.loadURL(session.api + '/ui/');
    },
    showFatalError: (message) => dialog.showErrorBox('SHN Kit', message),
    quit: () => app.quit(),
  });
}

async function main(): Promise<void> {
  // --- Step 0: single-instance guard. A second launch (e.g. an impatient
  // relaunch during the ~2min first boot, which looks hung) must not spawn a
  // second daemon — two shnkitd processes fight over the same on-disk H2
  // database file and the second one dies. requestSingleInstanceLock() must
  // be called before app.whenReady(); a losing second instance quits
  // immediately, before any daemon is spawned. 'second-instance' fires on
  // the winning (first) instance whenever a later launch is attempted —
  // surface the existing window instead of doing nothing. ---
  if (!app.requestSingleInstanceLock()) {
    app.quit();
    return;
  }
  app.on('second-instance', () => {
    if (win) {
      if (win.isMinimized()) win.restore();
      win.focus();
    }
  });

  await app.whenReady();

  // --- Step 1 ---
  let cfg: KitConfig;
  try {
    cfg = resolveConfig(
      (p) => fs.readFileSync(p, 'utf8'),
      process.env as Record<string, string | undefined>,
      defaultConfigPath(),
    );
  } catch (err) {
    dialog.showErrorBox('SHN Kit — configuration error', (err as Error).message);
    app.quit();
    return;
  }

  // --- Step 2 ---
  const stateDir = resolveStateDir(cfg);
  cfg.kitdBin = resolveKitdBin(cfg);
  cfg.gatewayBin = resolveGatewayBin(cfg);
  cfg.uiDir = resolveUiDir(cfg);
  cfg.manifest = resolveManifest(cfg);
  cfg.javaAssets = resolveJavaAssets(cfg);

  // --- Step 3 ---
  let session: Session;
  try {
    session = await manager.start(cfg, stateDir);
  } catch (err) {
    dialog.showErrorBox('SHN Kit — failed to start', (err as Error).message);
    app.quit();
    return;
  }
  sessionStore.set(session);

  // --- Step 4 ---
  win = createWindow(session);

  // --- Step 5 / 6 ---
  wireIPC(cfg, stateDir);
  wireCrashDialog(cfg, stateDir);

  // --- Step 7: stop the daemon on EVERY quit path (Cmd+Q, dock quit, the
  // crash-dialog Quit, window close, OS SIGTERM/SIGINT) BEFORE Electron
  // exits — see wireGracefulQuit's own comment for why this can't be
  // window-all-closed-only (that left every other quit path orphaning
  // shnkitd and its Java trio, which then held the validator's H2 file lock
  // and broke the next launch). ---
  app.on('before-quit', wireGracefulQuit(manager, { quit: () => app.quit() }));
  // An OS SIGTERM/SIGINT (system shutdown, `kill`) must shut down through
  // the same graceful path, not terminate Electron directly and orphan the
  // daemon.
  for (const sig of ['SIGTERM', 'SIGINT'] as const) {
    process.on(sig, () => app.quit());
  }
  app.on('window-all-closed', () => app.quit());
}

void main();
