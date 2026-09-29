# Third-Party Notices

SHN Kit includes the following third-party components. This file ships
alongside the app (`extraResources` in `electron-builder.yml`), with the licence
texts it names in `third-party-licenses/` beside it (`desktop/third-party-licenses/`
in the source), and is also carried at the root of the source snapshot.

## Go modules

Statically linked into the `shnkitd` daemon binary and the bundled
`shn-gateway` binary. Version source: `kit/go.mod` for `shnkitd`; the bundled gateway
is built with the gateway module's own `go.mod`, which sets the versions of every module
it links (the rows marked as the gateway's only are linked by it alone). None of these embed a
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
| `github.com/jackc/pgx/v5` (bundled `shn-gateway` binary only) | MIT | https://github.com/jackc/pgx/blob/master/LICENSE |
| `github.com/jackc/puddle/v2` (bundled `shn-gateway` binary only) | MIT | https://github.com/jackc/puddle/blob/master/LICENSE |
| `github.com/jackc/pgpassfile` (bundled `shn-gateway` binary only) | MIT | https://github.com/jackc/pgpassfile/blob/master/LICENSE |
| `github.com/jackc/pgservicefile` (bundled `shn-gateway` binary only) | MIT | https://github.com/jackc/pgservicefile/blob/master/LICENSE |
| `golang.org/x/sync` (bundled `shn-gateway` binary only) | BSD-3-Clause | https://cs.opensource.google/go/x/sync/+/master:LICENSE |
| `golang.org/x/text` (bundled `shn-gateway` binary only) | BSD-3-Clause | https://cs.opensource.google/go/x/text/+/master:LICENSE |

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

Version source: `tools/kitassets/pins.env` (the br-provider image identity is
`tools/brprovider/source.env`, which `pins.env` sources). The bundled FHIR validator and
the seeded provider data server both run the **same** WAR file
(`hapi/main.war`) under different configuration — it is one WAR, listed
once.

| Component | Shipped as | License | Full text |
|---|---|---|---|
| HAPI FHIR JPA-starter (validator + data server) | `Resources/java/hapi/main.war` | Apache-2.0 | https://github.com/hapifhir/hapi-fhir-jpaserver-starter/blob/image/v8.10.0-1/LICENSE (the WAR carries no license file of its own) |
| br-provider (the bundled Da Vinci reference provider: upstream commit `43a4806` plus the correction series in `tools/brprovider/patches`, image tag `43a4806-dtrname2`) | `Resources/java/brprovider/main.war` | MIT | https://github.com/HL7-DaVinci/br-provider/blob/43a4806a5662863298310374533352d840729cc3/LICENSE (the WAR carries no license file of its own) |

### Libraries inside the bundled Java applications

Both WARs carry third-party libraries as jars under `WEB-INF/lib/` and
`WEB-INF/lib-provided/`, unmodified except as stated under **Modifications**
below. Each jar's own licence and notice files ship inside it.

This section covers the jars. The web assets the WARs also carry outside
jars (br-provider's web client, LHC-Forms and documentation site under
`WEB-INF/classes/static/`, and the HAPI tester's scripts and styles) are not
yet listed here.

The table lists every bundled library whose licence files or pom name a licence
other than Apache-2.0, MIT or BSD, or describe parts under one, and every library
whose jar carries neither a licence text of its own nor an Apache pom
declaration, every library whose pom names a licence its own files do not
carry (`bootstrap`, `xpp3`), and every library whose bundled code points at
licence files the jar leaves out (`swagger-ui`). Unless its row says otherwise, each licence is the one the
library's Maven Central pom declares, read at the coordinates whose published
checksum matches the bundled jar (for a jar changed as stated under
**Modifications**, or a classifier jar, the coordinates the jar records). Where a library
is offered under a choice of licences, the Kit uses it under the one in
**bold**. A text named in backticks without a path is in `third-party-licenses/`.
For each library under the LGPL, MPL or EPL, its corresponding source is the
linked Maven Central sources jar.

Every other bundled jar carries its own Apache-2.0, MIT or BSD licence file, or
names the Apache License in its pom or manifest (`Apache-2.0.txt` is that
licence's text).

| Library | HAPI WAR | br-provider WAR | License | Text | Source |
|---|---|---|---|---|---|
| `org.eclipse.angus:angus-mail` | 2.0.4 | 2.0.4 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 OR EDL-1.0 | the jar's own `angus-mail-2.0.4.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/org/eclipse/angus/angus-mail/2.0.4/angus-mail-2.0.4-sources.jar) |
| `org.jetbrains:annotations` | 26.0.1 | 26.0.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `com.strumenta:antlr-kotlin-runtime` | 1.0.3 | — | Apache-2.0 and BSD-3-Clause (the pom names both) | `Apache-2.0.txt`; `antlr-kotlin-1.0.3-LICENSE-BSD.txt` |  |
| `com.strumenta:antlr-kotlin-runtime-jvm` | 1.0.3 | 1.0.3 | Apache-2.0 and BSD-3-Clause (the pom names both) | `Apache-2.0.txt`; `antlr-kotlin-1.0.3-LICENSE-BSD.txt` |  |
| `org.antlr:antlr-runtime` | 3.5.2 | 3.5.2 | BSD-3-Clause | `antlr-runtime-3.5.2.txt` |  |
| `org.antlr:antlr4-runtime` | 4.13.0 | 4.13.0 | BSD-3-Clause | `antlr4-runtime-4.13.0.txt` |  |
| `org.ow2.asm:asm` | 9.7.1 | 9.7.1 | BSD-3-Clause | `asm-9.7.1.txt` |  |
| `com.ionspin.kotlin:bignum-jvm` | 0.3.10 | 0.3.10 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.webjars:bootstrap` | 5.3.3 | 5.3.3 | MIT (the licence its files carry; its pom names Apache-2.0) | `bootstrap-5.3.3.txt` |  |
| `com.github.fge:btf` | 1.2 | 1.2 | LGPL-3.0-or-later OR **Apache-2.0** | the jar's own `btf-1.2.jar!/META-INF/LICENSE` |  |
| `com.github.andrewoma.dexx:collection` | 0.7 | 0.7 | MIT | `collection-0.7.txt` |  |
| `org.cqframework:cqf-fhir` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.opencds.cqf.fhir:cqf-fhir-cql` | 4.5.1 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.opencds.cqf.fhir:cqf-fhir-cr` | 4.5.1 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.opencds.cqf.fhir:cqf-fhir-cr-hapi` | 4.5.1 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:cqf-fhir-npm` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.opencds.cqf.fhir:cqf-fhir-utility` | 4.5.1 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:cql` | 4.6.0 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:cql-jvm` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:cql-to-elm` | 4.6.0 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:cql-to-elm-jvm` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `net.ttddyy:datasource-proxy` | 1.10 | 1.10 | MIT | `datasource-proxy-1.10.txt` |  |
| `org.cqframework:elm` | 4.6.0 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:elm-fhir` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:elm-jvm` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:engine` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:engine-fhir` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:engine-jvm` | 4.6.0 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.webjars:Eonasdan-bootstrap-datetimepicker` | 4.17.49 | 4.17.49 | MIT | `Eonasdan-bootstrap-datetimepicker-4.17.49.txt` |  |
| `com.vladsch.flexmark:flexmark` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-ast` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-builder` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-collection` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-data` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-dependency` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-format` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-html` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-misc` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-sequence` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `com.vladsch.flexmark:flexmark-util-visitor` | 0.64.8 | 0.64.8 | BSD-2-Clause | `flexmark-0.64.8.txt` |  |
| `org.webjars:font-awesome` | 5.15.4 | 5.15.4 | CC-BY-4.0 (icons), OFL-1.1 (fonts), MIT (code), per the jar's own licence file (the pom names CC BY 3.0) | the jar's own `font-awesome-5.15.4.jar!/META-INF/resources/webjars/font-awesome/5.15.4/LICENSE.txt` |  |
| `com.icegreen:greenmail-junit5` | 2.1.0-rc-1 | 2.1.0-rc-1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `com.h2database:h2` | 2.3.232 | 2.3.232 | **MPL-2.0** OR EPL-1.0 | `MPL-2.0.txt`; `h2-2.3.232.txt` | [sources](https://repo1.maven.org/maven2/com/h2database/h2/2.3.232/h2-2.3.232-sources.jar) |
| `ca.uhn.hapi.fhir:hapi-fhir-caching-api` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-caching-caffeine` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpa-hibernate-services` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpaserver-base` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpaserver-hfql` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpaserver-ips` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpaserver-mdm` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpaserver-model` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpaserver-searchparam` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-jpaserver-subscription` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-repositories` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-server-openapi` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-sql-migrate` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-storage` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-storage-batch2` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-storage-batch2-jobs` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-storage-mdm` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `ca.uhn.hapi.fhir:hapi-fhir-testpage-overlay` | 8.10.0 | 8.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.hdrhistogram:HdrHistogram` | 2.2.2 | 2.2.2 | CC0-1.0 OR BSD-2-Clause | the jar's own `HdrHistogram-2.2.2.jar!/META-INF/LICENSE.txt` |  |
| `org.hibernate.common:hibernate-commons-annotations` | 7.0.3.Final | 7.0.3.Final | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.hibernate.orm:hibernate-core` | 6.6.4.Final | 6.6.4.Final | LGPL-2.1-or-later | `LGPL-2.1.txt` | [sources](https://repo1.maven.org/maven2/org/hibernate/orm/hibernate-core/6.6.4.Final/hibernate-core-6.6.4.Final-sources.jar) |
| `org.hibernate.orm:hibernate-envers` | 6.6.4.Final | 6.6.4.Final | LGPL-2.1-or-later | the jar's own `hibernate-envers-6.6.4.Final.jar!/META-INF/COPYING.txt` | [sources](https://repo1.maven.org/maven2/org/hibernate/orm/hibernate-envers/6.6.4.Final/hibernate-envers-6.6.4.Final-sources.jar) |
| `com.carrotsearch:hppc` | 0.10.0 | 0.10.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `com.ibm.icu:icu4j` | 78.1 | 78.1 | Unicode-3.0 | `icu4j-78.1.txt` |  |
| `com.sun.istack:istack-commons-runtime` | 3.0.12 | 3.0.12 | EDL-1.0 (BSD-3-Clause) | the jar's own `istack-commons-runtime-3.0.12.jar!/META-INF/LICENSE.md` |  |
| `com.github.fge:jackson-coreutils` | 1.6 | 1.6 | LGPL-3.0-or-later OR **Apache-2.0** | the jar's own `jackson-coreutils-1.6.jar!/META-INF/LICENSE` |  |
| `jakarta-regexp:jakarta-regexp` | 1.4 | 1.4 | Apache-2.0 (the 1.4 source release's `LICENSE`; the pom names none) | `Apache-2.0.txt` |  |
| `com.sun.activation:jakarta.activation` | 1.2.2 | 1.2.2 | EDL-1.0 (BSD-3-Clause) | the jar's own `jakarta.activation-1.2.2.jar!/META-INF/LICENSE.md` |  |
| `jakarta.activation:jakarta.activation-api` | 2.1.2 | 2.1.2 | EDL-1.0 (BSD-3-Clause) | the jar's own `jakarta.activation-api-2.1.2.jar!/META-INF/LICENSE.md` |  |
| `jakarta.annotation:jakarta.annotation-api` | 2.1.1 | 2.1.1 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `jakarta.annotation-api-2.1.1.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/jakarta/annotation/jakarta.annotation-api/2.1.1/jakarta.annotation-api-2.1.1-sources.jar) |
| `org.glassfish:jakarta.el` | — | 4.0.2 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `jakarta.el-4.0.2.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/org/glassfish/jakarta.el/4.0.2/jakarta.el-4.0.2-sources.jar) |
| `jakarta.el:jakarta.el-api` | — | 4.0.0 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `jakarta.el-api-4.0.0.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/jakarta/el/jakarta.el-api/4.0.0/jakarta.el-api-4.0.0-sources.jar) |
| `jakarta.interceptor:jakarta.interceptor-api` | 2.1.0 | 2.1.0 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `jakarta.interceptor-api-2.1.0.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/jakarta/interceptor/jakarta.interceptor-api/2.1.0/jakarta.interceptor-api-2.1.0-sources.jar) |
| `org.glassfish:jakarta.json` | 2.0.1 | 2.0.1 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `jakarta.json-2.0.1.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/org/glassfish/jakarta.json/2.0.1/jakarta.json-2.0.1-sources.jar) |
| `jakarta.json:jakarta.json-api` | 2.0.1 | 2.0.1 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `jakarta.json-api-2.0.1.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/jakarta/json/jakarta.json-api/2.0.1/jakarta.json-api-2.0.1-sources.jar) |
| `org.eclipse.angus:jakarta.mail` | 2.0.3 | 2.0.3 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 OR EDL-1.0 | the jar's own `jakarta.mail-2.0.3.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/org/eclipse/angus/jakarta.mail/2.0.3/jakarta.mail-2.0.3-sources.jar) |
| `jakarta.persistence:jakarta.persistence-api` | 3.1.0 | 3.1.0 | EPL-2.0 OR **EDL-1.0** (BSD-3-Clause) | the jar's own `jakarta.persistence-api-3.1.0.jar!/META-INF/LICENSE.md` |  |
| `jakarta.servlet:jakarta.servlet-api` | 6.0.0 | 6.0.0 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | `EPL-2.0.txt` | [sources](https://repo1.maven.org/maven2/jakarta/servlet/jakarta.servlet-api/6.0.0/jakarta.servlet-api-6.0.0-sources.jar) |
| `jakarta.transaction:jakarta.transaction-api` | 2.0.1 | 2.0.1 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `jakarta.transaction-api-2.0.1.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/jakarta/transaction/jakarta.transaction-api/2.0.1/jakarta.transaction-api-2.0.1-sources.jar) |
| `jakarta.xml.bind:jakarta.xml.bind-api` | 4.0.1 | 4.0.1 | EDL-1.0 (BSD-3-Clause) | the jar's own `jakarta.xml.bind-api-4.0.1.jar!/META-INF/LICENSE.md` |  |
| `com.graphql-java:java-dataloader` | 4.0.0 | 4.0.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `info.debatty:java-string-similarity` | 1.2.1 | 1.2.1 | MIT | `java-string-similarity-1.2.1.txt` |  |
| `com.googlecode.owasp-java-html-sanitizer:java10-shim` | 20260102.1 | 20260102.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `com.googlecode.owasp-java-html-sanitizer:java8-shim` | 20260102.1 | 20260102.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.javassist:javassist` | 3.22.0-GA | 3.22.0-GA | MPL-1.1 OR LGPL-2.1 OR **Apache-2.0** | `Apache-2.0.txt` |  |
| `org.glassfish.jaxb:jaxb-runtime` | 2.3.8 | 2.3.8 | EDL-1.0 (BSD-3-Clause) | the jar's own `jaxb-runtime-2.3.8.jar!/META-INF/LICENSE.md` |  |
| `codes.rafael.jaxb2_commons:jaxb2-basics-runtime` | 3.0.0 | 3.0.0 | BSD-3-Clause | `jaxb2-basics-runtime-3.0.0.txt` |  |
| `org.apache.jena:jena-arq` | 5.5.0 | 5.5.0 | Apache-2.0 | the jar's own `jena-arq-5.5.0.jar!/META-INF/LICENSE` |  |
| `com.sanctionco.jmail:jmail` | 1.6.3 | 1.6.3 | MIT | `jmail-1.6.3.txt` |  |
| `org.jscience:jscience` | 4.3.1 | 4.3.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `io.dogote:json-patch` | 1.15 | 1.15 | LGPL-3.0-or-later OR **Apache-2.0** | the jar's own `json-patch-1.15.jar!/META-INF/LICENSE` |  |
| `com.jayway.jsonpath:json-path` | 2.9.0 | 2.9.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `com.github.victools:jsonschema-generator` | 4.38.0 | 4.38.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `com.github.victools:jsonschema-module-jackson` | 4.38.0 | 4.38.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `com.github.victools:jsonschema-module-swagger-2` | 4.38.0 | 4.38.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.webjars:jstimezonedetect` | 1.0.6 | 1.0.6 | MIT | `jstimezonedetect-1.0.6.txt` |  |
| `com.knuddels:jtokkit` | 1.1.0 | 1.1.0 | MIT | `jtokkit-1.1.0.txt` |  |
| `org.slf4j:jul-to-slf4j` | 2.0.17 | 2.0.17 | MIT | the jar's own `jul-to-slf4j-2.0.17.jar!/META-INF/LICENSE.txt`; `jul-to-slf4j-2.0.17.txt` |  |
| `junit:junit` | 4.13.2 | 4.13.2 | EPL-1.0 | the jar's own `junit-4.13.2.jar!/LICENSE-junit.txt` | [sources](https://repo1.maven.org/maven2/junit/junit/4.13.2/junit-4.13.2-sources.jar) |
| `org.jetbrains.kotlin:kotlin-stdlib` | 2.3.10 | 2.3.10 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlin:kotlin-stdlib-common` | 2.0.0 | 2.0.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlin:kotlin-stdlib-jdk7` | 2.0.0 | 2.0.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlin:kotlin-stdlib-jdk8` | 2.0.0 | 2.0.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-datetime-jvm` | 0.7.1 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-io-bytestring` | 0.8.2 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-io-bytestring-jvm` | 0.8.0 | 0.8.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-io-core` | 0.8.0 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-io-core-jvm` | 0.8.1 | 0.8.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-serialization-core-jvm` | 1.9.0 | 1.9.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-serialization-json-io-jvm` | 1.9.0 | 1.9.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.jetbrains.kotlinx:kotlinx-serialization-json-jvm` | 1.9.0 | 1.9.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.latencyutils:LatencyUtils` | 2.0.3 | 2.0.3 | CC0-1.0 | none required (public-domain dedication) |  |
| `com.google.guava:listenablefuture` | 9999.0-empty-to-avoid-conflict-with-guava | 9999.0-empty-to-avoid-conflict-with-guava | Apache-2.0 | `Apache-2.0.txt` |  |
| `ch.qos.logback:logback-classic` | 1.5.25 | 1.5.25 | **EPL-1.0** OR LGPL-2.1 | `EPL-1.0.txt` | [sources](https://repo1.maven.org/maven2/ch/qos/logback/logback-classic/1.5.25/logback-classic-1.5.25-sources.jar) |
| `ch.qos.logback:logback-core` | 1.5.25 | 1.5.25 | **EPL-1.0** OR LGPL-2.1 | `EPL-1.0.txt` | [sources](https://repo1.maven.org/maven2/ch/qos/logback/logback-core/1.5.25/logback-core-1.5.25-sources.jar) |
| `org.apache.lucene:lucene-analysis-common` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-analysis-common-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-analysis-phonetic` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-analysis-phonetic-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-core` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-core-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-facet` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-facet-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-highlighter` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-highlighter-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-join` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-join-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-memory` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-memory-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-queries` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-queries-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-queryparser` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-queryparser-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `org.apache.lucene:lucene-sandbox` | 9.11.1 | 9.11.1 | Apache-2.0 | the jar's own `lucene-sandbox-9.11.1.jar!/META-INF/LICENSE.txt` |  |
| `io.modelcontextprotocol.sdk:mcp` | 0.17.0 | 0.17.0 | MIT | `mcp-0.17.0.txt` |  |
| `org.springaicommunity:mcp-annotations` | 0.8.0 | 0.8.0 | Apache-2.0 (the project's `LICENSE` at v0.8.0; its pom names MIT) | `Apache-2.0.txt` |  |
| `io.modelcontextprotocol.sdk:mcp-core` | 0.17.0 | 0.17.0 | MIT | `mcp-0.17.0.txt` |  |
| `io.modelcontextprotocol.sdk:mcp-json` | 0.17.0 | 0.17.0 | MIT | `mcp-0.17.0.txt` |  |
| `io.modelcontextprotocol.sdk:mcp-json-jackson2` | 0.17.0 | 0.17.0 | MIT | `mcp-0.17.0.txt` |  |
| `com.github.fge:msg-simple` | 1.1 | 1.1 | LGPL-3.0-or-later OR **Apache-2.0** | the jar's own `msg-simple-1.1.jar!/META-INF/LICENSE` |  |
| `com.microsoft.sqlserver:mssql-jdbc` | 13.4.0.jre11 | 12.4.3.jre11 | MIT | `mssql-jdbc-12.4.3.jre11.txt`; `mssql-jdbc-13.4.0.jre11.txt` |  |
| `com.oracle.database.jdbc:ojdbc11` | 23.6.0.24.10 | 23.6.0.24.10 | Oracle Free Distribution, Hosting, and Use Terms and Conditions (the pom calls them the Oracle Free Use Terms and Conditions; they permit redistributing the unmodified jar with its licence) | the jar's own `ojdbc11-23.6.0.24.10.jar!/META-INF/license.txt` |  |
| `io.opentelemetry:opentelemetry-api` | 1.60.1 | 1.44.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `io.opentelemetry:opentelemetry-common` | 1.60.1 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `io.opentelemetry:opentelemetry-context` | 1.60.1 | 1.44.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `io.opentelemetry.instrumentation:opentelemetry-instrumentation-annotations` | 2.26.1 | 2.10.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.eclipse.jgit:org.eclipse.jgit` | 7.4.0.202509020913-r | — | EDL-1.0 (BSD-3-Clause) | `org.eclipse.jgit-7.4.0.202509020913-r.txt` |  |
| `org.eclipse.parsson:parsson` | 1.0.5 | 1.0.5 | **EPL-2.0** OR GPL-2.0-only WITH Classpath-exception-2.0 | the jar's own `parsson-1.0.5.jar!/META-INF/LICENSE.md` | [sources](https://repo1.maven.org/maven2/org/eclipse/parsson/parsson/1.0.5/parsson-1.0.5-sources.jar) |
| `net.sourceforge.plantuml:plantuml-mit` | 1.2026.1 | 1.2026.1 | MIT | `plantuml-mit-1.2026.1.txt` |  |
| `org.webjars:popper.js` | 2.11.7 | 2.11.7 | MIT | `popper.js-2.11.7.txt` |  |
| `com.google.protobuf:protobuf-java` | 4.31.1 | 4.31.1 | BSD-3-Clause | `protobuf-java-4.31.1.txt` |  |
| `org.cqframework:quick` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.reactivestreams:reactive-streams` | 1.0.3 | 1.0.3 | CC0-1.0 | none required (public-domain dedication) |  |
| `org.roaringbitmap:RoaringBitmap` | 1.3.0 | 1.3.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `net.sf.saxon:Saxon-HE` | 11.6 | 11.6 | MPL-2.0 | `MPL-2.0.txt`; `Saxon-HE-11.6-JAMESCLARK.txt`; `Saxon-HE-11.6-JLINE2.txt` (the third-party notices Saxon-HE 11.6 ships) | [sources](https://repo1.maven.org/maven2/net/sf/saxon/Saxon-HE/11.6/Saxon-HE-11.6-sources.jar) |
| `org.webjars:select2` | 4.0.13 | 4.0.13 | MIT | `select2-4.0.13.txt` |  |
| `org.cqframework:shared` | 4.6.0 | — | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.cqframework:shared-jvm` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.slf4j:slf4j-api` | 2.0.16 | 2.0.16 | MIT | the jar's own `slf4j-api-2.0.16.jar!/META-INF/LICENSE.txt`; `slf4j-api-2.0.16.txt` |  |
| `org.springframework:spring-aop` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-aop-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-aop-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-beans` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-beans-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-beans-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-context` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-context-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-context-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-context-support` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-context-support-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-context-support-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-core` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-core-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-core-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework.data:spring-data-commons` | 3.3.5 | 3.3.5 | Apache-2.0 | the jar's own `spring-data-commons-3.3.5.jar!/license.txt` |  |
| `org.springframework.data:spring-data-elasticsearch` | 5.3.5 | 5.3.5 | Apache-2.0 | the jar's own `spring-data-elasticsearch-5.3.5.jar!/license.txt` |  |
| `org.springframework.data:spring-data-envers` | 3.3.5 | 3.3.5 | Apache-2.0 | the jar's own `spring-data-envers-3.3.5.jar!/license.txt` |  |
| `org.springframework.data:spring-data-jpa` | 3.3.5 | 3.3.5 | Apache-2.0 | the jar's own `spring-data-jpa-3.3.5.jar!/license.txt` |  |
| `org.springframework:spring-expression` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-expression-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-expression-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-jcl` | — | 6.2.12 | Apache-2.0 | the jar's own `spring-jcl-6.2.12.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-jdbc` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-jdbc-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-jdbc-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-messaging` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-messaging-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-messaging-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-orm` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-orm-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-orm-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework.security:spring-security-config` | — | 6.3.4 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.security:spring-security-core` | — | 6.3.9 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.security:spring-security-crypto` | — | 6.3.9 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.security:spring-security-oauth2-authorization-server` | — | 1.3.3 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.security:spring-security-oauth2-core` | — | 6.3.4 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.security:spring-security-oauth2-jose` | — | 6.3.4 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.security:spring-security-oauth2-resource-server` | — | 6.3.4 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.security:spring-security-web` | — | 6.3.4 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.session:spring-session-core` | — | 3.5.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework.session:spring-session-jdbc` | — | 3.5.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.springframework:spring-test` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-test-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-test-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-tx` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-tx-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-tx-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-web` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-web-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-web-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-webmvc` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-webmvc-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-webmvc-6.2.18.jar!/META-INF/license.txt` |  |
| `org.springframework:spring-websocket` | 6.2.18 | 6.2.12 | Apache-2.0 | the jar's own `spring-websocket-6.2.12.jar!/META-INF/license.txt`; the jar's own `spring-websocket-6.2.18.jar!/META-INF/license.txt` |  |
| `org.antlr:ST4` | 4.0.8 | 4.0.8 | BSD-3-Clause | `ST4-4.0.8.txt` |  |
| `org.codehaus.woodstox:stax2-api` | 4.2.1 | 4.2.1 | BSD-2-Clause (the jar's own licence file names the Simplified BSD License; its pom names "The BSD License") | the jar's own `stax2-api-4.2.1.jar!/META-INF/LICENSE`; `stax2-api-4.2.1.txt` |  |
| `org.webjars:swagger-ui` | 5.32.5 | 5.21.0 | Apache-2.0, bundling third-party packages under their own licences: mostly MIT and Apache-2.0; also BSD-3-Clause, BSD-2-Clause, ISC, 0BSD, CC0-1.0, Unlicense, Python-2.0, BlueOak-1.0.0, (MIT AND BSD-3-Clause), MIT OR CC0-1.0, and MPL-2.0 OR **Apache-2.0** | `swagger-ui-5.21.0.txt`; `swagger-ui-5.32.5.txt` (its NOTICE, and the licence of each package its bundles are built from) |  |
| `org.thymeleaf:thymeleaf` | 3.1.5.RELEASE | 3.1.2.RELEASE | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.apache.tomcat.embed:tomcat-embed-core` | 10.1.50 | 10.1.50 | Apache-2.0 | the jar's own `tomcat-embed-core-10.1.50.jar!/META-INF/LICENSE` |  |
| `org.glassfish.jaxb:txw2` | 2.3.8 | 2.3.8 | EDL-1.0 (BSD-3-Clause) | the jar's own `txw2-2.3.8.jar!/META-INF/LICENSE.md` |  |
| `org.cqframework:ucum` | 4.6.0 | 4.4.0 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.fhir:ucum` | 1.0.9 | 1.0.9 | BSD-3-Clause | `ucum-1.0.9.txt` |  |
| `org.webjars:webjars-locator-lite` | 1.1.3 | — | MIT | `webjars-locator-lite-1.1.3.txt` |  |
| `com.fasterxml.woodstox:woodstox-core` | 6.4.0 | 6.4.0 | Apache-2.0 | the jar's own `woodstox-core-6.4.0.jar!/META-INF/LICENSE` |  |
| `com.github.dnault:xml-patch` | 0.3.1 | 0.3.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.xmlresolver:xmlresolver` | 5.2.1 | 5.2.1 | Apache-2.0 | `Apache-2.0.txt` |  |
| `org.ogce:xpp3` | 1.1.6 | 1.1.6 | Indiana University Extreme! Lab Software License 1.2 (the parser; 1.1.1 at its release tag; a sample class carries its own copyright under it), public domain (the XMLPULL API), the Jaxen and SAXPath BSD-style licences (their bundled copies) and the Apache Software License 1.1 (`javax.xml.namespace.QName`), per its sources; its pom names Apache-2.0 | `xpp3-1.1.6.txt` |  |

#### Modifications

- **`hapi-fhir-validation` 8.10.0, in the HAPI WAR:** one class,
  `org.hl7.fhir.common.hapi.validation.validator.ValidatorWrapper`, is rebuilt
  from its upstream source with a change applied, and replaces the upstream
  class in that jar. The source, the change and the build are in
  `tools/kitassets/backport/`, under Apache-2.0 like the upstream class.
- **`sqlite-jdbc`, in both WARs:** the macOS native libraries
  (`org/sqlite/native/Mac/**`) are removed from the jar so the macOS app can be
  notarized. Nothing else in the jar changes.

## Java runtime (`Resources/java/jre-{platform}/` in a packaged install)

| Component | Shipped as | License | Full text |
|---|---|---|---|
| Eclipse Temurin 21 (version pinned in `tools/kitassets/pins.env`) | `Resources/java/jre-{platform}/` | GPLv2 **with the Classpath Exception** | Ships at `Resources/java/jre-{platform}/legal/` — the `jlink`-preserved OpenJDK notices tree. `tools/kitassets/verify.sh` asserts this directory survives linking for every packaged JRE. |
