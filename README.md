## OpenSwells

Free, open-source surf reports and 16-day swell forecasts. This repository
holds both halves of the system:

| Directory | What it is | Runs on |
| --- | --- | --- |
| [`app/`](app/README.md) | Go web app (server, templates, spot data) | Debian host (`SERVER_IP` in `app/.env`), systemd service `open-swells-app` |
| [`pipeline/`](pipeline/README.md) | Python GFS-Wave/NWPS forecast pipeline (formerly `grib-parse-collect`) | `rochester-remote-evan`, `/home/evan/grib-parse-collect`, scheduled by cron |

The pipeline publishes forecast products over rsync into the app host's
`/var/lib/open-swells/forecast`, which the app serves. For local
development, `pipeline/run.sh --local` writes them to `app/data/forecast`.

`pipeline/spots.json` is a copy of `app/data/spots.json`; update both
together when spots change.

### Deploying

```sh
./deploy.sh app        # web app -> Debian host
./deploy.sh pipeline   # data pipeline -> rochester-remote-evan
./deploy.sh all        # pipeline, then app
```

Each part runs its own tests locally first and ships only its own
directory:

- **app** (`app/ops/deploy.sh`): rsyncs `app/` to `/opt/open-swells-app`,
  builds on the server, restarts the service, and waits for `/healthz`.
  Needs `SERVER_IP` in `app/.env`. See [`app/README.md`](app/README.md).
- **pipeline** (`pipeline/scripts/deploy.sh`): refuses to deploy while a
  forecast run is in progress (override with `FORCE=1`). It rsyncs
  `pipeline/` to the host, runs `uv sync --frozen`, and smoke-tests the
  scripts and imports. The host's `.env`, `.venv`, `files/`, `logs/` and its
  cron entry are left untouched. Override the target with `PIPELINE_HOST`
  (an SSH alias, default `rochester-remote-evan`) and `PIPELINE_DIR`.
