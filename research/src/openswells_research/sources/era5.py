"""ERA5 reanalysis from Google's public ARCO-ERA5 zarr (no account needed).

The store holds hourly 0.25 deg fields from 1940 on, atmosphere and ocean
waves together (ERA5's 0.5 deg wave grid is interpolated onto the 0.25 deg
atmospheric one). Every chunk is a single global time step, so whole-globe
subsets are cheap per step and long point time series are expensive: pull
grids here, and use NDBC for point histories.
"""

import numpy as np
import xarray as xr

from ..variables import era5_names

ARCO_URL = "gs://gcp-public-data-arco-era5/ar/full_37-1h-0p25deg-chunk-1.zarr-v3"
STORAGE_OPTIONS = {"token": "anon"}


def open_arco(chunks=None) -> xr.Dataset:
    """Lazily open the full store (metadata only; nothing is downloaded)."""
    return xr.open_zarr(ARCO_URL, chunks=chunks, storage_options=STORAGE_OPTIONS)


def valid_range(ds: xr.Dataset) -> tuple[str, str]:
    """(first, last) dates with final ERA5 data; later dates up to
    valid_time_stop_era5t are preliminary ERA5T, and after that NaN."""
    return ds.attrs["valid_time_start"], ds.attrs["valid_time_stop"]


def select(
    ds: xr.Dataset,
    names,
    start: str,
    end: str,
    step_hours: int = 6,
    resolution: float = 0.25,
) -> xr.Dataset:
    """Subset to canonical variables, a time range and a coarser grid.

    resolution must be a multiple of 0.25. Coarsening is a block mean over
    whole cells, which keeps the 0.25 deg grid's pole-to-pole extent as long
    as resolution divides 180 (0.5, 1.0, 1.5 and 2.0 all do). Block means
    treat NaN land cells as missing, so a coarse cell is wet if any of its
    fine cells is. Directions are averaged as unit vectors, not degrees.
    """
    mapping = era5_names(names)
    sub = ds[list(mapping)].rename(mapping)
    sub = sub.sel(time=slice(start, end))
    sub = sub.isel(time=(sub.time.dt.hour % step_hours) == 0)
    factor = round(resolution / 0.25)
    if not np.isclose(factor * 0.25, resolution) or factor < 1:
        raise ValueError(f"resolution {resolution} is not a multiple of 0.25")
    if factor > 1:
        sub = coarsen(sub, factor)
    # The source's chunking and v2 Blosc codec would otherwise be reused on
    # write; let the destination pick its own.
    for da in sub.variables.values():
        da.encoding = {}
    return sub


def coarsen(ds: xr.Dataset, factor: int) -> xr.Dataset:
    """Block-mean a 721x1440 lat/lon grid by an integer factor.

    The 721st latitude row (the south pole) is dropped first so the grid
    divides evenly; coarse latitudes are the block centers.
    """
    from ..variables import VARIABLES

    ds = ds.isel(latitude=slice(0, ds.sizes["latitude"] - 1))
    out = {}
    for name, da in ds.data_vars.items():
        var = VARIABLES.get(name)
        if var is not None and var.circular:
            rad = np.deg2rad(da)
            s = np.sin(rad).coarsen(latitude=factor, longitude=factor).mean()
            c = np.cos(rad).coarsen(latitude=factor, longitude=factor).mean()
            out[name] = (np.rad2deg(np.arctan2(s, c)) % 360).assign_attrs(da.attrs)
        else:
            out[name] = da.coarsen(latitude=factor, longitude=factor).mean()
    return xr.Dataset(out, attrs=ds.attrs)
