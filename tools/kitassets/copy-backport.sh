#!/usr/bin/env bash
# Prepare the shared verified WAR outside assets, then stage only runtime inputs.
set -euo pipefail
source_dir="$(cd "$(dirname "$0")/backport" && pwd)"
[ "$#" = 3 ] || { echo 'usage: copy-backport.sh CACHE_ROOT HAPI_RUNTIME_DIR STRIPPER_SCRIPT' >&2; exit 2; }
cache_root="$1"
destination="$2"
stripper="$3"
PYTHONDONTWRITEBYTECODE=1 python3 - "$cache_root" "$destination" <<'PY'
from pathlib import Path
import sys
cache, assets = Path(sys.argv[1]).resolve(), Path(sys.argv[2]).resolve().parent
if cache == assets or assets in cache.parents:
    raise SystemExit('build cache must be outside recursively shipped runtime assets')
PY
artifact="$(bash "$source_dir/prepare.sh" "$cache_root")"
PYTHONDONTWRITEBYTECODE=1 python3 "$source_dir/runtime.py" stage "$artifact" "$destination" "$stripper"
