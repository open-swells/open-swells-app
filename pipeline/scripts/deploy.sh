#!/usr/bin/env bash
# Deploy the forecast pipeline to the forecast host over SSH.
#
# Syncs this directory to PIPELINE_DIR, refreshes the virtualenv from
# uv.lock, and smoke-tests the result. The host's .env, .venv, files/,
# logs/ and scheduling (cron or systemd timer) are left untouched.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

PIPELINE_HOST="${PIPELINE_HOST:-rochester-remote-evan}"
PIPELINE_DIR="${PIPELINE_DIR:-/home/evan/grib-parse-collect}"
# Set FORCE=1 to deploy while a forecast run is in progress.
FORCE="${FORCE:-0}"

if ! command -v uv >/dev/null 2>&1; then
  echo "uv is required locally to run the pipeline tests." >&2
  exit 1
fi

echo "Running local tests..."
uv run --frozen python -m unittest discover -s tests

echo "Checking for an in-progress run on $PIPELINE_HOST..."
# A run spawns worker processes that import modules fresh, so swapping code
# underneath it could mix versions within one forecast.
if ssh "$PIPELINE_HOST" "pgrep -f -- '$PIPELINE_DIR/(run\\.sh|gfs_to_contours\\.py)'" >/dev/null; then
  if [[ "$FORCE" != 1 ]]; then
    echo "A forecast run is in progress on $PIPELINE_HOST. Retry when it finishes, or set FORCE=1." >&2
    exit 1
  fi
  echo "A forecast run is in progress; deploying anyway (FORCE=1)."
fi

echo "Creating $PIPELINE_DIR on $PIPELINE_HOST..."
ssh "$PIPELINE_HOST" "mkdir -p '$PIPELINE_DIR'"

# Excluded paths are also protected from --delete, so the host's state and
# anything matched by .gitignore (files/, logs/, .env, ...) survive.
echo "Syncing pipeline files to $PIPELINE_HOST:$PIPELINE_DIR..."
rsync -az --delete \
  --exclude '.git/' \
  --exclude '.venv/' \
  --exclude '.claude/' \
  --filter=':- .gitignore' \
  ./ "$PIPELINE_HOST:$PIPELINE_DIR/"

echo "Installing dependencies and verifying on $PIPELINE_HOST..."
ssh "$PIPELINE_HOST" "PIPELINE_DIR='$PIPELINE_DIR' bash -s" <<'REMOTE_SCRIPT'
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

echo "Syncing virtualenv with $UV_BIN..."
"$UV_BIN" sync --frozen

echo "Checking scripts and imports..."
for script in run.sh scripts/*.sh; do
  bash -n "$script"
done
.venv/bin/python -c 'import gfs_to_contours'

if [[ ! -f .env ]]; then
  echo "Warning: $PIPELINE_DIR/.env is missing; runs cannot publish until it exists." >&2
fi

if crontab -l 2>/dev/null | grep -v '^[[:space:]]*#' | grep -qF "$PIPELINE_DIR/run.sh"; then
  echo "Scheduled by cron: $(crontab -l | grep -v '^[[:space:]]*#' | grep -F "$PIPELINE_DIR/run.sh")"
elif systemctl is-enabled --quiet grib_parse.timer 2>/dev/null; then
  echo "Scheduled by grib_parse.timer."
else
  echo "Warning: no cron entry or enabled timer runs $PIPELINE_DIR/run.sh." >&2
fi
REMOTE_SCRIPT

echo "Deploy complete: ssh $PIPELINE_HOST 'tail -f $PIPELINE_DIR/logs/grib-run.log'"
