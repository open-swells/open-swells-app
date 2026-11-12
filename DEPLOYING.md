## Deploying

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

### Automatic deploys

`.github/workflows/deploy.yml` runs the same scripts on every push to `main`
that touches `app/` or `pipeline/` (and can be run by hand from the Actions
tab). Only the part that changed is deployed; when both change, the pipeline
goes first. A pipeline deploy that lands during a forecast run waits up to
two hours for the run to finish (`WAIT_FOR_RUN`).

Credentials are stored only as secrets in the repository's `production`
environment: `SSH_KNOWN_HOSTS`, `APP_HOST`, `APP_SSH_KEY`, `PIPELINE_HOST`
and `PIPELINE_SSH_KEY`. Keys are loaded into an `ssh-agent`, not written to
disk, and host keys are pinned.
