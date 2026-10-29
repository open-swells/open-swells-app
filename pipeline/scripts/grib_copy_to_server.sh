#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

ENV_FILE="${ENV_FILE:-"$PROJECT_ROOT/.env"}"
if [ ! -f "$ENV_FILE" ]; then
    echo "Error: env file not found at $ENV_FILE" >&2
    exit 1
fi

set -a
source "$ENV_FILE"
set +a

SOURCE_PATH="${SOURCE_PATH:-${PROJECT_ROOT}/files}"
DEST_PATH="${DEST_PATH:-}"
SSH_KEY_PATH="${SSH_KEY_PATH:-}"

for var in SOURCE_PATH DEST_PATH SSH_KEY_PATH; do
    value="${!var:-}"
    if [ -z "$value" ]; then
        echo "Error: $var must be set in the environment." >&2
        exit 1
    fi
done

if [ ! -d "$SOURCE_PATH" ]; then
    echo "Error: source path not found at $SOURCE_PATH" >&2
    exit 1
fi

if [ ! -f "$SSH_KEY_PATH" ]; then
    echo "Error: SSH key not found at $SSH_KEY_PATH" >&2
    exit 1
fi

if ! command -v rsync >/dev/null; then
    echo "Error: rsync is required (files must land atomically on the server)" >&2
    exit 1
fi

shopt -s nullglob
contour_files=("$SOURCE_PATH"/*.bin "$SOURCE_PATH"/*.png)
shopt -u nullglob
if [ -f "$SOURCE_PATH/tides.json" ]; then
    contour_files+=("$SOURCE_PATH/tides.json")
fi
if [ -f "$SOURCE_PATH/spot_forecasts.json.gz" ]; then
    contour_files+=("$SOURCE_PATH/spot_forecasts.json.gz")
fi
if [ -f "$SOURCE_PATH/spot_tides.json.gz" ]; then
    contour_files+=("$SOURCE_PATH/spot_tides.json.gz")
fi

if [ ${#contour_files[@]} -gt 0 ]; then
    # Sync the directory through publish.rules: --delete removes products
    # this run didn't make (a failed hour's frame from the previous run).
    # The file-count guard above keeps an empty run from emptying the
    # server. --delay-updates stages everything in a temp dir on the server
    # and renames at the end, so readers never see a truncated file
    # mid-transfer; --delete-delay holds deletions until then too.
    echo "Syncing ${#contour_files[@]} contour files from $SOURCE_PATH to $DEST_PATH"
    rsync -rt --delay-updates --delete-delay \
        --filter="merge $SCRIPT_DIR/publish.rules" \
        -e "ssh -i $SSH_KEY_PATH" "$SOURCE_PATH/" "$DEST_PATH"
else
    echo "No contour files to copy from $SOURCE_PATH"
fi

# metadata.json is copied last: it announces the run to the frontend, so it
# must never arrive before the contours it describes.
if [ -f "$SOURCE_PATH/metadata.json" ]; then
    echo "Copying metadata.json"
    rsync -t -e "ssh -i $SSH_KEY_PATH" "$SOURCE_PATH/metadata.json" "$DEST_PATH"
elif [ ${#contour_files[@]} -eq 0 ]; then
    echo "No metadata.json to copy from $SOURCE_PATH"
fi
