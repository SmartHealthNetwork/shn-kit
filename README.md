# SHN Kit

Download one installer, sign in, and participate on the Smart Health Network for
real from a desktop machine — no Docker, no engineering help.

## Download

Installers are attached to [Releases](https://github.com/SmartHealthNetwork/shn-kit/releases):
a macOS universal `.dmg` (Apple Silicon + Intel) and a Windows NSIS `.exe`.

Both installers are code-signed and notarized — download, open, and run, with no
security-warning workarounds:
- **macOS:** signed with an Apple Developer ID certificate and notarized by Apple; a
  plain double-click opens it, with no Gatekeeper block.
- **Windows:** Authenticode-signed (publisher "Smart Health Network PBC"); it installs
  without an "Unknown publisher" warning. On a brand-new download SmartScreen may still
  prompt until the file builds reputation — click **More info** → **Run anyway**.

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

Release installers are built from exactly this tree by the maintainers' CI, then
code-signed and notarized (macOS Developer ID + Apple notarization; Windows
Authenticode — see `desktop/README.md`). Building from source yourself produces a
locally runnable, unsigned build — see `desktop/README.md`'s dev recipe.

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
