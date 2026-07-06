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
# 1. Build the real gateway binary (the same package test/kitlive's TestMain builds).
go build -o /tmp/shn-gateway ./gateway/cmd/gateway

# 2. Build the real shnkitd binary.
cd kit && go build -o /tmp/shnkitd ./cmd/shnkitd && cd ..

# 3. Build the Kit UI renderer.
cd ui/kit && npm run build && cd ../..

# 4. Point dev.config.json at your local paths.
cd desktop
cp dev.config.example.json dev.config.json
# edit dev.config.json: gatewayBin=/tmp/shn-gateway, kitdBin=/tmp/shnkitd,
# uiDir=<repo>/ui/kit/dist, and EITHER:
#   - secretsDir: a pre-provisioned shn register/Init bundle dir (kit/README's
#     "CI provisioning recipe") — skips sign-in entirely, or
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

### v1 limitation — orphaned gateway child on a hard SIGKILL

If `stop()`'s 5-second SIGTERM grace period expires and the shell escalates to
SIGKILL on `shnkitd`, `shnkitd`'s own supervised gateway child is orphaned — there is
no longer any supervisor left to reap it. A subsequent Kit restart doesn't collide with
it (gateway ports are allocated `:0`), but the orphaned process lingers on disk/in the
process table until killed by hand (`kill <pid>`) or the machine reboots. This is a
known lifecycle gap (real signal handling + the Windows equivalent); it's
called out here because a developer force-quitting the Electron app mid-shutdown is
exactly when it's hit.

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
`APPLE_ID`/`APPLE_APP_SPECIFIC_PASSWORD`/`APPLE_TEAM_ID` (win: `WIN_CSC_LINK`/
`WIN_CSC_KEY_PASSWORD`); `build/afterSign.js` notarizes only when all three Apple
env vars are present, otherwise logs and no-ops. `build/entitlements.mac.plist`
(hardened runtime) is inert unless the build is actually signed. The identical
build comes out signed+notarized when the secrets land at CI and unsigned
locally — `npm run pack` never needs any of this present.

**No release-attach anywhere** in this config or the packaging pipeline it feeds
(a structural rule) — every artifact, signed or not, is a CI workflow artifact
only.
