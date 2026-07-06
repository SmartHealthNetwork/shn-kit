# SHN Kit — `shnkitd` (Go daemon)

`shnkitd` is the Go daemon at the heart of the SHN Kit — the Mac/Windows desktop
participant kit. It supervises a bundled Smart Gateway (provider role) and, in a
packaged install, three supervised Java children (a FHIR validator, a seeded FHIR
data server, and a Da Vinci reference provider), exposes a loopback HTTP+SSE API
the Kit UI drives, and lets you run real Da Vinci prior-authorization exchanges
against the Smart Health Network — no Docker, no engineering help.

This module (`github.com/SmartHealthNetwork/shn-kit`) is plain Go: `cd kit && go
test ./...`. The Electron shell that wraps it lives in `../desktop/`; the React
UI it serves lives in `../ui/kit/`.

## Boundary

This module imports ONLY the public SDK (`shn-sdk`), the public gateway module
(`shn-gateway`), and stdlib — `boundary_test.go`'s `TestKitBoundaryFence` enforces
this over the module's full closure (production and test imports). `kit/go.mod`
is maintained by hand (not by an automated `go mod tidy` sweep, since
`shn-gateway` is a separate module) — keep the pins current when bumping
`shn-sdk`/`shn-gateway`.

## Sign-in and bootstrap

A fresh Kit has no identity: before it can run anything it signs in to the
Accounts service and provisions its own client credentials. This is a resumable
state machine surfaced at `GET /api/bootstrap` (`signin-required` →
`signing-in` → `provisioning` → `provisioned`). `POST /api/bootstrap/signin`
starts a loopback-PKCE browser flow; `POST /api/bootstrap/reset` clears the
stored session and returns to `signin-required` (the Kit must be restarted
afterward — the one case where a restart is required rather than optional,
since the state machine cannot be re-armed in-process).

Two ways in: sign in interactively against `--accounts <url>`, or point
`--secrets <bundle-dir>` at a pre-provisioned registration bundle (skips
sign-in entirely and starts already provisioned). Exactly one of the two is
required.

### Credential storage

A signed-in session's refresh token is written to `tokens.json` under
`--state-dir` (mode `0600`), scoped to the `--accounts` URL it was issued
against. Packaged builds default to storing it in the OS keychain instead
(`--token-store keychain` — macOS Keychain / Windows Credential Manager);
only the refresh token is kept there, never the rest of the session state
(Windows Credential Manager's blob size cap makes storing more is a
reliability risk). If the OS keychain is unavailable, the Kit falls back to
the file store and says so — `GET /api/bootstrap`'s `tokenStorage` field
reads `"file (keychain unavailable: ...)"` rather than silently downgrading.
Dev builds default to the file store; either is selectable explicitly via
`--token-store`.

## `shnkitd` flags

| Flag | Default | Meaning |
|---|---|---|
| `--state-dir` | *(required)* | State directory: logs, `session.json`, `tokens.json`, and (packaged) `ingress-clients.json` |
| `--gateway-bin` | *(required)* | Absolute path to the built `shn-gateway` binary |
| `--accounts` | `""` | Accounts service base URL; required unless `--secrets` provides a pre-provisioned bundle |
| `--secrets` | `""` | Pre-provisioned `shn register`/Init bundle dir; a loadable bundle here skips sign-in and starts already `provisioned`; `""` ⇒ `{state-dir}/secrets`, populated by an operator sign-in |
| `--client-name` | `"SHN Kit"` | Accounts display name for this Kit's client registration |
| `--register-base-url` | `""` | Da Vinci registration base URL; `""` ⇒ derived from `--accounts` |
| `--no-browser` | `false` | Suppress the OS browser opener during sign-in; print the authorize URL instead |
| `--patient-app-url` | `""` | Smart Health account patient app URL, surfaced at `GET /api/status` |
| `--discovery-url` | `""` | The SHN discovery service URL |
| `--audit-url` | `""` | Audit Plane URL; omit to run without the run-timeline audit merge |
| `--phg-url` | `""` | PHG (Smart Health account) URL |
| `--consent-url` | `""` | Consent service URL |
| `--fhir-data-url` | `""` | Provider FHIR data server URL; `""` ⇒ an in-memory stub |
| `--gateway-port` | `0` | Gateway child port; `0` ⇒ allocate |
| `--api-addr` | `127.0.0.1:0` | The daemon's own loopback API bind address (host must be `127.0.0.1`/`::1`/`localhost`) |
| `--token` | `""` | Session token; `""` ⇒ generate a 128-bit random hex token |
| `--fake-validator` | `true` | When left unspecified AND `--java-assets` is set, derived to `false` so a packaged boot runs the real FHIR validator by default; an explicit pass always wins |
| `--ui-dir` | `""` | Built Kit UI (`ui/kit`) dir served ungated at `/ui/`; `""` ⇒ API-only, no UI route |
| `--java-assets` | `""` (`SHN_KIT_JAVA_ASSETS`) | Packaged Java-trio assets dir (validator, seeded data server, Da Vinci reference provider); `""` ⇒ no trio, gateway-child-only behavior |
| `--jre-dir` | `""` | JRE root containing `bin/java[.exe]`; `""` ⇒ `{java-assets}/jre-{GOOS}-{GOARCH}` (Go's own `GOOS`/`GOARCH` naming — `jre-linux-amd64`, `jre-darwin-arm64`, `jre-darwin-amd64`, `jre-windows-amd64` — not e.g. Temurin's own `x64`/`aarch64` archive naming) |
| `--token-store` | `""` | `"keychain"` or `"file"`; `""` ⇒ derived (keychain when `--java-assets` is set, file otherwise) |
| `--manifest` | `""` | Path to the package-time `versions.json`, served verbatim at `GET /api/about`; `""` ⇒ 404 with an honest "development build" body |
| `--releases-url` | GitHub `shn-kit` latest-release feed | The update-check feed GETed once at launch — see "Phone-home honesty" below |
| `--uc07-pci` | `""` | Patient-surface PCI override for the UC-07 demo persona; `""` ⇒ resolved live |

## `kit.config.json`

The packaged Electron shell (`../desktop/`) resolves its own configuration from
a JSON file — `kit.config.json` when packaged, `dev.config.json` in a dev
checkout (the `SHN_KIT_CONFIG` env var overrides the path). It carries the
non-path knobs (`discoveryUrl`; `accountsUrl` or `secretsDir`; `releasesUrl`;
and a *relative* `javaAssets` marker) — every packaged **path** (the
gateway/kitd binaries, the UI dir, the manifest, the resolved Java-assets dir)
is instead defaulted from Electron's own `process.resourcesPath` at runtime,
never baked into the JSON, since an install path varies per machine. See
`../desktop/src/config.ts` for the full shape and `../desktop/README.md`'s
"Packaging" section for exactly what lands under `Resources/`.

## The daemon's HTTP+SSE API

The daemon binds one loopback address (`--api-addr`). Every route except
`GET /health` requires the session token, via `Authorization: Bearer <token>`
or `?token=<token>` (the query-param form exists because `EventSource` cannot
set headers).

| Method | Path | Auth | Notes |
|---|---|---|---|
| `GET` | `/health` | none | `200 {"ok":true}` |
| `GET` | `/api/status` | token | Supervised-child status plus the update-check result |
| `GET` | `/api/bootstrap` | token | Sign-in/provisioning state |
| `POST` | `/api/bootstrap/signin` | token | Starts a loopback-PKCE browser sign-in |
| `POST` | `/api/bootstrap/reset` | token | Clears stored credentials; `{"restartRequired":true}` |
| `POST` | `/api/runs` | token | Start a scenario run (`{"lane","uc","branch","member"}`); `202 {"runId":...}`; `409` if a run is already in flight |
| `GET` | `/api/runs` | token | In-flight/completed run results |
| `GET` | `/api/history`, `/api/history/{runId}` | token | Saved run history, including the full per-run event story (the same bytes the UI's "Export" button downloads) |
| `GET` | `/events` | token | SSE stream of daemon/gateway events (`Last-Event-ID` replays the ring buffer) |
| `GET` | `/api/byo`, `PUT`/`DELETE /api/byo/ehr`, `/api/byo/davinci` | token | Bring-your-own-systems config — see below |
| `GET` | `/api/byo/patients`, `/api/byo/patients/{fhirId}/context` | token | Browse a connected bring-your-own EHR's patients |
| `POST`/`DELETE` | `/api/watch` | token | Open/close an observation window over partner-originated Da Vinci traffic reaching a registered bring-your-own ingress client |
| `POST` | `/api/verify` | token | Re-run the boot-time connectivity probes |
| `GET` | `/api/about` | token | The package-time `versions.json` manifest; 404 with an honest body on a dev checkout |
| `GET` | `/api/support-bundle` | token | A zip of per-child logs, the manifest, the boot probe results, and recent run history — secrets excluded by inventory, not by hope |
| `POST` | `/api/children/{name}/restart` | token | Restart one supervised Java child (`validator`/`data-server`/`br-provider`); `403` for the gateway child — restart the whole Kit for that |
| `GET` | `/ui/*` | **none (ungated)** | The built Kit UI, served as static assets |

`POST /api/runs` is async: it validates and acquires the sequential run lock
(one run at a time) and returns the pre-allocated `runId` immediately; poll
`GET /api/runs` or subscribe to `/events` for the terminal
`run.finished`/`run.failed`.

On `Serve`, before the HTTP listener accepts connections, the daemon writes
`session.json` into `--state-dir` (mode `0600`):

```json
{"api": "http://127.0.0.1:<port>", "token": "<32-hex-char session token>"}
```

This is the contract a UI shell polls for to discover the daemon's bound
address and session token — the token is generated (128-bit random hex)
whenever `--token` is left empty.

### Phone-home honesty

Once per launch (async, never blocks boot), the Kit GETs `--releases-url`'s
feed to check for a newer release, and surfaces the result at `GET
/api/status`. **This per-launch releases-feed GET is the Kit's only outbound
call beyond signing in and ordinary Hub/substrate traffic.** `--releases-url`
is the knob that repoints it (e.g. at a test double) or, pointed at an
unreachable/empty value, effectively disables it. Every failure path (feed
unreachable, malformed response) is silent-with-log — offline is a
first-class state, never an error toast.

### Daemon shutdown on quit

The Electron shell reaps `shnkitd` (and, transitively, its supervised gateway and Java
trio) on every deliberate quit. On macOS/Linux this is a real `SIGTERM` that reaches
`shnkitd`'s own `signal.Notify` handler, which runs `sup.StopAll()` to reap its
children before it exits itself — the shell waits up to a 15-second grace period for
that before escalating to `SIGKILL`. Windows has no POSIX signal delivery (a plain
`kill()` there just calls `TerminateProcess`, which never runs `sup.StopAll()`), so the
shell instead kills `shnkitd`'s whole process tree directly (`taskkill /PID <pid> /T
/F`), taking the gateway and Java trio down with it.

**Residual:** if `shnkitd` still hasn't exited once the mac/Linux grace period
elapses, the shell escalates to a bare `SIGKILL` on `shnkitd` itself, which — with no
process-group kill on that path — leaves its already-orphaned children lingering on
disk/in the process table until killed by hand or the machine reboots; it doesn't
collide with a later Kit restart (ports are allocated dynamically). A non-graceful
force-quit that bypasses the shell's quit handling entirely (e.g. the Electron process
killed from outside) is likewise outside this path. See `../desktop/README.md`'s
"Daemon shutdown on quit" section for the full detail.

## Bring-your-own systems

Both of the Kit's demo lanes can be repointed at your own systems instead of
the bundled sandbox: the provider-data lane at your own FHIR data server, and
the conformant Da Vinci lane at registering your own system as an inbound
ingress client. Both swaps are per-lane and reversible ("restore demo data"
via `DELETE /api/byo/ehr` or `/api/byo/davinci`), and — since the in-process
supervisor can't restart just one piece of the stack — take effect only on
the Kit's next full restart (every successful `PUT`/`DELETE` under
`/api/byo/*` answers `{"restartRequired":true}`).

Config is persisted to `{state-dir}/byo.json` (mode `0600`); any client
private key you supply is written write-only to a sibling key file and never
echoed back by a `GET` — only a `hasClientKey: true/false` bit is reported.
Validation mirrors the gateway's own boot-time checks exactly, so a swap the
Kit accepts is guaranteed to be one the gateway can actually boot on; only
`ES384`/`RS384` keys are accepted (no shared-secret algorithms).
`GET /api/byo/patients` lets you browse your connected EHR's patients (once
an EHR swap is applied) to pick one to run.

**Your patients need a `urn:shn:member` identifier** whose *value* is a
member id the payer counterparty actually covers — the SHN-hosted demo payer
recognizes only its own seeded member ids. This is a genuine onboarding
requirement, not a Kit limitation; full own-payer/own-provider onboarding is
the additive door that eventually removes it.

Registering your own Da Vinci system as an inbound ingress client
(`PUT /api/byo/davinci`) requires opening a **watch** (`POST`/`DELETE
/api/watch`) before your system sends traffic, so the run inspector can
narrate it — the Kit never opens a remote listener for this; your system
must run on the same machine and call the gateway's already-loopback-bound
ingress directly.

## Packaging: the Java trio and JRE

A packaged Kit ships more than "gateway + a stand-in validator": three
supervised Java children (validator, seeded data server, Da Vinci reference
provider), a bundled `jlink`-trimmed Temurin 21 JRE per platform, real
installers, and OS-keychain token storage.

Building those assets from source (`../tools/kitassets/`, Docker + a system
Temurin 21 JDK required):

```sh
../tools/kitassets/build.sh          # extract the pinned WARs, offline-bake the
                                      # IGs, prewarm both H2 databases, and
                                      # persona-seed the data server -> dist/kitassets/
../tools/kitassets/jlink.sh host      # jdeps module union + jlink the host
                                      # platform's trimmed Temurin 21 runtime
../tools/kitassets/verify.sh          # boot-proof all three WARs on the linked JRE
```

`dist/kitassets/` is a local build output (gitignored, never committed).
`../tools/kitassets/manifest.sh` writes `versions.json`, the package-time
manifest `shnkitd` serves at `GET /api/about` — Kit semver comes from
`../desktop/package.json`; the gateway/SDK module versions come from
`kit/go.mod`.

JRE directories follow **Go's own `GOOS`/`GOARCH` naming**, not Temurin's —
see the flags table above.

When the trio is present, the daemon boots it BEFORE the gateway child (each
blocking on its own readiness probe), then wires the gateway's
`FHIR_VALIDATE_URL` at the validator and (absent an explicit
`--fhir-data-url`/bring-your-own override) `FHIR_DATA_URL` at the seeded data
server.

macOS ships one universal `.dmg` (Go binaries `lipo`-merged; both per-arch
JREs ride along, since a JRE itself can't be lipo-merged). Windows ships an
NSIS `.exe`. See `../desktop/README.md`'s "Packaging" section for the full
resource layout and path-resolution story.

## Testing

```sh
cd kit && go test ./... -race
```
