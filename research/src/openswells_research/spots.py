"""Surf spots and the buoys that can verify forecasts near them."""

import json
from pathlib import Path

import numpy as np
import pandas as pd

REPO_ROOT = Path(__file__).resolve().parents[3]
SPOTS_PATH = REPO_ROOT / "pipeline" / "spots.json"
EARTH_RADIUS_KM = 6371.0


def load_spots(path: Path = SPOTS_PATH) -> pd.DataFrame:
    """The pipeline's spots.json, indexed by id, at each spot's sample point.

    sample_lat/sample_lon override the beach coordinates, matching
    pipeline/spot_forecasts.py and Spot.samplePoint() in the app.
    """
    spots = json.loads(Path(path).read_text())
    rows = []
    for s in spots:
        has_sample = s.get("sample_lat") is not None and s.get("sample_lon") is not None
        rows.append({
            "id": s["id"], "name": s["name"], "region": s.get("region", ""),
            "lat": s["sample_lat"] if has_sample else s["lat"],
            "lon": s["sample_lon"] if has_sample else s["lon"],
            "facing": s.get("facing"),
        })
    return pd.DataFrame(rows).set_index("id")


def haversine_km(lat1, lon1, lat2, lon2):
    lat1, lon1, lat2, lon2 = map(np.radians, (lat1, lon1, lat2, lon2))
    a = (np.sin((lat2 - lat1) / 2) ** 2
         + np.cos(lat1) * np.cos(lat2) * np.sin((lon2 - lon1) / 2) ** 2)
    return 2 * EARTH_RADIUS_KM * np.arcsin(np.sqrt(a))


def nearest(points: pd.DataFrame, targets: pd.DataFrame, max_km: float) -> pd.DataFrame:
    """For each row of points, its nearest target within max_km.

    Returns points' index with columns target and km; points with nothing
    in range are left out. Brute force is fine for thousands x thousands.
    """
    d = haversine_km(points["lat"].to_numpy()[:, None], points["lon"].to_numpy()[:, None],
                     targets["lat"].to_numpy()[None, :], targets["lon"].to_numpy()[None, :])
    j = d.argmin(axis=1)
    km = d[np.arange(len(points)), j]
    out = pd.DataFrame({"target": targets.index.to_numpy()[j], "km": km}, index=points.index)
    return out[out["km"] <= max_km]
