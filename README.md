## OpenSwells

Free, open-source surf reports and 16-day swell forecasts. This repository
holds both halves of the system:

| Directory | What it is | Runs on |
| --- | --- | --- |
| [`app/`](app/README.md) | Go web app (server, templates, spot data) | Debian host (`SERVER_IP` in `app/.env`), systemd service `open-swells-app` |
| [`pipeline/`](pipeline/README.md) | Python GFS-Wave/NWPS forecast pipeline (formerly `grib-parse-collect`) | `rochester-remote-evan`, `/home/evan/grib-parse-collect`, scheduled by cron |
| [`research/`](research/README.md) | Experiments toward our own wave forecast model (not deployed) | anywhere; GPU for model training |

### Data and config layout

Config and data live outside the git checkout, at the same paths on every
machine: a dev laptop, the pipeline host and the app server each fill in
the parts they use. The checkout (or a deploy directory such as
`/opt/open-swells-app`) holds only code and tracked reference data like
`spots.json`.

```
/etc/open-swells/                     $OPENSWELLS_CONFIG_DIR
  open-swells-app.env                 app runtime config (PORT, SITE_URL, ...)
  open-swells-pipeline.env            pipeline runtime config (DEST_PATH, SSH_KEY_PATH, TIDE_STATIONS, ...)
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

The pipeline publishes `pipeline/work/` into `forecast/`: over rsync to the
app host in production, or into the same machine's `forecast/` with
`pipeline/run.sh --local`. Either way the app reads
`/var/lib/open-swells/forecast` without any configuration.

Set `OPENSWELLS_DATA_DIR` or `OPENSWELLS_CONFIG_DIR` to move a whole tree
(for example to a bigger disk); single-path overrides such as `DB_PATH`,
`FORECAST_DIR` and `FIREBASE_CREDENTIALS` still work but are rarely needed.

**Set up a machine** once (creates both trees, owned by you):

```sh
sudo ops/setup-dirs.sh            # dev machine
ssh rochester-remote-evan 'sudo bash -s evan' < ops/setup-dirs.sh
```

The app server's directories are owned by its service account; see
[`app/README.md`](app/README.md).

**Moving from the old layout** (`app/main.db`, `app/data/forecast`,
`pipeline/.env`, `pipeline/files`, `pipeline/logs`, `~/.cache/openswells-*`):
`ops/migrate-legacy-layout.sh` moves everything it finds into place without
overwriting anything, writes the env files to `/etc/open-swells` with the
now-derived path settings dropped, and repoints the FES2022 config at the
moved atlas. The user database is the exception: it is copied, never moved
or modified, only while nothing has it open (stop the app first), and the
copy is checked by SHA-256; delete the original yourself once the app runs
well. The app also refuses to start if it would create a new, empty
database while an old `./main.db` is still beside it.

```sh
# dev machine, from the repository root
sudo ops/migrate-legacy-layout.sh --app app --pipeline pipeline
# pipeline host (pipeline deploys refuse to run until this is done)
scp ops/setup-dirs.sh ops/migrate-legacy-layout.sh rochester-remote-evan:/tmp/
ssh -t rochester-remote-evan 'sudo bash /tmp/migrate-legacy-layout.sh --pipeline /home/evan/grib-parse-collect'
```

The app server already keeps its data in `/var/lib/open-swells` and its
config in `/etc/open-swells`, so it needs no migration.

`pipeline/spots.json` is a copy of `app/data/spots.json`; update both
together when spots change.

Deployment instructions are in [DEPLOYING.md](DEPLOYING.md).
