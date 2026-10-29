#!/usr/bin/env bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL=false
VERBOSE=false
LIMIT=0
LOCAL_DEST_PATH="${LOCAL_DEST_PATH:-"$SCRIPT_DIR/../app/data/forecast"}"

usage() {
    echo "Usage: $0 [--local] [--verbose] [--limit <n>]"
    echo "  --limit <n>  only process the first n forecast hours (quick checks)"
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --local)
            LOCAL=true
            ;;
        --verbose)
            VERBOSE=true
            ;;
        --limit)
            shift
            if [ "$#" -eq 0 ] || ! [[ "$1" =~ ^[0-9]+$ ]] || [ "$1" -lt 1 ]; then
                echo "Error: --limit requires a positive integer" >&2
                usage >&2
                exit 1
            fi
            LIMIT="$1"
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
    shift
done

copy_files_locally() {
    local source_path="${SOURCE_PATH:-"$SCRIPT_DIR/files"}"
    local dest_path="$LOCAL_DEST_PATH"

    if [ ! -d "$source_path" ]; then
        echo "Error: source path not found at $source_path" >&2
        exit 1
    fi

    if [ ! -d "$dest_path" ]; then
        echo "Error: local destination path not found at $dest_path" >&2
        exit 1
    fi

    if ! command -v rsync >/dev/null; then
        echo "Error: rsync is required for local copy" >&2
        exit 1
    fi

    shopt -s nullglob
    local contour_files=("$source_path"/*.bin "$source_path"/*.png)
    shopt -u nullglob
    if [ -f "$source_path/tides.json" ]; then
        contour_files+=("$source_path/tides.json")
    fi
    if [ -f "$source_path/spot_forecasts.json.gz" ]; then
        contour_files+=("$source_path/spot_forecasts.json.gz")
    fi
    if [ -f "$source_path/spot_tides.json.gz" ]; then
        contour_files+=("$source_path/spot_tides.json.gz")
    fi

    if [ ${#contour_files[@]} -gt 0 ]; then
        # Same sync as grib_copy_to_server.sh: --delete through publish.rules
        # drops products this run didn't make.
        echo "Syncing ${#contour_files[@]} contour files from $source_path to $dest_path"
        rsync -rt --delay-updates --delete-delay \
            --filter="merge $SCRIPT_DIR/scripts/publish.rules" \
            "$source_path/" "$dest_path/"
    else
        echo "No contour files to copy from $source_path"
    fi

    if [ -f "$source_path/metadata.json" ]; then
        echo "Copying metadata.json"
        rsync -t "$source_path/metadata.json" "$dest_path/"
    elif [ ${#contour_files[@]} -eq 0 ]; then
        echo "No metadata.json to copy from $source_path"
    fi
}

mkdir -p "$SCRIPT_DIR/logs"
if [ "$LIMIT" -gt 0 ]; then
    export GRIB_LIMIT="$LIMIT"
    echo "Limiting run to the first $LIMIT forecast hours"
fi
if [ "$VERBOSE" = true ]; then
    # The parser draws a progress bar on /dev/tty; the full output still
    # goes only to the log so control characters never end up in it.
    export GRIB_PROGRESS=1
    echo "Progress will display here; full log: $SCRIPT_DIR/logs/grib-run.log"
fi
exec >>"$SCRIPT_DIR/logs/grib-run.log" 2>&1
echo "==== $(date -Is) START $$ ===="

# set -e matters for ordering: if generation fails, the copy step never
# runs, so the server keeps serving the previous complete run.
set -euo pipefail

echo "Deleting local grib files"
"$SCRIPT_DIR/scripts/delete_gribs.sh"

echo "Starting grib parse runner"
"$SCRIPT_DIR/scripts/grib_parse_runner.sh"

if [ "$LOCAL" = true ]; then
    echo "Copying files locally to app/data/forecast"
    copy_files_locally
else
    echo "Copying files to server"
    "$SCRIPT_DIR/scripts/grib_copy_to_server.sh"
fi

echo "All scripts executed successfully"
