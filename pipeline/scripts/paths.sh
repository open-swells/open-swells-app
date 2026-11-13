# Shared on-disk layout for the pipeline scripts (sourced, not run). The
# same paths are used on the pipeline host and on a dev machine; see "Data
# and config layout" in the top-level README.
#
#   $OPENSWELLS_CONFIG_DIR/open-swells-pipeline.env   runtime config (ENV_FILE)
#   $OPENSWELLS_DATA_DIR/pipeline/work                FILES_DIR: gribs and products before publish
#   $OPENSWELLS_DATA_DIR/pipeline/logs                LOG_DIR
#   $OPENSWELLS_DATA_DIR/pipeline/tide-cache          TIDE_CACHE_DIR
#   $OPENSWELLS_DATA_DIR/forecast                     FORECAST_DIR: where --local publishes
#
# Every path can still be overridden by the environment or the env file.

OPENSWELLS_CONFIG_DIR="${OPENSWELLS_CONFIG_DIR:-/etc/open-swells}"
ENV_FILE="${ENV_FILE:-"$OPENSWELLS_CONFIG_DIR/open-swells-pipeline.env"}"

# The env file may itself set OPENSWELLS_DATA_DIR or any path below, so it
# is read before the defaults are filled in.
if [ -f "$ENV_FILE" ]; then
    set -a
    # shellcheck disable=SC1090
    source "$ENV_FILE"
    set +a
fi

OPENSWELLS_DATA_DIR="${OPENSWELLS_DATA_DIR:-/var/lib/open-swells}"
PIPELINE_DATA_DIR="$OPENSWELLS_DATA_DIR/pipeline"
FILES_DIR="${FILES_DIR:-"$PIPELINE_DATA_DIR/work"}"
LOG_DIR="${LOG_DIR:-"$PIPELINE_DATA_DIR/logs"}"
TIDE_CACHE_DIR="${TIDE_CACHE_DIR:-"$PIPELINE_DATA_DIR/tide-cache"}"
FORECAST_DIR="${FORECAST_DIR:-"$OPENSWELLS_DATA_DIR/forecast"}"
export OPENSWELLS_CONFIG_DIR OPENSWELLS_DATA_DIR FILES_DIR LOG_DIR TIDE_CACHE_DIR FORECAST_DIR

# require_env_file: publishing to the server needs DEST_PATH and
# SSH_KEY_PATH from the env file; a --local run works without one.
require_env_file() {
    if [ ! -f "$ENV_FILE" ]; then
        echo "Error: env file not found at $ENV_FILE" >&2
        exit 1
    fi
}

# require_data_dir: fail with a pointer to the setup script instead of a
# bare mkdir error when the data directory hasn't been created.
require_data_dir() {
    if ! mkdir -p "$FILES_DIR" "$LOG_DIR" "$TIDE_CACHE_DIR" 2>/dev/null \
        || [ ! -w "$FILES_DIR" ] || [ ! -w "$LOG_DIR" ]; then
        echo "Error: cannot write to $PIPELINE_DATA_DIR." >&2
        echo "Create the data directories once with: sudo ops/setup-dirs.sh (from the repository root)," >&2
        echo "or set OPENSWELLS_DATA_DIR to a directory you own." >&2
        exit 1
    fi
}
