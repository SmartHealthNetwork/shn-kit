#!/usr/bin/env bash
#
# tools/brprovider/build-corrected.sh — build the pinned HL7-DaVinci/br-provider commit
# plus the reviewed correction series (patches/series) as br-provider:${BRPROVIDER_IMAGE_TAG}.
#
# The Kit's asset build (tools/kitassets/build.sh) ensures this image and extracts the
# WAR it bundles; a local reference-pair stack runs the same image. This script,
# source.env, the scripts it runs and the reviewed series must build from the Kit's
# own tree alone. The uncorrected upstream image br-provider:43a4806 is a different
# tag, and nothing here builds or overwrites it.
#
# Usage:
#   tools/brprovider/build-corrected.sh build         # prepare the series + build the corrected image
#   tools/brprovider/build-corrected.sh ensure-image  # rebuild only when source/series labels differ
set -euo pipefail

REPO_URL="https://github.com/HL7-DaVinci/br-provider.git"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/source.env"
PIN="$BRPROVIDER_UPSTREAM_PIN"
IMAGE="br-provider:${BRPROVIDER_IMAGE_TAG}"
CLONE_DIR="${BRPROVIDER_CLONE_DIR:-/tmp/br-provider}"
PATCH=""
prepare_patch() {
  if [ -z "$PATCH" ]; then
    PATCH="$(mktemp "${TMPDIR:-/tmp}/provider-run-series.XXXXXX")"
    trap 'rm -f "$PATCH"' EXIT
    "${SCRIPT_DIR}/prepare-patch-series.sh" "$PATCH" >/dev/null
  fi
}

ensure_source() {
  if [ ! -d "${CLONE_DIR}/.git" ]; then
    git clone --no-checkout "${REPO_URL}" "${CLONE_DIR}"
  fi
  if ! git -C "${CLONE_DIR}" cat-file -e "${PIN}^{commit}" 2>/dev/null; then
    git -C "${CLONE_DIR}" fetch origin "${PIN}"
  fi
}

build() {
  prepare_patch
  ensure_source
  "${SCRIPT_DIR}/build-image.sh" "${CLONE_DIR}" "$PIN" "$PATCH" "$IMAGE" local
}

ensure_image() {
  prepare_patch
  local expected
  expected="$(shasum -a 256 "$PATCH" | awk '{print $1}')"
  if [ "$(docker image inspect --format '{{index .Config.Labels "org.shn.provider.patch"}}' "$IMAGE" 2>/dev/null || true)" != "$expected" ] || \
     [ "$(docker image inspect --format '{{index .Config.Labels "org.shn.provider.upstream"}}' "$IMAGE" 2>/dev/null || true)" != "$PIN" ]; then
    build
  fi
}

case "${1:-build}" in
  build) build ;;
  ensure-image) ensure_image ;;
  *) echo "usage: $0 {build|ensure-image}" >&2; exit 2 ;;
esac
