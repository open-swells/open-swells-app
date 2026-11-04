"""Read and write the pipeline's published forecast products.

A model of ours shows up in the app by producing the same run-scoped
bundles the pipeline publishes (see "Bundle format" in pipeline/README.md):
swell_<run>.bin with partitions h1/p1/d1..h3/p3/d3 and wind_<run>.bin with
s/d/u/v, both int16 grids listed under "bundles" in metadata.json.

The format itself lives in pipeline/bundle.py, which is loaded from the
repository by path rather than copied, so there is one implementation.
It needs only numpy. The var tables below repeat gfs_to_contours.py's
SWELL_VARS/WIND_VARS, which can't be imported without pygrib; a test holds
them equal.

Readers and writers use canonical names (variables.py) and an xarray
Dataset over (hour, latitude, longitude).
"""

import importlib.util
import json
from functools import cache
from pathlib import Path

import numpy as np
import xarray as xr

from .spots import REPO_ROOT

PIPELINE_DIR = REPO_ROOT / "pipeline"

SWELL_VARS = [
    {"name": f"{field}{index}", "scale": scale}
    for index in (1, 2, 3)
    for field, scale in (("h", 0.01), ("p", 0.1), ("d", 1))
]
WIND_VARS = [
    {"name": "s", "scale": 0.1},
    {"name": "d", "scale": 1},
    {"name": "u", "scale": 0.1},
    {"name": "v", "scale": 0.1},
]
# Bundle var -> canonical name. Swell vars already match.
WIND_NAMES = {"s": "ws", "d": "wd", "u": "u10", "v": "v10"}
_DIRECTIONS = {"d1", "d2", "d3", "d"}

# The pipeline strides its 1/6 deg composite by ARROW_STRIDE (10), so the
# map draws arrows ~1.67 deg apart. Every bundle cell becomes an arrow, so
# finer grids should be thinned to about this spacing before writing.
MAP_SPACING_DEG = 10 / 6


@cache
def bundle_module():
    """pipeline/bundle.py, imported from its file."""
    spec = importlib.util.spec_from_file_location("openswells_pipeline_bundle",
                                                  PIPELINE_DIR / "bundle.py")
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {PIPELINE_DIR / 'bundle.py'}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def wind_speed_direction(u, v):
    """(speed m/s, direction the wind comes from in degrees true)."""
    speed = np.hypot(u, v)
    direction = np.rad2deg(np.arctan2(-u, -v)) % 360
    return speed, direction


def read_bundle(path) -> xr.Dataset:
    """One grid bundle as a Dataset with canonical names, NaN for NODATA."""
    header, hours = bundle_module().read_bundle(str(path))
    if header["kind"] != "grid":
        raise ValueError(f"{path} is a {header['kind']} bundle, not a grid")
    names = [v["name"] for v in header["vars"]]
    # Wind's "d" would otherwise read as a direction with no partition.
    rename = WIND_NAMES if set(names) == set(WIND_NAMES) else {}
    data = {
        rename.get(n, n): (("hour", "latitude", "longitude"),
                           np.stack([hours[h][n] for h in header["hours"]]))
        for n in names
    }
    return xr.Dataset(
        data,
        coords={"hour": header["hours"], "latitude": header["lat"], "longitude": header["lon"]},
        attrs={"run": header.get("run")},
    )


def read_run(forecast_dir) -> xr.Dataset:
    """A published run's swell and wind bundles, merged.

    forecast_dir holds metadata.json and the bundles it lists, e.g. the
    app's data/forecast after `pipeline/run.sh --local`.
    """
    forecast_dir = Path(forecast_dir)
    metadata = json.loads((forecast_dir / "metadata.json").read_text())
    bundles = metadata.get("bundles")
    if not bundles:
        raise ValueError(f"{forecast_dir}/metadata.json lists no bundles "
                         "(a run from before the bundle format?)")
    parts = [read_bundle(forecast_dir / bundles[p]) for p in ("swell", "wind") if p in bundles]
    ds = xr.merge(parts, join="outer", compat="override")
    ds.attrs["run"] = metadata["forecast_start"]
    return ds


def _packed(ds: xr.Dataset, hour, variables, names, valid) -> dict[str, bytes]:
    pack = bundle_module().pack_grid
    out = {}
    for var, name in zip(variables, names):
        values = ds[name].sel(hour=hour).values
        if var["name"] in _DIRECTIONS:
            values = np.round(values) % 360
        out[var["name"]] = pack(values, var["scale"], valid)
    return out


def write_run(ds: xr.Dataset, out_dir, run: str) -> dict[str, str]:
    """Write swell and wind bundles from a model forecast.

    ds: canonical vars h1..d3 and u10/v10 (ws/wd are derived when absent)
    over (hour, latitude, longitude), already on the grid to publish.
    run: the forecast start, "YYYYMMDD_HHZ", which names the files.

    As in the pipeline, a partition is stored only where its height, period
    and direction are all valid, and wind only where all four vars are, so
    NODATA in h<k> means "no such swell here". ERA5-trained output never
    leaves a partition empty (see docs/data.md), so mask weak partitions
    to NaN before writing or the map draws arrows for them.

    Returns {"swell": file, "wind": file} for metadata.json's "bundles".
    """
    bundle = bundle_module()
    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    if "ws" not in ds or "wd" not in ds:
        ws, wd = wind_speed_direction(ds["u10"], ds["v10"])
        ds = ds.assign(ws=ws, wd=wd)
    hours = [int(h) for h in ds["hour"].values]
    lat, lon = ds["latitude"].values, ds["longitude"].values

    swell_chunks, wind_chunks = {}, {}
    wind_names = [WIND_NAMES[v["name"]] for v in WIND_VARS]
    for hour in hours:
        swell_chunks[hour] = {}
        for k in (1, 2, 3):
            fields = [f"h{k}", f"p{k}", f"d{k}"]
            valid = np.logical_and.reduce(
                [np.isfinite(ds[f].sel(hour=hour).values) for f in fields])
            spec = [v for v in SWELL_VARS if v["name"] in fields]
            swell_chunks[hour].update(_packed(ds, hour, spec, fields, valid))
        valid = np.logical_and.reduce(
            [np.isfinite(ds[n].sel(hour=hour).values) for n in wind_names])
        wind_chunks[hour] = _packed(ds, hour, WIND_VARS, wind_names, valid)

    names = {}
    for product, variables, chunks in (("swell", SWELL_VARS, swell_chunks),
                                       ("wind", WIND_VARS, wind_chunks)):
        name = bundle.bundle_name(product, run)
        bundle.write_bundle(str(out_dir / name), kind="grid", hours=hours,
                            variables=variables, chunks=chunks, lat=lat, lon=lon,
                            run=run, axis_decimals=2)
        names[product] = name
    return names
