"""NDBC buoy observations: the ground truth for verifying forecasts.

Historical standard meteorological files (stdmet) give hourly (older) or
10-minute (newer) WVHT, DPD, APD, MWD and wind for each moored buoy-year.
Spectral density files (swden) hold the 1-D spectrum, from which swell
partitions comparable to the model's can be derived later.
"""

import gzip
import io
import xml.etree.ElementTree as ET

import numpy as np
import pandas as pd
import requests

BASE = "https://www.ndbc.noaa.gov"
STATIONS_URL = f"{BASE}/activestations.xml"
MISSING = {"WVHT": 99.0, "DPD": 99.0, "APD": 99.0, "MWD": 999.0,
           "WDIR": 999.0, "WSPD": 99.0, "GST": 99.0}


def stdmet_url(station: str, year: int) -> str:
    return f"{BASE}/data/historical/stdmet/{station.lower()}h{year}.txt.gz"


def swden_url(station: str, year: int) -> str:
    return f"{BASE}/data/historical/swden/{station.lower()}w{year}.txt.gz"


def parse_stdmet(text: str) -> pd.DataFrame:
    """Parse a stdmet file into a UTC-indexed frame with NaN for missing.

    Files since 2007 have a two-line header (names, then units) and a
    minute column; this is the format the archive serves for those years.
    """
    lines = text.splitlines()
    names = lines[0].lstrip("#").split()
    body = "\n".join(line for line in lines[1:] if not line.startswith("#"))
    df = pd.read_csv(io.StringIO(body), sep=r"\s+", names=names, header=None)
    time = pd.to_datetime(
        {"year": df["YY"], "month": df["MM"], "day": df["DD"], "hour": df["hh"],
             "minute": df.get("mm", 0)},
        utc=True,
    )
    df = df.drop(columns=[c for c in ("YY", "MM", "DD", "hh", "mm") if c in df])
    df.index = pd.DatetimeIndex(time, name="time")
    for col, sentinel in MISSING.items():
        if col in df:
            df[col] = df[col].where(df[col] < sentinel, np.nan)
    return df


def fetch_stdmet(station: str, year: int, session: requests.Session | None = None):
    """One buoy-year of stdmet, or None if NDBC has no file for it."""
    http = session or requests.Session()
    resp = http.get(stdmet_url(station, year), timeout=60)
    if resp.status_code == 404:
        return None
    resp.raise_for_status()
    return parse_stdmet(gzip.decompress(resp.content).decode())


def parse_stations(xml_text: str) -> pd.DataFrame:
    """Station catalog from activestations.xml.

    The catalog doesn't say which stations measure waves; only "met" (has
    meteorological sensors) narrows it. Whether a buoy reports WVHT is
    known only from its stdmet data.
    """
    rows = []
    for st in ET.fromstring(xml_text).iter("station"):
        a = st.attrib
        rows.append({
            "id": a["id"].lower(), "name": a.get("name", ""),
            "lat": float(a["lat"]), "lon": float(a["lon"]),
            "type": a.get("type", ""), "owner": a.get("owner", ""),
            "met": a.get("met") == "y",
        })
    return pd.DataFrame(rows).set_index("id")


def fetch_stations(session: requests.Session | None = None) -> pd.DataFrame:
    http = session or requests.Session()
    resp = http.get(STATIONS_URL, timeout=60)
    resp.raise_for_status()
    return parse_stations(resp.text)
