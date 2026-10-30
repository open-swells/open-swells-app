"""openswells-research: check data sources and build local datasets.

    uv run openswells-research inventory
    uv run openswells-research fetch-era5 --start 2020-01 --end 2020-12 --resolution 1.0
    uv run openswells-research fetch-ndbc --years 2021-2025
    uv run openswells-research fetch-gfswave --cycle 2025-01-01T00 --hours 0-120:6

Everything lands under $OPENSWELLS_DATA (default research/data/), which is
git-ignored. Fetches skip files that already exist, so they can be resumed.
"""

import argparse
import os
import sys
from datetime import datetime
from pathlib import Path

DATA_DIR = Path(os.environ.get("OPENSWELLS_DATA", Path(__file__).resolve().parents[2] / "data"))


def _years(spec: str) -> list[int]:
    a, _, b = spec.partition("-")
    return list(range(int(a), int(b or a) + 1))


def _hours(spec: str) -> list[int]:
    """'0-120:6' -> 0, 6, ..., 120; '0,3,6' -> 0, 3, 6."""
    if "," in spec:
        return [int(h) for h in spec.split(",")]
    rng, _, step = spec.partition(":")
    a, _, b = rng.partition("-")
    return list(range(int(a), int(b or a) + 1, int(step or 1)))


def cmd_inventory(args) -> int:
    """Confirm every source is reachable and report what it covers."""
    import requests

    from .sources import era5, gfswave, ndbc
    from .spots import load_spots, nearest
    from .variables import VARIABLES

    ok = True
    spots = load_spots()
    print(f"spots: {len(spots)} from pipeline/spots.json")

    ds = era5.open_arco()
    first, last = era5.valid_range(ds)
    missing = [v.era5 for v in VARIABLES.values() if v.era5 and v.era5 not in ds]
    print(f"ERA5 (ARCO): final data {first} .. {last}, "
          f"ERA5T to {ds.attrs.get('valid_time_stop_era5t')}")
    if missing:
        ok = False
        print(f"  MISSING variables: {missing}")
    else:
        print(f"  all {sum(1 for v in VARIABLES.values() if v.era5)} catalog variables present")

    cycle = datetime(args.gfs_cycle.year, args.gfs_cycle.month, args.gfs_cycle.day)
    url = gfswave.grib_url(cycle, 24)
    r = requests.get(url + ".idx", timeout=30)
    if r.ok:
        keys = {e.key for e in gfswave.parse_idx(r.text)}
        absent = [v.gfswave for v in VARIABLES.values() if v.gfswave and v.gfswave not in keys]
        print(f"GFS-Wave: {cycle:%Y-%m-%d %HZ} f024 found; archive starts "
              f"{gfswave.FIRST_CYCLE:%Y-%m-%d}" + (f"; MISSING {absent}" if absent else ""))
        ok &= not absent
    else:
        ok = False
        print(f"GFS-Wave: {url}.idx -> HTTP {r.status_code}")

    stations = ndbc.fetch_stations()
    buoys = stations[(stations["type"] == "buoy") & stations["met"]]
    near = nearest(spots, buoys, max_km=args.buoy_km)
    print(f"NDBC: {len(buoys)} active met buoys; {near['target'].nunique()} lie within "
          f"{args.buoy_km:.0f} km of {len(near)} spots ({len(near) / len(spots):.0%})")
    return 0 if ok else 1


def cmd_fetch_era5(args) -> int:
    """Write one zarr per month so a long pull can be resumed and parallelized."""
    import pandas as pd

    from .sources import era5
    from .variables import STATIC, WAVE_STATE, WIND_FORCING

    names = args.vars.split(",") if args.vars else [*WAVE_STATE, *WIND_FORCING, "siconc"]
    out_dir = DATA_DIR / "era5" / f"{args.resolution:g}deg_{args.step}h"
    out_dir.mkdir(parents=True, exist_ok=True)
    ds = era5.open_arco(chunks={})

    static_path = out_dir / "static.zarr"
    if not static_path.exists():
        static = era5.select(ds, STATIC, "2020-01-01T00", "2020-01-01T00", 1, args.resolution)
        static.isel(time=0, drop=True).to_zarr(static_path, mode="w")
        print(f"wrote {static_path}")

    for month in pd.period_range(args.start, args.end, freq="M"):
        path = out_dir / f"{month}.zarr"
        if path.exists():
            continue
        start, end = month.start_time, month.end_time
        sub = era5.select(ds, names, f"{start:%Y-%m-%dT%H}", f"{end:%Y-%m-%dT%H}",
                          args.step, args.resolution)
        sub = sub.chunk({"time": 1, "latitude": -1, "longitude": -1})
        tmp = path.with_suffix(".partial")
        sub.to_zarr(tmp, mode="w")
        tmp.rename(path)
        print(f"wrote {path} ({sub.sizes['time']} steps, {len(names)} vars)")
    return 0


def cmd_fetch_ndbc(args) -> int:
    import requests

    from .sources import ndbc
    from .spots import load_spots, nearest

    session = requests.Session()
    if args.stations:
        ids = args.stations.split(",")
    else:
        stations = ndbc.fetch_stations(session)
        buoys = stations[(stations["type"] == "buoy") & stations["met"]]
        ids = sorted(nearest(load_spots(), buoys, max_km=args.buoy_km)["target"].unique())
    out_dir = DATA_DIR / "ndbc" / "stdmet"
    out_dir.mkdir(parents=True, exist_ok=True)
    for station in ids:
        for year in _years(args.years):
            path = out_dir / f"{station}_{year}.parquet"
            if path.exists():
                continue
            df = ndbc.fetch_stdmet(station, year, session)
            if df is None or df["WVHT"].notna().sum() == 0:
                continue  # no file, or a met-only buoy with no wave sensor
            df.to_parquet(path)
            print(f"wrote {path} ({df['WVHT'].notna().sum()} wave obs)")
    return 0


def cmd_fetch_gfswave(args) -> int:
    import requests

    from .sources import gfswave
    from .variables import WAVE_STATE, WIND_FORCING, gfswave_names

    keys = list(gfswave_names([*WAVE_STATE, *WIND_FORCING]))
    cycle = args.cycle
    if cycle < gfswave.FIRST_CYCLE:
        print(f"GFS-Wave archive starts {gfswave.FIRST_CYCLE:%Y-%m-%d}", file=sys.stderr)
        return 1
    out_dir = DATA_DIR / "gfswave" / f"{cycle:%Y%m%d%H}"
    out_dir.mkdir(parents=True, exist_ok=True)
    session = requests.Session()
    for hour in _hours(args.hours):
        path = out_dir / f"{args.grid}.f{hour:03d}.grib2"
        if path.exists():
            continue
        blob = gfswave.fetch_fields(gfswave.grib_url(cycle, hour, args.grid), keys, session)
        path.write_bytes(blob)
        print(f"wrote {path} ({len(blob) / 1e6:.1f} MB)")
    return 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(prog="openswells-research", description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("inventory", help="check every data source")
    s.add_argument("--gfs-cycle", type=datetime.fromisoformat, default=datetime(2026, 1, 1))
    s.add_argument("--buoy-km", type=float, default=100.0)
    s.set_defaults(func=cmd_inventory)

    s = sub.add_parser("fetch-era5", help="ERA5 wave state + forcing, monthly zarrs")
    s.add_argument("--start", required=True, help="first month, e.g. 2020-01")
    s.add_argument("--end", required=True, help="last month, inclusive")
    s.add_argument("--resolution", type=float, default=1.0, help="degrees (multiple of 0.25)")
    s.add_argument("--step", type=int, default=6, help="hours between steps")
    s.add_argument("--vars", help="comma-separated canonical names (default: wave state, wind, ice)")
    s.set_defaults(func=cmd_fetch_era5)

    s = sub.add_parser("fetch-ndbc", help="NDBC stdmet for buoys near spots")
    s.add_argument("--years", default="2021-2025", help="e.g. 2021-2025 or 2024")
    s.add_argument("--stations", help="comma-separated ids (default: buoys near spots)")
    s.add_argument("--buoy-km", type=float, default=100.0)
    s.set_defaults(func=cmd_fetch_ndbc)

    s = sub.add_parser("fetch-gfswave", help="one archived GFS-Wave cycle (subset fields)")
    s.add_argument("--cycle", type=datetime.fromisoformat, required=True)
    s.add_argument("--hours", default="0-120:6")
    s.add_argument("--grid", default="global.0p25", choices=["global.0p25", "global.0p16"])
    s.set_defaults(func=cmd_fetch_gfswave)

    args = p.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
