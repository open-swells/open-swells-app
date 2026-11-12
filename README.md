## OpenSwells

Free, open-source surf reports and 16-day swell forecasts. This repository
holds both halves of the system:

| Directory | What it is | Runs on |
| --- | --- | --- |
| [`app/`](app/README.md) | Go web app (server, templates, spot data) | Debian host (`SERVER_IP` in `app/.env`), systemd service `open-swells-app` |
| [`pipeline/`](pipeline/README.md) | Python GFS-Wave/NWPS forecast pipeline (formerly `grib-parse-collect`) | `rochester-remote-evan`, `/home/evan/grib-parse-collect`, scheduled by cron |
| [`research/`](research/README.md) | Experiments toward our own wave forecast model (not deployed) | anywhere; GPU for model training |

The pipeline publishes forecast products over rsync into the app host's
`/var/lib/open-swells/forecast`, which the app serves. For local
development, `pipeline/run.sh --local` writes them to `app/data/forecast`.

`pipeline/spots.json` is a copy of `app/data/spots.json`; update both
together when spots change.

Deployment instructions are in [DEPLOYING.md](DEPLOYING.md).
