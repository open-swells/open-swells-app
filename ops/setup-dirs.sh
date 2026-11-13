#!/usr/bin/env bash
# Create the OpenSwells config and data directories (see "Data and config
# layout" in the top-level README). The same layout is used on every
# machine: a dev laptop, the pipeline host and the app server each fill in
# the parts they use.
#
#   sudo ops/setup-dirs.sh [OWNER]
#   ssh pipeline-host 'sudo bash -s evan' < ops/setup-dirs.sh
#
# OWNER (default: the user who ran sudo) owns the data directories and can
# read the config directory. On the app server the owner is the service
# account (openswells); see app/README.md. Safe to rerun: existing
# directories keep their contents.
set -euo pipefail

OWNER="${1:-${SUDO_USER:-$(id -un)}}"
GROUP="$(id -gn "$OWNER")"
CONFIG_DIR="${OPENSWELLS_CONFIG_DIR:-/etc/open-swells}"
DATA_DIR="${OPENSWELLS_DATA_DIR:-/var/lib/open-swells}"

if [[ "$(id -u)" != 0 && ( ! -w "$(dirname "$CONFIG_DIR")" || ! -w "$(dirname "$DATA_DIR")" ) ]]; then
  echo "Run as root (sudo $0 $OWNER), or set OPENSWELLS_CONFIG_DIR/OPENSWELLS_DATA_DIR to writable paths." >&2
  exit 1
fi

# Config holds secrets (deploy key paths, Firebase credentials): readable by
# the owner's group only.
install -d -o root -g "$GROUP" -m 0750 "$CONFIG_DIR" 2>/dev/null \
  || install -d -m 0750 "$CONFIG_DIR"

for dir in \
  "$DATA_DIR" \
  "$DATA_DIR/forecast" \
  "$DATA_DIR/pipeline" \
  "$DATA_DIR/pipeline/work" \
  "$DATA_DIR/pipeline/logs" \
  "$DATA_DIR/pipeline/tide-cache" \
  "$DATA_DIR/reference" \
  "$DATA_DIR/reference/fes2022" \
  "$DATA_DIR/reference/gshhg"; do
  if [[ -d "$dir" ]]; then
    chown "$OWNER:$GROUP" "$dir" 2>/dev/null || true
  else
    install -d -o "$OWNER" -g "$GROUP" -m 0750 "$dir" 2>/dev/null \
      || install -d -m 0750 "$dir"
  fi
done

echo "Config: $CONFIG_DIR"
echo "Data:   $DATA_DIR (owner $OWNER)"
