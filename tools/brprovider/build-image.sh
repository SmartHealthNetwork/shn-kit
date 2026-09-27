#!/usr/bin/env bash
# Local and published br-provider images use exactly the same prepared source contract.
#   local  host-arch `docker build` (local builds and checks)
#   amd64  `docker buildx --platform linux/amd64 --load` (for a published amd64 image)
set -euo pipefail
if [ "$#" -ne 5 ] || { [ "$5" != local ] && [ "$5" != amd64 ]; }; then
  echo "usage: $0 SOURCE_REPO FULL_COMMIT PATCH IMAGE local|amd64" >&2
  exit 2
fi
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/provider-build.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
cp "$3" "${WORK}/correction.patch"
PATCH_SHA="$(shasum -a 256 "${WORK}/correction.patch" | awk '{print $1}')"
"${ROOT}/prepare-source.sh" "$1" "$2" "${WORK}/correction.patch" "${WORK}/source"
printf 'provider build identity: upstream=%s series-sha256=%s image=%s mode=%s\n' "$2" "$PATCH_SHA" "$4" "$5"
LABELS=(--label "org.shn.provider.upstream=$2" --label "org.shn.provider.patch=${PATCH_SHA}")
if [ "$5" = amd64 ]; then
  docker buildx build --platform linux/amd64 --provenance=false "${LABELS[@]}" -t "$4" --load "${WORK}/source"
else
  docker build "${LABELS[@]}" -t "$4" "${WORK}/source"
fi
