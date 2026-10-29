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

**necessary .env: **
```
SOURCE_PATH=...../grib-parse-collect/files
DEST_PATH=<user>@<app-server>:/var/lib/open-swells/forecast/
PYTHON_SCRIPT=....../grib-parse-collect/gfs_to_contours.py
PYTHON_INTERPRETER=...../bin/python3
FILES_DIR=....../grib-parse-collect/files
LOG_DIR=...../grib-parse-collect/logs
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
```

`SSH_KEY_PATH` must refer to a dedicated deployment key. Do not use an SSH
server host key (such as `/etc/ssh/ssh_host_ed25519_key`) or a personal key
that is also used for other accounts. Restrict the corresponding server-side
key to the forecast upload destination and operations it actually needs.

`FILES_DIR` is erased at the beginning of a run and therefore must resolve to
a child directory of this repository. The runner rejects `/`, the repository
root itself, paths outside the repository, and symlinks that resolve outside
it.

**Source grids**: every layer is a composite of two NOAA GFS-Wave products —
`global.0p16` (1/6°, but only 15S–52.5N) inside its band and `global.0p25`
(1/4°, pole-to-pole) everywhere else, merged onto one 1/6° lattice clipped
to ±85° (see `composite.py`; the other regional products add nothing beyond
these two). Both files are downloaded per forecast hour; if one is missing
the hour degrades to partial coverage instead of failing.

**Outputs per forecast hour**:
- `heatmap_XXX.png` — continuous-color combined wind-wave-and-swell height
  field in Web Mercator with transparent land; the app's primary wave layer.
  Corner coordinates are published as `heatmap_bounds` in `metadata.json`.
  Colors come from `HEATMAP_COLORS` in `gfs_to_contours.py`, matched by
  `SWELL_BANDS` in the web app's `pages/today.html` — change them together.
- `contours_XXX.geojson` (+`.gz`) — combined wave-height polygons on fixed
  bands (`FIXED_LEVELS`); kept as the app's fallback layer when heatmaps are
  missing.
- `arrows_XXX.geojson` (+`.gz`) — coarse grid of swell direction points
  (properties `h`=height m, `p`=period s, `d`=direction from, deg true);
- `swell_partitions_XXX.geojson` (+`.gz`) — all three swell systems. Compact
  properties `h1`/`p1`/`d1` through `h3`/`p3`/`d3` are height in meters,
  period in seconds, and direction from in degrees true.
- `wind_XXX.geojson` (+`.gz`) — surface wind points from the GFS-Wave file.
  Properties are `s` speed in m/s, `d` direction from in degrees true, and
  `u`/`v` components in m/s.
- `nwps_<grid>_XXX.png` (e.g. `nwps_cg1_012.png`) — nearshore combined
  wave height from NOAA NWPS (SWAN), all configured office domains
  mosaicked onto one lattice at the finest source resolution (finer
  grids blend in over coarser ones across overlaps; the offshore edge is
  alpha-feathered so the frame fades into the global heatmap under it).
  Named by the same global forecast hour as `heatmap_XXX.png` — overlay
  directly. `metadata.json` lists bounds and covered hours per grid tier
  under `nwps_layers` (max 144 h; hours before the office's cycle or past
  its horizon have no frame and the global layer shows through).
- `nwps_points_<wfo>_<grid>_XXX.geojson` (+`.gz`) — every wet NWPS cell
  as a point, 3-hourly out to 144 h, for high-resolution beach forecasts.
  Compact properties `h` (combined wind-wave-and-swell height m — total
  sea state, not a swell partition), `s` (total swell height m, wind sea
  excluded — use this for "swell"), `p` (primary mean period s), `d`
  (primary direction from, deg true). Listed per domain under
  `nwps_points` in `metadata.json`.
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
unzip gshhg-bin-2.3.7.zip gshhs_f.b -d ~/.cache/openswells-gshhg
.venv/bin/python -I spot_facing.py --gshhg ~/.cache/openswells-gshhg/gshhs_f.b \
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
  skips the optional file request. Successful stations set it to `true`;
  older metadata without the flag retains the legacy loading behavior.
- `spot_tides.json.gz` — predicted highs and lows (meters MLLW, UTC) at the
  NOAA CO-OPS station nearest each spot within 50 km, plus hourly
  predictions for reference stations, for the tide panel on spot pages.
  A spot's `tide_station` in `spots.json` overrides the nearest station.
  CO-OPS rate-limits bursts, so stations fetched earlier the same UTC day
  are reused from `TIDE_CACHE_DIR` (default `~/.cache/openswells-tides`);
  a failed fetch keeps the server's previous file (`P` in publish.rules).
  also drives the app's hover readout.

See `webgl-swell-rendering.md` (kept outside this repository) for the
possible next step (client-side WebGL rendering with temporal interpolation).

`run.sh --verbose` draws a live progress bar in the terminal (the log is
unaffected); `--local` copies output to this monorepo's `app/data/forecast`
directory instead of the server. Override that
location for a one-off run with `LOCAL_DEST_PATH=/path/to/data/forecast`.
`--limit <n>` processes only the first n forecast hours for quick render checks
(don't publish a limited run to the server — see Publishing below). The application should set
`FORECAST_DIR='./data/forecast'` so it reads the generated files from the same
location.

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
