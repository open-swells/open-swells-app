"""Per-spot point forecasts sampled from the full-resolution composite.

The web app's spot pages used to read the map's swell_partitions_XXX.geojson
grids, which keep only every ARROW_STRIDE-th cell (~1.67 deg, ~185 km). Here
each spot reads its nearest wet cell of the 1/6 deg composite instead, for
every forecast hour (hourly to f120), and the whole run is written as one
spot_forecasts.json.gz.

Nearest-cell search uses great-circle distance (a KD-tree over unit
vectors), so longitude spacing shrinking with latitude is handled and the
0..360 grid longitudes need no special casing. Spots with no wet cell
within MAX_DISTANCE_KM are left out.

Payload layout (values are arrays aligned with "hours"; null = missing):

    {
      "forecast_start": "20260810_18Z",
      "hours": [0, 1, ..., 384],
      "fields": ["hs", "h1", "p1", "d1", ...],
      "spots": {
        "<spot id>": {"cell": [lat, lon], "km": 7.9, "hs": [...], ...}
      }
    }

Fields: hs combined wind-wave-and-swell height (m); hN/pN/dN swell
partition N height (m), mean period (s), direction from (deg true);
ws/wd surface wind speed (m/s) and direction from (deg true); hw/pw/dw the
locally generated wind sea's height (m), mean period (s) and direction
from (deg true), which the swell partitions exclude. "cell" is
the sampled grid cell (lon -180..180) and "km" its distance from the spot's
sample point.
"""

import gzip
import hashlib
import json
import logging
import os

import numpy as np
from scipy.spatial import cKDTree

logger = logging.getLogger("GFSWaveContours")

EARTH_RADIUS_KM = 6371.0
MAX_DISTANCE_KM = 100.0
OUTPUT_NAME = "spot_forecasts.json.gz"

FIELDS = (
    "hs",
    "h1", "p1", "d1",
    "h2", "p2", "d2",
    "h3", "p3", "d3",
    "ws", "wd",
    "hw", "pw", "dw",
)
# Decimal places per field in the JSON; 0 means integer degrees.
_DECIMALS = {"hs": 2, "ws": 1, "wd": 0, "hw": 2, "pw": 1, "dw": 0}
for _n in (1, 2, 3):
    _DECIMALS.update({f"h{_n}": 2, f"p{_n}": 1, f"d{_n}": 0})
_DIRECTIONS = {"d1", "d2", "d3", "wd", "dw"}

# (lattice + wet-mask fingerprint) -> (tree, flat indices of wet cells).
# The mask is effectively static across hours, so each worker builds one
# tree per run; a changed mask (sea ice, or a degraded single-grid hour on a
# different lattice) gets its own entry.
_TREE_CACHE: dict[tuple, tuple[cKDTree, np.ndarray]] = {}


def load_spots(path: str) -> tuple[list[str], np.ndarray, np.ndarray]:
    """Read the web app's spots.json as (ids, lats, lons).

    A spot's optional sample_lat/sample_lon override wins over its beach
    coordinates, matching Spot.samplePoint() in the app.
    """
    with open(path) as f:
        spots = json.load(f)
    ids, lats, lons = [], [], []
    for spot in spots:
        if spot.get("sample_lat") is not None and spot.get("sample_lon") is not None:
            lat, lon = spot["sample_lat"], spot["sample_lon"]
        else:
            lat, lon = spot["lat"], spot["lon"]
        ids.append(spot["id"])
        lats.append(float(lat))
        lons.append(float(lon))
    return ids, np.array(lats), np.array(lons)


def _unit_vectors(lat_deg: np.ndarray, lon_deg: np.ndarray) -> np.ndarray:
    lat = np.radians(lat_deg)
    lon = np.radians(lon_deg)
    return np.column_stack(
        (np.cos(lat) * np.cos(lon), np.cos(lat) * np.sin(lon), np.sin(lat))
    )


def _wet_tree(lat2d: np.ndarray, lon2d: np.ndarray, wet: np.ndarray):
    key = (
        wet.shape,
        float(lat2d.flat[0]),
        float(lon2d.flat[0]),
        hashlib.blake2b(np.packbits(wet).tobytes(), digest_size=16).digest(),
    )
    cached = _TREE_CACHE.get(key)
    if cached is None:
        wet_index = np.flatnonzero(wet)
        tree = cKDTree(
            _unit_vectors(lat2d.ravel()[wet_index], lon2d.ravel()[wet_index])
        )
        cached = (tree, wet_index)
        _TREE_CACHE[key] = cached
    return cached


def nearest_wet_cells(
    lat2d: np.ndarray,
    lon2d: np.ndarray,
    wet: np.ndarray,
    spot_lat: np.ndarray,
    spot_lon: np.ndarray,
    max_km: float = MAX_DISTANCE_KM,
) -> tuple[np.ndarray, np.ndarray]:
    """Flat grid index of each spot's nearest wet cell, and its distance.

    Index is -1 (distance NaN) where no wet cell lies within max_km.
    """
    tree, wet_index = _wet_tree(lat2d, lon2d, wet)
    max_chord = 2.0 * np.sin(max_km / (2.0 * EARTH_RADIUS_KM))
    chord, position = tree.query(
        _unit_vectors(spot_lat, spot_lon), distance_upper_bound=max_chord
    )
    found = np.isfinite(chord)
    cells = np.full(spot_lat.shape, -1, dtype=np.int64)
    cells[found] = wet_index[position[found]]
    km = np.full(spot_lat.shape, np.nan, dtype=np.float32)
    km[found] = 2.0 * EARTH_RADIUS_KM * np.arcsin(np.minimum(chord[found] / 2.0, 1.0))
    return cells, km


def sample_spots(
    data: dict,
    wind_data: dict | None,
    spot_lat: np.ndarray,
    spot_lon: np.ndarray,
) -> dict:
    """Sample one forecast hour's composite at every spot's nearest wet cell.

    data is a composite_swell() result, wind_data a composite_wind() result
    on the same lattice. Returns {"values": (n_spots, len(FIELDS)) float32
    with NaN for missing, "cells": flat indices (-1 = none), "km",
    "cell_lat", "cell_lon"}.
    """
    lat2d, lon2d = data["lat"], data["lon"]
    height = data["height"]
    wet = ~data["height_mask"] & np.isfinite(height)
    cells, km = nearest_wet_cells(lat2d, lon2d, wet, spot_lat, spot_lon)
    found = cells >= 0
    take = np.where(found, cells, 0)

    columns = {"hs": height}
    for partition in data["swell_partitions"]:
        n = partition["sequence"]
        columns[f"h{n}"] = partition["height"]
        columns[f"p{n}"] = partition["period"]
        columns[f"d{n}"] = partition["direction"]
    wind_sea = data.get("wind_sea")
    if wind_sea is not None:
        columns["hw"] = wind_sea["height"]
        columns["pw"] = wind_sea["period"]
        columns["dw"] = wind_sea["direction"]
    if wind_data is not None and wind_data["speed"].shape == height.shape:
        columns["ws"] = wind_data["speed"]
        columns["wd"] = wind_data["direction"]
    elif wind_data is not None:
        logger.warning("Wind lattice differs from wave lattice; spot wind omitted")

    values = np.full((spot_lat.size, len(FIELDS)), np.nan, dtype=np.float32)
    for j, field in enumerate(FIELDS):
        grid = columns.get(field)
        if grid is not None:
            values[:, j] = np.where(found, grid.ravel()[take], np.nan)

    lon_flat = lon2d.ravel()[take]
    return {
        "values": values,
        "cells": cells,
        "km": km,
        "cell_lat": np.where(found, lat2d.ravel()[take], np.nan).astype(np.float32),
        "cell_lon": np.where(
            found, (lon_flat + 180.0) % 360.0 - 180.0, np.nan
        ).astype(np.float32),
    }


def _column(values: np.ndarray, field: str) -> list:
    decimals = _DECIMALS[field]
    rounded = np.round(values.astype(np.float64), decimals)
    if field in _DIRECTIONS:
        rounded = np.mod(rounded, 360.0)
    if decimals == 0:
        return [None if v != v else int(v) for v in rounded.tolist()]
    return [None if v != v else v for v in rounded.tolist()]


def write_spot_forecasts(
    files_dir: str,
    spot_ids: list[str],
    hours: list[int],
    samples_by_position: dict[int, dict],
    forecast_start: str,
) -> dict:
    """Assemble per-hour samples into spot_forecasts.json.gz.

    samples_by_position maps an index into hours to that hour's
    sample_spots() result; hours without one (failed) become nulls rather
    than borrowing another run's data. Returns metadata for metadata.json.
    """
    n_spots, n_hours = len(spot_ids), len(hours)
    cube = np.full((n_spots, n_hours, len(FIELDS)), np.nan, dtype=np.float32)
    cell_lat = np.full(n_spots, np.nan, dtype=np.float32)
    cell_lon = np.full(n_spots, np.nan, dtype=np.float32)
    km = np.full(n_spots, np.nan, dtype=np.float32)
    # Walk hours in forecast order so the reported cell is the earliest one.
    for position in sorted(samples_by_position):
        sample = samples_by_position[position]
        cube[:, position, :] = sample["values"]
        unset = np.isnan(km) & (sample["cells"] >= 0)
        cell_lat[unset] = sample["cell_lat"][unset]
        cell_lon[unset] = sample["cell_lon"][unset]
        km[unset] = sample["km"][unset]

    spots = {}
    for i, spot_id in enumerate(spot_ids):
        if np.isnan(km[i]):
            continue
        entry = {
            "cell": [round(float(cell_lat[i]), 3), round(float(cell_lon[i]), 3)],
            "km": round(float(km[i]), 1),
        }
        for j, field in enumerate(FIELDS):
            entry[field] = _column(cube[i, :, j], field)
        spots[spot_id] = entry

    payload = {
        "forecast_start": forecast_start,
        "hours": [int(h) for h in hours],
        "fields": list(FIELDS),
        "spots": spots,
    }
    path = os.path.join(files_dir, OUTPUT_NAME)
    with gzip.open(path, "wt", encoding="utf-8", compresslevel=6) as f:
        json.dump(payload, f, separators=(",", ":"))
    logger.info(
        "Spot forecasts saved to %s (%d of %d spots, %d hours, %d with data)",
        path, len(spots), n_spots, n_hours, len(samples_by_position),
    )
    return {
        "file": OUTPUT_NAME,
        "spots": len(spots),
        "spots_skipped": n_spots - len(spots),
        "max_distance_km": MAX_DISTANCE_KM,
    }
