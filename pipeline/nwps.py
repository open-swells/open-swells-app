"""Nearshore wave layers from NOAA's NWPS (SWAN) regional model.

NWPS runs per NWS coastal forecast office at ~2-4 km (CG1) with ~500 m
nested grids (CG2+) over high-interest bays, hourly out to 144 h. Unlike
the deep-water global model it refracts and shoals swell over bathymetry
and shadows it behind islands, so it is the accuracy layer for beaches.

One GRIB2 file per office/grid/cycle holds every forecast hour. Offices
run on their own 00/06/12/18Z cycles independent of the GFS run driving
the global layers, so frames are aligned by valid time.

Adjacent office domains overlap (LOX and SGX share ~1.7 deg of ocean) and
run at different resolutions, so stacking one translucent PNG per office
draws hard rectangle seams. Instead all domains of a grid tier are
mosaicked onto one lattice per forecast hour — finer grids win overlaps —
and rendered as a single nwps_<grid>_<HHH>.png whose offshore edge is
alpha-feathered so it blends into the global heatmap underneath. The PNG
shows the same instant as the global heatmap_<HHH>.png and overlays it
directly; hours no domain covers are simply absent from the metadata.

For beach point forecasts each domain also emits 3-hourly
nwps_points_<wfo>_<grid>_<HHH>.geojson grids of every wet cell: compact
properties h (combined height m), p (primary mean period s) and
d (primary direction from, deg true).

Domains come from NWPS_DOMAINS as comma-separated region/wfo pairs, e.g.
"wr/lox,wr/sgx" (see https://nomads.ncep.noaa.gov/pub/data/nccf/com/nwps/
prod/ for regions and offices). Grids come from NWPS_GRIDS (default CG1).
"""

import datetime as dt
import gzip
import json
import logging
import os

import numpy as np
import pygrib
import requests
from scipy.ndimage import distance_transform_edt

logger = logging.getLogger("GFSWaveContours")

BASE_URL = "https://nomads.ncep.noaa.gov/pub/data/nccf/com/nwps/prod"
CYCLE_HOURS = ("18", "12", "06", "00")
COMBINED_HEIGHT_NAME = "Significant height of combined wind waves and swell"
TOTAL_SWELL_NAME = "Significant height of total swell"
PRIMARY_PERIOD_NAME = "Primary wave mean period"
PRIMARY_DIRECTION_NAME = "Primary wave direction"

DEFAULT_DOMAINS = "wr/lox,wr/sgx"
DEFAULT_GRIDS = "CG1"

# Cells over which the mosaic's offshore boundary fades to transparent.
# The lattice runs at the finest source resolution (~2 km for SGX), so 8
# cells is a ~15 km blend into the global layer. Coastlines stay crisp:
# only the edge of *coverage* feathers, not the land mask inside it.
FEATHER_CELLS = 8

# Beach point grids are emitted on the 3-hourly global cadence; the app
# interpolates spot forecasts to hourly rows anyway.
POINTS_HOUR_STEP = 3


def domains_from_env() -> list[tuple[str, str]]:
    """Parse NWPS_DOMAINS into (region, wfo) pairs."""
    raw = os.environ.get("NWPS_DOMAINS", DEFAULT_DOMAINS)
    domains = []
    for item in raw.split(","):
        item = item.strip().lower()
        if not item:
            continue
        region, _, wfo = item.partition("/")
        if not wfo:
            raise ValueError(
                f"NWPS_DOMAINS entries must be region/wfo (got {item!r})"
            )
        domains.append((region, wfo))
    return domains


def grids_from_env() -> list[str]:
    raw = os.environ.get("NWPS_GRIDS", DEFAULT_GRIDS)
    return [item.strip().upper() for item in raw.split(",") if item.strip()]


def _grib_url(region: str, wfo: str, grid: str, date_str: str, cycle_hour: str) -> str:
    return (
        f"{BASE_URL}/{region}.{date_str}/{wfo}/{cycle_hour}/{grid}/"
        f"{wfo}_nwps_{grid}_{date_str}_{cycle_hour}00.grib2"
    )


def find_latest_cycle(
    session: requests.Session,
    region: str,
    wfo: str,
    grid: str,
    around: dt.datetime,
) -> tuple[str, str] | None:
    """Newest published cycle for a domain, searching around the GFS run day."""
    for day_offset in (1, 0, -1):
        date_str = (around + dt.timedelta(days=day_offset)).strftime("%Y%m%d")
        for cycle_hour in CYCLE_HOURS:
            url = _grib_url(region, wfo, grid, date_str, cycle_hour)
            try:
                if session.head(url, timeout=10).status_code == 200:
                    return date_str, cycle_hour
            except requests.RequestException as exc:
                logger.debug("NWPS HEAD failed for %s: %s", url, exc)
    return None


def _download(session: requests.Session, url: str, file_path: str, attempts: int = 3) -> bool:
    if os.path.exists(file_path):
        return True
    for attempt in range(1, attempts + 1):
        try:
            response = session.get(url, timeout=180)
            response.raise_for_status()
            tmp_path = file_path + ".part"
            with open(tmp_path, "wb") as f:
                f.write(response.content)
            os.replace(tmp_path, file_path)
            return True
        except requests.RequestException as exc:
            logger.warning(
                "NWPS download attempt %d/%d failed for %s: %s",
                attempt, attempts, url, exc,
            )
    return False


def extract_nwps_fields(filepath: str) -> dict:
    """Read every forecast step of the height, period and direction fields.

    Returns 1-D lon/lat axes, the analysis (cycle) time, and a step ->
    fields mapping keyed by forecast hour. Period/direction may be absent
    for a step (they are optional, points-only inputs); height is required.
    """
    grbs = pygrib.open(filepath)
    try:
        try:
            height_msgs = grbs.select(name=COMBINED_HEIGHT_NAME)
        except ValueError as exc:
            raise RuntimeError(f"No combined height field in {filepath}") from exc
        lats, lons = height_msgs[0].latlons()
        steps: dict[int, dict] = {}
        for message in height_msgs:
            steps[int(message.forecastTime)] = {
                "height": np.ma.filled(message.values, np.nan).astype(np.float32),
                "mask": np.ma.getmaskarray(message.values),
            }
        for name, key in (
            (TOTAL_SWELL_NAME, "swell"),
            (PRIMARY_PERIOD_NAME, "period"),
            (PRIMARY_DIRECTION_NAME, "direction"),
        ):
            try:
                messages = grbs.select(name=name)
            except ValueError:
                logger.warning("No %r field in %s", name, filepath)
                continue
            for message in messages:
                step = steps.get(int(message.forecastTime))
                if step is not None:
                    step[key] = np.ma.filled(message.values, np.nan).astype(np.float32)
        return {
            "lon": np.asarray(lons, dtype=np.float64)[0, :],
            "lat": np.asarray(lats, dtype=np.float64)[:, 0],
            "cycle_time": height_msgs[0].analDate,
            "steps": steps,
        }
    finally:
        grbs.close()


def select_frames(
    hour_sequence,
    forecast_start: dt.datetime,
    cycle_time: dt.datetime,
    available_steps,
) -> list[tuple[int, int]]:
    """Map global forecast hours to NWPS steps at the same valid time.

    Returns (global_hour, nwps_step) pairs. Hours whose valid time the NWPS
    cycle does not cover are dropped: the global layer underneath remains
    the only data there.
    """
    offset = cycle_time - forecast_start
    offset_hours, remainder = divmod(int(offset.total_seconds()), 3600)
    if remainder:
        # Cycles are on whole hours; a sub-hour offset means clock skew
        # between inputs and nothing can align.
        logger.warning("NWPS cycle not on a whole-hour offset; skipping domain")
        return []
    frames = []
    for hour in hour_sequence:
        step = int(hour) - offset_hours
        if step in available_steps:
            frames.append((int(hour), step))
    return frames


class _Mosaic:
    """Composites several office domains onto one shared lattice.

    The lattice covers the union of the domain bounding boxes at the finest
    source resolution. Domains are painted coarsest-first (then oldest
    cycle first), so in overlaps the finer/fresher grid wins — including
    its land mask, which overwrites the coarser coastline beneath it.
    """

    def __init__(self, domains: list[dict]):
        # domains: [{"wfo", "lat" (1-D), "lon" (1-D), ...}] — axes may run
        # either direction; the lattice runs north->south, west->east.
        lat_step = min(float(np.abs(np.diff(d["lat"])).mean()) for d in domains)
        lon_step = min(float(np.abs(np.diff(d["lon"])).mean()) for d in domains)
        south = min(float(d["lat"].min()) for d in domains)
        north = max(float(d["lat"].max()) for d in domains)
        west = min(float(d["lon"].min()) for d in domains)
        east = max(float(d["lon"].max()) for d in domains)
        n_lat = max(2, int(round((north - south) / lat_step)) + 1)
        n_lon = max(2, int(round((east - west) / lon_step)) + 1)
        self.lat = np.linspace(north, south, n_lat)
        self.lon = np.linspace(west, east, n_lon)
        self.lat2d, self.lon2d = np.meshgrid(self.lat, self.lon, indexing="ij")
        self.shape = (n_lat, n_lon)

        # Per-domain nearest-neighbor index maps, restricted to the lattice
        # cells inside that domain's bounding box.
        self._index = {}
        for d in domains:
            rows = np.nonzero(
                (self.lat >= d["lat"].min() - 1e-9) & (self.lat <= d["lat"].max() + 1e-9)
            )[0]
            cols = np.nonzero(
                (self.lon >= d["lon"].min() - 1e-9) & (self.lon <= d["lon"].max() + 1e-9)
            )[0]
            src_rows = np.abs(self.lat[rows][:, None] - d["lat"][None, :]).argmin(axis=1)
            src_cols = np.abs(self.lon[cols][:, None] - d["lon"][None, :]).argmin(axis=1)
            # Blend weight: 0 at the domain's own bounding-box edge ramping
            # to 1 over FEATHER_CELLS, so where domains overlap the one
            # painted later fades in over the earlier one instead of
            # drawing a hard rectangle seam through the ocean.
            row_dist = np.minimum(np.arange(rows.size), rows.size - 1 - np.arange(rows.size))
            col_dist = np.minimum(np.arange(cols.size), cols.size - 1 - np.arange(cols.size))
            weight = np.clip(
                np.minimum(row_dist[:, None], col_dist[None, :]) / FEATHER_CELLS,
                0.0, 1.0,
            ).astype(np.float32)
            self._index[d["wfo"]] = (rows, cols, src_rows, src_cols, weight)
        self._alpha_cache: dict[frozenset, np.ndarray] = {}

    def compose(self, painted: list[tuple[str, np.ndarray, np.ndarray]]):
        """Paint (wfo, height, mask) layers in the given order.

        Returns (grid, alpha): the mosaicked height field (NaN where no
        coverage or land) and the feathered edge-opacity array. Where a
        later domain overlaps an earlier one its values blend in across
        the feather band; its land mask wins outright (the finer grid's
        coastline is the better one).
        """
        grid = np.full(self.shape, np.nan, dtype=np.float32)
        for wfo, height, mask in painted:
            rows, cols, src_rows, src_cols, weight = self._index[wfo]
            values = np.where(mask, np.nan, height)[np.ix_(src_rows, src_cols)]
            existing = grid[np.ix_(rows, cols)]
            grid[np.ix_(rows, cols)] = np.where(
                np.isnan(existing),
                values,
                np.where(
                    np.isnan(values),
                    np.nan,
                    weight * values + (1.0 - weight) * existing,
                ),
            )
        return grid, self._edge_alpha(frozenset(wfo for wfo, _, _ in painted))

    def _edge_alpha(self, wfos: frozenset) -> np.ndarray:
        """0..1 ramp over FEATHER_CELLS at the edge of covered lattice area.

        Coverage is the union of the contributing domains' bounding boxes —
        deliberately not the wet mask, so coastlines inside a domain stay
        fully opaque and only the offshore boundary fades out.
        """
        cached = self._alpha_cache.get(wfos)
        if cached is not None:
            return cached
        covered = np.zeros(self.shape, dtype=bool)
        for wfo in wfos:
            rows, cols, *_ = self._index[wfo]
            covered[np.ix_(rows, cols)] = True
        # Pad so the lattice border itself counts as an edge to fade from.
        padded = np.pad(covered, 1, constant_values=False)
        distance = distance_transform_edt(padded)[1:-1, 1:-1]
        alpha = np.clip(distance / FEATHER_CELLS, 0.0, 1.0).astype(np.float32)
        self._alpha_cache[wfos] = alpha
        return alpha


def _write_geojson(payload: str, geojson_path: str) -> None:
    with open(geojson_path, "w") as f:
        f.write(payload)
    with gzip.open(geojson_path + ".gz", "wt", encoding="utf-8", compresslevel=6) as f:
        f.write(payload)


def write_nwps_points(domain: dict, step_fields: dict, geojson_path: str) -> int:
    """Write every wet cell of one forecast step as compact point features."""
    height = step_fields["height"]
    mask = step_fields["mask"]
    swell = step_fields.get("swell")
    period = step_fields.get("period")
    direction = step_fields.get("direction")
    rows, cols = np.nonzero(~mask & np.isfinite(height))
    features = []
    for r, c in zip(rows.tolist(), cols.tolist()):
        properties = {"h": round(float(height[r, c]), 2)}
        if swell is not None and np.isfinite(swell[r, c]):
            properties["s"] = round(float(swell[r, c]), 2)
        if period is not None and np.isfinite(period[r, c]):
            properties["p"] = round(float(period[r, c]), 1)
        if direction is not None and np.isfinite(direction[r, c]):
            properties["d"] = int(round(float(direction[r, c]))) % 360
        features.append(
            {
                "type": "Feature",
                "geometry": {
                    "type": "Point",
                    "coordinates": [
                        round(float(domain["lon"][c]), 3),
                        round(float(domain["lat"][r]), 3),
                    ],
                },
                "properties": properties,
            }
        )
    payload = json.dumps(
        {"type": "FeatureCollection", "features": features}, separators=(",", ":")
    )
    _write_geojson(payload, geojson_path)
    return len(features)


def process_nwps_domains(
    session: requests.Session,
    files_dir: str,
    forecast_start: dt.datetime,
    hour_sequence,
    render_heatmap,
    *,
    domains: list[tuple[str, str]] | None = None,
    grids: list[str] | None = None,
) -> dict:
    """Produce the nearshore mosaic frames and beach point grids.

    render_heatmap is gfs_to_contours.render_heatmap_png (injected to keep
    this module import-independent of the main pipeline). Returns
    {"layers": [...], "points": [...]} metadata. Failures skip a domain —
    nearshore layers are an enhancement and must never fail the run.
    """
    if domains is None:
        domains = domains_from_env()
    if grids is None:
        grids = grids_from_env()

    layers: list[dict] = []
    points: list[dict] = []
    for grid in grids:
        grid_slug = grid.lower()
        loaded: list[dict] = []
        for region, wfo in domains:
            try:
                cycle = find_latest_cycle(session, region, wfo, grid, forecast_start)
                if cycle is None:
                    logger.warning("No NWPS cycle found for %s/%s %s", region, wfo, grid)
                    continue
                date_str, cycle_hour = cycle
                file_name = f"{wfo}_nwps_{grid}_{date_str}_{cycle_hour}00.grib2"
                file_path = os.path.join(files_dir, file_name)
                url = _grib_url(region, wfo, grid, date_str, cycle_hour)
                if not _download(session, url, file_path):
                    logger.error("Giving up on NWPS %s/%s %s", region, wfo, grid)
                    continue

                data = extract_nwps_fields(file_path)
                frames = select_frames(
                    hour_sequence, forecast_start, data["cycle_time"], data["steps"]
                )
                if not frames:
                    logger.warning(
                        "NWPS %s %s cycle %s %sZ overlaps no forecast hours",
                        wfo, grid, date_str, cycle_hour,
                    )
                    continue
                loaded.append(
                    {
                        "wfo": wfo,
                        "lat": data["lat"],
                        "lon": data["lon"],
                        "steps": data["steps"],
                        "cycle": f"{date_str}_{cycle_hour}Z",
                        "cycle_time": data["cycle_time"],
                        "frames": dict(frames),  # global hour -> nwps step
                        "resolution": float(
                            max(
                                np.abs(np.diff(data["lat"])).mean(),
                                np.abs(np.diff(data["lon"])).mean(),
                            )
                        ),
                    }
                )
            except Exception as exc:
                logger.error(
                    "NWPS domain %s/%s %s failed: %s", region, wfo, grid, exc,
                    exc_info=True,
                )
        if not loaded:
            continue

        try:
            # Paint order: coarsest first, then oldest cycle first, so the
            # finest/freshest data wins where domains overlap.
            loaded.sort(key=lambda d: (-d["resolution"], d["cycle_time"]))
            mosaic = _Mosaic(loaded)
            all_hours = sorted({hour for d in loaded for hour in d["frames"]})
            bounds = None
            for hour in all_hours:
                painted = [
                    (d["wfo"], d["steps"][d["frames"][hour]]["height"],
                     d["steps"][d["frames"][hour]]["mask"])
                    for d in loaded
                    if hour in d["frames"]
                ]
                height_grid, alpha = mosaic.compose(painted)
                frame_bounds = render_heatmap(
                    {
                        "lon": mosaic.lon2d,
                        "lat": mosaic.lat2d,
                        "height": height_grid,
                    },
                    os.path.join(files_dir, f"nwps_{grid_slug}_{hour:03}.png"),
                    alpha=alpha,
                )
                bounds = bounds or frame_bounds
            layers.append(
                {
                    "grid": grid_slug,
                    "bounds": bounds,
                    "hours": all_hours,
                    "domains": [
                        {"wfo": d["wfo"], "cycle": d["cycle"]} for d in loaded
                    ],
                }
            )
            logger.info(
                "NWPS %s mosaic: %d frames (hours %d-%d) from %s",
                grid, len(all_hours), all_hours[0], all_hours[-1],
                ", ".join(f"{d['wfo']} {d['cycle']}" for d in loaded),
            )
        except Exception as exc:
            logger.error("NWPS %s mosaic failed: %s", grid, exc, exc_info=True)

        for d in loaded:
            try:
                point_hours = [
                    hour for hour in sorted(d["frames"])
                    if hour % POINTS_HOUR_STEP == 0
                ]
                for hour in point_hours:
                    write_nwps_points(
                        d,
                        d["steps"][d["frames"][hour]],
                        os.path.join(
                            files_dir,
                            f"nwps_points_{d['wfo']}_{grid_slug}_{hour:03}.geojson",
                        ),
                    )
                points.append(
                    {
                        "wfo": d["wfo"],
                        "grid": grid_slug,
                        "cycle": d["cycle"],
                        "hours": point_hours,
                    }
                )
                logger.info(
                    "NWPS %s %s points: %d hoursteps", d["wfo"], grid, len(point_hours)
                )
            except Exception as exc:
                logger.error(
                    "NWPS %s %s points failed: %s", d["wfo"], grid, exc, exc_info=True
                )
    return {"layers": layers, "points": points}
