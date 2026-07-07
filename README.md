# SHN Kit

Download one installer, sign in, and participate on the Smart Health Network for
real from a desktop machine — no Docker, no engineering help.

## Download

Installers are attached to [Releases](https://github.com/SmartHealthNetwork/shn-kit/releases):
a macOS universal `.dmg` (Apple Silicon + Intel) and a Windows NSIS `.exe`.

> **Early access — these builds are UNSIGNED.** Code signing is not yet in place, so your OS
> will warn you the app is from an unidentified developer. To open it anyway:
> - **macOS:** right-click (or Control-click) **SHN Kit** → **Open** → **Open** in the dialog.
>   If that is still blocked, run once after installing:
>   `xattr -dr com.apple.quarantine "/Applications/SHN Kit.app"`.
> - **Windows:** on the SmartScreen prompt, click **More info** → **Run anyway**.
>
> Signed builds will replace these once code signing lands.

## What's inside

An Electron shell wrapping `shnkitd` (a Go daemon), a bundled Smart Gateway
(provider role), a FHIR validator, a Da Vinci reference provider, a seeded FHIR
data server, and one trimmed Temurin JRE per platform. Exact component versions
ship in the app's About screen (`versions.json`). All bundled data is
synthetic — no PHI, ever.

## Layout

| Path | What it is |
|---|---|
| `kit/` | `shnkitd` — the Go daemon: gateway/child supervision, the loopback HTTP+SSE API, scenario driving. See `kit/README.md`. |
| `desktop/` | The Electron shell that spawns/monitors `shnkitd` and opens a window onto it. See `desktop/README.md`. |
| `ui/kit/` | The React UI `shnkitd` serves at `/ui/`. |
| `tools/kitassets/` | The Java asset pipeline (validator + reference-provider WARs, JRE linking) — Docker + Temurin 21 required. |
| `tools/brprovider/` | The bundled Da Vinci reference provider's own build. |

## Build from source

- `kit/` (Go): `cd kit && go test ./...`
- `ui/kit/` (React): `cd ui/kit && npm ci && npm test`
- `desktop/` (Electron): `cd desktop && npm ci && npm test`
- `tools/kitassets/` + `tools/brprovider/` (the Java asset pipeline): needs
  Docker and a system Temurin 21 JDK; see `kit/README.md`'s "Packaging"
  section.

Release installers are built from exactly this tree by the maintainers' CI.
(Current early-access Releases are unsigned; signing is conditional and switches
on when the signing secrets are present — see `desktop/README.md`.) Building from
source yourself also produces a locally runnable build — see `desktop/README.md`'s
dev recipe.

## Privacy

The only outbound call this app makes beyond signing in and ordinary Smart
Health Network traffic is a once-per-launch check for a newer release
(configurable via `--releases-url`/`kit.config.json`'s `releasesUrl`,
repointable or effectively disableable). Every credential and log stays on
your machine — or, for the signed-in refresh token, your OS keychain. All
bundled demo/sandbox data is synthetic; this app never handles real patient
data.

## License

Apache License 2.0 — see `LICENSE`. Third-party components and their
licenses are listed in `THIRD_PARTY_NOTICES.md`.
