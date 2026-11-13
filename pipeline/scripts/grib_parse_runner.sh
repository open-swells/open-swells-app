#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

source "$SCRIPT_DIR/paths.sh"

PYTHON_SCRIPT="${PYTHON_SCRIPT:-"$PROJECT_ROOT/gfs_to_contours.py"}"

for var in PYTHON_SCRIPT FILES_DIR LOG_DIR; do
    value="${!var:-}"
    if [ -z "$value" ]; then
        echo "Error: $var must be set in the environment." >&2
        exit 1
    fi
done

# This directory is emptied before every run. Resolve symlinks and `..` first,
# then require the result to remain strictly below the pipeline's data
# directory ($OPENSWELLS_DATA_DIR/pipeline). That keeps a typo such as
# FILES_DIR=/ , the data root (which holds the app's database) or the
# published forecast directory from becoming a destructive cleanup target.
CANONICAL_PIPELINE_DATA_DIR="$(realpath -m "$PIPELINE_DATA_DIR")"
CANONICAL_FILES_DIR="$(realpath -m "$FILES_DIR")"
case "$CANONICAL_FILES_DIR" in
    "$CANONICAL_PIPELINE_DATA_DIR"/*)
        FILES_DIR="$CANONICAL_FILES_DIR"
        ;;
    *)
        echo "Error: FILES_DIR must be a child of $CANONICAL_PIPELINE_DATA_DIR; got $CANONICAL_FILES_DIR" >&2
        exit 1
        ;;
esac

if [ ! -f "$PYTHON_SCRIPT" ]; then
    alt_path="$PROJECT_ROOT/$PYTHON_SCRIPT"
    if [ -f "$alt_path" ]; then
        PYTHON_SCRIPT="$alt_path"
    else
        echo "Error: Python script not found at $PYTHON_SCRIPT" >&2
        exit 1
    fi
fi

mkdir -p "$FILES_DIR" "$LOG_DIR"

echo "Cleaning files directory: $FILES_DIR"
# Run-stamped bundles (<product>_<YYYYMMDD>_<HH>Z.bin) stay: each publish
# keeps the previous run's for maps opened before it, and the pipeline
# prunes older ones (bundle.prune_bundles).
find "$FILES_DIR" -maxdepth 1 -type f ! -name '*_[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]_[0-9][0-9]Z.bin' -delete

PYTHON_BIN="${PYTHON_BIN:-$PROJECT_ROOT/.venv/bin/python}"

if [ ! -x "$PYTHON_BIN" ]; then
    echo "Error: Python interpreter not found at $PYTHON_BIN" >&2
    exit 1
fi

export FILES_DIR LOG_DIR TIDE_CACHE_DIR OPENSWELLS_DATA_DIR

echo "Running Python script with interpreter: $PYTHON_BIN"
"$PYTHON_BIN" "$PYTHON_SCRIPT"
