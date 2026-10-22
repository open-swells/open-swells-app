"""Per-spot beach facing and swell window from a high-resolution coastline.

A one-time (re-run when spots change) offline job. The web app rates surf
by how squarely each swell hits a beach, which needs to know which way the
beach faces and which directions are open to the sea. Without this the
app guesses from the bearing to the nearest wave-model cell (~10 km away),
which is wrong at points, coves and anywhere islands shadow the coast.

For each spot in spots.json:

1. Snap to the nearest shoreline within SNAP_MAX_M of the spot.
2. Fit the shoreline's direction around that point (principal axis of
   the shoreline vertices within TANGENT_RADIUS_M) and take the
   perpendicular pointing out to sea: the beach's *facing*.
3. Cast rays every RAY_STEP_DEG from just offshore and measure how far each
   travels before hitting land (up to RAY_LENGTH_M). Directions with open
   water past the fetch threshold make up the beach's *open water*, as
   one or more arcs: narrow blocked gaps (small rocks and islets swell
   wraps around) are bridged, and every remaining arc is kept, so an
   island splits the window rather than truncating it (Catalina gives
   Huntington Beach a south arc and a west slot). This is what catches
   island shadows and headlands around coves.

Spots more than SNAP_MAX_M from a coastline are left alone: in water they
are offshore reefs (window only, from the spot itself), on land they are
lakes or rivers.

Written into each spot: "facing" (degrees true the beach faces) and
"open_water" [[from, to], ...] (clockwise arcs of open-water directions,
degrees true, widest first). Spots with a hand-tuned "swell_window" are
skipped: that is an effective window the app uses on its own, while
open_water is geometry the app combines with the facing (swell arriving
at a steep angle to the beach loses energy even through open water).

Coastline: GSHHG full resolution native binary (gshhs_f.b) from
https://www.soest.hawaii.edu/pwessel/gshhg/ (LGPL). Usage:

    python -I spot_facing.py --gshhg /path/to/gshhs_f.b \\
        --report facing_report.csv spots.json ../app/data/spots.json

Geometry runs in a spherical azimuthal equidistant projection centered on
each spot, where distances and bearings from the center are exact.
"""

import argparse
import csv
import json
import math
import multiprocessing
import os
import re
import struct
import sys
import time

import numpy as np
import shapely
from shapely import STRtree

EARTH_RADIUS_M = 6_371_008.8

SNAP_MAX_M = 5_000.0  # farther from any shoreline: not a beach
TANGENT_RADIUS_M = 1_000.0  # shoreline span the facing is fitted over
# Coarser and finer fits; facings that disagree across them are flagged
# for review (a cove inside a bay, a jetty on a straight beach).
CHECK_RADII_M = (400.0, 2_500.0)
OFFSHORE_STEPS_M = (150.0, 300.0, 600.0, 1_200.0)  # ray origin distance from shore
RAY_STEP_DEG = 3.0
RAY_LENGTH_M = 150_000.0
# A direction is open when its ray runs this far before land. Inside
# enclosed water (lakes, small seas, harbors) the bar drops to a share of
# the longest ray, so the widest water still reads as the window.
OPEN_FETCH_M = 120_000.0
ENCLOSED_SHARE = 0.6
MAX_GAP_DEG = 9.0  # blocked runs this narrow are bridged
MIN_ARC_DEG = 6.0  # open runs narrower than this are ignored
SPREAD_FLAG_DEG = 45.0

# Tiles keep each spot's geometry small: GSHHG's continents are single
# polygons of ~1M vertices. Coarse tiles are clipped once from the full
# polygons, fine tiles from the coarse pieces.
COARSE_DEG = 10
FINE_DEG = 1


# --- GSHHG reader ---------------------------------------------------------

_HEADER = struct.Struct(">11i")


def read_gshhg(path: str, levels=(1,)) -> list[np.ndarray]:
    """Shoreline polygons from a GSHHG native binary file.

    Returns one (n, 2) float64 array of (lon, lat) per polygon of the given
    levels (1 = land/ocean boundary). Longitudes follow the GSHHG reader's
    convention: west of Greenwich is negative, so most polygons sit in
    -180..180 but those crossing the dateline run past 180.
    """
    with open(path, "rb") as f:
        data = f.read()
    polygons = []
    offset = 0
    while offset < len(data):
        (_, n, flag, west, _, _, _, _, _, _, _) = _HEADER.unpack_from(data, offset)
        offset += _HEADER.size
        level = flag & 255
        if level in levels:
            points = np.frombuffer(data, dtype=">i4", count=2 * n, offset=offset)
            coords = points.reshape(n, 2).astype(np.float64) * 1e-6
            greenwich = (flag >> 16) & 1
            if west > 180_000_000:
                coords[:, 0] -= 360.0
            elif greenwich:
                coords[coords[:, 0] > 270.0, 0] -= 360.0
            polygons.append(coords)
        offset += 8 * n
    return polygons


# --- Coastline index ------------------------------------------------------


class Coastline:
    """Land polygons and shoreline lines, clipped into lazily built tiles.

    Tiles cover lon -180..180; a polygon past 180 is also indexed shifted
    by -360 so tiles just east of the dateline find it.
    """

    def __init__(self, rings: list[np.ndarray]):
        polygons = []
        for coords in rings:
            polygon = shapely.polygons(coords)
            polygons.append(polygon)
            if coords[:, 0].max() > 180.0:
                polygons.append(shapely.transform(polygon, lambda c: c - (360.0, 0.0)))
        self.polygons = np.array(polygons, dtype=object)
        self.tree = STRtree(self.polygons)
        self._coarse: dict[tuple[int, int], tuple[np.ndarray, np.ndarray]] = {}
        self._fine: dict[tuple[int, int], tuple[np.ndarray, np.ndarray]] = {}

    @staticmethod
    def _clip(land: np.ndarray, coast: np.ndarray, box: tuple, *, repair: bool = False):
        land = shapely.clip_by_rect(land, *box)
        coast = shapely.clip_by_rect(coast, *box)
        land, coast = land[~shapely.is_empty(land)], coast[~shapely.is_empty(coast)]
        if repair:
            # A few full-resolution polygons self-intersect (e.g. southern
            # Brazil), which makes GEOS overlay ops raise. Repair the small
            # fine-tile pieces rather than the continent-sized originals.
            invalid = ~shapely.is_valid(land)
            if invalid.any():
                land = land.copy()
                land[invalid] = shapely.make_valid(land[invalid])
        return land, coast

    def _coarse_tile(self, key: tuple[int, int]):
        tile = self._coarse.get(key)
        if tile is None:
            lat0, lon0 = key[0] * COARSE_DEG, key[1] * COARSE_DEG
            box = (lon0, lat0, lon0 + COARSE_DEG, lat0 + COARSE_DEG)
            candidates = self.polygons[self.tree.query(shapely.box(*box))]
            # Clip shoreline from the polygon boundary, not from clipped
            # polygons: those gain artificial edges along the tile border.
            tile = self._clip(candidates, shapely.boundary(candidates), box)
            self._coarse[key] = tile
        return tile

    def tile(self, lat_index: int, lon_index: int) -> tuple[np.ndarray, np.ndarray]:
        """(land polygons, shoreline lines) of the 1-degree tile whose
        south-west corner is (lat_index, lon_index), lon_index in -180..179."""
        key = (lat_index, lon_index)
        tile = self._fine.get(key)
        if tile is None:
            coarse = self._coarse_tile(
                (math.floor(lat_index / COARSE_DEG), math.floor(lon_index / COARSE_DEG))
            )
            box = (lon_index, lat_index, lon_index + FINE_DEG, lat_index + FINE_DEG)
            tile = self._clip(*coarse, box, repair=True)
            self._fine[key] = tile
        return tile

    def around(self, lat: float, lon: float, radius_m: float):
        """Land and shoreline geometry within radius_m of (lat, lon), with
        longitudes shifted to sit next to lon (dateline-safe)."""
        dlat = math.degrees(radius_m / EARTH_RADIUS_M) + 0.01
        coslat = max(math.cos(math.radians(lat)), 0.05)
        dlon = min(dlat / coslat, 180.0)
        land, coast = [], []
        for lat_index in range(math.floor(lat - dlat), math.floor(lat + dlat) + 1):
            if lat_index < -90 or lat_index >= 90:
                continue
            for raw in range(math.floor(lon - dlon), math.floor(lon + dlon) + 1):
                lon_index = (raw + 180) % 360 - 180
                tile_land, tile_coast = self.tile(lat_index, lon_index)
                shift = raw - lon_index
                if shift:
                    offset = (float(shift), 0.0)
                    tile_land = shapely.transform(tile_land, lambda c, o=offset: c + o)
                    tile_coast = shapely.transform(tile_coast, lambda c, o=offset: c + o)
                land.append(tile_land)
                coast.append(tile_coast)
        empty = np.array([], dtype=object)
        return (
            np.concatenate(land) if land else empty,
            np.concatenate(coast) if coast else empty,
        )


# --- Geometry helpers -----------------------------------------------------


def aeqd(lat0: float, lon0: float):
    """Spherical azimuthal equidistant projection centered on (lat0, lon0),
    as a shapely.transform function: (lon, lat) degrees -> (x east, y north)
    meters, with distance and bearing from the center exact."""
    phi0, lam0 = math.radians(lat0), math.radians(lon0)
    sin0, cos0 = math.sin(phi0), math.cos(phi0)

    def project(coords: np.ndarray) -> np.ndarray:
        phi = np.radians(coords[:, 1])
        dlam = np.radians(coords[:, 0]) - lam0
        sin_phi, cos_phi = np.sin(phi), np.cos(phi)
        cos_c = np.clip(sin0 * sin_phi + cos0 * cos_phi * np.cos(dlam), -1.0, 1.0)
        c = np.arccos(cos_c)
        with np.errstate(invalid="ignore", divide="ignore"):
            k = np.where(c > 1e-12, c / np.sin(c), 1.0)
        x = EARTH_RADIUS_M * k * cos_phi * np.sin(dlam)
        y = EARTH_RADIUS_M * k * (cos0 * sin_phi - sin0 * cos_phi * np.cos(dlam))
        return np.column_stack((x, y))

    return project


def unit(bearing_deg: float) -> np.ndarray:
    rad = math.radians(bearing_deg)
    return np.array((math.sin(rad), math.cos(rad)))


def bearing_of(vector) -> float:
    return math.degrees(math.atan2(vector[0], vector[1])) % 360.0


def ang_diff(a: float, b: float) -> float:
    d = abs(a - b) % 360.0
    return 360.0 - d if d > 180.0 else d


def shoreline_axis(coast_tree: STRtree, coast: np.ndarray, point, radius_m: float):
    """Unit vector along the shoreline at point: the principal axis of the
    shoreline vertices within radius_m (None if there are too few)."""
    disk = shapely.buffer(point, radius_m)
    nearby = coast[coast_tree.query(disk)]
    coords = shapely.get_coordinates(shapely.intersection(nearby, disk))
    if len(coords) < 2:
        return None
    centered = coords - coords.mean(axis=0)
    _, _, vt = np.linalg.svd(centered, full_matrices=False)
    return vt[0]


def free_distances(land_tree: STRtree, land: np.ndarray, origin: np.ndarray, bearings: np.ndarray):
    """Meters each ray from origin travels before hitting land (capped at
    RAY_LENGTH_M)."""
    rads = np.radians(bearings)
    ends = origin + RAY_LENGTH_M * np.column_stack((np.sin(rads), np.cos(rads)))
    starts = np.broadcast_to(origin, ends.shape)
    rays = shapely.linestrings(np.stack((starts, ends), axis=1))
    distances = np.full(len(bearings), RAY_LENGTH_M)
    ray_index, land_index = land_tree.query(rays, predicate="intersects")
    if len(ray_index):
        hits = shapely.intersection(rays[ray_index], land[land_index])
        d = shapely.distance(shapely.points(origin), hits)
        np.minimum.at(distances, ray_index, d)
    return distances


def open_arcs(bearings: np.ndarray, distances: np.ndarray, step: float) -> list:
    """Open-water arcs as [(from, to), ...] degrees clockwise, widest first;
    [(0, 360)] when open all around, [] when nothing is. Blocked runs up to
    MAX_GAP_DEG wide are bridged; open runs narrower than MIN_ARC_DEG are
    dropped unless they are all there is (a beach that only sees the sea
    through one narrow slot keeps that slot)."""
    threshold = min(OPEN_FETCH_M, ENCLOSED_SHARE * float(distances.max()))
    is_open = distances >= threshold
    n = len(is_open)
    if not is_open.any():
        return []
    # Rotate so index 0 is open: no blocked run then wraps the array end.
    shift = int(np.argmax(is_open))
    rolled = np.roll(is_open, -shift)
    bridged = rolled.copy()
    max_gap = int(round(MAX_GAP_DEG / step))
    i = 0
    while i < n:
        j = i
        while j < n and rolled[j] == rolled[i]:
            j += 1
        if not rolled[i] and j - i <= max_gap:
            bridged[i:j] = True
        i = j
    if bridged.all():
        return [(0.0, 360.0)]
    # Rotate again so index 0 is blocked: no open run wraps the end.
    shift2 = int(np.argmin(bridged))
    rolled2 = np.roll(bridged, -shift2)
    arcs = []
    i = 0
    while i < n:
        j = i
        while j < n and rolled2[j] == rolled2[i]:
            j += 1
        if rolled2[i]:
            first = bearings[(i + shift2 + shift) % n]
            start = (first - step / 2) % 360.0
            arcs.append((start, (start + (j - i) * step) % 360.0, (j - i) * step))
        i = j
    arcs.sort(key=lambda arc: -arc[2])
    kept = [arc for arc in arcs if arc[2] >= MIN_ARC_DEG] or arcs[:1]
    return [(start, end) for start, end, _ in kept]


def arc_contains(window, bearing: float) -> bool:
    start, end = window
    width = (end - start) % 360.0 or 360.0
    return (bearing - start) % 360.0 <= width


def circular_mid(start: float, end: float) -> float:
    width = (end - start) % 360.0 or 360.0
    return (start + width / 2) % 360.0


# --- Per-spot computation -------------------------------------------------


def compute_spot(coastline: Coastline, lat: float, lon: float) -> dict:
    """Facing and open water for one spot. Returns a dict with "status"
    ("beach", "offshore", "inland", "no_coast" or "no_window") and, when
    resolved, "facing", "arcs" (open_arcs), "snap_m", "spread", "flags"."""
    land_ll, coast_ll = coastline.around(lat, lon, RAY_LENGTH_M + SNAP_MAX_M)
    project = aeqd(lat, lon)
    land = shapely.transform(land_ll, project)
    coast = shapely.transform(coast_ll, project)
    # Projection can pinch near-touching rings into self-intersections.
    invalid = ~shapely.is_valid(land)
    if invalid.any():
        land[invalid] = shapely.make_valid(land[invalid])
    land_tree = STRtree(land)
    coast_tree = STRtree(coast)
    spot = shapely.points(0.0, 0.0)
    step = RAY_STEP_DEG
    bearings = np.arange(0.0, 360.0, step)

    def in_land(point) -> bool:
        return len(land_tree.query(point, predicate="intersects")) > 0

    near = coast_tree.query(spot, predicate="dwithin", distance=SNAP_MAX_M)
    if len(near) == 0:
        if in_land(spot):
            return {"status": "inland"}
        # Offshore reef or bank: the window is all there is.
        distances = free_distances(land_tree, land, np.zeros(2), bearings)
        arcs = open_arcs(bearings, distances, step)
        if not arcs:
            return {"status": "no_window"}
        return {
            "status": "offshore",
            "facing": circular_mid(*arcs[0]),
            "arcs": arcs,
            "snap_m": None,
            "spread": 0.0,
            "flags": ["offshore"],
        }

    candidates = coast[near]
    nearest = candidates[int(np.argmin(shapely.distance(spot, candidates)))]
    _, shore = shapely.shortest_line(spot, nearest).coords
    shore = np.array(shore)
    snap_m = float(np.hypot(*shore))
    shore_point = shapely.points(shore)

    axis = shoreline_axis(coast_tree, coast, shore_point, TANGENT_RADIUS_M)
    if axis is None:
        return {"status": "no_coast"}
    normal = np.array((axis[1], -axis[0]))

    # Pick the side of the shoreline that is the sea: the side whose
    # offshore point is in water, preferring the one more open to the
    # directions it faces (a sandspit has water on both sides).
    best = None
    for sign in (1.0, -1.0):
        n = sign * normal
        for offset in OFFSHORE_STEPS_M:
            origin = shore + offset * n
            if not in_land(shapely.points(origin)):
                break
        else:
            continue
        distances = free_distances(land_tree, land, origin, bearings)
        facing = bearing_of(n)
        ahead = np.array([ang_diff(b, facing) <= 60.0 for b in bearings])
        openness = float(distances[ahead].mean())
        if best is None or openness > best[0]:
            best = (openness, facing, distances)
    if best is None:
        return {"status": "no_coast"}
    _, facing, distances = best

    arcs = open_arcs(bearings, distances, step)
    if not arcs:
        return {"status": "no_window"}

    flags = []
    spread = 0.0
    for radius in CHECK_RADII_M:
        other = shoreline_axis(coast_tree, coast, shore_point, radius)
        if other is None:
            continue
        other_normal = np.array((other[1], -other[0]))
        other_facing = bearing_of(other_normal)
        # The axis has no sign; compare against whichever way matches.
        diff = min(ang_diff(other_facing, facing), ang_diff(other_facing + 180.0, facing))
        spread = max(spread, diff)
    if spread > SPREAD_FLAG_DEG:
        flags.append("scale_disagreement")
    if not any(arc_contains(arc, facing) for arc in arcs):
        flags.append("facing_outside_window")
    total = sum((end - start) % 360.0 or 360.0 for start, end in arcs)
    if total < 30.0:
        flags.append("narrow_window")
    if total >= 330.0:
        flags.append("open_all_around")
    return {
        "status": "beach",
        "facing": facing,
        "arcs": arcs,
        "snap_m": snap_m,
        "spread": spread,
        "flags": flags,
    }


# --- Batch run ------------------------------------------------------------

_WORKER_COASTLINE: Coastline | None = None


def _compute_chunk(chunk: list[tuple[int, float, float]]) -> list[tuple[int, dict]]:
    out = []
    for i, lat, lon in chunk:
        try:
            out.append((i, compute_spot(_WORKER_COASTLINE, lat, lon)))
        except Exception as exc:  # one bad spot must not lose the run
            out.append((i, {"status": "error", "error": str(exc)}))
    return out


def compute_all(coastline: Coastline, points: list[tuple[int, float, float]],
                workers: int, progress=None) -> dict[int, dict]:
    """compute_spot over (index, lat, lon) points, in spatial order.

    Workers are forked after the coastline is loaded, so they share it
    copy-on-write; each takes contiguous chunks of the spatially sorted
    points so its tile cache stays warm.
    """
    global _WORKER_COASTLINE
    _WORKER_COASTLINE = coastline
    chunk_size = 50
    chunks = [points[k:k + chunk_size] for k in range(0, len(points), chunk_size)]
    results: dict[int, dict] = {}
    if workers <= 1:
        batches = map(_compute_chunk, chunks)
        pool = None
    else:
        pool = multiprocessing.get_context("fork").Pool(workers)
        batches = pool.imap(_compute_chunk, chunks)
    try:
        for batch in batches:
            results.update(batch)
            if progress:
                progress(len(results), len(points))
    finally:
        if pool is not None:
            pool.close()
            pool.join()
    return results


# --- spots.json update ----------------------------------------------------


def is_curated(spot: dict) -> bool:
    """A hand-tuned swell_window wins over anything computed here."""
    return spot.get("swell_window") is not None


def apply_result(spot: dict, result: dict) -> None:
    """Write (or clear) a spot's computed facing and open water in place.
    Spots with a hand-tuned swell_window are left as they are."""
    if is_curated(spot):
        return
    spot.pop("facing", None)
    spot.pop("open_water", None)
    if not result.get("arcs"):
        return
    spot["facing"] = int(round(result["facing"])) % 360
    arcs = []
    for start, end in result["arcs"]:
        if (end - start) % 360.0 == 0.0:  # open all around
            arcs.append([0, 360])
        else:
            arcs.append([int(round(start)) % 360, int(round(end)) % 360])
    spot["open_water"] = arcs


def load_json(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as f:
        return json.load(f)


_NUMBER_ARRAY = re.compile(r"\[\s+(-?[\d.]+(?:,\s+-?[\d.]+)*)\s+\]")
_ARRAY_OF_NUMBER_ARRAYS = re.compile(r"\[\s+(\[[^\[\]]*\](?:,\s+\[[^\[\]]*\])*)\s+\]")


def format_spots(spots: list[dict]) -> str:
    """spots.json text: 2-space indented like the original file, but with
    number arrays (and arrays of them) on one line, e.g.
    "open_water": [[184, 332], [70, 76]], instead of a line per number."""
    text = json.dumps(spots, indent=2, ensure_ascii=False)
    text = _NUMBER_ARRAY.sub(lambda m: "[" + re.sub(r",\s+", ", ", m.group(1)) + "]", text)
    text = _ARRAY_OF_NUMBER_ARRAYS.sub(lambda m: "[" + re.sub(r"\],\s+\[", "], [", m.group(1)) + "]", text)
    return text + "\n"


def write_json(path: str, spots: list[dict]) -> None:
    with open(path, "w", encoding="utf-8") as f:
        f.write(format_spots(spots))


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--gshhg", required=True, help="path to gshhs_f.b")
    parser.add_argument("--report", help="write a per-spot CSV review report here")
    parser.add_argument("--only", nargs="*", help="limit to these spot ids (no files written)")
    parser.add_argument("--workers", type=int, default=max(1, min(8, (os.cpu_count() or 2) - 1)),
                        help="parallel worker processes")
    parser.add_argument("spots", nargs="+", help="spots.json files to update (same spot list)")
    args = parser.parse_args(argv)

    files = {path: load_json(path) for path in args.spots}
    reference = files[args.spots[0]]
    ids = [s["id"] for s in reference]
    for path, spots in files.items():
        if [s["id"] for s in spots] != ids:
            parser.error(f"{path} has a different spot list from {args.spots[0]}")

    started = time.monotonic()
    coastline = Coastline(read_gshhg(args.gshhg))
    print(f"Loaded coastline in {time.monotonic() - started:.0f}s", file=sys.stderr)

    # Neighboring spots share tiles; walk them in spatial order.
    order = sorted(range(len(reference)), key=lambda i: (
        math.floor(reference[i]["lat"]), math.floor(reference[i]["lon"])))
    if args.only:
        wanted = set(args.only)
        order = [i for i in order if reference[i]["id"] in wanted]

    last_report = [0]

    def progress(done: int, total: int) -> None:
        if done - last_report[0] >= 500 or done == total:
            last_report[0] = done
            print(f"{done}/{total} spots ({time.monotonic() - started:.0f}s)", file=sys.stderr)

    points = [(i, float(reference[i]["lat"]), float(reference[i]["lon"])) for i in order]
    results = compute_all(coastline, points, args.workers, progress)

    if args.report:
        with open(args.report, "w", newline="", encoding="utf-8") as f:
            writer = csv.writer(f)
            writer.writerow(["id", "name", "region", "lat", "lon", "status", "facing",
                             "open_water", "curated_window", "snap_m", "scale_spread", "flags"])
            for i in order:
                spot, result = reference[i], results[i]
                arcs = " ".join(f"{round(a)}-{round(b)}" for a, b in result.get("arcs", []))
                writer.writerow([
                    spot["id"], spot["name"], spot.get("region", ""), spot["lat"], spot["lon"],
                    result["status"],
                    None if result.get("facing") is None else round(result["facing"]),
                    arcs,
                    spot.get("swell_window") if is_curated(spot) else "",
                    None if result.get("snap_m") is None else round(result["snap_m"]),
                    None if result.get("spread") is None else round(result["spread"]),
                    " ".join(result.get("flags", [])) or result.get("error", ""),
                ])

    statuses: dict[str, int] = {}
    for result in results.values():
        statuses[result["status"]] = statuses.get(result["status"], 0) + 1
    print(f"Done in {time.monotonic() - started:.0f}s: {statuses}", file=sys.stderr)

    if args.only:
        for i in order:
            print(reference[i]["id"], results[i])
        return 0
    for path, spots in files.items():
        for i, result in results.items():
            apply_result(spots[i], result)
        write_json(path, spots)
        print(f"Updated {path}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
