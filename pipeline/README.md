## Systemd process to parse and store grib files

**Start Service**: `sudo systemctl start grib_parse.service`   
**View Logs**: `journalctl -u grib_parse.service `   
**stop service**: sudo systemctl stop grib_parse.service  
**status of service**: systemctl status grib_parse.service  
**Verify timer**:`systemctl list-timers`  
    
**Reload the system and enable the timer (this happens in setup_service.sh)**:  
```
sudo systemctl daemon-reload
sudo systemctl enable my_python_script.timer
sudo systemctl start my_python_script.timer
```

**Where things live**: the pipeline keeps nothing in its checkout. Config
is `/etc/open-swells/open-swells-pipeline.env`; the work directory, logs
and tide cache are under `/var/lib/open-swells/pipeline/`, and `--local`
publishes to `/var/lib/open-swells/forecast`, the directory the app reads.
`scripts/paths.sh` defines the layout (the top-level README has the whole
tree). Create it once with `sudo ops/setup-dirs.sh` from the repository
root. Logs: `/var/lib/open-swells/pipeline/logs/grib-run.log`.

**`/etc/open-swells/open-swells-pipeline.env`** (needed to publish to the
server; a `--local` run works without it):
```
DEST_PATH=<user>@<app-server>:/var/lib/open-swells/forecast/
SSH_KEY_PATH=...../.ssh/openswells_deploy_ed25519
PARALLEL_HOURS=3               # optional: worker processes for forecast hours
                               # (default: cores-1, capped at 4; each worker
                               # holds a few hundred MB of grids)
NWPS_DOMAINS=wr/lox,wr/sgx     # optional: NWPS nearshore domains as
                               # region/wfo pairs (this is the default;
                               # set empty to disable nearshore layers)
NWPS_GRIDS=CG1                 # optional: CG1 (~4 km full domain) and/or
                               # nested CG2+ (~500 m bays), e.g. CG1,CG2
SPOTS_PATH=...../spots.json    # optional: surf spots to sample into
                               # spot_forecasts.json.gz (default:
                               # spots.json in this repository)
OPENSWELLS_DATA_DIR=/data/os   # optional: move the whole data tree
                               # (default /var/lib/open-swells)
```

`SSH_KEY_PATH` must refer to a dedicated deployment key. Do not use an SSH
server host key (such as `/etc/ssh/ssh_host_ed25519_key`) or a personal key
that is also used for other accounts. Restrict the corresponding server-side
key to the forecast upload destination and operations it actually needs.

`FILES_DIR` (default `/var/lib/open-swells/pipeline/work`) is erased at the
beginning of a run and therefore must resolve to a child directory of
`$OPENSWELLS_DATA_DIR/pipeline`. The runner rejects `/`, the data root
(which holds the app's database on a dev machine), the published
`forecast/` directory, paths elsewhere, and symlinks that resolve outside
it.

**Source grids**: every layer is a composite of two NOAA GFS-Wave products —
`global.0p16` (1/6°, but only 15S–52.5N) inside its band and `global.0p25`
(1/4°, pole-to-pole) everywhere else, merged onto one 1/6° lattice clipped
to ±85° (see `composite.py`; the other regional products add nothing beyond
these two). Both files are downloaded per forecast hour; if one is missing
the hour degrades to partial coverage instead of failing.

**Outputs**: images stay one file per forecast hour (the map loads them
directly); everything else is one bundle per product per run, named
`<product>_<run>.bin` (e.g. `swell_20261028_12Z.bin`) and listed under
`bundles` (and per domain under `nwps_points`) in `metadata.json`. A
publish keeps the previous run's bundles alongside, so a map opened before
it keeps reading the files it started with: the runner leaves bundles in
the work directory and the pipeline prunes all but the current and previous run's.

- `heatmap_XXX.webp` — continuous-color combined wind-wave-and-swell height
  field in Web Mercator with transparent land; the app's primary wave layer.
  Lossless WebP with a 128-step ramp (`HEATMAP_STEPS`), ~200 KB a frame.
  Corner coordinates are published as `heatmap_bounds` in `metadata.json`.
  Colors come from `HEATMAP_COLORS` in `gfs_to_contours.py`, matched by
  `SWELL_BANDS` in the web app's `pages/today.html` — change them together.
- `nwps_<grid>_XXX.png` (e.g. `nwps_cg1_012.png`) — nearshore combined
  wave height from NOAA NWPS (SWAN), all configured office domains
  mosaicked onto one lattice at the finest source resolution (finer
  grids blend in over coarser ones across overlaps; the offshore edge is
  alpha-feathered so the frame fades into the global heatmap under it).
  Named by the same global forecast hour as `heatmap_XXX.webp` — overlay
  directly. `metadata.json` lists bounds and covered hours per grid tier
  under `nwps_layers` (max 144 h; hours before the office's cycle or past
  its horizon have no frame and the global layer shows through).
- `swell_<run>.bin` — all three swell systems on a coarse grid (every
  `ARROW_STRIDE`th cell of the 1/6° composite, ~1.67°), every forecast
  hour. Vars `h1`/`p1`/`d1` through `h3`/`p3`/`d3` are height in meters,
  period in seconds, and direction from in degrees true; the map draws
  partition 1 as the swell arrows.
- `wind_<run>.bin` — surface wind from the GFS-Wave file on the same grid. Vars
  `s` speed in m/s, `d` direction from in degrees true, and `u`/`v`
  components in m/s.
- `contours_<run>.bin` — combined wave-height polygons on fixed bands
  (`FIXED_LEVELS`), one GeoJSON document per hour; the map's contour mode
  and its fallback when a heatmap frame is missing.
- `nwps_points_<wfo>_<grid>_<run>.bin` — every wet NWPS cell, 3-hourly out to
  144 h, for high-resolution beach forecasts. Vars `h` (combined
  wind-wave-and-swell height m — total sea state, not a swell partition),
  `s` (total swell height m, wind sea excluded — use this for "swell"),
  `p` (primary mean period s), `d` (primary direction from, deg true).
  Listed per domain, with its hours, under `nwps_points` in
  `metadata.json`.

**Bundle format** (`bundle.py`; read by the app's `server/bundle.go` and
`web/templates/components/forecastbundle.html`): `OSB1`, a little-endian
uint32 header length, a JSON header, then gzip chunks back to back. The
header has the `hours`, the `vars` (with `scale`), and `chunks`, the byte
length of every (hour, var) chunk in hour-major order. Grid bundles also
carry the lattice axes `lat`/`lon`; each chunk is a row-major int16 array,
value = raw × scale, with `nodata` (-32768) on land or where a field is
missing. An hour's vars are contiguous, so the browser fetches one hour
with a single HTTP Range request. Each chunk names itself in its gzip
header (FNAME `<run>/<hour>/<var>`), which the server's reader checks.
Inspect a bundle with `bundle.read_bundle(path)`.

- `spot_forecasts.json.gz` — one file per run (not per hour): every
  surf spot in the web app's `spots.json` sampled at its nearest wet cell
  of the full-resolution 1/6° composite (great-circle distance, within
  100 km; lakes, rivers and unmodeled seas are left out), for every
  forecast hour. Arrays align with the top-level `hours` list; `null`
  marks a failed hour. Fields are `hs` (combined height m), `h1`/`p1`/`d1`
  through `h3`/`p3`/`d3` (swell partitions, as above), `ws`/`wd` (wind
  m/s, direction from) and `hw`/`pw`/`dw` (the local wind sea's height m,
  mean period s and direction from — the part of the sea state the swell
  partitions leave out). Each spot also records the sampled `cell`
  `[lat, lon]` and its distance `km`. See `spot_forecasts.py` for the
  full layout; `metadata.json` summarizes it under `spot_forecasts`.
  Spots are read from `spots.json` in this repository, a copy of the web
  app's `data/spots.json` — update both together when spots change (a
  spot missing here just falls back to the app's coarse sampling).
  `SPOTS_PATH` overrides the location; if the file is missing the run
  continues without spot forecasts.

**Spot facing (one-time, offline)**: `spot_facing.py` adds each spot's
beach `facing` and `open_water` arcs to `spots.json`, from the GSHHG
full-resolution shoreline. The web app uses them to judge how squarely
swell and wind hit each beach, and which swell land blocks. It is not part
of the forecast run; re-run it when spots are added. Spots with a
hand-tuned `swell_window` are left alone.

```sh
# gshhg-bin-2.3.7.zip from https://www.soest.hawaii.edu/pwessel/gshhg/
unzip gshhg-bin-2.3.7.zip gshhs_f.b -d /var/lib/open-swells/reference/gshhg
.venv/bin/python -I spot_facing.py --gshhg /var/lib/open-swells/reference/gshhg/gshhs_f.b \
    --report facing_report.csv spots.json ../app/data/spots.json
```

It takes a few minutes on 8 cores. `--report` writes a CSV with each spot's
status and review flags (`scale_disagreement`: the shoreline turns sharply
near the spot; `facing_outside_window`: the beach faces into land, such as
an island shadow or across a bay; `narrow_window`); `--only <ids>` prints
results for a few spots without writing anything.
- `tides.json` — NOAA CO-OPS hourly astronomical predictions and the latest
  48 hours of observed water levels, in meters relative to MLLW and UTC.
  Set `TIDE_STATIONS` to comma-separated CO-OPS station IDs to generate it,
  for example `TIDE_STATIONS=9410230,9410840`. Metadata publishes
  `tide_stations: false` when disabled or no stations succeed, so the map
  skips the optional file request. Successful stations set it to `true`.
- `spot_tides.json.gz` — predicted highs and lows (meters MLLW, UTC) at the
  NOAA CO-OPS station nearest each spot within 50 km, plus hourly
  predictions for reference stations, for the tide panel on spot pages.
  A spot's `tide_station` in `spots.json` overrides the nearest station.
  CO-OPS rate-limits bursts, so stations fetched earlier the same UTC day
  are reused from `TIDE_CACHE_DIR` (default `/var/lib/open-swells/pipeline/tide-cache`);
  a failed fetch keeps the server's previous file (`P` in publish.rules).
  Spots with no CO-OPS station within 50 km fall back to the FES2022 global
  tide atlas when `FES_CONFIG` names a PyFES config (see below); NOAA stays
  the source wherever a station is in range. Each station in the file names
  its `source` (`NOAA CO-OPS` or `FES2022`) and `datum` (`MLLW` or
  `FES MLLW`), and the spot page's tide panel labels the datum.

### FES2022 tide fallback

`fes_tides.py` evaluates FES2022 with [PyFES](https://github.com/CNES/aviso-fes)
on the pipeline host, so spot pages never call an outside service.

1. **Data.** FES2022b is distributed by AVISO+, not with this repository:
   register at aviso.altimetry.fr, accept the AVISO License, and download the
   `ocean_tide_extrapolated/*.nc.xz` archives (unxz needs ~18 GB of memory).
   The extrapolated grids fill the coastal pixels the native grid masks.
   Licence terms that apply to this site (AVISO License, Issue 20):
   - Never commit, serve or otherwise republish the `.nc` files (section 3.2).
     The predictions derived from them are fine to publish.
   - Keep the attribution: the credit under the tide chart on FES spots (in
     `components/forecast.html`) and the FES2022 entry on the About page give
     the product, producers, DOI, licence link and how the data was modified
     (section 3.1).
   - Don't imply that AVISO or CNES endorses the site (section 2.6).
2. **PyFES.** `uv sync --extra fes` (`scripts/deploy.sh` does this on the
   host). PyFES is source-only on PyPI and builds against Boost 1.79 or newer
   and Eigen. On Debian 12 that means `apt install libboost1.81-dev
   libeigen3-dev`: plain `libboost-dev` there is 1.74, too old.
3. **Config.** Copy PyFES's `data/fes2022b/ocean_tide_extrapolated.yaml` and
   make its paths absolute. The default location is
   `/var/lib/open-swells/reference/fes2022/` (grids in `ocean_tide_extrapolated/`, config
   `ocean_tide_extrapolated.yaml`), used whenever that config exists and
   `FES_CONFIG` is unset; set `FES_CONFIG` to use another path, or to an
   empty value to turn the fallback off. `scripts/fes_download.sh` fetches
   the grids there (resumable; AVISO throttles each connection, so it
   downloads six at a time).

How it works: spots are grouped onto a 0.05° lattice so neighbors share a
prediction; a site the atlas masks as land moves to the nearest wet point
within ~30 km. The atlas is loaded 10° tile at a time. Predictions are
made every 10 minutes over the same window as NOAA's; highs and lows are
the refined turns of that curve and hourly values ride along. FES heights
are relative to mean sea level, so they are shifted to a modeled MLLW
(the mean lower low of each tidal day over 2026's predictions); each site's
`msl` is mean sea level above that datum. NOAA's MLLW averages a 19-year
epoch, so expect a few cm of difference where the two overlap.

Caching: today's predictions come from `TIDE_CACHE_DIR` like NOAA's, so the
atlas is read at most once a UTC day, and each site's wet point and datum
are computed once and kept in `TIDE_CACHE_DIR/fes_sites.json` (reset when
`FES_CONFIG` changes). If PyFES or the atlas fails, sites keep the previous
file's predictions while those still reach four days ahead.

Accuracy: FES2022 is an open-ocean model at 1/30°; inside estuaries,
lagoons and reef passes its amplitude and timing can be off by tens of
centimeters and tens of minutes. Before trusting a region, compare a few
sites against local gauges (for example `fes_tides.fes_spot_tides` on spots
next to UHSLC or national tide stations).
  also drives the app's hover readout.

See `webgl-swell-rendering.md` (kept outside this repository) for the
possible next step (client-side WebGL rendering with temporal interpolation).

`run.sh --verbose` draws a live progress bar in the terminal (the log is
unaffected); `--local` publishes to this machine's
`/var/lib/open-swells/forecast` instead of the server: the same path the
app reads by default, here and in production. Override it for a one-off run
with `FORECAST_DIR=/path/to/forecast`.
`--limit <n>` processes only the first n forecast hours for quick render checks
(don't publish a limited run to the server — see Publishing below).

**Publishing**: both the server copy and `--local` sync the files directory
with `rsync --delete` through `scripts/publish.rules`. Forecast products the
new run didn't produce (a failed hour) are deleted from the destination
rather than left behind from the previous run; files outside those
patterns, including `metadata.json`, are never touched. That also means a
`--limit` run published to the server would delete every later hour.
`metadata.json` lists the published forecast hours under `hours`; the map
builds its timeline from it and requests each file as `?run=<forecast_start>`
so caches never mix runs. If the server restricts the deploy key with
`rrsync`, don't pass it `-no-del`.

**Optional tuning env vars**:
```
CONTOUR_STRIDE=2               # grid downsampling for contours
CONTOUR_SMOOTHING_SIGMA=1.0    # gaussian smoothing before contouring
CONTOUR_SIMPLIFY_TOLERANCE=    # shapely simplify tolerance (off by default)
ARROW_STRIDE=10                # arrow grid spacing (10 = one per 1.6 deg)
```
