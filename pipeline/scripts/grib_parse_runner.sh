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

PYTHON_SCRIPT="${PYTHON_SCRIPT:-"$PROJECT_ROOT/gfs_to_contours.py"}"
FILES_DIR="${FILES_DIR:-"$PROJECT_ROOT/files"}"
LOG_DIR="${LOG_DIR:-"$PROJECT_ROOT/logs"}"

for var in PYTHON_SCRIPT FILES_DIR LOG_DIR; do
    value="${!var:-}"
    if [ -z "$value" ]; then
        echo "Error: $var must be set in the environment." >&2
        exit 1
    fi
done

# This directory is emptied before every run. Resolve symlinks and `..` first,
# then require the result to remain strictly below the project root. This keeps
# a typo such as FILES_DIR=/ (or an external directory) from becoming a
# destructive cleanup target.
CANONICAL_PROJECT_ROOT="$(realpath -e "$PROJECT_ROOT")"
CANONICAL_FILES_DIR="$(realpath -m "$FILES_DIR")"
case "$CANONICAL_FILES_DIR" in
    "$CANONICAL_PROJECT_ROOT"/*)
        FILES_DIR="$CANONICAL_FILES_DIR"
        ;;
    *)
        echo "Error: FILES_DIR must be a child of $CANONICAL_PROJECT_ROOT; got $CANONICAL_FILES_DIR" >&2
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
rm -f "$FILES_DIR"/*

PYTHON_BIN="${PYTHON_BIN:-$PROJECT_ROOT/.venv/bin/python}"

if [ ! -x "$PYTHON_BIN" ]; then
    echo "Error: Python interpreter not found at $PYTHON_BIN" >&2
    exit 1
fi

export FILES_DIR LOG_DIR

echo "Running Python script with interpreter: $PYTHON_BIN"
"$PYTHON_BIN" "$PYTHON_SCRIPT"
