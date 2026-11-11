"""Shared helpers for reading GRIB2 messages with pygrib.

pygrib does not cache anything on a message: every ``.values`` access
decodes the JPEG2000 payload again (0.15-0.3 s for a global wave grid) and
every ``latlons()`` call rebuilds the lattice (~0.2 s). A forecast hour
reads 34 fields from two files and decoding them is most of its processing
time, so a field's values and mask come from one decode (decode) and a
lattice is built once per process (lat_lon_grid).
"""

import time

import numpy as np

# Grid definition -> (lon, lat) float32 2-D arrays. A run's files all share
# a few lattices, so each worker process builds each one once.
GRID_CACHE: dict[tuple, tuple[np.ndarray, np.ndarray]] = {}


def retry_wait(attempt: int, attempts: int, base_seconds: float = 3.0) -> None:
    """Pause before retrying a failed download: 3 s, 6 s, 12 s, ...

    NOMADS rate-limits bursts and drops connections for a few seconds at a
    time; retrying at once tends to fail the same way, and three failures
    cost the run that forecast hour. No wait after the last attempt.
    """
    if attempt < attempts:
        time.sleep(base_seconds * 2 ** (attempt - 1))


def _grid_cache_key(msg) -> tuple:
    keys = []
    for attr in (
        "gridType",
        "Ni",
        "Nj",
        "latitudeOfFirstGridPointInDegrees",
        "longitudeOfFirstGridPointInDegrees",
        "latitudeOfLastGridPointInDegrees",
        "longitudeOfLastGridPointInDegrees",
    ):
        try:
            value = msg[attr]
        except (KeyError, AttributeError, TypeError):
            value = getattr(msg, attr, None)
        if isinstance(value, (int, float)):
            keys.append(float(value))
        else:
            keys.append(value)
    return tuple(keys)


def lat_lon_grid(msg) -> tuple[np.ndarray, np.ndarray]:
    """The message's (lon, lat) lattice as float32 2-D arrays, cached."""
    key = _grid_cache_key(msg)
    cached = GRID_CACHE.get(key)
    if cached is not None:
        return cached
    lats, lons = msg.latlons()
    grid = (np.array(lons, dtype=np.float32), np.array(lats, dtype=np.float32))
    GRID_CACHE[key] = grid
    return grid


def decode(msg) -> tuple[np.ndarray, np.ndarray]:
    """Decode a message once: (float32 values with NaN where masked, mask)."""
    values = msg.values
    return (
        np.ma.filled(values, np.nan).astype(np.float32),
        np.ma.getmaskarray(values),
    )
