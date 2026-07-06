#!/usr/bin/env bash
#
# tools/kitassets/verify.sh — the "module union verified" gate: boot
# validator, data server (both on COPIES of their prewarmed H2), and
# br-provider on the CURRENT platform's freshly linked JRE; all three ready
# probes 2xx ⇒ PASS. jdeps output is advisory — THIS is the proof.
#
# br-provider's payer env quad deliberately points at a NOT-YET-LIVE loopback
# port and the script asserts it still reaches /fhir/metadata ready anyway:
# the Kit's boot order starts br-provider before the gateway it will call —
# this row turns that sequencing assumption into a proven fact.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
DIST="${KIT_ASSETS_DIST:-$REPO/dist/kitassets}"
case "$(uname -sm)" in
  "Darwin arm64")  HOST_TARGET=darwin-arm64 ;;
  "Darwin x86_64") HOST_TARGET=darwin-amd64 ;;
  "Linux x86_64")  HOST_TARGET=linux-amd64 ;;
  *) echo "unsupported verify host: $(uname -sm)" >&2; exit 1 ;;
esac
JRE="${KIT_JRE:-$DIST/jre-$HOST_TARGET}"
[ -x "$JRE/bin/java" ] || { echo "linked JRE missing at $JRE — run jlink.sh $HOST_TARGET first" >&2; exit 1; }
# Temurin's OpenJDK class libraries are GPLv2 WITH THE CLASSPATH EXCEPTION —
# the required notice is shipping the JRE's own legal/ tree (jlink preserves
# it from the JDK image by default; this only breaks if a future jlink
# invocation strips it). See docs/kit-license-audit.md.
[ -d "$JRE/legal" ] || { echo "jlink output lost the legal/ notices tree (license-required) at $JRE/legal" >&2; exit 1; }

VPORT=18085 DPORT=18086 BPORT=18087 DEADPORT=19999
WORK=$(mktemp -d "${TMPDIR:-/tmp}/kitverify.XXXXXX")
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT

fail() { # fail <name> <log>
  echo "[verify] FAIL: $1 never became ready on the linked JRE. Log tail (missing-module signature, if any):" >&2
  tail -30 "$2" >&2
  grep -iE 'ClassNotFound|NoClassDefFound|module' "$2" | tail -5 >&2 || true
  exit 1
}

boot() { # boot <name> <war> <workdir> <ready-url> <bound> <env...>
  local name="$1" war="$2" work="$3" ready="$4" bound="$5"; shift 5
  mkdir -p "$work"
  [ -e "$work/main.war" ] || ln -s "$war" "$work/main.war"
  # exec: $! must be the JVM's own PID so cleanup's kill reaps it (a bare
  # subshell PID would leave the JVM orphaned); redirect on the parens so no
  # descendant holds this script's stdout.
  ( cd "$work" && exec env "$@" "$JRE/bin/java" -Xmx768m \
      --class-path "$work/main.war" \
      "-Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes" \
      org.springframework.boot.loader.PropertiesLauncher ) > "$work/boot.log" 2>&1 &
  local pid=$!
  PIDS+=("$pid")
  local deadline=$(( $(date +%s) + bound ))
  while :; do
    local code
    code=$(curl -s -o /dev/null -w '%{http_code}' "$ready" || true)
    [ "$code" = 200 ] && { echo "[verify] $name ready"; return 0; }
    [ "$(date +%s)" -le "$deadline" ] || fail "$name" "$work/boot.log"
    kill -0 "$pid" 2>/dev/null || fail "$name (process died)" "$work/boot.log"
    sleep 3
  done
}

igj() { # igj <dir> <key> <name> <ver>
  printf '"hapi.fhir.implementationguides.%s.packageUrl":"file://%s/%s/%s-%s.tgz","hapi.fhir.implementationguides.%s.name":"%s","hapi.fhir.implementationguides.%s.version":"%s"' \
    "$2" "$DIST" "$1" "$3" "$4" "$2" "$3" "$2" "$4"
}

# 1. Validator (8 IGs, single-tenant) on a copy of its prewarmed H2.
cp -R "$DIST/prewarm/validator-h2" "$WORK/validator-h2"
VCONF="{\"spring.datasource.url\":\"jdbc:h2:file:$WORK/validator-h2/db;DB_CLOSE_DELAY=-1;DB_CLOSE_ON_EXIT=FALSE\",\"spring.datasource.username\":\"sa\",\"spring.datasource.driverClassName\":\"org.h2.Driver\",\"server.port\":\"$VPORT\",$(igj igs-validator uscore hl7.fhir.us.core 6.1.0),$(igj igs-validator crd hl7.fhir.us.davinci-crd 2.0.1),$(igj igs-validator dtr hl7.fhir.us.davinci-dtr 2.0.1),$(igj igs-validator pas hl7.fhir.us.davinci-pas 2.0.1),$(igj igs-validator pdex hl7.fhir.us.davinci-pdex 2.1.0),$(igj igs-validator sdc hl7.fhir.uv.sdc 3.0.0),$(igj igs-validator cdex hl7.fhir.us.davinci-cdex 2.1.0),$(igj igs-validator hrex hl7.fhir.us.davinci-hrex 1.1.0)}"
boot validator "$DIST/hapi/main.war" "$WORK/validator" "http://127.0.0.1:$VPORT/fhir/metadata" 120 \
  SPRING_APPLICATION_JSON="$VCONF"

# 2. Data server (4 IGs, URL_BASED + partitioning + CR) on a copy of its prewarmed H2.
cp -R "$DIST/prewarm/data-h2" "$WORK/data-h2"
DCONF="{\"spring.datasource.url\":\"jdbc:h2:file:$WORK/data-h2/db;DB_CLOSE_DELAY=-1;DB_CLOSE_ON_EXIT=FALSE\",\"spring.datasource.username\":\"sa\",\"spring.datasource.driverClassName\":\"org.h2.Driver\",\"server.port\":\"$DPORT\",$(igj igs-data uscore hl7.fhir.us.core 6.1.0),$(igj igs-data cdex hl7.fhir.us.davinci-cdex 2.1.0),$(igj igs-data hrex hl7.fhir.us.davinci-hrex 1.1.0),$(igj igs-data pas hl7.fhir.us.davinci-pas 2.0.1),\"hapi.fhir.tenant_identification_strategy\":\"URL_BASED\",\"hapi.fhir.partitioning.partitioning_include_in_search_hashes\":\"false\",\"hapi.fhir.partitioning.allow_references_across_partitions\":\"false\",\"hapi.fhir.cr.enabled\":\"true\"}"
boot data-server "$DIST/hapi/main.war" "$WORK/data" "http://127.0.0.1:$DPORT/fhir/DEFAULT/metadata" 120 \
  SPRING_APPLICATION_JSON="$DCONF"

# 3. br-provider — payer URLs at a DEAD port (M10), throwaway PFX (its
# CertificateHolder needs a loadable cert file at boot; fetch-cert=false).
# 600s bound (vs 120s for the prewarmed HAPIs): br-provider has no prewarm —
# its embedded FHIR server builds a fresh H2 + seeds its curated personas on
# every boot (the two-RI compose grants it similar first-boot headroom).
openssl req -x509 -newkey rsa:2048 -keyout "$WORK/brp-key.pem" -out "$WORK/brp-cert.pem" \
  -days 1 -nodes -subj "/CN=kit-verify" > "$WORK/openssl.log" 2>&1 \
  || { echo "[verify] FAIL: openssl keypair generation failed:" >&2; cat "$WORK/openssl.log" >&2; exit 1; }
openssl pkcs12 -export -out "$WORK/brp.pfx" -inkey "$WORK/brp-key.pem" -in "$WORK/brp-cert.pem" \
  -passout pass:kit-verify >> "$WORK/openssl.log" 2>&1 \
  || { echo "[verify] FAIL: openssl pkcs12 export failed:" >&2; cat "$WORK/openssl.log" >&2; exit 1; }
boot br-provider "$DIST/brprovider/main.war" "$WORK/brp" "http://127.0.0.1:$BPORT/fhir/metadata" 600 \
  SERVER_PORT="$BPORT" \
  APP_PAYER_SERVERS_0_CDS_URL="http://127.0.0.1:$DEADPORT/cds-services" \
  APP_PAYER_SERVERS_0_FHIR_URL="http://127.0.0.1:$DEADPORT" \
  SECURITY_ALLOWEDLOCALHOSTS_0="127.0.0.1" \
  SECURITY_EXTERNAL_BASE_URL="http://127.0.0.1:$BPORT" \
  SECURITY_CERT_FILE="$WORK/brp.pfx" \
  SECURITY_CERT_PASSWORD="kit-verify" \
  SECURITY_FETCH_CERT="false"

cleanup; trap - EXIT
rm -rf "$WORK"
echo "[verify] PASS: validator + data server + br-provider all ready on $JRE (br-provider against a not-yet-live payer host)"
