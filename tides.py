"""NOAA CO-OPS tide predictions and recent observed water levels.

Two products:

- tides.json: hourly predictions plus recent observations for the stations
  listed in TIDE_STATIONS (the map's tide layer).
- spot_tides.json.gz: high/low predictions for the station nearest each
  surf spot, for the spot page's tide chart. Most stations near the coast
  are subordinate stations, which CO-OPS only predicts as high/low events
  (an hourly request fails), and the web app draws the curve between their
  events. Reference stations also get hourly predictions: around neap tides
  one high/low pair can flatten into a stand that has no event, which a
  curve drawn between events misses.
"""

import gzip
import json
import logging
import math
import os
import shutil
import threading
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta

import requests

logger = logging.getLogger("GFSWaveContours")
DATA_URL = "https://api.tidesandcurrents.noaa.gov/api/prod/datagetter"
METADATA_URL = "https://api.tidesandcurrents.noaa.gov/mdapi/prod/webapi/stations"

SPOT_TIDES_NAME = "spot_tides.json.gz"
# A spot farther than this from every prediction station gets no tide
# chart; past it the station's tide no longer stands in for the beach's.
SPOT_TIDE_MAX_KM = 50.0
EARTH_RADIUS_KM = 6371.0
# Each run's spot_tides file is also kept here (FILES_DIR is wiped per
# run); stations it holds from the same UTC day are reused instead of
# refetched, so each station costs about one fetch a day.
DEFAULT_TIDE_CACHE_DIR = os.path.expanduser("~/.cache/openswells-tides")


def _get_json(session: requests.Session, url: str, params: dict | None = None) -> dict:
    response = session.get(url, params=params, timeout=30)
    response.raise_for_status()
    payload = response.json()
    if "error" in payload:
        raise RuntimeError(payload["error"].get("message", str(payload["error"])))
    return payload


def _data_params(station: str, product: str) -> dict:
    return {
        "application": "openswells",
        "format": "json",
        "station": station,
        "product": product,
        "datum": "MLLW",
        "time_zone": "gmt",
        "units": "metric",
    }


def fetch_station(
    session: requests.Session,
    station: str,
    *,
    now: datetime | None = None,
    forecast_hours: int = 384,
) -> dict:
    """Fetch hourly tide predictions and the last 48 hours of observations."""
    now = (now or datetime.now(UTC)).astimezone(UTC)
    prediction_end = now + timedelta(hours=forecast_hours)

    prediction_params = _data_params(station, "predictions")
    prediction_params.update(
        {
            "begin_date": now.strftime("%Y%m%d"),
            "end_date": prediction_end.strftime("%Y%m%d"),
            "interval": "h",
        }
    )
    predictions = _get_json(session, DATA_URL, prediction_params)

    observation_params = _data_params(station, "water_level")
    observation_params.update(
        {
            "begin_date": (now - timedelta(hours=48)).strftime("%Y%m%d %H:%M"),
            "end_date": now.strftime("%Y%m%d %H:%M"),
        }
    )
    try:
        observations = _get_json(session, DATA_URL, observation_params).get("data", [])
    except (requests.RequestException, RuntimeError, ValueError) as exc:
        # Prediction-only subordinate stations legitimately have no gauge.
        logger.warning("No observed water level for station %s: %s", station, exc)
        observations = []

    metadata = _get_json(session, f"{METADATA_URL}/{station}.json")
    station_data = metadata.get("stations", [{}])[0]
    return {
        "id": station,
        "name": station_data.get("name"),
        "lat": station_data.get("lat"),
        "lon": station_data.get("lng"),
        "datum": "MLLW",
        "units": "meters",
        "time_zone": "UTC",
        "predictions": predictions.get("predictions", []),
        "observations": observations,
    }


def write_tides(
    session: requests.Session,
    station_ids: list[str],
    output_path: str,
    *,
    now: datetime | None = None,
) -> tuple[int, int]:
    """Write one resilient tide payload for all configured CO-OPS stations."""
    stations = []
    errors = []
    for station_id in station_ids:
        try:
            stations.append(fetch_station(session, station_id, now=now))
        except (requests.RequestException, RuntimeError, ValueError, KeyError) as exc:
            logger.error("Tide station %s failed: %s", station_id, exc)
            errors.append({"station": station_id, "error": str(exc)})

    payload = {
        "generated_at": (now or datetime.now(UTC)).astimezone(UTC).isoformat(),
        "source": "NOAA CO-OPS",
        "stations": stations,
        "errors": errors,
    }
    with open(output_path, "w") as output:
        json.dump(payload, output, separators=(",", ":"))
    logger.info("Tides saved to %s (%d stations, %d errors)", output_path, len(stations), len(errors))
    return len(stations), len(errors)


def fetch_prediction_stations(session: requests.Session) -> list[dict]:
    """Every CO-OPS station with tide predictions (reference and subordinate)."""
    payload = _get_json(session, f"{METADATA_URL}.json", {"type": "tidepredictions"})
    stations = []
    for station in payload.get("stations", []):
        try:
            stations.append(
                {
                    "id": str(station["id"]),
                    "name": station.get("name"),
                    "lat": float(station["lat"]),
                    "lon": float(station["lng"]),
                    "type": station.get("type"),
                }
            )
        except (KeyError, TypeError, ValueError):
            continue
    return stations


def _haversine_km(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    p1, p2 = math.radians(lat1), math.radians(lat2)
    dlat, dlon = p2 - p1, math.radians(lon2 - lon1)
    h = math.sin(dlat / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dlon / 2) ** 2
    return 2 * EARTH_RADIUS_KM * math.asin(min(1.0, math.sqrt(h)))


def match_spots(
    spots: list[dict],
    stations: list[dict],
    max_km: float = SPOT_TIDE_MAX_KM,
) -> dict[str, dict]:
    """Map spot id -> {"station", "km"} for spots near a prediction station.

    A spot's hand-set "tide_station" wins over the nearest station, as long
    as CO-OPS still predicts it.
    """
    by_id = {station["id"]: station for station in stations}
    matches = {}
    for spot in spots:
        try:
            spot_id, lat, lon = spot["id"], float(spot["lat"]), float(spot["lon"])
        except (KeyError, TypeError, ValueError):
            continue
        pinned = by_id.get(str(spot.get("tide_station", "")))
        if pinned:
            best, best_km = pinned, _haversine_km(lat, lon, pinned["lat"], pinned["lon"])
        else:
            best, best_km = None, max_km
            for station in stations:
                # cheap latitude reject before the trig: 1 deg lat ~ 111 km
                if abs(station["lat"] - lat) * 111.0 > best_km:
                    continue
                km = _haversine_km(lat, lon, station["lat"], station["lon"])
                if km <= best_km:
                    best, best_km = station, km
        if best is not None:
            matches[spot_id] = {"station": best["id"], "km": round(best_km, 1)}
    return matches


def _spot_prediction_params(station: str, now: datetime, forecast_hours: int, interval: str) -> dict:
    # The window starts a day early and ends a day late so the curve can
    # be drawn through local midnight at either end of the forecast.
    params = _data_params(station, "predictions")
    params.update(
        {
            "begin_date": (now - timedelta(days=1)).strftime("%Y%m%d"),
            "end_date": (now + timedelta(hours=forecast_hours + 24)).strftime("%Y%m%d"),
            "interval": interval,
        }
    )
    return params


def _unix(t: str) -> int:
    return int(datetime.strptime(t, "%Y-%m-%d %H:%M").replace(tzinfo=UTC).timestamp())


def fetch_hilo(
    session: requests.Session,
    station: str,
    *,
    now: datetime,
    forecast_hours: int = 384,
) -> list[list]:
    """High/low predictions as [unix seconds, meters MLLW, "H" | "L"]."""
    params = _spot_prediction_params(station, now, forecast_hours, "hilo")
    events = [
        [_unix(event["t"]), round(float(event["v"]), 3), event.get("type", "")]
        for event in _get_json(session, DATA_URL, params).get("predictions", [])
    ]
    if not events:
        raise RuntimeError("no high/low predictions")
    return events


def fetch_hourly(
    session: requests.Session,
    station: str,
    *,
    now: datetime,
    forecast_hours: int = 384,
) -> dict:
    """Hourly predictions (reference stations only) as {"start", "v"}:
    unix seconds of the first value and meters MLLW one hour apart."""
    params = _spot_prediction_params(station, now, forecast_hours, "h")
    predictions = _get_json(session, DATA_URL, params).get("predictions", [])
    if not predictions:
        raise RuntimeError("no hourly predictions")
    start = _unix(predictions[0]["t"])
    for i, prediction in enumerate(predictions):
        if _unix(prediction["t"]) != start + i * 3600:
            raise RuntimeError(f"hourly predictions not contiguous at {prediction['t']}")
    return {"start": start, "v": [round(float(p["v"]), 3) for p in predictions]}


def _cached_stations(cache_path: str | None, now: datetime) -> dict:
    """Stations from the cached file when it was written on now's UTC day."""
    if not cache_path:
        return {}
    try:
        with gzip.open(cache_path, "rt") as f:
            cached = json.load(f)
        generated = datetime.fromisoformat(cached["generated_at"]).astimezone(UTC)
    except (OSError, ValueError, KeyError, TypeError):
        return {}
    if generated.date() != now.date():
        return {}
    return cached.get("stations", {})


def write_spot_tides(
    session: requests.Session,
    spots_path: str,
    output_path: str,
    *,
    now: datetime | None = None,
    max_km: float = SPOT_TIDE_MAX_KM,
    workers: int = 4,
    cache_path: str | None = None,
) -> tuple[int, int]:
    """Write high/low tides for the station nearest each spot.

    Payload:

        {
          "generated_at": "...", "source": "NOAA CO-OPS",
          "datum": "MLLW", "units": "meters",
          "stations": {"<id>": {"name", "lat", "lon", "type", "hilo": [[t, v, "H"], ...],
                                "hourly": {"start": t, "v": [...]}}},
          "spots": {"<spot id>": {"station": "<id>", "km": 3.2}},
          "errors": [{"station": "<id>", "error": "..."}]
        }

    "type" is "R" (reference) or "S" (subordinate); only reference stations
    have "hourly", and it is left out when that request fails. Spots whose
    station failed are left out. Returns (stations written, stations failed).

    With cache_path, stations already in that file from the same UTC day are
    reused, and the result is copied there for the next run.
    """
    now = (now or datetime.now(UTC)).astimezone(UTC)
    with open(spots_path) as f:
        spots = json.load(f)
    stations = fetch_prediction_stations(session)
    matches = match_spots(spots, stations, max_km)
    by_id = {station["id"]: station for station in stations}
    wanted = sorted({match["station"] for match in matches.values()})
    cached = _cached_stations(cache_path, now)
    logger.info(
        "Tides: %d spots matched to %d CO-OPS stations (%d cached today)",
        len(matches), len(wanted), sum(bool(cached.get(station_id, {}).get("hilo")) for station_id in wanted),
    )

    failures = (requests.RequestException, RuntimeError, ValueError, KeyError)
    # CO-OPS answers a burst of requests with 403 for a while; once it does,
    # stop asking this run. The next run fetches whatever is still missing.
    blocked = threading.Event()

    def fetch(station_id):
        if cached.get(station_id, {}).get("hilo"):
            station = cached[station_id]
            return station_id, station["hilo"], station.get("hourly"), None
        if blocked.is_set():
            return station_id, None, None, "skipped: CO-OPS is rate limiting this run"
        try:
            events = fetch_hilo(session, station_id, now=now)
        except failures as exc:
            if isinstance(exc, requests.HTTPError) and exc.response is not None and exc.response.status_code in (403, 429):
                blocked.set()
            return station_id, None, None, str(exc)
        hourly = None
        if by_id[station_id]["type"] == "R":
            try:
                hourly = fetch_hourly(session, station_id, now=now)
            except failures as exc:
                # the curve falls back to the high/low events
                logger.warning("Hourly tides for station %s failed: %s", station_id, exc)
        return station_id, events, hourly, None

    out_stations, errors = {}, []
    with ThreadPoolExecutor(max_workers=workers) as pool:
        for station_id, events, hourly, error in pool.map(fetch, wanted):
            if error:
                logger.warning("Tide station %s failed: %s", station_id, error)
                errors.append({"station": station_id, "error": error})
                continue
            station = by_id[station_id]
            out_stations[station_id] = {
                "name": station["name"],
                "lat": station["lat"],
                "lon": station["lon"],
                "type": station["type"],
                "hilo": events,
            }
            if hourly:
                out_stations[station_id]["hourly"] = hourly

    payload = {
        "generated_at": now.isoformat(),
        "source": "NOAA CO-OPS",
        "datum": "MLLW",
        "units": "meters",
        "stations": out_stations,
        "spots": {k: v for k, v in matches.items() if v["station"] in out_stations},
        "errors": errors,
    }
    # write-then-rename so a reader never sees a half-written file
    tmp_path = output_path + ".tmp"
    with gzip.open(tmp_path, "wt") as output:
        json.dump(payload, output, separators=(",", ":"))
    os.replace(tmp_path, output_path)
    if cache_path:
        try:
            os.makedirs(os.path.dirname(cache_path), exist_ok=True)
            shutil.copyfile(output_path, cache_path + ".tmp")
            os.replace(cache_path + ".tmp", cache_path)
        except OSError as exc:
            logger.warning("Could not cache spot tides at %s: %s", cache_path, exc)
    if blocked.is_set():
        logger.warning("CO-OPS rate limited the tide fetch; missing stations are retried next run")
    logger.info(
        "Spot tides saved to %s (%d stations, %d errors, %d spots)",
        output_path, len(out_stations), len(errors), len(payload["spots"]),
    )
    return len(out_stations), len(errors)
