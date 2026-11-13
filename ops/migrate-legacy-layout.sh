#!/usr/bin/env bash
# One-time move from the old per-checkout locations (app/main.db,
# app/data/forecast, pipeline/.env, pipeline/files, ~/.cache/openswells-*)
# into the shared layout under /etc/open-swells and /var/lib/open-swells.
#
#   Dev machine (both halves, from the repo root):
#     sudo ops/migrate-legacy-layout.sh --app app --pipeline pipeline
#   Pipeline host:
#     scp ops/*.sh pipeline-host:/tmp/
#     ssh -t pipeline-host 'sudo bash /tmp/migrate-legacy-layout.sh --pipeline /home/evan/grib-parse-collect'
#
# Runs ops/setup-dirs.sh first. Nothing is overwritten: an entry that
# already exists at the destination is skipped and reported. Old env files
# are renamed to .env.migrated (app/.env is copied, not moved, because it
# still holds SERVER_IP for deploys). Don't run it during a forecast run.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR=""
PIPELINE_DIR=""
OWNER="${SUDO_USER:-$(id -un)}"
OWNER_HOME=""

usage() {
  echo "Usage: sudo $0 [--app DIR] [--pipeline DIR] [--owner USER] [--home DIR]" >&2
  echo "  --home DIR  where to find .cache/openswells-* (default: OWNER's home)" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --app) APP_DIR="$(realpath -e "$2")"; shift ;;
    --pipeline) PIPELINE_DIR="$(realpath -e "$2")"; shift ;;
    --owner) OWNER="$2"; shift ;;
    --home) OWNER_HOME="$2"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 1 ;;
  esac
  shift
done
if [[ -z "$APP_DIR" && -z "$PIPELINE_DIR" ]]; then
  usage
  exit 1
fi

CONFIG_DIR="${OPENSWELLS_CONFIG_DIR:-/etc/open-swells}"
DATA_DIR="${OPENSWELLS_DATA_DIR:-/var/lib/open-swells}"
GROUP="$(id -gn "$OWNER")"
OWNER_HOME="${OWNER_HOME:-$(getent passwd "$OWNER" | cut -d: -f6)}"

if [[ -n "$PIPELINE_DIR" ]] && pgrep -f -- "$PIPELINE_DIR/(run\\.sh|gfs_to_contours\\.py)" >/dev/null; then
  echo "A forecast run is in progress in $PIPELINE_DIR; retry when it finishes." >&2
  exit 1
fi

if [[ -f "$SCRIPT_DIR/setup-dirs.sh" ]]; then
  bash "$SCRIPT_DIR/setup-dirs.sh" "$OWNER"
else
  echo "setup-dirs.sh not found beside this script." >&2
  exit 1
fi

skipped=0

# move_file SRC DEST: move a file or directory unless DEST exists. Across
# filesystems (a home directory to /var) it copies to DEST.migrating first
# and only then removes SRC, so running out of space never leaves a
# truncated DEST that a rerun would mistake for a finished move.
move_file() {
  local src="$1" dest="$2"
  [[ -e "$src" ]] || return 0
  if [[ -e "$dest" ]]; then
    echo "skip: $dest exists (left $src in place)"
    skipped=$((skipped + 1))
    return 0
  fi
  if [[ "$(stat -c %d "$src")" == "$(stat -c %d "$(dirname "$dest")")" ]]; then
    mv "$src" "$dest"
  else
    rm -rf "$dest.migrating"
    if ! cp -a "$src" "$dest.migrating"; then
      rm -rf "$dest.migrating"
      echo "Error: copying $src to $dest failed (out of space?); $src is unchanged. Free space and rerun." >&2
      exit 1
    fi
    mv "$dest.migrating" "$dest"
    rm -rf "$src"
  fi
  chown -R "$OWNER:$GROUP" "$dest" 2>/dev/null || true
  echo "moved: $src -> $dest"
}

# move_contents SRC DEST: move every entry of directory SRC into DEST.
move_contents() {
  local src="$1" dest="$2" entry
  [[ -d "$src" ]] || return 0
  shopt -s nullglob dotglob
  for entry in "$src"/*; do
    move_file "$entry" "$dest/$(basename "$entry")"
  done
  shopt -u nullglob dotglob
  rmdir "$src" 2>/dev/null && echo "removed empty $src" || true
}

# migrate_env SRC DEST: copy an env file into the config dir without the
# path settings the new layout derives on its own.
migrate_env() {
  local src="$1" dest="$2"
  [[ -f "$src" ]] || return 0
  if [[ -e "$dest" ]]; then
    echo "skip: $dest exists (compare it with $src by hand)"
    skipped=$((skipped + 1))
    return 0
  fi
  grep -vE '^[[:space:]]*(export[[:space:]]+)?(SERVER_IP|DEPLOY_IP|DEPLOY_USER|DB_PATH|FORECAST_DIR|SOURCE_PATH|FILES_DIR|LOG_DIR|PYTHON_SCRIPT|PYTHON_INTERPRETER|TIDE_CACHE_DIR|LOCAL_DEST_PATH)[[:space:]]*=' "$src" \
    | sed -E 's#^([[:space:]]*(export[[:space:]]+)?FES_CONFIG[[:space:]]*=).*openswells-fes2022.*#\# \1 (now the default under '"$DATA_DIR"'/reference/fes2022)#' \
    > "$dest"
  chown "root:$GROUP" "$dest" 2>/dev/null || true
  chmod 0640 "$dest"
  echo "wrote: $dest (from $src; path settings dropped)"
  grep -E '^[[:space:]]*(export[[:space:]]+)?(DB_PATH|FORECAST_DIR|SOURCE_PATH|FILES_DIR|LOG_DIR|TIDE_CACHE_DIR)[[:space:]]*=' "$src" \
    | sed 's/^/  dropped: /' || true
}

if [[ -n "$APP_DIR" ]]; then
  echo "== app ($APP_DIR)"
  migrate_env "$APP_DIR/.env" "$CONFIG_DIR/open-swells-app.env"
  for suffix in "" -wal -shm; do
    move_file "$APP_DIR/main.db$suffix" "$DATA_DIR/main.db$suffix"
  done
  move_contents "$APP_DIR/data/forecast" "$DATA_DIR/forecast"
  move_file "$APP_DIR/data/spot_forecast_history.json.gz" "$DATA_DIR/spot_forecast_history.json.gz"
fi

if [[ -n "$PIPELINE_DIR" ]]; then
  echo "== pipeline ($PIPELINE_DIR)"
  if [[ -f "$PIPELINE_DIR/.env" && ! -e "$CONFIG_DIR/open-swells-pipeline.env" ]]; then
    migrate_env "$PIPELINE_DIR/.env" "$CONFIG_DIR/open-swells-pipeline.env"
    mv "$PIPELINE_DIR/.env" "$PIPELINE_DIR/.env.migrated"
    echo "renamed: $PIPELINE_DIR/.env -> .env.migrated (delete it once runs succeed)"
  else
    migrate_env "$PIPELINE_DIR/.env" "$CONFIG_DIR/open-swells-pipeline.env"
  fi
  move_contents "$PIPELINE_DIR/files" "$DATA_DIR/pipeline/work"
  move_contents "$PIPELINE_DIR/logs" "$DATA_DIR/pipeline/logs"
fi

if [[ -n "$OWNER_HOME" ]]; then
  echo "== caches ($OWNER_HOME/.cache)"
  move_contents "$OWNER_HOME/.cache/openswells-tides" "$DATA_DIR/pipeline/tide-cache"
  old_fes="$OWNER_HOME/.cache/openswells-fes2022"
  new_fes="$DATA_DIR/reference/fes2022"
  move_contents "$old_fes" "$new_fes"
  # PyFES's config names its grids by absolute path.
  if [[ -f "$new_fes/ocean_tide_extrapolated.yaml" ]] && grep -qF "$old_fes" "$new_fes/ocean_tide_extrapolated.yaml"; then
    sed -i "s#$old_fes#$new_fes#g" "$new_fes/ocean_tide_extrapolated.yaml"
    echo "updated grid paths in $new_fes/ocean_tide_extrapolated.yaml"
  fi
  move_contents "$OWNER_HOME/.cache/openswells-gshhg" "$DATA_DIR/reference/gshhg"
fi

echo
if (( skipped > 0 )); then
  echo "Done, with $skipped entries skipped (see above)."
else
  echo "Done."
fi
