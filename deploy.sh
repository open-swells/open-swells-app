#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-open-swells-app}"
ENV_FILE="${ENV_FILE:-.env}"
DEPLOY_USER="${DEPLOY_USER:-root}"
DEPLOY_DIR="${DEPLOY_DIR:-/root/open-swells-app}"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing $ENV_FILE. Add SERVER_IP to it before deploying." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

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
  --exclude 'static/' \
  --exclude '*.db' \
  --exclude '*.db-wal' \
  --exclude '*.db-shm' \
  --exclude "$APP_NAME" \
  --exclude 'gosurf' \
  ./ "$REMOTE:$DEPLOY_DIR/"

echo "Building and restarting $APP_NAME on $REMOTE..."
ssh "$REMOTE" "DEPLOY_DIR='$DEPLOY_DIR' APP_NAME='$APP_NAME' bash -s" <<'REMOTE_SCRIPT'
set -euo pipefail

cd "$DEPLOY_DIR"

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
"$GO_BIN" build -buildvcs=false -o "$APP_NAME" .

systemctl restart "$APP_NAME"
systemctl --no-pager --full status "$APP_NAME"

PORT="$(awk -F= '/^[[:space:]]*PORT[[:space:]]*=/{gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); gsub(/^"|"$/, "", $2); print $2; exit}' .env)"
PORT="${PORT:-8081}"

if command -v curl >/dev/null 2>&1; then
  echo "Checking http://127.0.0.1:$PORT/healthz ..."
  if ! curl -fsS "http://127.0.0.1:$PORT/healthz"; then
    echo
    echo "Service is running, but /healthz did not return 200. Check logs with:"
    echo "  journalctl -u $APP_NAME -f"
  fi
fi
REMOTE_SCRIPT

echo "Deploy complete: ssh $REMOTE 'journalctl -u $APP_NAME -f'"
