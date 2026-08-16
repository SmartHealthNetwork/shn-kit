#!/usr/bin/env bash
#
# tools/kitassets/build.sh — the Kit's Java asset pipeline.
#
# Extracts the two Spring Boot WARs from the EXACT pinned images the whole program
# already conformance-tests (never rebuilt from divergent sources), downloads the
# 8+4 IG tgzs offline-bake style (same URLs as the two Dockerfiles it mirrors),
# and PREWARMS both HAPI H2 stores at package time so first-boot IG indexing
# (the 10-15 minute cost) is paid here, never on a user's machine.
#
# Build-time-only dependencies: Docker + a system Temurin 21 (JAVA_HOME or
# `/usr/libexec/java_home -v 21`). The Kit itself never needs either.
# Outputs land under dist/kitassets/ (gitignored — NEVER committed).
#
# ── Config channel: SPRING_APPLICATION_JSON (spike-certified 2026-07-04) ──────
# An early spike booted the extracted WAR on Temurin 21.0.11 and proved
# the FULL load-bearing config surface binds through one SPRING_APPLICATION_JSON
# env var (all four, not just the datasource key):
#   (a) spring.datasource.url=jdbc:h2:file:<dir>/db;DB_CLOSE_DELAY=-1;DB_CLOSE_ON_EXIT=FALSE
#       (+ spring.datasource.username=sa, driverClassName=org.h2.Driver)
#       -> H2 files land exactly where pointed
#   (b) hapi.fhir.implementationguides.<key>.{packageUrl,name,version} with
#       packageUrl=file://<abs>.tgz -> IG indexes; profile resolvable
#       (no "Failed to retrieve profile" from $validate)
#   (c) hapi.fhir.tenant_identification_strategy=URL_BASED
#       + hapi.fhir.partitioning.partitioning_include_in_search_hashes=false
#       + hapi.fhir.partitioning.allow_references_across_partitions=false
#       -> "Request tenant partitioning mode" in the boot log; untenanted
#          /fhir/Patient 400s, /fhir/DEFAULT/Patient 200s. NOTE: untenanted
#          /fhir/metadata is special-cased 200 under URL_BASED — probe a DATA
#          route, never metadata, to discriminate tenancy.
#   (d) hapi.fhir.cr.enabled=true -> Questionnaire/$populate route exists
# Fallback channel (unused — recorded for reference): dotted env NAMES, exactly
# as deploy/compose.multiprocess.yml passes them in production today.
#
# ── Launch mechanics (spike-learned, reused verbatim by kit/kitd/javachildren.go) ──
#   java -Xmx… --class-path <abs>/main.war \
#     "-Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes" \
#     org.springframework.boot.loader.PropertiesLauncher
# The loader.path `main.war!/…` entries are CWD-RELATIVE (the images run from
# WORKDIR /app): the process working dir MUST contain main.war (a symlink works).
# /app/extra-classes is a tolerated-missing loader.path entry — NEITHER pinned
# image actually ships the directory; do not create it.
#
# ── KNOWN GAP ──────────────────────────────────────────────────────────────
# This script's live packaging (build.sh through verify.sh, i.e. an actual
# WAR extraction + IG-tgz download + H2 prewarm + boot-verify) is UNPROVEN on
# this branch — recent work touched the tri-line IG pin set (FR-G49/G50) but
# did not run a live kit build. Load-bearing before the next kit release: run
# build.sh + verify.sh for real and fix whatever the tri-line pin widening
# broke before cutting a release.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
DIST="${KIT_ASSETS_DIST:-$REPO/dist/kitassets}"
# shellcheck source=tools/kitassets/pins.env
. "$REPO/tools/kitassets/pins.env"
# shellcheck source=tools/kitassets/igpins.gen.sh
. "$REPO/tools/kitassets/igpins.gen.sh"
PORT="${KIT_ASSETS_PORT:-18080}"
READY_BOUND_FIRST=1200   # 20-min bound on first (indexing) boots
READY_BOUND_WARM=90      # the I3 gate: prewarmed second boot must be ready inside this

log() { printf '[kitassets] %s\n' "$*"; }
die() { printf '[kitassets] FAIL: %s\n' "$*" >&2; exit 1; }

java_bin() {
  if [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/java" ]; then echo "$JAVA_HOME/bin/java"; return; fi
  if [ "$(uname)" = Darwin ] && /usr/libexec/java_home -v 21 >/dev/null 2>&1; then
    echo "$(/usr/libexec/java_home -v 21)/bin/java"; return
  fi
  command -v java >/dev/null || die "no Java found (need Temurin 21: set JAVA_HOME)"
  echo java
}
JAVA="$(java_bin)"
"$JAVA" -version 2>&1 | grep -q 'version "21' || die "Java 21 required, got: $("$JAVA" -version 2>&1 | head -1)"

mkdir -p "$DIST"/{hapi,brprovider,igs-validator,igs-data,prewarm}

# Extraction/download guards use extract-to-tmp + atomic mv: a bare presence
# check would treat a partially-written file (interrupted docker cp / dropped
# curl) as done on the next run and silently bake a corrupt artifact — the
# same discipline as the .prewarm-ok marker.
extract_war() { # extract_war <image> <container-path> <dest>
  local image="$1" src="$2" dest="$3" cid
  cid=$(docker create "$image")
  docker cp -q "$cid:$src" "$dest.tmp" || { docker rm "$cid" >/dev/null; die "docker cp $src from $image failed"; }
  docker rm "$cid" >/dev/null
  mv "$dest.tmp" "$dest"
}

# Notarization guard (mac): Apple's notary service recursively unpacks the
# shipped WARs (war -> jar) and REJECTS any unsigned Mach-O it finds inside a
# nested jar. codesign/osx-sign only reach loose files in the .app bundle, never
# a binary sealed inside a jar, so those are unsignable in place. sqlite-jdbc
# bundles per-platform natives incl. Mac .dylibs, but the Kit never uses it (the
# validator + data server + br-provider all run on H2 -- org.h2.Driver -- never
# jdbc:sqlite), so those dylibs are dead weight that fails `notarytool` ("not
# signed with a valid Developer ID certificate"). Strip just the Mac natives from
# the jar: its Java classes stay (no ClassNotFound if org.sqlite is referenced)
# and the other platforms' natives stay. Spring Boot's PropertiesLauncher needs
# nested jars STORED, so the jar is re-inserted uncompressed (-0). Idempotent +
# asserted, so a cached/rebuilt WAR converges and a future dylib reintroduction
# fails the build here, not 25 minutes into mac notarization.
strip_mac_natives_from_war() { # $1 = war path
  local war="$1" jarent stage
  jarent="$(unzip -Z1 "$war" 'WEB-INF/lib/sqlite-jdbc-*.jar' 2>/dev/null | head -1 || true)"
  [ -n "$jarent" ] || { log "notarization strip: no sqlite-jdbc jar in $war — nothing to strip"; return 0; }
  stage="$(mktemp -d)"
  ( cd "$stage" && unzip -oq "$war" "$jarent" )
  if unzip -Z1 "$stage/$jarent" 'org/sqlite/native/Mac/*' 2>/dev/null | grep -q .; then
    zip -dq "$stage/$jarent" 'org/sqlite/native/Mac/*'
    ( cd "$stage" && zip -0 -X -q "$war" "$jarent" )
    log "notarization strip: removed org/sqlite/native/Mac/** from $jarent in $war"
  else
    log "notarization strip: $jarent already free of Mac natives"
  fi
  # Guard: re-extract and assert no Mac Mach-O survived — an unsigned Mach-O in a
  # nested jar is exactly what fails Apple notarization.
  rm -f "$stage/$jarent"
  ( cd "$stage" && unzip -oq "$war" "$jarent" )
  if unzip -Z1 "$stage/$jarent" 'org/sqlite/native/Mac/*' 2>/dev/null | grep -q .; then
    rm -rf "$stage"; die "notarization strip failed: Mac natives still present in $jarent ($war)"
  fi
  rm -rf "$stage"
}

# ── (a) HAPI WAR from the pinned digest ───────────────────────────────────────
if [ ! -f "$DIST/hapi/main.war" ]; then
  log "extracting HAPI WAR from $HAPI_DIGEST"
  extract_war "$HAPI_DIGEST" /app/main.war "$DIST/hapi/main.war"
else
  log "hapi/main.war present — skip"
fi

# ── (b) br-provider WAR (build the pinned image if absent) ────────────────────
if [ ! -f "$DIST/brprovider/main.war" ]; then
  docker image inspect "$BRP_IMAGE" >/dev/null 2>&1 || "$REPO/tools/brprovider/run.sh" build
  log "extracting br-provider WAR from $BRP_IMAGE"
  # /app/extra-classes does not exist in the pinned image (tolerated-missing
  # loader.path entry, verified at extraction spike) — nothing else to copy.
  extract_war "$BRP_IMAGE" /app/main.war "$DIST/brprovider/main.war"
else
  log "brprovider/main.war present — skip"
fi

# Make both shipped WARs notarization-clean (see strip_mac_natives_from_war).
# Runs every build (idempotent) so a WAR cached from before this guard converges;
# it runs BEFORE prewarm/verify below, so both boot the already-stripped WARs.
strip_mac_natives_from_war "$DIST/hapi/main.war"
strip_mac_natives_from_war "$DIST/brprovider/main.war"

# ── (c) IG packages, from the manifest-generated PER-LINE pin tables
# (igpins.gen.sh) ──────────────────────────────────────────────────────────
# tools/contracts/manifest.json is the single source now (tools/contractsgen).
# The kit ships EVERY line's IG tgz set (validator-sidecar for 2.0/2.1,
# validator-sidecar-ext — the extensions-closure superset — for 2.2), not just
# line 2.0's, so kitd.BuildValidatorChildSpec can boot its validator at an
# operator-configured line, at an accepted download/notarization size cost.
# All lines' packages land in the SAME two subdirectories
# (igs-validator/igs-data) — filenames already disambiguate by version, and
# shared packages (e.g. cdex, pinned identically across every line) download
# exactly ONCE thanks to ig()'s existing "skip if already present" guard, so
# the union costs less than a naive per-line-subdirectory layout would.
ig() { # ig <dir> <file> <simplifier-path> — download-to-tmp + atomic mv (see above)
  if [ ! -s "$DIST/$1/$2" ]; then
    case "$3" in
      shn.fhir.carry/*)
        # shn.fhir.carry is SHN-authored, never published to Simplifier — every
        # OTHER row in igpins.gen.sh is a real Simplifier package, so this is
        # the ONE name that must never hit the curl branch below. Sourced from
        # the LOCALLY built package (tools/shnig, deterministic tgz) instead,
        # same posture as gateway/deploy/validator/Dockerfile's COPY solve. A
        # fresh checkout (incl. the nightly CI runner) has no dist/ yet, so
        # build it here if missing — `make shnig-build` does the same thing
        # by hand. If tools/shnig itself is absent (a kit-repo snapshot
        # checkout does not mirror tools/shnig or
        # tools/contracts/manifest.json today), fail LOUDLY naming the fix
        # rather than falling through to a 404 curl.
        local_src="$REPO/dist/shnig/$2"
        if [ ! -s "$local_src" ]; then
          if [ ! -d "$REPO/tools/shnig" ]; then
            die "shn.fhir.carry: no local source (tools/shnig absent — a kit-repo snapshot checkout?) and no pre-built $local_src. Build from a full monorepo checkout ('make shnig-build'), or ship a pre-built shn.fhir.carry-*.tgz at that path."
          fi
          log "shn.fhir.carry not yet built locally — building via tools/shnig (see 'make shnig-build')"
          ( cd "$REPO" && go run ./tools/shnig ) || die "tools/shnig build failed — run 'make shnig-build' manually and retry"
        fi
        [ -s "$local_src" ] || die "tools/shnig ran but did not produce $local_src"
        log "IG (local build): $2"
        cp "$local_src" "$DIST/$1/$2.tmp"
        mv "$DIST/$1/$2.tmp" "$DIST/$1/$2"
        ;;
      *)
        log "IG download: $2"
        curl --retry 5 --retry-delay 5 --retry-all-errors -fLSso "$DIST/$1/$2.tmp" "https://packages.simplifier.net/$3"
        mv "$DIST/$1/$2.tmp" "$DIST/$1/$2"
        ;;
    esac
  fi
}
for line in "${KITASSETS_LINES[@]}"; do
  suffix="${line//./}"
  validator_var="KITASSETS_VALIDATOR_IGS_${suffix}[@]"
  data_var="KITASSETS_DATA_IGS_${suffix}[@]"
  for row in "${!validator_var}"; do
    read -r _ name version <<<"$row"
    ig igs-validator "$name-$version.tgz" "$name/$version"
  done
  for row in "${!data_var}"; do
    read -r _ name version <<<"$row"
    ig igs-data "$name-$version.tgz" "$name/$version"
  done
done
( cd "$DIST" && shasum -a 256 igs-validator/*.tgz igs-data/*.tgz ) | tee "$DIST/igs.sha256"

# Size-delta note (the delta is recorded in the kit manifest) —
# unique tgz file counts, all-lines union vs. the single-line-2.0 baseline;
# computed from the pin tables (not `du` on the downloaded bytes), so it is
# stable and reviewable independent of package sizes changing upstream.
# manifest.sh recomputes + writes the SAME note into versions.json (its own
# copy, from the same igpins.gen.sh source — no dependency on this file
# having run first).
IGS_20_COUNT=$(( ${#KITASSETS_VALIDATOR_IGS_20[@]} + ${#KITASSETS_DATA_IGS_20[@]} ))
IGS_ALL_UNIQUE=$(
  for line in "${KITASSETS_LINES[@]}"; do
    suffix="${line//./}"
    validator_var="KITASSETS_VALIDATOR_IGS_${suffix}[@]"
    data_var="KITASSETS_DATA_IGS_${suffix}[@]"
    for row in "${!validator_var}" "${!data_var}"; do
      read -r _ name version <<<"$row"
      printf '%s-%s\n' "$name" "$version"
    done
  done | sort -u | wc -l | tr -d ' '
)
log "IG set size: line 2.0 alone = $IGS_20_COUNT rows; all ${#KITASSETS_LINES[@]} lines' union = $IGS_ALL_UNIQUE unique tgz files (accepted size cost)"

# ── IG config JSON fragments (the spike-certified nested-map shape) ───────────
# The package-time PREWARM boot below stays scoped to line 2.0 ONLY — the
# canonical default, the one line this pipeline pays the 10-15-min IG-indexing
# cost for so a user's machine never has to (any other line boots cold, at
# runtime, only if an operator explicitly configures it — --validator-line /
# --additional-validator-lines; kit/kitd/javachildren.go's
# javaReadyTimeoutCold reflects that). Do NOT triple-boot the prewarm here.
ig_json() { # ig_json <dir> <key> <name> <version> -> three JSON k/v lines
  printf '"hapi.fhir.implementationguides.%s.packageUrl":"file://%s/%s/%s-%s.tgz","hapi.fhir.implementationguides.%s.name":"%s","hapi.fhir.implementationguides.%s.version":"%s"' \
    "$2" "$DIST" "$1" "$3" "$4" "$2" "$3" "$2" "$4"
}
VALIDATOR_IGS=""
for row in "${KITASSETS_VALIDATOR_IGS_20[@]}"; do
  read -r key name version <<<"$row"
  VALIDATOR_IGS="${VALIDATOR_IGS:+$VALIDATOR_IGS,}$(ig_json igs-validator "$key" "$name" "$version")"
done
DATA_IGS=""
for row in "${KITASSETS_DATA_IGS_20[@]}"; do
  read -r key name version <<<"$row"
  DATA_IGS="${DATA_IGS:+$DATA_IGS,}$(ig_json igs-data "$key" "$name" "$version")"
done

# Validator: single-tenant $validate only (mirrors gateway/deploy/validator/Dockerfile).
validator_config() { # $1=h2dir
  printf '{"spring.datasource.url":"jdbc:h2:file:%s/db;DB_CLOSE_DELAY=-1;DB_CLOSE_ON_EXIT=FALSE","spring.datasource.username":"sa","spring.datasource.driverClassName":"org.h2.Driver","server.port":"%s",%s}' "$1" "$PORT" "$VALIDATOR_IGS"
}
# Data server: URL_BASED multitenancy + partitioning flags + operated CQL
# (mirrors deploy/compose.multiprocess.yml:48-84 — verification fact 5).
data_config() { # $1=h2dir
  printf '{"spring.datasource.url":"jdbc:h2:file:%s/db;DB_CLOSE_DELAY=-1;DB_CLOSE_ON_EXIT=FALSE","spring.datasource.username":"sa","spring.datasource.driverClassName":"org.h2.Driver","server.port":"%s",%s,"hapi.fhir.tenant_identification_strategy":"URL_BASED","hapi.fhir.partitioning.partitioning_include_in_search_hashes":"false","hapi.fhir.partitioning.allow_references_across_partitions":"false","hapi.fhir.cr.enabled":"true"}' "$1" "$PORT" "$DATA_IGS"
}

# boot_war sets globals (BOOT_PID, BOOT_ELAPSED) — deliberately NOT $()-captured:
# a command substitution would block on the pipe the backgrounded subshell holds
# until the JVM exits. `exec` makes $! the JVM's own PID (kill/stop work), and
# the redirect sits on the parens so no descendant holds the caller's stdout.
BOOT_PID=""
BOOT_ELAPSED=0
boot_war() { # boot_war <workdir> <config-json> <ready-path> <bound-seconds>
  local work="$1" config="$2" ready="$3" bound="$4"
  mkdir -p "$work"
  [ -e "$work/main.war" ] || ln -s "$DIST/hapi/main.war" "$work/main.war"
  ( cd "$work" && exec env SPRING_APPLICATION_JSON="$config" "$JAVA" -Xmx768m \
      --class-path "$work/main.war" \
      "-Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes" \
      org.springframework.boot.loader.PropertiesLauncher ) >> "$work/boot.log" 2>&1 &
  BOOT_PID=$!
  local start deadline code
  start=$(date +%s); deadline=$((start + bound))
  while :; do
    code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT$ready" || true)
    [ "$code" = 200 ] && break
    [ "$(date +%s)" -le "$deadline" ] || { kill "$BOOT_PID" 2>/dev/null || true; die "WAR not ready on $ready within ${bound}s (log: $work/boot.log)"; }
    kill -0 "$BOOT_PID" 2>/dev/null || die "WAR died during boot (log: $work/boot.log)"
    sleep 3
  done
  BOOT_ELAPSED=$(( $(date +%s) - start ))
}
stop_war() { # clean shutdown, wait for exit
  kill "$BOOT_PID" 2>/dev/null || true
  wait "$BOOT_PID" 2>/dev/null || true
  BOOT_PID=""
}

# Inventory assertion: after each clean shutdown the
# WAR's working dir must contain ONLY the war symlink + boot.log — persistent
# state written OUTSIDE the (external) H2 dir fails BY NAME here, so a missed
# directory (Lucene index, package cache) presents as a named gap in the
# pipeline, never as a mysterious re-index at the I3 gate.
assert_workdir_clean() { # $1=workdir
  local stray
  stray=$(find "$1" -mindepth 1 -not -name main.war -not -name boot.log | head -20)
  [ -z "$stray" ] || die "WAR wrote persistent state outside its H2 dir: $stray"
}

# ── (d) prewarm: validator (8 IGs, single-tenant) ─────────────────────────────
if [ ! -f "$DIST/prewarm/validator-h2/.prewarm-ok" ]; then
  rm -rf "$DIST/prewarm/validator-h2" "$DIST/prewarm/.work-validator"
  log "prewarm validator: first boot (IG indexing — the slow one)"
  boot_war "$DIST/prewarm/.work-validator" "$(validator_config "$DIST/prewarm/validator-h2")" /fhir/metadata "$READY_BOUND_FIRST"
  log "validator first boot ready in ${BOOT_ELAPSED}s"
  stop_war
  assert_workdir_clean "$DIST/prewarm/.work-validator"
  touch "$DIST/prewarm/validator-h2/.prewarm-ok"
else
  log "validator-h2 prewarmed — skip"
fi

# ── (d) prewarm: data server (4 IGs, URL_BASED + partitions + CR + personas) ──
if [ ! -f "$DIST/prewarm/data-h2/.prewarm-ok" ]; then
  rm -rf "$DIST/prewarm/data-h2" "$DIST/prewarm/.work-data"
  log "prewarm data server: first boot + full persona seed"
  boot_war "$DIST/prewarm/.work-data" "$(data_config "$DIST/prewarm/data-h2")" /fhir/DEFAULT/metadata "$READY_BOUND_FIRST"
  log "data server first boot ready in ${BOOT_ELAPSED}s"
  ( cd "$REPO/kit" && go run ./cmd/prewarm --base "http://127.0.0.1:$PORT/fhir" )
  stop_war
  assert_workdir_clean "$DIST/prewarm/.work-data"
  touch "$DIST/prewarm/data-h2/.prewarm-ok"
else
  log "data-h2 prewarmed — skip"
fi

# ── (e) The I3 gate: measure SECOND boot against COPIES of the prewarmed H2 ───
# A $validate-only HAPI may hold its IG packages in the in-memory validation
# chain, not H2 — the prewarm's value for the validator is unproven until
# measured. If either warm boot exceeds ${READY_BOUND_WARM}s the pipeline STOPS
# here (decide the fallback on evidence — never on an end user's machine).
i3_check() { # $1=name $2=h2dir $3=config-fn $4=ready-path — leaves the WAR RUNNING (caller stops)
  local work="$DIST/prewarm/.i3-$1" h2copy
  rm -rf "$work"; mkdir -p "$work"
  h2copy="$work/h2"
  cp -R "$2" "$h2copy"
  boot_war "$work" "$($3 "$h2copy")" "$4" "$READY_BOUND_WARM"
  log "I3: $1 warm boot ready in ${BOOT_ELAPSED}s (bound ${READY_BOUND_WARM}s)"
}
i3_check validator "$DIST/prewarm/validator-h2" validator_config /fhir/metadata
tv=$BOOT_ELAPSED
stop_war
i3_check data "$DIST/prewarm/data-h2" data_config /fhir/DEFAULT/metadata
td=$BOOT_ELAPSED
# Seed-survival proof while the warm data server is still up:
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/fhir/provider/Basic/seed-complete")
[ "$code" = 200 ] || { stop_war; die "seed marker not readable after warm reboot (got $code) — prewarm did not survive shutdown"; }
log "seed survived shutdown: /fhir/provider/Basic/seed-complete -> 200"
stop_war
rm -rf "$DIST/prewarm/.i3-validator" "$DIST/prewarm/.i3-data" "$DIST/prewarm/.work-validator" "$DIST/prewarm/.work-data"

log "DONE: layout under $DIST (validator warm ${tv}s, data warm ${td}s)"
