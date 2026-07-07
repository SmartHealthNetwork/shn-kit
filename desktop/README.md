# SHN Kit desktop shell (`desktop/`)

The Electron thin shell around `shnkitd` (`kit/cmd/shnkitd`): it resolves a `KitConfig`
(`src/config.ts`), spawns/supervises the daemon (`src/daemon.ts`), and opens one
`BrowserWindow` pointed at the daemon's own served UI (`{api}/ui/`). `main.ts`/`preload.ts`
are thin Electron wiring — every testable rule lives in the electron-free `config.ts`/
`daemon.ts` (`npm test`); the manual dev-run below is `main.ts`/`preload.ts`'s own proof
(no Electron in any unit test).

## Dev recipe

From the repo root:

```sh
# 1. Build the gateway binary from the pinned shn-gateway module (the kit depends on it).
(cd kit && go build -o /tmp/shn-gateway github.com/SmartHealthNetwork/shn-gateway/cmd/gateway)

# 2. Build the real shnkitd binary.
cd kit && go build -o /tmp/shnkitd ./cmd/shnkitd && cd ..

# 3. Build the Kit UI renderer.
cd ui/kit && npm run build && cd ../..

# 4. Point dev.config.json at your local paths.
cd desktop
cp dev.config.example.json dev.config.json
# edit dev.config.json: gatewayBin=/tmp/shn-gateway, kitdBin=/tmp/shnkitd,
# uiDir=<repo>/ui/kit/dist, and EITHER:
#   - secretsDir: a pre-provisioned secrets bundle dir (e.g. from `shn register`)
#     — skips sign-in entirely, or
#   - accountsUrl: the Accounts service to sign in against (e.g. the preview
#     portal, https://accounts.shn-preview.org) for a fresh interactive
#     Cognito sign-in.

# 5. Install desktop deps (first run only) and launch.
npm install
npm run dev
```

`npm run dev` runs `tsc -b` then launches Electron against `dist/main.js`. The window
loads `{shnkitd's api}/ui/` once the daemon reports its session (`session.json` in the
state dir — `stateDir` in `dev.config.json`, or `<userData>/kit` if unset).

`SHN_KIT_CONFIG` (env) overrides the default config path, if you want to keep several
named configs around instead of overwriting `dev.config.json`.

### Browser-only debug (no Electron shell)

To iterate on the UI alone without an Electron window: run `shnkitd` directly with a
fixed address/token, start the `ui/kit` Vite dev server (which proxies `/api` and
`/events` to it — see `ui/kit/vite.config.ts`), and open the browser with the token on
the query string (`EventSource` cannot set headers, so the session token travels as
`?token=`):

```sh
/tmp/shnkitd --state-dir /tmp/kit-state --gateway-bin /tmp/shn-gateway \
  --secrets <bundle-dir> --discovery-url <url> \
  --api-addr 127.0.0.1:8471 --token dev-token
cd ui/kit && KITD_URL=http://127.0.0.1:8471 npm run dev
# open http://localhost:5173/?token=dev-token
```

### Daemon shutdown on quit

Every deliberate quit path (Cmd+Q, dock quit, the crash-dialog Quit, window-all-closed,
an OS `SIGTERM`/`SIGINT`) routes through `app.quit()`, which fires `before-quit` —
`wireGracefulQuit` (`src/daemon.ts`) stops the daemon there, before Electron actually
exits:

- **macOS/Linux:** `stop()` sends a real `SIGTERM`, which reaches `shnkitd`'s own
  `signal.Notify` handler; it runs `sup.StopAll()` to reap the gateway and Java trio
  before exiting itself. `stop()` waits up to a 15-second grace period for that to
  finish before escalating to `SIGKILL`.
- **Windows:** Node maps `child.kill('SIGTERM')` to `TerminateProcess`, which never
  reaches `shnkitd`'s signal handler — `sup.StopAll()` would never run. Instead,
  `stop()` runs `taskkill /PID <pid> /T /F` (`/T` kills the whole process tree, taking
  the gateway and Java trio down with `shnkitd`; `/F` forces it), which releases the
  same H2 file lock a graceful mac/Linux shutdown does, without ever needing
  `shnkitd`'s own reap logic.

**Residual:** the 15-second grace period is sized generously for `shnkitd`'s own
bounded shutdown (its HTTP server, then reaping four supervised children in series),
but if `shnkitd` still hasn't exited once it elapses, `stop()`'s mac/Linux path
escalates to a bare `SIGKILL` on `shnkitd` itself — with no process-group kill on that
path, its already-orphaned children linger on disk/in the process table until killed
by hand (`kill <pid>`) or the machine reboots. A subsequent Kit restart doesn't collide
with them (gateway ports are allocated `:0`). Likewise, a non-graceful force-quit that
bypasses `app.quit()`/`before-quit` entirely (e.g. killing the Electron process itself
from outside, or a system crash) is outside any shell-driven shutdown path.

## Packaging (full `electron-builder.yml`)

```sh
npm run pack   # tsc -b && electron-builder --dir (unsigned, into release/, gitignored)
```

`electron-builder.yml`'s `extraResources` copies a `resources/` staging dir (gitignored;
assembled by the packaging pipeline, not checked in) verbatim into the app's
`Resources/` — that's where `npm run pack`/the real packaging job put:

```
resources/
  shnkitd                # built shnkitd binary (mac: lipo universal)
  shn-gateway             # built gateway binary (mac: lipo universal)
  ui/**                    # ui/kit's built renderer (served by shnkitd at /ui)
  kit.config.json          # packaged config -- see below, no absolute paths
  versions.json            # tools/kitassets/manifest.sh's output
  java/**                  # Java trio assets + BOTH per-arch JREs:
                            # jre-darwin-arm64/, jre-darwin-amd64/ (Go GOOS/GOARCH
                            # naming, jre-{GOOS}-{GOARCH})
```

**Packaged `kit.config.json` carries only the non-path knobs** — `discoveryUrl`,
`accountsUrl` (or `secretsDir`), `releasesUrl`, and `javaAssets` as a *relative*
marker rather than an absolute path. Every packaged **path** (`gatewayBin`,
`kitdBin`, `uiDir`, `manifest`, and the resolved `javaAssets` directory) is instead
defaulted from Electron's own `process.resourcesPath` at **runtime**, by
`packagedDefaults()` in `src/config.ts` + the `resolve*` helpers in `src/main.ts` --
never baked into the config file at package time, since the actual install
location varies per machine. `gatewayBin` defaults to `{resourcesPath}/shn-gateway`
exactly the way `kitdBin` already defaulted to `{resourcesPath}/shnkitd`.
`dev.config.json` is unaffected: dev mode has no `resourcesPath` to default from,
so it must still set every path explicitly.

**Installer shapes:** mac ships one **universal `.dmg`** (Go binaries
lipo-merged; both per-arch JREs ride along since the JRE itself cannot be
lipo-merged — `shnkitd` picks the right one by `runtime.GOARCH` at child-spawn
time). Windows ships **NSIS `.exe`** only (no `.msi`).

**Signing/notarization are conditional, never required.** mac codesigning +
notarization key off `CSC_LINK`/`CSC_KEY_PASSWORD` and
`APPLE_ID`/`APPLE_APP_SPECIFIC_PASSWORD`/`APPLE_TEAM_ID`; the Windows installer is
Authenticode-signed by a post-build `Azure/artifact-signing-action` step (Azure
Artifact Signing) keyed off `AZURE_TENANT_ID`/`AZURE_CLIENT_ID`/`AZURE_CLIENT_SECRET`
— electron-builder builds the installer unsigned. `build/afterSign.js` notarizes
only when all three Apple env vars are present, otherwise logs and no-ops. `build/entitlements.mac.plist`
(hardened runtime) is inert unless the build is actually signed. The identical
build comes out signed+notarized when the secrets land at CI and unsigned
locally — `npm run pack` never needs any of this present.

**No release-attach anywhere** in this config or the packaging pipeline it feeds
(a structural rule) — every artifact, signed or not, is a CI workflow artifact
only.
