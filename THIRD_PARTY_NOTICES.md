# Third-Party Notices

SHN Kit includes the following third-party components. This file ships
alongside the app (`extraResources` in `electron-builder.yml`) and is also
carried at the root of the source snapshot. The full audit record — scan
method, per-component redistribution basis, and the per-release refresh rule
— lives in `docs/kit-license-audit.md` in the source repository.

## Go modules

Statically linked into the `shnkitd` daemon binary and the bundled
`shn-gateway` binary. Version source: `kit/go.mod`. None of these embed a
license file inside the compiled binary itself — see the upstream link for
each component's full text.

| Component | License | Full text |
|---|---|---|
| `github.com/SmartHealthNetwork/shn-sdk` | Apache-2.0 | https://github.com/SmartHealthNetwork/shn-sdk/blob/main/LICENSE |
| `github.com/SmartHealthNetwork/shn-gateway` | Apache-2.0 | https://github.com/SmartHealthNetwork/shn-gateway/blob/main/LICENSE |
| `github.com/golang-jwt/jwt/v5` | MIT | https://github.com/golang-jwt/jwt/blob/main/LICENSE |
| `github.com/zalando/go-keyring` | MIT | https://github.com/zalando/go-keyring/blob/master/LICENSE |
| `software.sslmate.com/src/go-pkcs12` | BSD-3-Clause | https://github.com/SSLMate/go-pkcs12/blob/master/LICENSE |
| `github.com/samply/golang-fhir-models/fhir-models` | Apache-2.0 | https://github.com/samply/golang-fhir-models/blob/main/LICENSE |
| `github.com/danieljoos/wincred` (Windows builds only) | MIT | https://github.com/danieljoos/wincred/blob/master/LICENSE |
| `github.com/godbus/dbus/v5` (Linux builds only — not present in the macOS/Windows installers shipped today) | BSD-2-Clause | https://github.com/godbus/dbus/blob/master/LICENSE-BSD |
| `golang.org/x/crypto` | BSD-3-Clause | https://cs.opensource.google/go/x/crypto/+/master:LICENSE |
| `golang.org/x/sys` | BSD-3-Clause | https://cs.opensource.google/go/x/sys/+/master:LICENSE |

## React UI runtime

Bundled (minified) into the built `ui/kit` JavaScript served at the app's
`/ui/` route. Version source: `ui/kit/package.json`.

| Component | License | Full text |
|---|---|---|
| `react` | MIT | https://github.com/facebook/react/blob/main/LICENSE |
| `react-dom` | MIT | https://github.com/facebook/react/blob/main/LICENSE |

## Fonts

Bundled as static files and referenced via `@font-face`. Version source:
`ui/kit/src/fonts/` (vendored `.woff2` files, not an npm runtime
dependency).

| Component | Shipped as | License | Full text |
|---|---|---|---|
| Inter (variable, Latin subset) | `ui/kit/src/fonts/inter-variable-latin.woff2` | SIL Open Font License 1.1 | https://github.com/rsms/inter/blob/master/LICENSE.txt |
| JetBrains Mono (variable, Latin subset) | `ui/kit/src/fonts/jetbrains-mono-variable-latin.woff2` | SIL Open Font License 1.1 | https://github.com/JetBrains/JetBrainsMono/blob/master/OFL.txt |

## Electron / Chromium

The app's runtime shell. Version source: `desktop/package.json`'s `electron`
dependency. `electron-builder` embeds Electron's own license and Chromium's
aggregated third-party notices into the packaged app automatically at
package time — `LICENSE.electron.txt` and `LICENSES.chromium.html` ship
inside the packaged app's own resources (no manual step; verify their
presence in a built artifact rather than this repository).

| Component | License |
|---|---|
| Electron (and the Node.js/Chromium runtime it embeds) | MIT (plus Chromium's own aggregated third-party notices, embedded separately) |

## Java assets (`Resources/java/` in a packaged install)

Version source: `tools/kitassets/pins.env`. The bundled FHIR validator and
the seeded provider data server both run the **same** WAR file
(`hapi/main.war`) under different configuration — it is one WAR, listed
once.

| Component | Shipped as | License | Full text |
|---|---|---|---|
| HAPI FHIR JPA-starter (validator + data server) | `Resources/java/hapi/main.war` | Apache-2.0 | The WAR's own `META-INF/LICENSE`/`META-INF/NOTICE` (and each bundled jar's own notices under `WEB-INF/lib/`) |
| Spring Boot (bundled inside the HAPI WAR) | `Resources/java/hapi/main.war`'s `WEB-INF/lib/*.jar` | Apache-2.0 | Each jar's own `META-INF/LICENSE` |
| H2 database (bundled inside the HAPI WAR) | `Resources/java/hapi/main.war`'s `WEB-INF/lib/*.jar` | Dual EPL-1.0 / MPL-2.0 | The H2 jar's own `META-INF/LICENSE` |
| br-provider (the bundled Da Vinci reference provider, commit `43a4806`) | `Resources/java/brprovider/main.war` | MIT | The WAR's own `META-INF/LICENSE` |

## Java runtime (`Resources/java/jre-{platform}/` in a packaged install)

| Component | Shipped as | License | Full text |
|---|---|---|---|
| Eclipse Temurin 21 (version pinned in `tools/kitassets/pins.env`) | `Resources/java/jre-{platform}/` | GPLv2 **with the Classpath Exception** | Ships at `Resources/java/jre-{platform}/legal/` — the `jlink`-preserved OpenJDK notices tree. `tools/kitassets/verify.sh` asserts this directory survives linking for every packaged JRE. |
