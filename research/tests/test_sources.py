from datetime import datetime

import numpy as np
import pandas as pd
import pytest
import xarray as xr

from openswells_research.cli import _hours, _years
from openswells_research.sources import era5, gfswave, ndbc
from openswells_research.spots import haversine_km, load_spots, nearest
from openswells_research.variables import (
    VARIABLES,
    WAVE_STATE,
    era5_names,
    gfswave_names,
)


def test_variable_names_are_unique_per_source():
    era5 = [v.era5 for v in VARIABLES.values() if v.era5]
    gfs = [v.gfswave for v in VARIABLES.values() if v.gfswave]
    assert len(era5) == len(set(era5))
    assert len(gfs) == len(set(gfs))


def test_wave_state_is_in_both_sources():
    assert len(era5_names(WAVE_STATE)) == len(WAVE_STATE)
    assert len(gfswave_names(WAVE_STATE)) == len(WAVE_STATE)
    with pytest.raises(KeyError):
        gfswave_names(["depth"])


def test_grib_url():
    url = gfswave.grib_url(datetime(2025, 1, 2, 6), 24)
    assert url.endswith("/gfs.20250102/06/wave/gridded/gfswave.t06z.global.0p25.f024.grib2")
    with pytest.raises(ValueError):
        gfswave.grib_url(datetime(2025, 1, 2, 3), 0)


IDX = """\
1:0:d=2026010100:WIND:surface:24 hour fcst:
2:704079:d=2026010100:WDIR:surface:24 hour fcst:
3:1712855:d=2026010100:SWELL:1 in sequence:24 hour fcst:
"""


def test_parse_idx_byte_ranges():
    entries = gfswave.parse_idx(IDX)
    assert [e.key for e in entries] == ["WIND:surface", "WDIR:surface", "SWELL:1 in sequence"]
    assert (entries[0].start, entries[0].end) == (0, 704079)
    assert entries[-1].end is None


STDMET = """\
#YY  MM DD hh mm WDIR WSPD GST  WVHT   DPD   APD MWD   PRES  ATMP  WTMP  DEWP  VIS  TIDE
#yr  mo dy hr mn degT m/s  m/s     m   sec   sec degT   hPa  degC  degC  degC   mi    ft
2024 01 01 00 00 145  1.7  3.7 99.00 99.00 99.00 999 1017.4  15.6  17.2  10.5 99.0 99.00
2024 01 01 00 10 174  1.5  2.7  3.03 13.79 10.88 272 1017.5  15.6  17.2  10.5 99.0 99.00
"""


def test_parse_stdmet_masks_missing():
    df = ndbc.parse_stdmet(STDMET)
    assert df.index[1] == pd.Timestamp("2024-01-01 00:10", tz="UTC")
    assert np.isnan(df["WVHT"].iloc[0]) and np.isnan(df["MWD"].iloc[0])
    assert df["WVHT"].iloc[1] == pytest.approx(3.03)
    assert df["MWD"].iloc[1] == 272


def test_parse_stations():
    xml = ('<stations><station id="46086" lat="32.5" lon="-118" name="San Clemente" '
           'owner="NDBC" type="buoy" met="y"/></stations>')
    df = ndbc.parse_stations(xml)
    assert df.loc["46086", "met"] and df.loc["46086", "type"] == "buoy"


def test_coarsen_averages_directions_as_vectors():
    lat = np.linspace(90, -90, 721)
    lon = np.arange(0, 360, 0.25)
    d = np.full((721, 1440), 350.0)
    d[:, 1::2] = 10.0  # alternating 350/10 must average to 0, not 180
    h = np.ones((721, 1440))
    ds = xr.Dataset({"d1": (("latitude", "longitude"), d), "hs": (("latitude", "longitude"), h)},
                    coords={"latitude": lat, "longitude": lon})
    out = era5.coarsen(ds, 4)
    assert out.sizes == {"latitude": 180, "longitude": 360}
    dirs = out["d1"].values
    assert np.allclose(np.minimum(dirs, 360 - dirs), 0, atol=1e-6)
    assert np.allclose(out["hs"], 1)


def test_spots_and_nearest():
    spots = load_spots()
    assert len(spots) > 1000 and spots["lat"].between(-90, 90).all()
    pts = pd.DataFrame({"lat": [32.5, 0.0], "lon": [-118.0, 0.0]}, index=["a", "b"])
    targets = pd.DataFrame({"lat": [32.6], "lon": [-118.0]}, index=["buoy"])
    near = nearest(pts, targets, max_km=50)
    assert list(near.index) == ["a"]
    assert near.loc["a", "km"] == pytest.approx(haversine_km(32.5, -118, 32.6, -118))


def test_cli_ranges():
    assert _hours("0-12:6") == [0, 6, 12]
    assert _hours("0,3") == [0, 3]
    assert _years("2021-2023") == [2021, 2022, 2023]
    assert _years("2024") == [2024]
