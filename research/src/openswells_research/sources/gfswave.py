"""Archived NOAA GFS-Wave forecasts from the NODD AWS bucket.

This is the same product the production pipeline renders, so it is the
baseline any model of ours has to beat. The bucket keeps every 00/06/12/18Z
cycle since GFSv16 started publishing waves there on 2021-03-23.

Each grib2 file has a .idx sidecar listing byte offsets per field, so a few
fields can be fetched with HTTP range requests instead of the whole ~12 MB
(0.25 deg) or larger 0.16 deg file.
"""

from dataclasses import dataclass
from datetime import datetime

import requests
import xarray as xr

BUCKET_URL = "https://noaa-gfs-bdp-pds.s3.amazonaws.com"
FIRST_CYCLE = datetime(2021, 3, 23, 0)
GRIDS = ("global.0p25", "global.0p16")


def grib_url(cycle: datetime, hour: int, grid: str = "global.0p25") -> str:
    if grid not in GRIDS:
        raise ValueError(f"unknown grid {grid!r}; expected one of {GRIDS}")
    if cycle.hour % 6 or cycle.minute or cycle.second:
        raise ValueError(f"{cycle} is not a 00/06/12/18Z cycle")
    return (
        f"{BUCKET_URL}/gfs.{cycle:%Y%m%d}/{cycle:%H}/wave/gridded/"
        f"gfswave.t{cycle:%H}z.{grid}.f{hour:03d}.grib2"
    )


@dataclass(frozen=True)
class IdxEntry:
    key: str  # "<shortName>:<level>", e.g. "SWELL:1 in sequence"
    start: int
    end: int | None  # exclusive; None for the last field in the file


def parse_idx(text: str) -> list[IdxEntry]:
    """Parse a wgrib2-style .idx inventory into byte ranges per field."""
    rows = []
    for line in text.splitlines():
        if not line.strip():
            continue
        parts = line.split(":")
        rows.append((int(parts[1]), f"{parts[3]}:{parts[4]}"))
    entries = []
    for i, (start, key) in enumerate(rows):
        end = rows[i + 1][0] if i + 1 < len(rows) else None
        entries.append(IdxEntry(key, start, end))
    return entries


def fetch_fields(url: str, keys, session: requests.Session | None = None) -> bytes:
    """Download just the requested fields of a grib2 file as one grib2 blob.

    GRIB2 messages are self-contained, so concatenating them yields a valid
    file that cfgrib/pygrib can read.
    """
    http = session or requests.Session()
    resp = http.get(url + ".idx", timeout=30)
    resp.raise_for_status()
    entries = {e.key: e for e in parse_idx(resp.text)}
    missing = [k for k in keys if k not in entries]
    if missing:
        raise KeyError(f"{url} has no {missing}")
    chunks = []
    for key in keys:
        e = entries[key]
        rng = f"bytes={e.start}-" + ("" if e.end is None else str(e.end - 1))
        r = http.get(url, headers={"Range": rng}, timeout=60)
        r.raise_for_status()
        chunks.append(r.content)
    return b"".join(chunks)


# eccodes shortName -> canonical name. Swell partitions decode as one 3-D
# variable over orderedSequenceData (1..3) and are split out below.
_ECCODES = {"swh": "hs", "perpw": "tp", "dirpw": "dp", "shww": "hw",
            "mpww": "pw", "wvdir": "dw", "u": "u10", "v": "v10"}
_PARTITIONS = {"shts": "h", "mpts": "p", "swdir": "d"}


def decode(path) -> xr.Dataset:
    """Read a GFS-Wave grib2 file into canonical names (see variables.py),
    with longitudes 0..360 and latitudes north to south like ERA5."""
    import cfgrib

    out = {}
    for ds in cfgrib.open_datasets(str(path), backend_kwargs={"indexpath": ""}):
        for name, da in ds.data_vars.items():
            if name in _ECCODES:
                out[_ECCODES[name]] = da
            elif name in _PARTITIONS:
                for n in da["orderedSequenceData"].values:
                    out[f"{_PARTITIONS[name]}{int(n)}"] = da.sel(orderedSequenceData=n, drop=True)
    drop = [c for c in ("surface", "step", "orderedSequenceData") if c in xr.Dataset(out).coords]
    return xr.Dataset(out).drop_vars(drop)
