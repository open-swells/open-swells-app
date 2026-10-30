"""One vocabulary for wave and wind fields across every data source.

Each canonical name matches a field in the pipeline's spot_forecasts.json.gz
(see pipeline/spot_forecasts.py) where one exists, so research output can be
compared with, and eventually dropped into, what the app already serves.

ERA5 names are the ARCO-ERA5 zarr variable names. GFS-Wave names are the
GRIB2 inventory entries ("<shortName>:<level>") in gfswave.*.grib2.idx.
"""

from dataclasses import dataclass


@dataclass(frozen=True)
class Variable:
    name: str
    units: str
    description: str
    era5: str | None
    gfswave: str | None
    # Directions are circular; models should see them as sin/cos pairs.
    circular: bool = False


VARIABLES: dict[str, Variable] = {
    v.name: v
    for v in [
        Variable("hs", "m", "combined wind-wave and swell significant height",
                 "significant_height_of_combined_wind_waves_and_swell", "HTSGW:surface"),
        Variable("tp", "s", "peak (primary) wave period",
                 "peak_wave_period", "PERPW:surface"),
        Variable("dp", "deg from", "primary wave direction",
                 "mean_wave_direction", "DIRPW:surface", circular=True),
        Variable("hw", "m", "wind sea significant height",
                 "significant_height_of_wind_waves", "WVHGT:surface"),
        Variable("pw", "s", "wind sea mean period",
                 "mean_period_of_wind_waves", "WVPER:surface"),
        Variable("dw", "deg from", "wind sea mean direction",
                 "mean_direction_of_wind_waves", "WVDIR:surface", circular=True),
        *[
            v
            for n, word in ((1, "first"), (2, "second"), (3, "third"))
            for v in (
                Variable(f"h{n}", "m", f"swell partition {n} significant height",
                         f"significant_wave_height_of_{word}_swell_partition",
                         f"SWELL:{n} in sequence"),
                Variable(f"p{n}", "s", f"swell partition {n} period",
                         f"mean_wave_period_of_{word}_swell_partition",
                         f"SWPER:{n} in sequence"),
                Variable(f"d{n}", "deg from", f"swell partition {n} direction",
                         f"mean_wave_direction_of_{word}_swell_partition",
                         f"SWDIR:{n} in sequence", circular=True),
            )
        ],
        Variable("u10", "m/s", "10 m eastward wind",
                 "10m_u_component_of_wind", "UGRD:surface"),
        Variable("v10", "m/s", "10 m northward wind",
                 "10m_v_component_of_wind", "VGRD:surface"),
        # Inputs only: forcing and static context that GFS-Wave doesn't publish.
        Variable("siconc", "1", "sea ice fraction", "sea_ice_cover", None),
        Variable("lsm", "1", "land-sea mask (static)", "land_sea_mask", None),
        Variable("depth", "m", "model bathymetry (static)", "model_bathymetry", None),
    ]
}

# The state a wave model predicts: the fields the app shows on spot pages.
WAVE_STATE = ("hs", "tp", "dp", "hw", "pw", "dw",
              "h1", "p1", "d1", "h2", "p2", "d2", "h3", "p3", "d3")
WIND_FORCING = ("u10", "v10")
STATIC = ("lsm", "depth")


def era5_names(names) -> dict[str, str]:
    """{ERA5 zarr name: canonical name} for the given canonical names."""
    out = {}
    for name in names:
        var = VARIABLES[name]
        if var.era5 is None:
            raise KeyError(f"{name} has no ERA5 equivalent")
        out[var.era5] = name
    return out


def gfswave_names(names) -> dict[str, str]:
    """{GFS-Wave idx entry: canonical name} for the given canonical names."""
    out = {}
    for name in names:
        var = VARIABLES[name]
        if var.gfswave is None:
            raise KeyError(f"{name} is not in GFS-Wave output")
        out[var.gfswave] = name
    return out
