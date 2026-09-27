#!/usr/bin/env bash
# Snapshot the complete reviewed br-provider correction in its application order.
set -euo pipefail
if [ "$#" -ne 1 ]; then
  echo "usage: $0 OUTPUT" >&2
  exit 2
fi
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PATCHES=()
while IFS= read -r entry || [ -n "$entry" ]; do
  if [[ ! "$entry" =~ ^[a-zA-Z0-9_-]+\.patch$ ]]; then
    echo "invalid patch basename: $entry" >&2
    exit 1
  fi
  for previous in ${PATCHES[@]+"${PATCHES[@]}"}; do
    if [ "$previous" = "$entry" ]; then
      echo "duplicate patch: $entry" >&2
      exit 1
    fi
  done
  if [ ! -s "${ROOT}/patches/${entry}" ]; then
    echo "missing or empty patch: $entry" >&2
    exit 1
  fi
  PATCHES+=("$entry")
done < "${ROOT}/patches/series"
# The reviewed corrections, in application order. A partial, extended or reordered
# series is not another valid build identity: `series` must name exactly these.
REVIEWED=(
  dtr-package-bundle-name.patch
  dtr-image-build-tests.patch
  maven-gclocker-retry.patch
)
if [ "${#PATCHES[@]}" -ne "${#REVIEWED[@]}" ]; then
  echo "expected the ${#REVIEWED[@]}-patch reviewed series, got ${#PATCHES[@]} entries" >&2
  exit 1
fi
for i in "${!REVIEWED[@]}"; do
  if [ "${PATCHES[$i]}" != "${REVIEWED[$i]}" ]; then
    echo "series entry $((i + 1)) is ${PATCHES[$i]}; the reviewed series expects ${REVIEWED[$i]}" >&2
    exit 1
  fi
done
WORK="$(mktemp "${TMPDIR:-/tmp}/provider-series.XXXXXX")"
trap 'rm -f "$WORK"' EXIT
for entry in "${PATCHES[@]}"; do
  cat "${ROOT}/patches/${entry}" >> "$WORK"
done
# Do not truncate a caller output until every entry has been checked and copied.
cp "$WORK" "$1"
shasum -a 256 "$WORK" | awk '{print $1}'
