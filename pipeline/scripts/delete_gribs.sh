#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
FILES_DIR="$PROJECT_ROOT/files"

if [ ! -d "$FILES_DIR" ]; then
    echo "Directory not found: $FILES_DIR"
    exit 1
fi

echo "Deleting files from $FILES_DIR"
find "$FILES_DIR" -type f -name '*.grib2' -delete
echo "All .grib2 files have been deleted."
find "$FILES_DIR" -type f -name '*.grib2.part' -delete
# Bundles stay (each publish keeps the previous run's; the pipeline prunes
# older ones); only partial writes go.
find "$FILES_DIR" -type f -name '*.bin.part' -delete
find "$FILES_DIR" -type f -name 'heatmap_*.webp' -delete
find "$FILES_DIR" -type f -name 'nwps_*.png' -delete
echo "All heatmap image files have been deleted."