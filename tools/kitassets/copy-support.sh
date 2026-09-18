#!/usr/bin/env bash
# Refresh the local terminology package even when a prior asset build cached it.
set -euo pipefail
source_dir="$(cd "$(dirname "$0")/support" && pwd)"
source_package="$source_dir/shn.fhir.validation-support-1.2.0.tgz"
destination="${1:?usage: copy-support.sh destination}"
[ -s "$source_package" ] || { echo "missing vendored validation support: $source_package" >&2; exit 1; }
mkdir -p "$(dirname "$destination")"
cp "$source_package" "$destination.tmp"
mv "$destination.tmp" "$destination"
