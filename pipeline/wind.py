"""Extract and publish wind fields carried inside GFS-Wave GRIB files."""

import gzip
import logging

import geojson
import numpy as np
import pygrib
from geojson import Feature, FeatureCollection

logger = logging.getLogger("GFSWaveContours")


def extract_wind(filepath: str) -> dict:
    """Return the surface wind grid from a GFS-Wave GRIB2 file."""
    grbs = pygrib.open(filepath)
    try:
        try:
            speed_msg = grbs.select(name="Wind speed")[0]
            direction_msg = grbs.select(name="Wind direction")[0]
            u_msg = grbs.select(name="U component of wind")[0]
            v_msg = grbs.select(name="V component of wind")[0]
        except (IndexError, ValueError) as exc:
            raise RuntimeError(f"Missing wind fields in {filepath}") from exc

        messages = (speed_msg, direction_msg, u_msg, v_msg)
        if len({message.validDate for message in messages}) != 1:
            raise ValueError("Mismatched valid times between wind fields")

        lats, lons = speed_msg.latlons()
        return {
            "lon": np.asarray(lons, dtype=np.float32),
            "lat": np.asarray(lats, dtype=np.float32),
            "speed": np.ma.filled(speed_msg.values, np.nan).astype(np.float32),
            "direction": np.ma.filled(direction_msg.values, np.nan).astype(np.float32),
            "u": np.ma.filled(u_msg.values, np.nan).astype(np.float32),
            "v": np.ma.filled(v_msg.values, np.nan).astype(np.float32),
            "mask": np.ma.getmaskarray(speed_msg.values),
            "valid_date": speed_msg.validDate,
        }
    finally:
        grbs.close()


def write_wind_arrows(data: dict, path: str, *, stride: int = 10) -> int:
    """Write coarse wind vectors as GeoJSON points.

    Compact properties are: ``s`` speed in m/s, ``d`` direction wind comes
    from in degrees true, and ``u``/``v`` vector components in m/s.
    """
    slices = np.s_[::stride, ::stride]
    lon = data["lon"][slices]
    lat = data["lat"][slices]
    speed = data["speed"][slices]
    direction = data["direction"][slices]
    u = data["u"][slices]
    v = data["v"][slices]
    valid = np.isfinite(speed) & np.isfinite(direction) & np.isfinite(u) & np.isfinite(v)
    mask = data.get("mask")
    if mask is not None:
        valid &= ~mask[slices]

    features = []
    for lo, la, speed_value, direction_value, u_value, v_value in zip(
        lon[valid], lat[valid], speed[valid], direction[valid], u[valid], v[valid]
    ):
        features.append(
            Feature(
                geometry={
                    "type": "Point",
                    "coordinates": [round(float(lo), 2), round(float(la), 2)],
                },
                properties={
                    "s": round(float(speed_value), 1),
                    "d": int(round(float(direction_value))) % 360,
                    "u": round(float(u_value), 1),
                    "v": round(float(v_value), 1),
                },
            )
        )

    payload = geojson.dumps(FeatureCollection(features))
    with open(path, "w") as output:
        output.write(payload)
    with gzip.open(path + ".gz", "wt", encoding="utf-8", compresslevel=6) as output:
        output.write(payload)
    logger.info("Wind arrows saved to %s (%d points)", path, len(features))
    return len(features)
