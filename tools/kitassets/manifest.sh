#!/usr/bin/env bash
#
# tools/kitassets/manifest.sh — emit dist/kitassets/versions.json:
# the package-time versions manifest shnkitd serves at GET /api/about, the UI
# renders as About, and the support bundle includes. Kit semver's single source
# of truth is desktop/package.json (electron-builder consumes the same field;
# the packaging pipeline injects it into shnkitd via -ldflags -X main.kitVersion=).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
DIST="${KIT_ASSETS_DIST:-$REPO/dist/kitassets}"
# shellcheck source=tools/kitassets/pins.env
. "$REPO/tools/kitassets/pins.env"
mkdir -p "$DIST"

# Version reads fail LOUD on format drift: the whole go.mod token is captured
# and must be a clean tagged semver — a pseudo-version (replace'd dev build)
# must never silently truncate into versions.json.
mod_version() { # mod_version <module-path>
  local v
  v=$(awk -v m="$1" '$1 == m { print $2 }' "$REPO/kit/go.mod")
  case "$v" in
    v[0-9]*.[0-9]*.[0-9]*) case "$v" in *-*) return 1 ;; esac; printf '%s' "$v" ;;
    *) return 1 ;;
  esac
}
KIT_VERSION=$(sed -n 's/^  "version": "\(.*\)",$/\1/p' "$REPO/desktop/package.json")
GW_VERSION=$(mod_version github.com/SmartHealthNetwork/shn-gateway) \
  || { echo "manifest: shn-gateway version in kit/go.mod is not a clean tagged semver" >&2; exit 1; }
SDK_VERSION=$(mod_version github.com/SmartHealthNetwork/shn-sdk) \
  || { echo "manifest: shn-sdk version in kit/go.mod is not a clean tagged semver" >&2; exit 1; }
GIT_SHA=$(git -C "$REPO" rev-parse --short HEAD)
STAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
[ -n "$KIT_VERSION" ] && [ -n "$GW_VERSION" ] && [ -n "$SDK_VERSION" ] \
  || { echo "manifest: failed to read versions (kit='$KIT_VERSION' gw='$GW_VERSION' sdk='$SDK_VERSION')" >&2; exit 1; }

# Image/runtime pins come from pins.env (shared with build.sh — one source);
# the IG sets mirror the two offline-bake Dockerfiles.
cat > "$DIST/versions.json" <<EOF
{
  "kit": "$KIT_VERSION",
  "modules": {
    "shn-gateway": "$GW_VERSION",
    "shn-sdk": "$SDK_VERSION"
  },
  "brProvider": "$BRP_COMMIT",
  "hapiImage": "$HAPI_DIGEST",
  "temurin": "$TEMURIN_RELEASE",
  "igsValidator": [
    "hl7.fhir.us.core 6.1.0",
    "hl7.fhir.us.davinci-crd 2.0.1",
    "hl7.fhir.us.davinci-dtr 2.0.1",
    "hl7.fhir.us.davinci-pas 2.0.1",
    "hl7.fhir.us.davinci-pdex 2.1.0",
    "hl7.fhir.uv.sdc 3.0.0",
    "hl7.fhir.us.davinci-cdex 2.1.0",
    "hl7.fhir.us.davinci-hrex 1.1.0"
  ],
  "igsData": [
    "hl7.fhir.us.core 6.1.0",
    "hl7.fhir.us.davinci-cdex 2.1.0",
    "hl7.fhir.us.davinci-hrex 1.1.0",
    "hl7.fhir.us.davinci-pas 2.0.1"
  ],
  "build": {
    "timestamp": "$STAMP",
    "commit": "$GIT_SHA"
  }
}
EOF
echo "[manifest] wrote $DIST/versions.json (kit $KIT_VERSION, gw $GW_VERSION, sdk $SDK_VERSION, commit $GIT_SHA)"
