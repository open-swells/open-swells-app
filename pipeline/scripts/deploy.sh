#!/usr/bin/env bash
# Deploy the forecast pipeline to the forecast host over SSH.
#
# Syncs this directory to PIPELINE_DIR, refreshes the virtualenv from
# uv.lock, and smoke-tests the result. Only code lives in PIPELINE_DIR: the
# host's config (/etc/open-swells) and data (/var/lib/open-swells), its
# .venv and its scheduling (cron or systemd timer) are left untouched.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

PIPELINE_HOST="${PIPELINE_HOST:-rochester-remote-evan}"
PIPELINE_DIR="${PIPELINE_DIR:-/home/evan/grib-parse-collect}"
# Set FORCE=1 to deploy while a forecast run is in progress.
FORCE="${FORCE:-0}"
# Seconds to wait for an in-progress run to finish before giving up (CI sets
# this so a push doesn't fail just because it landed mid-run).
WAIT_FOR_RUN="${WAIT_FOR_RUN:-0}"
# The host's shared layout (see scripts/paths.sh).
REMOTE_CONFIG_DIR="${REMOTE_CONFIG_DIR:-/etc/open-swells}"
REMOTE_DATA_DIR="${REMOTE_DATA_DIR:-/var/lib/open-swells}"

if ! command -v uv >/dev/null 2>&1; then
  echo "uv is required locally to run the pipeline tests." >&2
  exit 1
fi

echo "Running local tests..."
uv run --frozen python -m unittest discover -s tests

echo "Checking for an in-progress run on $PIPELINE_HOST..."
# A run spawns worker processes that import modules fresh, so swapping code
# underneath it could mix versions within one forecast.
run_in_progress() {
  ssh "$PIPELINE_HOST" "pgrep -f -- '$PIPELINE_DIR/(run\\.sh|gfs_to_contours\\.py)'" >/dev/null
}
if [[ "$FORCE" != 1 ]] && (( WAIT_FOR_RUN > 0 )) && run_in_progress; then
  echo "A forecast run is in progress; waiting up to ${WAIT_FOR_RUN}s for it to finish..."
  deadline=$((SECONDS + WAIT_FOR_RUN))
  while (( SECONDS < deadline )) && run_in_progress; do
    sleep 60
  done
fi
if run_in_progress; then
  if [[ "$FORCE" != 1 ]]; then
    echo "A forecast run is in progress on $PIPELINE_HOST. Retry when it finishes, or set FORCE=1." >&2
    exit 1
  fi
  echo "A forecast run is in progress; deploying anyway (FORCE=1)."
fi

echo "Checking the config and data directories on $PIPELINE_HOST..."
# Before syncing: code that expects the shared layout must not replace
# code that still runs from the old one.
if ! ssh "$PIPELINE_HOST" "test -r '$REMOTE_CONFIG_DIR/open-swells-pipeline.env' && test -w '$REMOTE_DATA_DIR/pipeline'"; then
  cat >&2 <<EOF
$PIPELINE_HOST is missing $REMOTE_CONFIG_DIR/open-swells-pipeline.env or a
writable $REMOTE_DATA_DIR/pipeline. Move it to the shared layout once, from
the repository root:

  scp ops/setup-dirs.sh ops/migrate-legacy-layout.sh $PIPELINE_HOST:/tmp/
  ssh -t $PIPELINE_HOST 'sudo bash /tmp/migrate-legacy-layout.sh --pipeline $PIPELINE_DIR'

Nothing was deployed.
EOF
  exit 1
fi

echo "Creating $PIPELINE_DIR on $PIPELINE_HOST..."
ssh "$PIPELINE_HOST" "mkdir -p '$PIPELINE_DIR'"

# Excluded paths are also protected from --delete, so anything matched by
# .gitignore (including leftovers such as .env.migrated) survives.
echo "Syncing pipeline files to $PIPELINE_HOST:$PIPELINE_DIR..."
rsync -az --delete \
  --exclude '.git/' \
  --exclude '.venv/' \
  --exclude '.claude/' \
  --filter=':- .gitignore' \
  ./ "$PIPELINE_HOST:$PIPELINE_DIR/"

echo "Installing dependencies and verifying on $PIPELINE_HOST..."
ssh "$PIPELINE_HOST" "PIPELINE_DIR='$PIPELINE_DIR' REMOTE_DATA_DIR='$REMOTE_DATA_DIR' bash -s" <<'REMOTE_SCRIPT'
set -euo pipefail

cd "$PIPELINE_DIR"

UV_BIN="${UV_BIN:-$(command -v uv 2>/dev/null || true)}"
if [[ -z "$UV_BIN" && -x "$HOME/.local/bin/uv" ]]; then
  UV_BIN="$HOME/.local/bin/uv"
fi
if [[ -z "$UV_BIN" || ! -x "$UV_BIN" ]]; then
  echo "uv was not found in PATH or ~/.local/bin." >&2
  echo "Set UV_BIN in the remote environment if uv is installed elsewhere." >&2
  exit 1
fi

# --extra fes installs PyFES for the FES2022 tide fallback; a plain
# `uv sync` would uninstall it. It builds from source (needs Boost >= 1.79,
# e.g. libboost1.81-dev on Debian 12, and libeigen3-dev); uv caches the
# build, so later deploys don't recompile.
echo "Syncing virtualenv with $UV_BIN..."
"$UV_BIN" sync --frozen --extra fes

echo "Checking scripts and imports..."
for script in run.sh scripts/*.sh; do
  bash -n "$script"
done
.venv/bin/python -c 'import gfs_to_contours'
if ! .venv/bin/python -c 'import pyfes' 2>/dev/null; then
  echo "Warning: PyFES is not importable; spots far from NOAA stations get no tides." >&2
fi
if [[ ! -f "$REMOTE_DATA_DIR/reference/fes2022/ocean_tide_extrapolated.yaml" ]]; then
  echo "Warning: FES2022 data not found in $REMOTE_DATA_DIR/reference/fes2022 (see scripts/fes_download.sh)." >&2
fi

if crontab -l 2>/dev/null | grep -v '^[[:space:]]*#' | grep -qF "$PIPELINE_DIR/run.sh"; then
  echo "Scheduled by cron: $(crontab -l | grep -v '^[[:space:]]*#' | grep -F "$PIPELINE_DIR/run.sh")"
elif systemctl is-enabled --quiet grib_parse.timer 2>/dev/null; then
  echo "Scheduled by grib_parse.timer."
else
  echo "Warning: no cron entry or enabled timer runs $PIPELINE_DIR/run.sh." >&2
fi
REMOTE_SCRIPT

echo "Deploy complete: ssh $PIPELINE_HOST 'tail -f $REMOTE_DATA_DIR/pipeline/logs/grib-run.log'"
