"""FES2022 tide predictions for spots with no nearby NOAA CO-OPS station.

FES2022 is a global tidal atlas (CNES/LEGOS/NOVELTIS, distributed by
AVISO+); PyFES evaluates it at a point and time. Everything runs locally:
the atlas files sit on the pipeline host and users never trigger a request.

- Spots are grouped into sites on a 0.05 degree lattice (about 5 km), so
  neighboring spots share one prediction. A site whose own point is
  masked as land by the atlas uses the nearest wet point within
  PROBE_RINGS_DEG; one with none is skipped.
- The atlas is loaded one TILE_DEG tile at a time (with a margin for the
  probe), so memory stays bounded however many spots there are.
- Predictions are made at STEP_S resolution; highs and lows are refined
  with a parabola through the samples around each turn, and the hourly
  curve is every hour's sample.
- FES heights are relative to the model's mean sea level. They are shifted
  to a modeled MLLW (the mean lower low of each tidal day over one year of
  predictions) so the chart reads like the NOAA ones, and each site keeps
  "msl" (mean sea level above that datum) so the shift can be undone.
  NOAA's MLLW averages a 19-year epoch, so the two differ by a few cm.

The site's model point and datum never change for a given atlas, so they
are kept in a cache file and computed once per site.
"""

import json
import logging
import math
import os
from collections.abc import Callable
from datetime import UTC, datetime, timedelta

import numpy as np

from paths import reference_dir

logger = logging.getLogger("GFSWaveContours")

FES_SOURCE = "FES2022"
FES_DATUM = "FES MLLW"
# Station "type" in spot_tides.json.gz: "M" for model, beside NOAA's "R"
# and "S". Model sites always carry hourly predictions.
FES_TYPE = "M"
FES_ID_PREFIX = "fes:"
FES_SITES_NAME = "fes_sites.json"
# Where the pipeline looks for the atlas when FES_CONFIG is unset: PyFES's
# ocean_tide_extrapolated.yaml beside the ocean_tide_extrapolated/ grids,
# under $OPENSWELLS_DATA_DIR/reference/fes2022.
DEFAULT_FES_CONFIG = reference_dir("fes2022", "ocean_tide_extrapolated.yaml")

SITE_STEP_DEG = 0.05
PROBE_RINGS_DEG = (0.05, 0.1, 0.2, 0.3)
TILE_DEG = 10.0
TILE_MARGIN_DEG = 1.0
STEP_S = 600
# A high and low closer than this are a wiggle on a stand, not events.
MIN_EVENT_RANGE_M = 0.05
TIDAL_DAY_S = 24.8412 * 3600
# The datum year is fixed so the datum doesn't drift from run to run.
DATUM_START = datetime(2026, 1, 1, tzinfo=UTC)
DATUM_DAYS = 365
DATUM_STEP_S = 1800
# FES2022 amplitudes are in cm.
CM_TO_M = 0.01

# predict(lons, lats, times) -> meters above model mean sea level, NaN where
# the atlas has no data. times are datetime64[us].
Predict = Callable[[np.ndarray, np.ndarray, np.ndarray], np.ndarray]
# predictor_for(bbox) -> Predict over (min_lon, min_lat, max_lon, max_lat)
PredictorFactory = Callable[[tuple[float, float, float, float]], Predict]


def resolve_config(env_value: str | None) -> str | None:
    """FES_CONFIG's value; unset means DEFAULT_FES_CONFIG if it exists, and
    an empty value turns the fallback off."""
    if env_value is not None:
        return env_value or None
    return DEFAULT_FES_CONFIG if os.path.exists(DEFAULT_FES_CONFIG) else None


def pyfes_predictors(config_path: str) -> PredictorFactory:
    """Predictors backed by PyFES and the atlas named in its YAML config.

    Only the "tide" model is used (the ocean tide relative to the sea
    floor, as a gauge sees it), plus PyFES's long-period equilibrium tide.
    """
    import pyfes  # optional dependency: the "fes" extra

    def for_bbox(bbox):
        config = pyfes.config.load(config_path, bbox=bbox)
        model, settings = config.models["tide"], config.settings

        def predict(lons, lats, times):
            tide, lp, _ = pyfes.evaluate_tide(model, times, lons, lats, settings=settings)
            return (np.asarray(tide) + np.asarray(lp)) * CM_TO_M

        return predict

    return for_bbox


def site_id(lat: float, lon: float) -> str:
    lat = round(lat / SITE_STEP_DEG) * SITE_STEP_DEG
    lon = round(lon / SITE_STEP_DEG) * SITE_STEP_DEG
    return f"{FES_ID_PREFIX}{lat:.2f},{lon:.2f}"


def _site_point(sid: str) -> tuple[float, float]:
    lat, lon = sid.removeprefix(FES_ID_PREFIX).split(",")
    return float(lat), float(lon)


def _haversine_km(lat1, lon1, lat2, lon2):
    p1, p2 = math.radians(lat1), math.radians(lat2)
    dlat, dlon = p2 - p1, math.radians(lon2 - lon1)
    h = math.sin(dlat / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dlon / 2) ** 2
    return 2 * 6371.0 * math.asin(min(1.0, math.sqrt(h)))


def _times(start: datetime, count: int, step_s: int) -> np.ndarray:
    first = np.datetime64(int(start.timestamp()), "s")
    return (first + np.arange(count) * np.timedelta64(step_s, "s")).astype("datetime64[us]")


def _probe_points(lat: float, lon: float) -> list[tuple[float, float]]:
    """The site's point, then rings around it, nearest first."""
    points = [(lat, lon)]
    lon_scale = 1 / max(math.cos(math.radians(lat)), 0.1)
    for radius in PROBE_RINGS_DEG:
        for k in range(8):
            angle = math.radians(45 * k)
            points.append((lat + radius * math.sin(angle), lon + radius * math.cos(angle) * lon_scale))
    return points


def find_wet_point(predict: Predict, lat: float, lon: float, at: datetime) -> tuple[float, float] | None:
    """The point nearest (lat, lon) the atlas has a tide for, or None."""
    points = _probe_points(lat, lon)
    lats = np.array([p[0] for p in points])
    lons = np.array([p[1] for p in points])
    heights = predict(lons, lats, _times(at, 1, STEP_S).repeat(len(points)))
    wet = [(i, _haversine_km(lat, lon, *points[i])) for i in range(len(points)) if np.isfinite(heights[i])]
    if not wet:
        return None
    best = min(wet, key=lambda item: item[1])[0]
    return round(points[best][0], 4), round(points[best][1], 4)


def model_mllw(predict: Predict, lat: float, lon: float) -> float:
    """Mean lower low water over DATUM_DAYS, meters above model MSL."""
    count = DATUM_DAYS * 86400 // DATUM_STEP_S
    heights = predict(np.full(count, lon), np.full(count, lat), _times(DATUM_START, count, DATUM_STEP_S))
    tidal_day = (np.arange(count) * DATUM_STEP_S // TIDAL_DAY_S).astype(int)
    day_starts = np.concatenate(([0], np.flatnonzero(np.diff(tidal_day)) + 1))
    lower_lows = np.minimum.reduceat(heights, day_starts)[:-1]  # the last day is cut short
    return float(np.mean(lower_lows))


def find_extremes(start: int, step_s: int, values: np.ndarray, min_range: float = MIN_EVENT_RANGE_M) -> list[list]:
    """Highs and lows of a sampled curve as [unix seconds, value, "H" | "L"].

    Each turn is refined with the parabola through it and its neighbors.
    Adjacent high/low pairs whose samples differ by less than min_range are
    dropped as wiggles.
    """
    v = np.asarray(values, dtype=float)
    sign = np.sign(np.diff(v))
    for i in range(1, len(sign)):  # carry the trend across flat samples
        if sign[i] == 0:
            sign[i] = sign[i - 1]
    events = []
    for i in np.nonzero(sign[:-1] != sign[1:])[0] + 1:
        if sign[i - 1] == 0:
            continue
        a, b, c = v[i - 1], v[i], v[i + 1]
        denom = a - 2 * b + c
        offset = float(np.clip(0.5 * (a - c) / denom, -1, 1)) if denom else 0.0
        events.append((start + (i + offset) * step_s, b - 0.25 * (a - c) * offset, "H" if sign[i - 1] > 0 else "L", b))
    while len(events) > 1:
        gaps = [abs(events[j + 1][3] - events[j][3]) for j in range(len(events) - 1)]
        j = int(np.argmin(gaps))
        if gaps[j] >= min_range:
            break
        # removing an adjacent pair keeps highs and lows alternating
        del events[j : j + 2]
    return [[int(round(t)), round(float(h), 3), kind] for t, h, kind, _ in events]


def predict_site(predict: Predict, lat: float, lon: float, msl: float, start: datetime, end: datetime) -> dict:
    """High/low events and hourly heights (meters above the site's datum)."""
    count = int((end - start).total_seconds()) // STEP_S + 1
    heights = predict(np.full(count, lon), np.full(count, lat), _times(start, count, STEP_S)) + msl
    if not np.all(np.isfinite(heights)):
        raise RuntimeError("model has no tide at the site's point")
    t0 = int(start.timestamp())
    per_hour = 3600 // STEP_S
    return {
        "hilo": find_extremes(t0, STEP_S, heights),
        "hourly": {"start": t0, "v": [round(float(h), 3) for h in heights[::per_hour]]},
    }


def _tile(lat: float, lon: float) -> tuple[int, int]:
    return math.floor(lat / TILE_DEG), math.floor(lon / TILE_DEG)


def _tile_bbox(tile: tuple[int, int]) -> tuple[float, float, float, float]:
    lat0, lon0 = tile[0] * TILE_DEG, tile[1] * TILE_DEG
    return (
        lon0 - TILE_MARGIN_DEG,
        max(-90.0, lat0 - TILE_MARGIN_DEG),
        lon0 + TILE_DEG + TILE_MARGIN_DEG,
        min(90.0, lat0 + TILE_DEG + TILE_MARGIN_DEG),
    )


def load_site_cache(path: str | None, model: str) -> dict:
    """Site id -> {"lat", "lon", "msl"} (or None for land) for this atlas."""
    if not path:
        return {}
    try:
        with open(path) as f:
            cached = json.load(f)
    except (OSError, ValueError):
        return {}
    if not isinstance(cached, dict) or cached.get("model") != model:
        return {}
    return cached.get("sites", {})


def save_site_cache(path: str | None, model: str, sites: dict) -> None:
    if not path:
        return
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path + ".tmp", "w") as f:
            json.dump({"model": model, "sites": sites}, f, separators=(",", ":"))
        os.replace(path + ".tmp", path)
    except OSError as exc:
        logger.warning("Could not cache FES sites at %s: %s", path, exc)


def fes_spot_tides(
    spots: list[dict],
    predictor_for: PredictorFactory,
    *,
    now: datetime,
    forecast_hours: int = 384,
    cached: dict | None = None,
    previous: dict | None = None,
    sites: dict | None = None,
) -> tuple[dict, dict, list]:
    """FES2022 tides for spots, as (stations, spot matches, errors) shaped
    like write_spot_tides' NOAA entries.

    cached holds stations from earlier today, reused as they are; the atlas
    is only loaded for tiles with sites still missing. A site that fails
    (say the atlas can't be read) keeps its station from previous, an
    older file, while that still covers the forecast (see still_current).
    sites is the site cache (see load_site_cache) and gains any site
    worked out here.
    """
    cached, previous = cached or {}, previous or {}
    sites = sites if sites is not None else {}
    # the same window as the NOAA predictions: a day either side
    start = datetime(now.year, now.month, now.day, tzinfo=UTC) - timedelta(days=1)
    end = datetime.combine((now + timedelta(hours=forecast_hours + 24)).date(), datetime.min.time(), UTC) + timedelta(days=1)

    spot_sites: dict[str, tuple[str, float, float]] = {}
    for spot in spots:
        try:
            spot_id, lat, lon = spot["id"], float(spot["lat"]), float(spot["lon"])
        except (KeyError, TypeError, ValueError):
            continue
        spot_sites[spot_id] = (site_id(lat, lon), lat, lon)
    wanted = sorted({sid for sid, _, _ in spot_sites.values()})

    stations, errors = {}, []
    by_tile: dict[tuple[int, int], list[str]] = {}
    for sid in wanted:
        if cached.get(sid, {}).get("hilo"):
            stations[sid] = cached[sid]
        elif sid in sites and sites[sid] is None:
            continue  # land as far as the probe reaches
        else:
            by_tile.setdefault(_tile(*_site_point(sid)), []).append(sid)
    logger.info(
        "FES tides: %d spots at %d sites (%d cached today, %d tiles to load)",
        len(spot_sites), len(wanted), len(stations), len(by_tile),
    )

    for tile, tile_sites in sorted(by_tile.items()):
        try:
            predict = predictor_for(_tile_bbox(tile))
        except Exception as exc:  # a missing package or atlas file, a bad config
            logger.error("Loading FES tile %s failed: %s", tile, exc)
            errors.extend({"station": sid, "error": f"loading the atlas: {exc}"} for sid in tile_sites)
            continue
        for sid in tile_sites:
            try:
                site = sites.get(sid)
                if site is None:
                    point = find_wet_point(predict, *_site_point(sid), now)
                    if point is None:
                        sites[sid] = None
                        continue
                    site = {"lat": point[0], "lon": point[1]}
                    site["msl"] = round(-model_mllw(predict, site["lat"], site["lon"]), 3)
                    sites[sid] = site
                prediction = predict_site(predict, site["lat"], site["lon"], site["msl"], start, end)
            except (RuntimeError, ValueError, KeyError, TypeError) as exc:
                errors.append({"station": sid, "error": str(exc)})
                continue
            stations[sid] = {
                "name": f"{FES_SOURCE} model",
                "lat": site["lat"],
                "lon": site["lon"],
                "type": FES_TYPE,
                "source": FES_SOURCE,
                "datum": FES_DATUM,
                "msl": site["msl"],
                **prediction,
            }

    for error in errors:
        if error["station"] in previous:
            stations[error["station"]] = previous[error["station"]]
            error["error"] += " (kept the previous prediction)"

    matches = {}
    for spot_id, (sid, lat, lon) in spot_sites.items():
        station = stations.get(sid)
        if station:
            matches[spot_id] = {"station": sid, "km": round(_haversine_km(lat, lon, station["lat"], station["lon"]), 1)}
    return stations, matches, errors


def still_current(stations: dict, now: datetime, forecast_hours: int = 384) -> dict:
    """FES stations from an older file whose events still reach past the
    forecast's first days, to stand in when the atlas can't be read."""
    horizon = (now + timedelta(hours=forecast_hours / 4)).timestamp()
    return {
        sid: station
        for sid, station in stations.items()
        if sid.startswith(FES_ID_PREFIX) and station.get("hilo") and station["hilo"][-1][0] > horizon
    }
