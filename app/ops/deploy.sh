#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

APP_NAME="${APP_NAME:-open-swells-app}"
APP_USER="${APP_USER:-openswells}"
ENV_FILE="${ENV_FILE:-.env}"
DEPLOY_USER="${DEPLOY_USER:-root}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/open-swells-app}"
SERVICE_ENV_FILE="${SERVICE_ENV_FILE:-/etc/open-swells/open-swells-app.env}"

# CI passes SERVER_IP in the environment instead of shipping an .env file.
if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
elif [[ -z "${SERVER_IP:-${DEPLOY_IP:-}}" ]]; then
  echo "Missing $ENV_FILE. Add SERVER_IP to it before deploying." >&2
  exit 1
fi

SERVER_IP="${SERVER_IP:-${DEPLOY_IP:-}}"
if [[ -z "$SERVER_IP" ]]; then
  echo "Missing SERVER_IP in $ENV_FILE." >&2
  echo "Add a line like: SERVER_IP=203.0.113.10" >&2
  exit 1
fi

REMOTE="${DEPLOY_USER}@${SERVER_IP}"

echo "Running local tests..."
go test ./...

echo "Creating $DEPLOY_DIR on $REMOTE..."
ssh "$REMOTE" "mkdir -p '$DEPLOY_DIR'"

echo "Syncing app files to $REMOTE:$DEPLOY_DIR..."
rsync -az --delete \
  --exclude '.git/' \
  --exclude '.agents/' \
  --exclude '.codex/' \
  --exclude '.env' \
  --exclude '/static/' \
  --exclude 'data/forecast/' \
  --exclude '*.db' \
  --exclude '*.db-wal' \
  --exclude '*.db-shm' \
  --exclude "$APP_NAME" \
  --exclude 'gosurf' \
  ./ "$REMOTE:$DEPLOY_DIR/"

echo "Building and restarting $APP_NAME on $REMOTE..."
ssh "$REMOTE" "DEPLOY_DIR='$DEPLOY_DIR' APP_NAME='$APP_NAME' APP_USER='$APP_USER' SERVICE_ENV_FILE='$SERVICE_ENV_FILE' bash -s" <<'REMOTE_SCRIPT'
set -euo pipefail

cd "$DEPLOY_DIR"

if ! id "$APP_USER" >/dev/null 2>&1; then
  echo "Service account $APP_USER does not exist. Complete the one-time server setup first." >&2
  exit 1
fi

install -d -o "$APP_USER" -g "$APP_USER" -m 0750 /var/lib/open-swells /var/lib/open-swells/forecast

GO_BIN="${GO_BIN:-}"
if [[ -z "$GO_BIN" ]]; then
  GO_BIN="$(command -v go 2>/dev/null || true)"
fi

if [[ -z "$GO_BIN" ]]; then
  for candidate in /usr/local/go/bin/go /usr/bin/go /snap/bin/go; do
    if [[ -x "$candidate" ]]; then
      GO_BIN="$candidate"
      break
    fi
  done
fi

if [[ -z "$GO_BIN" || ! -x "$GO_BIN" ]]; then
  echo "Go was not found in PATH or a standard install location." >&2
  echo "Set GO_BIN in the remote environment if Go is installed elsewhere." >&2
  exit 1
fi

echo "Building with $GO_BIN..."
"$GO_BIN" build -buildvcs=false -o "$APP_NAME" ./server
chown root:"$APP_USER" "$APP_NAME"
chmod 0750 "$APP_NAME"

show_diagnostics() {
  systemctl --no-pager --full status "$APP_NAME" >&2 || true
  journalctl -u "$APP_NAME" -n 80 --no-pager >&2 || true
}

# Type=simple reports started before the application is listening.
# Require HTTP readiness separately from systemd's process state.
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required on the server to verify deployment readiness." >&2
  exit 1
fi

if ! systemctl restart "$APP_NAME"; then
  show_diagnostics
  exit 1
fi

PORT="$(awk -F= '/^[[:space:]]*PORT[[:space:]]*=/{gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); gsub(/^"|"$/, "", $2); print $2; exit}' "$SERVICE_ENV_FILE" 2>/dev/null || true)"
PORT="${PORT:-8081}"

HEALTH_URL="http://127.0.0.1:$PORT/healthz"
HEALTH_BODY="$(mktemp)"
HEALTH_ERROR="$(mktemp)"
trap 'rm -f "$HEALTH_BODY" "$HEALTH_ERROR"' EXIT

echo "Waiting for $HEALTH_URL (up to 60 attempts)..."
sleep 1
READY=false
for ((attempt = 1; attempt <= 60; attempt++)); do
  HEALTH_STATUS="$(curl -sS --connect-timeout 2 --max-time 5 \
    -o "$HEALTH_BODY" -w '%{http_code}' "$HEALTH_URL" 2>"$HEALTH_ERROR")" || HEALTH_STATUS=000
  if [[ "$HEALTH_STATUS" == 200 ]]; then
    READY=true
    break
  fi
  if ! systemctl is-active --quiet "$APP_NAME"; then
    echo "Service stopped before becoming ready." >&2
    break
  fi
  if (( attempt < 60 )); then
    sleep 1
  fi
done

if [[ "$READY" != true ]]; then
  echo "Deployment failed: $HEALTH_URL did not return 200 (last status: $HEALTH_STATUS)." >&2
  cat "$HEALTH_ERROR" "$HEALTH_BODY" >&2
  show_diagnostics
  exit 1
fi

echo "Service is ready."
echo "Checking embedded Firebase auth asset ..."
AUTH_ASSET_URL="http://127.0.0.1:$PORT/assets/firebase-auth.js"
AUTH_CONTENT_TYPE="$(curl -fsS --connect-timeout 2 --max-time 10 -o /dev/null -w '%{content_type}' "$AUTH_ASSET_URL")" || {
  echo "Firebase auth asset is unavailable at $AUTH_ASSET_URL." >&2
  show_diagnostics
  exit 1
}
case "$AUTH_CONTENT_TYPE" in
  application/javascript*|text/javascript*) ;;
  *)
    echo "Firebase auth asset has unexpected Content-Type: $AUTH_CONTENT_TYPE" >&2
    show_diagnostics
    exit 1
    ;;
esac

systemctl --no-pager --full status "$APP_NAME"

REMOTE_SCRIPT

echo "Deploy complete: ssh $REMOTE 'journalctl -u $APP_NAME -f'"
