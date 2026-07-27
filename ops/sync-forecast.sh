#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

ENV_FILE="${ENV_FILE:-.env}"
DEPLOY_USER="${DEPLOY_USER:-root}"
REMOTE_FORECAST_DIR="${REMOTE_FORECAST_DIR:-/var/lib/open-swells/forecast}"
LOCAL_FORECAST_DIR="${LOCAL_FORECAST_DIR:-data/forecast}"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing $ENV_FILE. Add SERVER_IP to it before syncing forecasts." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

if [[ -z "${SERVER_IP:-}" ]]; then
  echo "Missing SERVER_IP in $ENV_FILE." >&2
  exit 1
fi

REMOTE="${DEPLOY_USER}@${SERVER_IP}"

mkdir -p "$LOCAL_FORECAST_DIR"

echo "Syncing forecast data from $REMOTE:$REMOTE_FORECAST_DIR/ to $LOCAL_FORECAST_DIR/..."
rsync -az --delete \
  "$REMOTE:$REMOTE_FORECAST_DIR/" \
  "$LOCAL_FORECAST_DIR/"

echo "Forecast data sync complete."
