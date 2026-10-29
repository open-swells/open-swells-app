import gc
import os
import json
import logging
import logging.handlers
import sys
from concurrent.futures import ProcessPoolExecutor, as_completed
from datetime import datetime, timedelta
from functools import partial
import datetime as dt

import numpy as np
import pygrib
import requests
import geojson

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.path import Path

from PIL import Image as PILImage

from geojson import Feature, FeatureCollection
from shapely.geometry import Polygon
from shapely.ops import transform as shapely_transform
from scipy.ndimage import gaussian_filter

from bundle import bundle_name, pack_document, pack_grid, prune_bundles, write_bundle
from composite import composite_swell, composite_wind
from nwps import process_nwps_domains
from spot_forecasts import load_spots, sample_spots, write_spot_forecasts
from tides import DEFAULT_TIDE_CACHE_DIR, SPOT_TIDES_NAME, write_spot_tides, write_tides
from wind import extract_wind, pack_wind

logger = logging.getLogger("GFSWaveContours")
logger.setLevel(logging.INFO)

_GRID_CACHE: dict[tuple, tuple[np.ndarray, np.ndarray]] = {}

# The two NOAA wave grids composited for whole-map coverage: the fine
# grid only spans 15S-52.5N, the coarse one is pole-to-pole (see
# composite.py for why the other regional products are not used).
GLOBAL_GRIDS = ("global.0p16", "global.0p25")

# One bundle per product per run (see bundle.py; published as
# <product>_<run>.bin), replacing the per-hour files. Vars and scales are
# part of the format the web app reads; the scales keep the precision the
# GeoJSON points were rounded to.
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
CONTOURS_VARS = [{"name": "geojson"}]

# Height bands shared with the frontend color scale and legend (meters).
# Levels must be identical for every forecast hour: per-file derived levels
# made the band boundaries shift between frames, so the animation flickered.
# 20 m is a catch-all top for the "8 m+" band.
FIXED_LEVELS = np.array(
    [0.0, 0.5, 1.0, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0, 6.0, 8.0, 20.0]
)

# Continuous color ramp for the heatmap PNGs. Colors match SWELL_BANDS in
# the web app's pages/today.html (change them together); each color is
# anchored at its band's midpoint so the legend stays truthful.
HEATMAP_ANCHORS = np.array(
    [0.25, 0.75, 1.25, 1.75, 2.25, 2.75, 3.5, 4.5, 5.5, 7.0, 10.0]
)
HEATMAP_COLORS = [
    "#a5d5f0", "#64a8e8", "#3178d2", "#15a3a3", "#2fb54e", "#a3c520",
    "#f2ce08", "#f59a0b", "#ea4b28", "#b31212", "#4a0a1e",
]


def setup_logging(log_directory: str) -> None:
    if logger.handlers:
        return
    os.makedirs(log_directory, exist_ok=True)
    log_file = os.path.join(log_directory, "gfs_wave_contours.log")
    file_handler = logging.handlers.RotatingFileHandler(
        log_file, maxBytes=10_485_760, backupCount=5
    )
    console_handler = logging.StreamHandler()
    log_format = logging.Formatter(
        "%(asctime)s - %(name)s - %(levelname)s - %(message)s"
    )
    file_handler.setFormatter(log_format)
    console_handler.setFormatter(log_format)
    logger.addHandler(file_handler)
    logger.addHandler(console_handler)
    logger.propagate = False


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


def _get_lat_lon_grid(msg) -> tuple[np.ndarray, np.ndarray]:
    key = _grid_cache_key(msg)
    cached = _GRID_CACHE.get(key)
    if cached is not None:
        return cached
    lats, lons = msg.latlons()
    grid = (np.array(lons, dtype=np.float32), np.array(lats, dtype=np.float32))
    _GRID_CACHE[key] = grid
    return grid


def _gaussian_filter_nan(array: np.ndarray, sigma: float) -> np.ndarray:
    if not sigma or sigma <= 0:
        return array
    if np.isnan(array).all():
        return array
    nan_mask = np.isnan(array)
    filled = np.where(nan_mask, 0.0, array)
    filtered = gaussian_filter(filled, sigma=sigma, mode="nearest")
    weights = gaussian_filter((~nan_mask).astype(np.float32), sigma=sigma, mode="nearest")
    with np.errstate(invalid="ignore", divide="ignore"):
        filtered = np.divide(
            filtered,
            weights,
            out=np.full_like(filtered, np.nan),
            where=weights > 0,
        )
    # Keep land cells NaN. The weighted filter extrapolates values into
    # masked cells near the coast; leaving those in makes the contour
    # polygons spill onto land in the map.
    filtered[nan_mask] = np.nan
    return filtered


_progress_tty = None
_progress_tty_tried = False


def _print_progress(done: int, total: int, label: str) -> None:
    """Draw a progress bar on the controlling terminal when GRIB_PROGRESS=1.

    Writes to /dev/tty directly: run.sh redirects stdout/stderr into the run
    log, and \\r control characters don't belong in a log file. Under systemd
    (no terminal) this is a no-op.
    """
    global _progress_tty, _progress_tty_tried
    if not os.environ.get("GRIB_PROGRESS") or total <= 0:
        return
    if not _progress_tty_tried:
        _progress_tty_tried = True
        try:
            _progress_tty = open("/dev/tty", "w")
        except OSError:
            _progress_tty = None
    if _progress_tty is None:
        return
    width = 30
    filled = int(width * done / total)
    bar = "#" * filled + "-" * (width - filled)
    end = "\n" if done >= total else ""
    _progress_tty.write(f"\r[{bar}] {done}/{total} {label:<18}{end}")
    _progress_tty.flush()


def _hex_to_rgb(color: str) -> tuple[int, int, int]:
    color = color.lstrip("#")
    return (int(color[0:2], 16), int(color[2:4], 16), int(color[4:6], 16))


def render_heatmap_png(
    data: dict,
    png_path: str,
    *,
    rows_scale: float = 2.0,
    alpha: np.ndarray | None = None,
) -> dict:
    """Render the height field as a continuous-color PNG heatmap.

    MapLibre stretches an image source linearly in Web Mercator space, so
    rows are resampled here from the equirectangular GRIB grid to equal
    Mercator spacing; displaying the PNG at the returned bounds then puts
    every pixel at the correct latitude. Land is transparent.

    alpha, if given, is a per-cell 0..1 opacity multiplier (same shape as
    the height grid) and switches the output from indexed-palette to RGBA —
    the nearshore mosaics use it to feather their offshore edges into the
    global layer underneath instead of cutting off in a hard line.
    """
    height = data["height"].astype(np.float32, copy=False)
    mask = data.get("height_mask")
    grid = np.where(mask, np.nan, height) if mask is not None else height

    lats = data["lat"][:, 0].astype(np.float64)
    lons = data["lon"][0, :].astype(np.float64)
    if lats[0] < lats[-1]:  # rows must run north -> south for the image
        lats = lats[::-1]
        grid = grid[::-1, :]
        if alpha is not None:
            alpha = alpha[::-1, :]

    def merc_y(lat_deg: np.ndarray) -> np.ndarray:
        return np.log(np.tan(np.pi / 4 + np.radians(lat_deg) / 2))

    n_rows = int(grid.shape[0] * rows_scale)
    y_targets = np.linspace(merc_y(lats[0]), merc_y(lats[-1]), n_rows)
    target_lats = np.degrees(2 * np.arctan(np.exp(y_targets)) - np.pi / 2)
    # Nearest source row per target row keeps the land/sea edge crisp.
    src_rows = np.abs(target_lats[:, None] - lats[None, :]).argmin(axis=1)
    warped = grid[src_rows, :]

    colors = np.array([_hex_to_rgb(c) for c in HEATMAP_COLORS], dtype=np.float64)
    values = np.clip(
        np.nan_to_num(warped, nan=HEATMAP_ANCHORS[0]),
        HEATMAP_ANCHORS[0],
        HEATMAP_ANCHORS[-1],
    )

    # Write an indexed-color PNG with a palette built directly from the ramp:
    # index 0 is transparent land, indices 1..255 are evenly spaced ramp
    # steps (~0.04 m apart). Letting Pillow *derive* a palette (quantize) is
    # not safe here — its octree merged rare colors (8 m+ storm cores) into
    # the transparent slot, punching holes in the heatmap. A fixed palette
    # keeps the file ~5x smaller than true RGBA with exact transparency.
    steps = 255
    ramp_values = np.linspace(HEATMAP_ANCHORS[0], HEATMAP_ANCHORS[-1], steps)
    palette = np.zeros((256, 3), dtype=np.uint8)
    for channel in range(3):
        palette[1:, channel] = np.interp(
            ramp_values, HEATMAP_ANCHORS, colors[:, channel]
        ).astype(np.uint8)

    fraction = (values - HEATMAP_ANCHORS[0]) / (HEATMAP_ANCHORS[-1] - HEATMAP_ANCHORS[0])
    indices = (1 + np.round(fraction * (steps - 1))).astype(np.uint8)
    indices[np.isnan(warped)] = 0

    if alpha is not None:
        alpha_warped = np.clip(alpha, 0.0, 1.0)[src_rows, :]
        alpha_bytes = np.round(alpha_warped * 255).astype(np.uint8)
        alpha_bytes[indices == 0] = 0
        rgba = np.dstack([palette[indices], alpha_bytes])
        PILImage.fromarray(rgba).save(png_path, optimize=True)
    else:
        # fromarray yields mode "L"; putpalette converts it to "P" in place.
        # (Passing mode= to fromarray is deprecated and gone in Pillow 13.)
        image = PILImage.fromarray(indices)
        image.putpalette(palette.flatten())
        image.save(png_path, optimize=True, transparency=0)
    logger.info("Heatmap saved to %s (%dx%d)", png_path, indices.shape[1], indices.shape[0])
    return {
        "west": float(lons[0]),
        "east": float(lons[-1]),
        "north": float(lats[0]),
        "south": float(lats[-1]),
    }


WIND_SEA_FIELDS = {
    "height": "Significant height of wind waves",
    "period": "Mean period of wind waves",
    "direction": "Direction of wind waves",
}


def _extract_wind_sea(grbs, valid_date) -> dict | None:
    """The locally generated wind sea, separate from the three swells.

    Where the wind is blowing, much of the sea state is wind sea rather
    than swell (often all of it in enclosed seas), so spot forecasts that
    only read the swell partitions miss it. Optional: None when the file
    lacks the fields, so an hour degrades to swell-only instead of failing.
    """
    wind_sea = {}
    for key, name in WIND_SEA_FIELDS.items():
        try:
            message = grbs.select(name=name)[0]
        except (KeyError, IndexError, ValueError):
            logger.warning("Wind sea field %r missing; spot wind sea omitted", name)
            return None
        if message.validDate != valid_date:
            raise ValueError("Mismatched valid times between GRIB fields")
        wind_sea[key] = np.ma.filled(message.values, np.nan).astype(np.float32)
    return wind_sea


def extract_from_grib2_to_np(filepath: str) -> dict:
    grbs = pygrib.open(filepath)
    try:
        height_param_name = "Significant height of total swell"
        period_param_name = "Mean period of total swell"
        direction_param_name = "Direction of swell waves"
        combined_param_name = "Significant height of combined wind waves and swell"

        try:
            height_msgs = grbs.select(name=height_param_name)
            period_msgs = grbs.select(name=period_param_name)
            direction_msgs = grbs.select(name=direction_param_name)
            combined_msg = grbs.select(name=combined_param_name)[0]
        except (IndexError, ValueError) as exc:
            raise RuntimeError(f"Missing required fields in {filepath}") from exc

        if not (len(height_msgs) == len(period_msgs) == len(direction_msgs) == 3):
            raise RuntimeError(f"Expected three swell partitions in {filepath}")
        partition_messages = [
            message
            for group in (height_msgs, period_msgs, direction_msgs)
            for message in group
        ]
        if any(message.validDate != combined_msg.validDate for message in partition_messages):
            raise ValueError("Mismatched valid times between GRIB fields")

        height_msg = height_msgs[0]
        lon_grid, lat_grid = _get_lat_lon_grid(height_msg)
        combined_values = np.ma.filled(combined_msg.values, np.nan)
        combined_mask = np.ma.getmaskarray(combined_msg.values)
        partitions = []
        for sequence, (partition_height, partition_period, partition_direction) in enumerate(
            zip(height_msgs, period_msgs, direction_msgs), start=1
        ):
            partitions.append(
                {
                    "sequence": sequence,
                    "height": np.ma.filled(partition_height.values, np.nan).astype(np.float32),
                    "period": np.ma.filled(partition_period.values, np.nan).astype(np.float32),
                    "direction": np.ma.filled(partition_direction.values, np.nan).astype(np.float32),
                    "mask": np.ma.getmaskarray(partition_height.values),
                }
            )

        # Render the complete sea state, not a mixture of swell partition 1
        # and the combined field. Partition 1 can remain valid in a storm eye
        # while reporting only a small background swell (for example 0.4 m
        # beside 9 m combined seas). Using it whenever it is merely unmasked
        # punches pale, apparently transparent holes into storm cores. The
        # combined field is continuous across both wind sea and swell; keep
        # the individual partitions below for the directional-arrow output.
        height_values = combined_values.astype(np.float32)
        mask = combined_mask | ~np.isfinite(combined_values)

        return {
            "lon": lon_grid,
            "lat": lat_grid,
            "height": height_values,
            "height_mask": mask,
            "period": partitions[0]["period"],
            "direction": partitions[0]["direction"],
            "swell_partitions": partitions,
            "wind_sea": _extract_wind_sea(grbs, combined_msg.validDate),
            "valid_date": height_msg.validDate,
        }
    finally:
        grbs.close()


def calculate_contours4(
    data: dict,
    *,
    levels: np.ndarray | None = None,
    smoothing_sigma: float = 1.5,
    simplify_tolerance: float | None = 0.02,
    min_area: float | None = None,
    stride: int = 1,
    extra_properties: dict | None = None,
) -> str:
    """Band the combined height field into GeoJSON polygons (the payload)."""
    lon_grid = data["lon"]
    lat_grid = data["lat"]
    height_values = data["height"].astype(np.float32, copy=False)
    mask = data.get("height_mask")

    grid = np.where(mask, np.nan, height_values) if mask is not None else height_values
    grid = _gaussian_filter_nan(grid, smoothing_sigma)

    if stride and stride > 1:
        grid = grid[::stride, ::stride]
        lon_grid = lon_grid[::stride, ::stride]
        lat_grid = lat_grid[::stride, ::stride]

    if levels is None:
        levels = FIXED_LEVELS

    masked_data = np.ma.masked_invalid(grid)
    fig, ax = plt.subplots(figsize=(4, 2.5), dpi=100)
    try:
        contour = ax.contourf(
            lon_grid,
            lat_grid,
            masked_data,
            levels=levels,
            antialiased=True,
        )
    finally:
        plt.close(fig)

    if min_area is None:
        lon_spacing = np.nanmedian(np.abs(np.diff(lon_grid, axis=1)))
        lat_spacing = np.nanmedian(np.abs(np.diff(lat_grid, axis=0)))
        if np.isfinite(lon_spacing) and np.isfinite(lat_spacing):
            min_area = float((lon_spacing * lat_spacing) / 8.0)
        else:
            min_area = 0.0

    features: list[Feature] = []
    extra_properties = extra_properties or {}
    valid_time = data.get("valid_date")
    base_properties = dict(extra_properties)
    if valid_time:
        base_properties.setdefault("valid_time", valid_time.isoformat())

    def _iter_paths():
        collections = getattr(contour, "collections", None)
        if collections is not None:
            for collection, lower, upper in zip(
                collections, contour.levels[:-1], contour.levels[1:]
            ):
                for path in collection.get_paths():
                    yield lower, upper, path.to_polygons()
            return

        allsegs = getattr(contour, "allsegs", None)
        if allsegs is None:
            raise RuntimeError(
                "Matplotlib contour output does not expose polygon collections or segments."
            )
        allkinds = getattr(contour, "allkinds", None)

        for idx, (lower, upper) in enumerate(
            zip(contour.levels[:-1], contour.levels[1:])
        ):
            segs = allsegs[idx]
            if not segs:
                continue
            if allkinds is not None:
                kind_list = allkinds[idx]
            else:
                kind_list = [None] * len(segs)
            for seg_coords, kind in zip(segs, kind_list):
                if seg_coords is None or len(seg_coords) < 3:
                    continue
                try:
                    path = Path(seg_coords, kind) if kind is not None else Path(seg_coords)
                    polygons = path.to_polygons()
                except Exception:
                    polygons = [seg_coords]
                if not polygons:
                    continue
                yield lower, upper, polygons

    for lower, upper, polygon_coords in _iter_paths():
        exterior = polygon_coords[0]
        if exterior.shape[0] < 3:
            continue
        holes = [hole for hole in polygon_coords[1:] if hole.shape[0] >= 3]
        polygon = Polygon(exterior, holes)
        if not polygon.is_valid:
            polygon = polygon.buffer(0)
        if polygon.is_empty:
            continue
        if min_area and polygon.area < min_area:
            continue
        if simplify_tolerance:
            simplified = polygon.simplify(simplify_tolerance, preserve_topology=True)
            if simplified.is_empty:
                continue
            polygon = simplified
        # ~11m precision; full float precision roughly doubles file size
        polygon = shapely_transform(
            lambda x, y, z=None: (np.round(x, 4), np.round(y, 4)), polygon
        )
        properties = {
            "contour_min": float(lower),
            "contour_max": float(upper),
            "contour_mean": float((lower + upper) / 2.0),
        }
        properties.update(base_properties)
        features.append(
            Feature(geometry=polygon.__geo_interface__, properties=properties)
        )

    if not features:
        logger.warning("No contour polygons generated for %s", base_properties)
    return geojson.dumps(FeatureCollection(features))


def pack_swell_partitions(data: dict, *, stride: int = 10) -> dict[str, bytes]:
    """Pack all three swell partitions on a coarse grid for the swell bundle.

    Vars h1/p1/d1 through h3/p3/d3: height (m), mean period (s) and the
    direction the swell comes from (degrees true). A partition is stored
    only where its height, period and direction are all valid, so one
    NODATA in h<k> means the whole system is absent there. Partition 1 is
    what the map draws as arrows.
    """
    chunks: dict[str, bytes] = {}
    for partition in data["swell_partitions"]:
        index = partition["sequence"]
        height = partition["height"][::stride, ::stride]
        period = partition["period"][::stride, ::stride]
        direction = partition["direction"][::stride, ::stride]
        valid = np.isfinite(height) & np.isfinite(period) & np.isfinite(direction)
        mask = partition.get("mask")
        if mask is not None:
            valid &= ~mask[::stride, ::stride]
        chunks[f"h{index}"] = pack_grid(height, 0.01, valid)
        chunks[f"p{index}"] = pack_grid(period, 0.1, valid)
        chunks[f"d{index}"] = pack_grid(np.round(direction) % 360, 1, valid)
    return chunks


def grid_axes(data: dict, stride: int) -> tuple[np.ndarray, np.ndarray]:
    """1-D (lat, lon) axes of the strided composite lattice.

    Copies, not views: the caller keeps these per hour, and a view would
    keep the hour's full-resolution lat/lon grids alive with it.
    """
    return data["lat"][::stride, 0].copy(), data["lon"][0, ::stride].copy()


def find_latest_gfs_time(session: requests.Session | None = None) -> tuple[str, str]:
    hours = ["18", "12", "06", "00"]
    base_url = "https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod/"

    own_session = False
    if session is None:
        session = requests.Session()
        own_session = True

    try:
        current_date = dt.datetime.now(dt.UTC) + timedelta(days=1)
        for _ in range(3):
            date_str = current_date.strftime("%Y%m%d")
            for hour in hours:
                # NOAA uploads forecast hours progressively; the run directory
                # appears long before it is complete. Probe the last forecast
                # hour of both global grids we consume so we never process a
                # half-uploaded run.
                try:
                    if all(
                        session.head(
                            f"{base_url}gfs.{date_str}/{hour}/wave/gridded/"
                            f"gfswave.t{hour}z.{grid}.f384.grib2",
                            timeout=10,
                        ).status_code
                        == 200
                        for grid in GLOBAL_GRIDS
                    ):
                        return date_str, hour
                except requests.RequestException as exc:
                    logger.debug("HEAD request failed for run %s %sZ: %s", date_str, hour, exc)
                    continue
            current_date -= timedelta(days=1)
    finally:
        if own_session:
            session.close()

    raise RuntimeError("Could not find valid GFS wave data in the last 2 days")


def write_metadata(
    files_dir: str,
    date_str: str,
    hour: str,
    successes: int | None = None,
    failures: int | None = None,
    heatmap_bounds: dict | None = None,
    nwps: dict | None = None,
    spot_forecasts: dict | None = None,
    hours: list[int] | None = None,
    bundles: dict | None = None,
) -> str:
    metadata_path = os.path.join(files_dir, "metadata.json")
    metadata: dict[str, object] = {
        "date": date_str,
        "hour": hour,
        "timestamp": datetime.now(dt.UTC).isoformat(),
        "forecast_start": f"{date_str}_{hour}Z",
    }
    if successes is not None:
        metadata["hours_processed"] = successes
    if failures is not None:
        metadata["hours_failed"] = failures
    if hours is not None:
        # The forecast hours this run published frames for, ascending. The
        # map's timeline is built from this list, so a failed hour is
        # skipped instead of showing a hole (or a previous run's frame).
        metadata["hours"] = hours
    if bundles:
        # This run's bundle files by product ({"swell": "swell_<run>.bin",
        # ...}); the web app reads only what is listed here.
        metadata["bundles"] = bundles
    if heatmap_bounds is not None:
        # The web app pins the heatmap PNGs to these corner coordinates.
        metadata["heatmap_bounds"] = heatmap_bounds
    if nwps:
        if nwps.get("layers"):
            # Nearshore mosaic overlays: per-grid-tier bounds and which
            # forecast hours have a nwps_<grid>_<HHH>.png frame.
            metadata["nwps_layers"] = nwps["layers"]
        if nwps.get("points"):
            # Beach point grids: one bundle per domain ("file") and the
            # 3-hourly hours it holds.
            metadata["nwps_points"] = nwps["points"]
    if spot_forecasts:
        # Per-spot hourly series sampled at full grid resolution.
        metadata["spot_forecasts"] = spot_forecasts
    with open(metadata_path, "w") as f:
        json.dump(metadata, f, indent=2)
    logger.info("Saved metadata to %s", metadata_path)
    return metadata_path


def _process_single_hour(
    forecast_hour,
    date_str: str,
    run_hour: str,
    files_dir: str,
    *,
    stride: int = 1,
    smoothing_sigma: float = 1.5,
    simplify_tolerance: float | None = 0.02,
    arrow_stride: int = 10,
    spots: tuple[np.ndarray, np.ndarray] | None = None,
) -> tuple[str, bool, dict | None, dict | None, dict | None]:
    """Download and render one forecast hour; runs in a worker process.

    Returns (file_index, succeeded, heatmap_bounds, spot_samples, layers),
    where spot_samples is the sample_spots() result for the (lats, lons) in
    spots, or None, and layers holds this hour's packed bundle chunks
    ({"swell": ..., "wind": ..., "contours": ..., "axes": (lat, lon)}) for
    the parent to assemble. Never raises: hours are independent, so one bad
    hour must not take down the pool.
    """
    # Free the previous hour's grids before allocating this hour's. They sit
    # in reference cycles (matplotlib's contour objects), and an hour makes
    # too few Python objects to trigger the collector on its own, so without
    # this a worker's memory climbs by ~85 MB an hour for several hours.
    gc.collect()
    base_url = (
        "https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod/"
        f"gfs.{date_str}/{run_hour}/wave/gridded"
    )
    file_index = f"{int(forecast_hour):03}"

    # One file per global grid. A missing grid degrades the hour to partial
    # coverage rather than losing it; only both missing is a failure.
    grid_paths: dict[str, str] = {}
    with requests.Session() as session:
        for grid in GLOBAL_GRIDS:
            file_name = f"gfswave.t{run_hour}z.{grid}.f{file_index}.grib2"
            file_path = os.path.join(files_dir, file_name)
            if not os.path.exists(file_path):
                url = f"{base_url}/{file_name}"
                if not _download_file(session, url, file_path):
                    logger.error("Giving up on %s file %s", grid, file_index)
                    continue
                logger.info("File %s (%s) downloaded and saved", file_index, grid)
            else:
                logger.info("File %s (%s) exists", file_index, grid)
            grid_paths[grid] = file_path
    if not grid_paths:
        return file_index, False, None, None, None
    if len(grid_paths) < len(GLOBAL_GRIDS):
        logger.warning(
            "File %s: only %s available; coverage will be partial",
            file_index,
            ", ".join(grid_paths),
        )

    try:
        extracted = {
            grid: extract_from_grib2_to_np(path)
            for grid, path in grid_paths.items()
        }
        data = composite_swell(
            extracted.get(GLOBAL_GRIDS[0]), extracted.get(GLOBAL_GRIDS[1])
        )
        contours = calculate_contours4(
            data,
            stride=stride,
            smoothing_sigma=smoothing_sigma,
            simplify_tolerance=simplify_tolerance,
            extra_properties={"forecast_hour": int(forecast_hour)},
        )
        wind_extracted = {
            grid: extract_wind(path) for grid, path in grid_paths.items()
        }
        wind_data = composite_wind(
            wind_extracted.get(GLOBAL_GRIDS[0]),
            wind_extracted.get(GLOBAL_GRIDS[1]),
        )
        layers = {
            "contours": {"geojson": pack_document(contours)},
            "swell": pack_swell_partitions(data, stride=arrow_stride),
            "wind": pack_wind(wind_data, stride=arrow_stride),
            "axes": grid_axes(data, arrow_stride),
        }
        heatmap_path = os.path.join(files_dir, f"heatmap_{file_index}.png")
        bounds = render_heatmap_png(data, heatmap_path)
    except Exception as exc:
        logger.error("Error processing file %s: %s", file_index, exc, exc_info=True)
        return file_index, False, None, None, None

    # Spot sampling is an add-on: a failure here loses this hour's spot
    # values, not the map layers already written.
    spot_samples = None
    if spots is not None:
        try:
            spot_samples = sample_spots(data, wind_data, *spots)
        except Exception as exc:
            logger.error("Spot sampling failed for %s: %s", file_index, exc, exc_info=True)
    return file_index, True, bounds, spot_samples, layers


def _worker_init() -> None:
    """Attach log handlers in pool workers.

    With the default fork start method the workers inherit the parent's
    handlers and this is a no-op; under spawn/forkserver it recreates them.
    Multiple processes appending to the same log file is safe enough here
    (O_APPEND, small writes); lines may interleave but are not lost.
    """
    log_dir = os.environ.get("LOG_DIR")
    if log_dir:
        setup_logging(log_dir)


def default_workers() -> int:
    """Worker count from PARALLEL_HOURS, else a memory-conscious default.

    Each worker holds a few hundred MB of grids, so cap the default at 4
    even on wide machines; set PARALLEL_HOURS explicitly to go higher.
    """
    env_value = os.environ.get("PARALLEL_HOURS")
    if env_value:
        return max(1, int(env_value))
    return max(1, min(4, (os.cpu_count() or 2) - 1))


def process_forecast_hours(
    hour_sequence,
    date_str: str,
    run_hour: str,
    files_dir: str,
    *,
    stride: int = 1,
    smoothing_sigma: float = 1.5,
    simplify_tolerance: float | None = 0.02,
    arrow_stride: int = 10,
    workers: int | None = None,
    run_info: dict | None = None,
    spots: tuple[np.ndarray, np.ndarray] | None = None,
) -> tuple[int, int]:
    """Process all forecast hours, fanning out over a process pool.

    Hours are fully independent (own downloads, own output files), so they
    are distributed across worker processes; workers=1 runs inline in this
    process, which keeps a simple path for debugging and tests.

    With spots (lats, lons), each hour's spot samples are collected into
    run_info["spot_samples"], keyed by position in hour_sequence. The
    forecast hours that succeeded go to run_info["hours"], ascending.
    Workers hand back packed map layers, which are written here as one
    bundle per product (write_layer_bundles); their file names go to
    run_info["bundles"].
    """
    hours = list(hour_sequence)
    if workers is None:
        workers = default_workers()
    workers = max(1, min(workers, len(hours) or 1))
    # partial() pickles by reference to the module-level function, so the
    # same callable serves both the inline and the pool path.
    process_hour = partial(
        _process_single_hour,
        date_str=date_str,
        run_hour=run_hour,
        files_dir=files_dir,
        stride=stride,
        smoothing_sigma=smoothing_sigma,
        simplify_tolerance=simplify_tolerance,
        arrow_stride=arrow_stride,
        spots=spots,
    )
    if workers > 1:
        logger.info("Processing %d forecast hours with %d workers", len(hours), workers)

    successes = 0
    failures = 0
    bounds_by_position: dict[int, dict] = {}
    spot_samples: dict[int, dict] = {}
    layers_by_hour: dict[int, dict] = {}
    succeeded_positions: list[int] = []

    def tally(
        result: tuple[str, bool, dict | None, dict | None, dict | None],
        done: int,
        position: int,
    ) -> None:
        nonlocal successes, failures
        file_index, succeeded, bounds, samples, layers = result
        if succeeded:
            successes += 1
            succeeded_positions.append(position)
            if bounds is not None:
                bounds_by_position[position] = bounds
            if samples is not None:
                spot_samples[position] = samples
            if layers is not None:
                layers_by_hour[int(hours[position])] = layers
        else:
            failures += 1
        _print_progress(done, len(hours), f"f{file_index}")

    if workers == 1:
        for position, forecast_hour in enumerate(hours):
            tally(process_hour(forecast_hour), position + 1, position)
    else:
        with ProcessPoolExecutor(
            max_workers=workers, initializer=_worker_init
        ) as pool:
            futures = {
                pool.submit(process_hour, forecast_hour): position
                for position, forecast_hour in enumerate(hours)
            }
            for done, future in enumerate(as_completed(futures), start=1):
                tally(future.result(), done, futures[future])

    if run_info is not None and bounds_by_position:
        first_position = min(bounds_by_position)
        run_info.setdefault("heatmap_bounds", bounds_by_position[first_position])
    if run_info is not None and spot_samples:
        run_info["spot_samples"] = spot_samples
    if run_info is not None:
        run_info["hours"] = [int(hours[p]) for p in sorted(succeeded_positions)]
    if layers_by_hour:
        bundles = write_layer_bundles(files_dir, layers_by_hour, f"{date_str}_{run_hour}Z")
        if run_info is not None:
            run_info["bundles"] = bundles

    _print_progress(
        len(hours), len(hours), f"done ({failures} failed)" if failures else "done"
    )
    return successes, failures


def write_layer_bundles(files_dir: str, layers_by_hour: dict[int, dict], run: str) -> dict[str, str]:
    """Write the swell, wind and contours bundles from the hours' chunks.

    Returns their file names by product, for metadata.json.

    The grid bundles take their lattice from the first hour; an hour on a
    different lattice (which composite_* should never produce) is left out
    of them rather than mislabeled.
    """
    hours = sorted(layers_by_hour)
    lat, lon = layers_by_hour[hours[0]]["axes"]
    same_lattice = {
        hour: layers
        for hour, layers in layers_by_hour.items()
        if np.array_equal(layers["axes"][0], lat) and np.array_equal(layers["axes"][1], lon)
    }
    if len(same_lattice) < len(layers_by_hour):
        logger.error(
            "Hours %s are on a different lattice; left out of the grid bundles",
            sorted(set(layers_by_hour) - set(same_lattice)),
        )
    names = {product: bundle_name(product, run) for product in ("swell", "wind", "contours")}
    for key, variables in (("swell", SWELL_VARS), ("wind", WIND_VARS)):
        name = names[key]
        count = write_bundle(
            os.path.join(files_dir, name),
            kind="grid",
            hours=hours,
            variables=variables,
            chunks={hour: layers[key] for hour, layers in same_lattice.items()},
            lat=lat,
            lon=lon,
            run=run,
            axis_decimals=2,
        )
        logger.info("Saved %s (%d hours)", name, count)
    count = write_bundle(
        os.path.join(files_dir, names["contours"]),
        kind="geojson",
        hours=hours,
        variables=CONTOURS_VARS,
        chunks={hour: layers["contours"] for hour, layers in layers_by_hour.items()},
        run=run,
    )
    logger.info("Saved %s (%d hours)", names["contours"], count)
    return names


def _download_file(
    session: requests.Session, url: str, file_path: str, attempts: int = 3
) -> bool:
    for attempt in range(1, attempts + 1):
        try:
            response = session.get(url, timeout=120)
            response.raise_for_status()
            # Write then rename so an interrupted download never leaves a
            # partial .grib2 that a later run would treat as complete.
            tmp_path = file_path + ".part"
            with open(tmp_path, "wb") as file:
                file.write(response.content)
            os.replace(tmp_path, file_path)
            return True
        except requests.RequestException as exc:
            logger.warning(
                "Download attempt %d/%d failed for %s: %s", attempt, attempts, url, exc
            )
    return False


def main() -> None:
    files_dir = os.environ.get("FILES_DIR")
    if not files_dir:
        raise EnvironmentError("FILES_DIR environment variable is not set")
    log_dir = os.environ.get("LOG_DIR")
    if not log_dir:
        raise EnvironmentError("LOG_DIR environment variable is not set")

    setup_logging(log_dir)

    # Full grid resolution (0.16 deg) with a touch more smoothing and light
    # simplification: smooth coastline-accurate polygons at a manageable size.
    stride = max(int(os.environ.get("CONTOUR_STRIDE", "1") or 1), 1)
    smoothing_sigma = float(os.environ.get("CONTOUR_SMOOTHING_SIGMA", "1.5") or 1.5)
    simplify_env = os.environ.get("CONTOUR_SIMPLIFY_TOLERANCE")
    simplify_tolerance = float(simplify_env) if simplify_env else 0.02
    arrow_stride = max(int(os.environ.get("ARROW_STRIDE", "10") or 10), 1)

    # Surf spots to sample (a copy of the web app's data/spots.json kept in
    # this repo); spot forecasts are skipped, not fatal, if it is missing.
    spots_path = os.environ.get("SPOTS_PATH") or os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "spots.json"
    )
    spot_ids: list[str] = []
    spot_points = None
    try:
        spot_ids, spot_lats, spot_lons = load_spots(spots_path)
        spot_points = (spot_lats, spot_lons)
        logger.info("Loaded %d spots from %s", len(spot_ids), spots_path)
    except (OSError, ValueError, KeyError) as exc:
        logger.warning("Spot forecasts disabled; cannot load %s: %s", spots_path, exc)

    with requests.Session() as session:
        date_str, hour = find_latest_gfs_time(session=session)
        logger.info(
            "Found latest GFS wave data for date %s hour %sZ", date_str, hour
        )
        run_info: dict = {}
        # NOAA publishes the wave grids hourly out to f120 (5 days), then
        # 3-hourly to f384. One combined sequence so the --verbose progress
        # bar spans the run.
        hour_sequence = list(range(0, 121)) + list(range(123, 387, 3))
        # run.sh --limit <n>: only the first n hours, for quick local checks.
        limit = int(os.environ.get("GRIB_LIMIT", "0") or 0)
        if limit > 0:
            hour_sequence = hour_sequence[:limit]
            logger.info(
                "GRIB_LIMIT set: processing only the first %d forecast hours",
                len(hour_sequence),
            )
        successes, failures = process_forecast_hours(
            hour_sequence,
            date_str,
            hour,
            files_dir,
            stride=stride,
            smoothing_sigma=smoothing_sigma,
            simplify_tolerance=simplify_tolerance,
            arrow_stride=arrow_stride,
            run_info=run_info,
            spots=spot_points,
        )

        spot_forecasts = None
        if run_info.get("spot_samples"):
            try:
                spot_forecasts = write_spot_forecasts(
                    files_dir,
                    spot_ids,
                    hour_sequence,
                    run_info.pop("spot_samples"),
                    f"{date_str}_{hour}Z",
                )
            except Exception as exc:
                logger.error("Writing spot forecasts failed: %s", exc, exc_info=True)

        # Nearshore NWPS mosaics and beach point grids, aligned by valid
        # time to the GFS run.
        forecast_start = datetime.strptime(f"{date_str}{hour}", "%Y%m%d%H")
        nwps = process_nwps_domains(
            session, files_dir, forecast_start, hour_sequence, render_heatmap_png
        )

        tide_stations = [
            station.strip()
            for station in os.environ.get("TIDE_STATIONS", "").split(",")
            if station.strip()
        ]
        tide_path = os.path.join(files_dir, "tides.json")
        if tide_stations:
            write_tides(session, tide_stations, tide_path)
        else:
            logger.info("TIDE_STATIONS is empty; skipping NOAA CO-OPS tide data")
            # Do not republish a file from an older configuration/run.
            try:
                os.remove(tide_path)
            except FileNotFoundError:
                pass

        # High/low tides at the station nearest each spot, for the spot
        # pages' tide chart. Predictions are astronomical, so when this
        # fails publish.rules keeps the previous run's file on the server.
        if spot_ids:
            try:
                write_spot_tides(
                    session,
                    spots_path,
                    os.path.join(files_dir, SPOT_TIDES_NAME),
                    cache_path=os.path.join(
                        os.environ.get("TIDE_CACHE_DIR") or DEFAULT_TIDE_CACHE_DIR, SPOT_TIDES_NAME
                    ),
                )
            except Exception as exc:
                logger.error("Writing spot tides failed: %s", exc, exc_info=True)

        # Keep only this run's bundles and the previous run's: maps opened
        # before this run still read theirs.
        pruned = prune_bundles(files_dir, f"{date_str}_{hour}Z")
        if pruned:
            logger.info("Pruned bundles of older runs: %s", ", ".join(pruned))

        # Metadata is written last (and copied to the server last) so the
        # frontend never sees a run announced before its contours exist.
        write_metadata(
            files_dir,
            date_str,
            hour,
            successes=successes,
            failures=failures,
            heatmap_bounds=run_info.get("heatmap_bounds"),
            nwps=nwps,
            spot_forecasts=spot_forecasts,
            hours=run_info.get("hours"),
            bundles=run_info.get("bundles"),
        )

        total = successes + failures
        logger.info("Run complete: %d/%d forecast hours processed", successes, total)
        if successes == 0:
            logger.error("All forecast hours failed; nothing to publish")
            sys.exit(2)
        if failures > total // 4:
            logger.error(
                "Too many failures (%d of %d); marking run as failed", failures, total
            )
            sys.exit(1)


if __name__ == "__main__":
    main()
