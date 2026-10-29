"""Composite the GFS-Wave global grids onto one pole-to-pole lattice.

NOAA publishes the wave model on several gridded products, but only two
matter for whole-map coverage: ``global.0p16`` covers just 15S-52.5N at
1/6 deg, and ``global.0p25`` is pole-to-pole at 1/4 deg. (The regional
0p16 grids — wcoast, epacif, atlocn — are cutouts inside the global 0p16
band at the same resolution, and arctic.9km is a curvilinear polar-
stereographic grid whose area 0p25 already covers.)

The composite keeps the finer 0p16 values inside their native band and
fills everything else from 0p25 by nearest-neighbor regridding, on a
single uniform 1/6 deg grid clipped to +/-85 deg: Web Mercator diverges
at the poles, and the wave model is ice-masked there anyway. Nearest
neighbor (rather than bilinear) keeps land masks crisp and never averages
directional fields across the dateline of their period.
"""

import numpy as np

LAT_LIMIT = 85.0
GRID_STEP = 1.0 / 6.0


def _target_axes() -> tuple[np.ndarray, np.ndarray]:
    n_lat = int(round(2 * LAT_LIMIT / GRID_STEP)) + 1
    lat = np.linspace(LAT_LIMIT, -LAT_LIMIT, n_lat)  # north -> south, like GFS
    n_lon = int(round(360.0 / GRID_STEP))
    lon = np.arange(n_lon) * GRID_STEP
    return lat, lon


def _axes(data: dict) -> tuple[np.ndarray, np.ndarray]:
    return (
        data["lat"][:, 0].astype(np.float64),
        data["lon"][0, :].astype(np.float64),
    )


def _nearest(source: np.ndarray, target: np.ndarray) -> np.ndarray:
    return np.abs(target[:, None] - source[None, :]).argmin(axis=1)


def _blank_like(value, key: str | None = None):
    """An all-missing copy of a grid dict: NaN fields and fully set masks.

    Stands in for a missing source grid so a single grid still lands on the
    target lattice (lat/lon and scalars are kept as they are).
    """
    if isinstance(value, dict):
        return {k: _blank_like(v, k) for k, v in value.items()}
    if isinstance(value, list):
        return [_blank_like(v) for v in value]
    if isinstance(value, np.ndarray) and key not in ("lat", "lon"):
        if value.dtype == bool:
            return np.ones_like(value)
        if np.issubdtype(value.dtype, np.floating):
            return np.full_like(value, np.nan)
    return value


def _fill_missing(data_hi: dict | None, data_lo: dict | None) -> tuple[dict, dict]:
    """Both sources, standing in for a missing one.

    Without the coarse grid the fine one keeps its band and the rest of
    the lattice is empty; without the fine grid the coarse one fills
    everything. Either way the result is on the usual target lattice, so
    every hour of a run shares one grid.
    """
    if data_lo is None:
        if data_hi is None:
            raise ValueError("Both source grids are missing")
        return data_hi, _blank_like(data_hi)
    if data_hi is None:
        return data_lo, data_lo
    return data_hi, data_lo


class _Compositor:
    """Regrids arrays from two sources onto the target lattice.

    Cells inside the high-resolution grid's latitude band come from it
    verbatim (its lattice is a subset of the target's, so nearest-neighbor
    lookup is exact there); all other cells come from the low-resolution
    grid. The band is trimmed to rows that hold any valid data: the 0p16
    product pads its edges with fully-masked rows (2 in the north, 15 in
    the south as of 2026) which would otherwise punch transparent seam
    lines into the composite.
    """

    def __init__(self, data_hi: dict, data_lo: dict, hi_mask: np.ndarray):
        target_lat, target_lon = _target_axes()
        hi_lat, hi_lon = _axes(data_hi)
        lo_lat, lo_lon = _axes(data_lo)
        data_rows = hi_lat[~hi_mask.all(axis=1)]
        if data_rows.size:
            in_band = (target_lat >= data_rows.min() - 1e-6) & (
                target_lat <= data_rows.max() + 1e-6
            )
        else:
            in_band = np.zeros(target_lat.shape, dtype=bool)
        # Gather only the rows each source contributes. Expanding both
        # sources to full grids before np.where doubles the gather work
        # and keeps two full-size temporaries alive for every field.
        self._hi_rows = np.flatnonzero(in_band)
        self._lo_rows = np.flatnonzero(~in_band)
        self._hi_index = np.ix_(
            _nearest(hi_lat, target_lat[in_band]), _nearest(hi_lon, target_lon)
        )
        self._lo_index = np.ix_(
            _nearest(lo_lat, target_lat[~in_band]), _nearest(lo_lon, target_lon)
        )
        self._shape = (target_lat.size, target_lon.size)
        self.lon_grid, self.lat_grid = np.meshgrid(
            target_lon.astype(np.float32), target_lat.astype(np.float32)
        )

    def pick(self, hi_array: np.ndarray, lo_array: np.ndarray) -> np.ndarray:
        result = np.empty(self._shape, dtype=np.result_type(hi_array, lo_array))
        result[self._hi_rows] = hi_array[self._hi_index]
        result[self._lo_rows] = lo_array[self._lo_index]
        return result


def composite_swell(data_hi: dict | None, data_lo: dict | None) -> dict:
    """Merge two extract_from_grib2_to_np() results into one global dict.

    Either argument may be None (a failed download); the other is then
    regridded alone so a run degrades to partial coverage instead of
    losing the forecast hour.
    """
    data_hi, data_lo = _fill_missing(data_hi, data_lo)
    if data_hi["valid_date"] != data_lo["valid_date"]:
        raise ValueError("Mismatched valid times between global wave grids")

    compositor = _Compositor(data_hi, data_lo, data_hi["height_mask"])
    partitions = []
    for partition_hi, partition_lo in zip(
        data_hi["swell_partitions"], data_lo["swell_partitions"], strict=True
    ):
        partitions.append(
            {
                "sequence": partition_hi["sequence"],
                "height": compositor.pick(partition_hi["height"], partition_lo["height"]),
                "period": compositor.pick(partition_hi["period"], partition_lo["period"]),
                "direction": compositor.pick(
                    partition_hi["direction"], partition_lo["direction"]
                ),
                "mask": compositor.pick(partition_hi["mask"], partition_lo["mask"]),
            }
        )
    # Wind sea is optional per file; with either side missing it is
    # dropped for the hour rather than half-filled from one grid.
    wind_sea = None
    if data_hi.get("wind_sea") is not None and data_lo.get("wind_sea") is not None:
        wind_sea = {
            key: compositor.pick(data_hi["wind_sea"][key], data_lo["wind_sea"][key])
            for key in ("height", "period", "direction")
        }
    return {
        "lon": compositor.lon_grid,
        "lat": compositor.lat_grid,
        "wind_sea": wind_sea,
        "height": compositor.pick(data_hi["height"], data_lo["height"]),
        "height_mask": compositor.pick(data_hi["height_mask"], data_lo["height_mask"]),
        "period": partitions[0]["period"],
        "direction": partitions[0]["direction"],
        "swell_partitions": partitions,
        "valid_date": data_hi["valid_date"],
    }


def composite_wind(data_hi: dict | None, data_lo: dict | None) -> dict:
    """Merge two extract_wind() results into one global dict."""
    data_hi, data_lo = _fill_missing(data_hi, data_lo)
    if data_hi["valid_date"] != data_lo["valid_date"]:
        raise ValueError("Mismatched valid times between global wind grids")

    compositor = _Compositor(data_hi, data_lo, data_hi["mask"])
    combined = {
        key: compositor.pick(data_hi[key], data_lo[key])
        for key in ("speed", "direction", "u", "v", "mask")
    }
    combined["lon"] = compositor.lon_grid
    combined["lat"] = compositor.lat_grid
    combined["valid_date"] = data_hi["valid_date"]
    return combined
