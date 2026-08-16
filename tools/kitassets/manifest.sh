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
# shellcheck source=tools/kitassets/igpins.gen.sh
. "$REPO/tools/kitassets/igpins.gen.sh"
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

# IG sets come from igpins.gen.sh (manifest-generated); versions.json only wants
# "<package-name> <version>" per entry (no key), so strip the key column.
igs_json_list() { # igs_json_list <row...> -> quoted, comma-joined "name version" entries
  local row name version out=""
  for row in "$@"; do
    read -r _ name version <<<"$row"
    out="${out:+$out,
    }\"$name $version\""
  done
  printf '%s' "$out"
}
# Top-level igsValidator/igsData stay line-2.0's sets, with their original
# meaning: "the IGs the package-time prewarm boots on".
IGS_VALIDATOR_JSON="$(igs_json_list "${KITASSETS_VALIDATOR_IGS_20[@]}")"
IGS_DATA_JSON="$(igs_json_list "${KITASSETS_DATA_IGS_20[@]}")"

# igLines: the per-line breakdown for EVERY line the kit ships tgz sets for
# (KITASSETS_LINES, from igpins.gen.sh) — additive to the two flat fields
# above, so the About panel can show all three lines without a breaking shape
# change for any older consumer of the flat igsValidator/igsData fields.
IG_LINES_JSON=""
for line in "${KITASSETS_LINES[@]}"; do
  suffix="${line//./}"
  validator_var="KITASSETS_VALIDATOR_IGS_${suffix}[@]"
  data_var="KITASSETS_DATA_IGS_${suffix}[@]"
  line_validator_json="$(igs_json_list "${!validator_var}")"
  line_data_json="$(igs_json_list "${!data_var}")"
  IG_LINES_JSON="${IG_LINES_JSON:+$IG_LINES_JSON,
  }\"$line\":{\"igsValidator\":[$line_validator_json],\"igsData\":[$line_data_json]}"
done

# igSizeNote: the accepted download/notarization size
# delta of shipping every line's tgz set instead of just line 2.0's — unique
# file counts, derived from the pin tables (not `du` on the actual downloaded
# bytes, so this note is correct even in a dev checkout that never ran
# build.sh). Mirrors build.sh's own IGS_20_COUNT/IGS_ALL_UNIQUE log line.
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
IGS_SIZE_NOTE="ships ${#KITASSETS_LINES[@]} contract lines' IG tgz sets: line 2.0 alone = $IGS_20_COUNT rows; all lines' union = $IGS_ALL_UNIQUE unique tgz files (accepted download/notarization size cost)"

# Image/runtime pins come from pins.env (shared with build.sh — one source);
# the IG sets mirror the two offline-bake Dockerfiles (via igpins.gen.sh).
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
    $IGS_VALIDATOR_JSON
  ],
  "igsData": [
    $IGS_DATA_JSON
  ],
  "igLines": {
    $IG_LINES_JSON
  },
  "igSizeNote": "$IGS_SIZE_NOTE",
  "build": {
    "timestamp": "$STAMP",
    "commit": "$GIT_SHA"
  }
}
EOF
echo "[manifest] wrote $DIST/versions.json (kit $KIT_VERSION, gw $GW_VERSION, sdk $SDK_VERSION, commit $GIT_SHA)"
