#!/usr/bin/env bash
# Deploy one or both parts of the monorepo.
#
#   ./deploy.sh app       web app      -> Debian host (SERVER_IP in app/.env)
#   ./deploy.sh pipeline  data pipeline -> rochester-remote-evan
#   ./deploy.sh all       both, pipeline first
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  echo "Usage: $0 app|pipeline|all" >&2
}

case "${1:-}" in
  app)
    exec "$ROOT/app/ops/deploy.sh"
    ;;
  pipeline)
    exec "$ROOT/pipeline/scripts/deploy.sh"
    ;;
  all)
    "$ROOT/pipeline/scripts/deploy.sh"
    exec "$ROOT/app/ops/deploy.sh"
    ;;
  *)
    usage
    exit 1
    ;;
esac
