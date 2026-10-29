"""Extract and publish wind fields carried inside GFS-Wave GRIB files."""

import logging

import numpy as np
import pygrib

from bundle import pack_grid

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


def pack_wind(data: dict, *, stride: int = 10) -> dict[str, bytes]:
    """Pack coarse wind vectors for the wind bundle.

    Vars are ``s`` speed in m/s, ``d`` direction wind comes from in degrees
    true, and ``u``/``v`` vector components in m/s. A cell is stored only
    where all four are valid.
    """
    slices = np.s_[::stride, ::stride]
    speed = data["speed"][slices]
    direction = data["direction"][slices]
    u = data["u"][slices]
    v = data["v"][slices]
    valid = np.isfinite(speed) & np.isfinite(direction) & np.isfinite(u) & np.isfinite(v)
    mask = data.get("mask")
    if mask is not None:
        valid &= ~mask[slices]
    return {
        "s": pack_grid(speed, 0.1, valid),
        "d": pack_grid(np.round(direction) % 360, 1, valid),
        "u": pack_grid(u, 0.1, valid),
        "v": pack_grid(v, 0.1, valid),
    }
