#!/usr/bin/env bash
#
# tools/kitassets/jlink.sh <target> — link the trimmed Temurin 21 runtime for one
# platform. Targets follow Go's GOOS-GOARCH naming — the SAME
# naming kit/cmd/shnkitd/main.go and test/kitlive/java_test.go default to when
# resolving {java-assets}/jre-{GOOS}-{GOARCH} — NOT Temurin's own archive naming:
#   linux-amd64 darwin-arm64 darwin-amd64 windows-amd64
#
# Temurin publishes jmods inside the per-platform JDK archives under ITS OWN
# x64/aarch64 nomenclature; jlink cross-targets by --module-path, so one ubuntu
# host can link every platform's JRE. Each case arm below maps one Go-named
# target to its Temurin archive + sha256 + jmods path — that mapping is the
# ONLY place x64 naming survives; every output dir/arg elsewhere is Go's, so
# consumers (shnkitd, java_test.go) resolve the default location with no
# override. Archives are pinned to release 21.0.11+10 and sha256-verified
# against constants below (true pinning — not the registry's own companion
# file at fetch time).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
DIST="${KIT_ASSETS_DIST:-$REPO/dist/kitassets}"
MODULES="$REPO/tools/kitassets/modules.txt"
# shellcheck source=tools/kitassets/pins.env
. "$REPO/tools/kitassets/pins.env"
RELEASE="$TEMURIN_RELEASE"
BASE_URL="https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.11%2B10"

TARGET="${1:-}"
if [ "$TARGET" = host ]; then
  case "$(uname -sm)" in
    "Darwin arm64")  TARGET=darwin-arm64 ;;
    "Darwin x86_64") TARGET=darwin-amd64 ;;
    "Linux x86_64")  TARGET=linux-amd64 ;;
    *) echo "no jlink target for host: $(uname -sm)" >&2; exit 1 ;;
  esac
fi
# Go-named target -> Temurin archive/sha256/jmods-path. The pinned SHA table
# is unchanged from Temurin's actual archives — only our arg/dir names moved
# to Go's convention.
case "$TARGET" in
  linux-amd64)  ARCHIVE="OpenJDK21U-jdk_x64_linux_hotspot_21.0.11_10.tar.gz"
               SHA256="4b2220e232a97997b436ca6ab15cbf70171ecff52958a46159dfa5a8c44ca4de"
               JMODS_REL="jdk-$RELEASE/jmods" ;;
  darwin-arm64) ARCHIVE="OpenJDK21U-jdk_aarch64_mac_hotspot_21.0.11_10.tar.gz"
               SHA256="6ebcf221c9b41507b14c098e93c6ead6440b8d9bd154f8ec666c4c73abbdb201"
               JMODS_REL="jdk-$RELEASE/Contents/Home/jmods" ;;
  darwin-amd64) ARCHIVE="OpenJDK21U-jdk_x64_mac_hotspot_21.0.11_10.tar.gz"
               SHA256="34180eb03e6d207c388cce3da668f6cc7cd7508c185c24782fadac2c9c0e66f9"
               JMODS_REL="jdk-$RELEASE/Contents/Home/jmods" ;;
  windows-amd64) ARCHIVE="OpenJDK21U-jdk_x64_windows_hotspot_21.0.11_10.zip"
               SHA256="d3625e7cadf23787ea540229544b6e2ab494b3b54da1801879e583e1dfee0a64"
               JMODS_REL="jdk-$RELEASE/jmods" ;;
  *) echo "usage: $0 {linux-amd64|darwin-arm64|darwin-amd64|windows-amd64}" >&2; exit 2 ;;
esac

java_home() {
  if [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/jlink" ]; then echo "$JAVA_HOME"; return; fi
  if [ "$(uname)" = Darwin ]; then /usr/libexec/java_home -v 21; return; fi
  echo "JAVA_HOME with a JDK 21 (jlink) required" >&2; exit 1
}
JLINK="$(java_home)/bin/jlink"
"$JLINK" --version 2>/dev/null | grep -q '^21' \
  || { echo "[jlink] FAIL: jlink 21 required, got: $("$JLINK" --version 2>&1 | head -1)" >&2; exit 1; }
[ -f "$MODULES" ] || { echo "modules.txt missing — run tools/kitassets/modules.sh first" >&2; exit 1; }

# .extracted marker (not dir presence) gates the cache — an interrupted
# extraction must re-run, not silently serve a partial jmods tree.
CACHE="$DIST/.jmods/$TARGET"
if [ ! -f "$CACHE/.extracted" ]; then
  rm -rf "$CACHE"; mkdir -p "$CACHE"
  echo "[jlink] fetching $ARCHIVE"
  curl -fLSso "$CACHE/$ARCHIVE" "$BASE_URL/$ARCHIVE"
  echo "$SHA256  $CACHE/$ARCHIVE" | shasum -a 256 -c - >/dev/null \
    || { echo "[jlink] FAIL: sha256 mismatch for $ARCHIVE" >&2; exit 1; }
  case "$ARCHIVE" in
    *.tar.gz) tar xzf "$CACHE/$ARCHIVE" -C "$CACHE" ;;
    *.zip)    unzip -qq "$CACHE/$ARCHIVE" -d "$CACHE" ;;
  esac
  rm -f "$CACHE/$ARCHIVE"
  touch "$CACHE/.extracted"
fi

MODLIST="$(grep -v '^#' "$MODULES" | paste -sd, -)"
OUT="$DIST/jre-$TARGET"
rm -rf "$OUT"
echo "[jlink] linking $OUT"
"$JLINK" --module-path "$CACHE/$JMODS_REL" --add-modules "$MODLIST" \
  --strip-debug --no-man-pages --no-header-files --compress zip-6 --output "$OUT"
echo "[jlink] OK: $OUT ($(du -sh "$OUT" | cut -f1))"
