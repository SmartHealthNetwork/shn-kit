#!/usr/bin/env bash
# Export an exact br-provider source commit and apply the reviewed series without checking out or
# modifying the caller's repository. Publish the build context only on success.
set -euo pipefail
if [ "$#" -ne 4 ]; then
  echo "usage: $0 SOURCE_REPO FULL_COMMIT PATCH DESTINATION" >&2
  exit 2
fi
SOURCE_REPO="$1"
SOURCE_REPO="$(cd "$SOURCE_REPO" && pwd)"
REVISION="$2"
PATCH_FILE="$3"
DESTINATION="$4"
if [[ ! "$REVISION" =~ ^[0-9a-f]{40}$ ]]; then
  echo "source revision must be a full immutable commit id" >&2
  exit 1
fi
if [ -e "$DESTINATION" ] || [ -L "$DESTINATION" ]; then
  echo "destination already exists; refusing to overwrite it" >&2
  exit 1
fi
if [ ! -f "$PATCH_FILE" ] || [ ! -s "$PATCH_FILE" ]; then
  echo "reviewed source patch missing or empty" >&2
  exit 1
fi
PATCH_FILE="$(cd "$(dirname "$PATCH_FILE")" && pwd)/$(basename "$PATCH_FILE")"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1
git -C "$SOURCE_REPO" cat-file -e "${REVISION}^{commit}"
PARENT="$(cd "$(dirname "$DESTINATION")" && pwd)"
DESTINATION="${PARENT}/$(basename "$DESTINATION")"
SCRATCH="$(mktemp -d "${PARENT}/.provider-source.XXXXXX")"
trap 'rm -rf "$SCRATCH"' EXIT
cp "$PATCH_FILE" "$SCRATCH/correction.patch"
# Fetch into private Git state rather than archive inside the caller repository:
# local export-ignore attributes and replacement refs must not change the source.
git init -q "$SCRATCH/source"
git -C "$SCRATCH/source" config core.attributesFile /dev/null
git -C "$SCRATCH/source" config core.autocrlf false
git -C "$SCRATCH/source" fetch -q --no-tags --depth=1 "$SOURCE_REPO" "$REVISION"
git -C "$SCRATCH/source" checkout -q --detach "$REVISION"
git -C "$SCRATCH/source" apply --check "$SCRATCH/correction.patch"
git -C "$SCRATCH/source" apply "$SCRATCH/correction.patch"
rm -rf "$SCRATCH/source/.git"
mv "$SCRATCH/source" "$DESTINATION"
