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
  scripts and imports. It first checks that the host uses the shared
  [config and data layout](README.md#data-and-config-layout)
  (`/etc/open-swells/open-swells-pipeline.env`, a writable
  `/var/lib/open-swells/pipeline`) and stops, deploying nothing, if not.
  The host's config, data, `.venv` and cron entry are left untouched. Override the target with `PIPELINE_HOST`
  (an SSH alias, default `rochester-remote-evan`) and `PIPELINE_DIR`.

### Automatic deploys

Development happens on `main`; merging there deploys nothing. Production
deploys when a GitHub release is published:

```sh
gh release create v2026.11.24 --target main --generate-notes
```

`.github/workflows/deploy.yml` then runs the same scripts against the tagged
commit. It deploys only the parts (`app/`, `pipeline/`) that changed since the
last successful release deploy, so a release whose deploy failed is picked up
by the next one; the first release deploys both. When both change, the
pipeline goes first. A pipeline deploy that lands during a forecast run waits
up to two hours for the run to finish (`WAIT_FOR_RUN`). Prereleases and
drafts do not deploy. The workflow can also be run by hand from the Actions
tab to deploy a chosen branch or tag.

Releases are named by date: `vYYYY.MM.DD`, with `.2`, `.3`, ... for further
releases the same day (`v2026.11.24.2`). To roll back, run the workflow by
hand on an earlier tag.

Credentials are stored only as secrets in the repository's `production`
environment: `SSH_KNOWN_HOSTS`, `APP_HOST`, `APP_SSH_KEY`, `PIPELINE_HOST`
and `PIPELINE_SSH_KEY`. Keys are loaded into an `ssh-agent`, not written to
disk, and host keys are pinned.

The `production` environment only accepts deploys from the `main` branch
(manual runs) and from `v*` tags (release runs). A release whose tag doesn't
match `v*` is rejected by the environment before any step runs.
