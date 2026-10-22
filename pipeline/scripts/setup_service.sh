#!/bin/bash
# Install/refresh the systemd service + timer. Run from the deployed
# project directory (expected at /home/evan/grib-parse-collect).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

sudo cp "$PROJECT_ROOT/systemd/grib_parse.service" /etc/systemd/system/
sudo cp "$PROJECT_ROOT/systemd/grib_parse.timer" /etc/systemd/system/
sudo chmod 644 /etc/systemd/system/grib_parse.service /etc/systemd/system/grib_parse.timer

sudo systemctl daemon-reload
sudo systemctl enable --now grib_parse.timer

echo "Timer status:"
sudo systemctl list-timers grib_parse.timer --no-pager
