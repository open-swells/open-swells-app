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
find "$FILES_DIR" -type f -name '*.geojson' -delete
find "$FILES_DIR" -type f -name '*.geojson.gz' -delete
echo "All .geojson files have been deleted."
find "$FILES_DIR" -type f -name 'heatmap_*.png' -delete
find "$FILES_DIR" -type f -name 'nwps_*.png' -delete
echo "All heatmap .png files have been deleted."
find "$FILES_DIR" -type f -name '*.csv' -delete
echo "All .csv files have been deleted."