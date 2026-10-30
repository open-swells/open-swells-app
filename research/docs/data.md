# Data requirements

What each line of research needs, where it comes from, how big it is, and
what we've learned by pulling it. All numbers marked *measured* come from
`openswells-research` runs on 2026-10-28; the rest are estimates.

## Sources

| Source | Role | Coverage | Access | Tool |
| --- | --- | --- | --- | --- |
| **ERA5** (ARCO zarr) | Training targets (wave state) and forcing (10 m wind, sea ice) | 1940 – 2026-06 final, ERA5T to ~9 days ago; hourly, 0.25° | `gs://gcp-public-data-arco-era5`, anonymous | `fetch-era5` |
| **GFS-Wave** (NODD) | Baseline to beat; same product the pipeline serves | 2021-03-23 on, 4 cycles/day, f000–f384 | `s3://noaa-gfs-bdp-pds`, anonymous, byte-range via `.idx` | `fetch-gfswave` |
| **NDBC** buoys | Point verification, bias correction | Decades per buoy; 10-min since ~2010s | ndbc.noaa.gov HTTP | `fetch-ndbc` |
| GraphCast params/stats | Pretrained atmosphere model | ERA5 1979–2017 (0.25°, 37 lvl), HRES 1979–2021 (operational), 1° small | `gs://dm_graphcast`, anonymous | `envs/graphcast` |
| FourCastNet / SFNO / FCN3 | Pretrained atmosphere models | per NGC model card | NGC via Earth2Studio | `envs/earth2` |
| GFS atmospheric analysis | Initial conditions to run GraphCast/FCN in real time | rolling | NODD, Earth2Studio `GFS` source | `envs/earth2` |
| *Not yet wired up* | | | | |
| Satellite altimetry (CMEMS L3 SWH, or ESA Sea State CCI) | Global Hs verification where there are no buoys | 1991 on | Free Copernicus Marine account | — |
| CDIP buoys | Directional spectra for checking swell partitions; dense along CA | per station | THREDDS/OPeNDAP, anonymous | — |
| NDBC `swden` | 1-D spectra at NDBC buoys | per station | ndbc.noaa.gov | — (URL helper only) |
| ECMWF open data (IFS/WAM waves) | Second operational baseline | rolling, recent days only | `ecmwf-opendata` | — |
| NWPS (SWAN) | Nearshore target for downscaling | rolling | already fetched by `pipeline/nwps.py` | — |
| GEBCO bathymetry | Nearshore context finer than ERA5's | static | download | — |

## Variables

`src/openswells_research/variables.py` gives every field one canonical
name, chosen to match `pipeline/spot_forecasts.py` so a new model can emit
the payload the app already reads:

- **Wave state** (model output): `hs`, `tp`, `dp`, wind sea `hw`/`pw`/`dw`,
  swell partitions `h1..3`, `p1..3`, `d1..3`.
- **Forcing**: `u10`, `v10`, `siconc`.
- **Static**: `lsm`, `depth`.

Directions are circular. They are averaged as unit vectors when
coarsening, and models should see them as sin/cos, ideally weighted by
height so that a calm partition's noisy direction doesn't dominate.

## ERA5 vs GFS-Wave: they aren't the same quantities (measured)

These were compared cell by cell on 2026-01-01 00Z, using the GFS-Wave
analysis (f000) and ERA5T on the same 0.25° lattice (562,567 wet cells):

| field | corr | bias GFS − ERA5 | notes |
| --- | --- | --- | --- |
| `hs` | 0.96 | −0.07 m | Directly comparable. |
| `u10`/`v10` | 0.98 | < 0.1 m/s | Directly comparable. |
| `tp` | 0.80 | +0.16 s | Comparable. |
| `hw` | 0.88 | +0.47 m | GFS has wind sea in only 59% of cells, ERA5 everywhere. |
| `pw` | 0.67 | +2.5 s | The two define "wind sea" differently. |
| `h1` | 0.73 | −0.31 m | Both order partitions by height (h1 ≥ h2 in 100% of cells). |
| `p1`, `p2`, `p3` | 0.31, 0.29, 0.14 | +0.5 to +1.6 s | Not the same period definition. |

What this means:

1. **Train on ERA5, but verify against observations, not GFS-Wave.**
   Combined `hs`, `tp` and winds carry over between the two. Partition
   periods and the wind-sea split do not, so a model trained to match
   ERA5 partitions will "disagree" with GFS-Wave even when both are right.
2. **Partitions make poor regression targets as-is.** Slots are ordered by
   height, so when two swells cross in size their labels swap and every
   field jumps. ERA5 also fills empty partitions with small values where
   GFS leaves NaN. Options are in [approaches.md](approaches.md).
3. ERA5's period and direction are less constrained by observations than
   its Hs (altimeters only measure Hs). That's another reason to verify
   with buoys, and with CDIP spectra for partitions.

## Volumes and fetch times

| dataset | size | time | notes |
| --- | --- | --- | --- |
| ERA5 1°, 6-hourly, 18 vars | **320 MB/month** (measured) → 3.8 GB/yr | **7 min/month** (measured) | Each fetched step reads a full 0.25° chunk, so cost is independent of output resolution. Months are independent; run several processes in parallel. |
| ERA5 1°, 2010–2025 | ~60 GB | ~22 h serial | Prototype training set. |
| ERA5 0.25°, same vars | ~16× → ~60 GB/yr | about the same as 1° | Only once the 1° model works. |
| GFS-Wave 0.25°, 17 fields | **10.5 MB per forecast hour** (measured) | ~1 s/file | 29 files per cycle at 6-hourly to f168 ≈ 305 MB/cycle. For verification, extract buoy/spot points and discard the grib; storing a year of grids is ~110 GB. |
| NDBC stdmet | ~0.8 MB per buoy-year (parquet, measured) | seconds | 129 active buoys within 100 km of spots → ~0.5 GB for 5 years. |

The machine this was set up on (8 cores, 15 GB RAM, no GPU) can fetch and
prepare all of this. It cannot train any of the models described in
approaches.md, and of the pretrained models it can realistically run only
GraphCast_small (1°) on CPU.

## Verification coverage (measured)

Only **18% of spots (1,052 of 5,878) have an active NDBC buoy within
100 km**, and those are nearly all in US waters. Buoys alone can't tell us
whether a model is better for Portugal or Indonesia. Global Hs
verification needs satellite altimetry, which means setting up a
Copernicus Marine account.

## Splits

ERA5 weather is autocorrelated over days to weeks, so split by contiguous
time, never by random step:

- train 2010–2022, validation 2023, test 2024–2025.
- The test years overlap the GFS-Wave archive (2021-03 on), so the same
  period can be scored against GFS-Wave and buoys.

## Open questions

- [ ] Is GFS-Wave's `SWPER` a partition peak period or a mean period?
      ecCodes labels it `mpts` (mean). The low correlation with ERA5's
      partition *mean* periods suggests a different definition either way.
      Check the WW3 `ww3_grib` configuration NCEP uses.
- [ ] How to build training targets for partitions (see approaches.md).
- [ ] Altimetry source: CMEMS L3 vs ESA CCI. Both are free and need an account.
