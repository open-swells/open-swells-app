## OpenSwells

Free, open-source surf reports and 16-day swell forecasts. This repository
holds both halves of the system:

| Directory | What it is |
| --- | --- |
| [`app/`](app/README.md) | Go web app (server, templates, spot data) |
| [`pipeline/`](pipeline/README.md) | Python GFS-Wave/NWPS forecast pipeline |
| [`research/`](research/README.md) | Experiments toward our own wave forecast model (not deployed) |
| [`ops/`](ops/README.md) | Machine setup and layout migration scripts |

### Setup

Create the config and data directories once (owned by you):

```sh
sudo ops/setup-dirs.sh
```

Then follow [`app/README.md`](app/README.md) to run the web app and
[`pipeline/README.md`](pipeline/README.md) to build forecasts. Run the
pipeline with `pipeline/run.sh --local` to publish forecasts straight into
your local app's data directory.

`pipeline/spots.json` is a copy of `app/data/spots.json`; update both
together when spots change.

Deployment is covered separately in [DEPLOYING.md](DEPLOYING.md).

### Data and config layout

Config and data live outside the git checkout. The checkout holds only code
and tracked reference data like `spots.json`.

```
/etc/open-swells/                     $OPENSWELLS_CONFIG_DIR
  open-swells-app.env                 app runtime config (PORT, SITE_URL, ...)
  open-swells-pipeline.env            pipeline runtime config (TIDE_STATIONS, ...)
  firebase.json                       Firebase service account key (app; optional)
/var/lib/open-swells/                 $OPENSWELLS_DATA_DIR
  main.db                             app: SQLite (users, favorites, stations, surf zones)
  spot_forecast_history.json.gz       app: recent spot forecasts for day views
  forecast/                           published forecast products: the app serves them,
                                      the pipeline publishes into them
  pipeline/work/                      pipeline: GRIBs and products before publish (emptied each run)
  pipeline/logs/                      pipeline: grib-run.log, gfs_wave_contours.log
  pipeline/tide-cache/                pipeline: same-day NOAA/FES tide predictions
  reference/fes2022/                  FES2022 tide atlas (licensed; never published)
  reference/gshhg/                    GSHHG shoreline for spot_facing.py
```

The app reads `/var/lib/open-swells/forecast` without any configuration.

Set `OPENSWELLS_DATA_DIR` or `OPENSWELLS_CONFIG_DIR` to move a whole tree
(for example to a bigger disk); single-path overrides such as `DB_PATH`,
`FORECAST_DIR` and `FIREBASE_CREDENTIALS` still work but are rarely needed.
