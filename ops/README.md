## ops

Machine setup scripts shared by the app and the pipeline. The directory
layout they create is described in
[Data and config layout](../README.md#data-and-config-layout).

### `setup-dirs.sh`

Creates `/etc/open-swells` and `/var/lib/open-swells`, owned by you. Run it
once per machine:

```sh
sudo ops/setup-dirs.sh            # dev machine
ssh <pipeline-host> 'sudo bash -s <user>' < ops/setup-dirs.sh
```

### `migrate-legacy-layout.sh`

Moves a checkout from the old layout (`app/main.db`, `app/data/forecast`,
`pipeline/.env`, `pipeline/files`, `pipeline/logs`, `~/.cache/openswells-*`)
into the current one. It moves everything it finds into place without
overwriting anything, writes the env files to `/etc/open-swells` with the
now-derived path settings dropped, and repoints the FES2022 config at the
moved atlas.

The user database is the exception: it is copied, never moved or modified,
only while nothing has it open (stop the app first), and the copy is checked
by SHA-256. Delete the original yourself once the app runs well. The app also
refuses to start if it would create a new, empty database while an old
`./main.db` is still beside it.

```sh
# dev machine, from the repository root
sudo ops/migrate-legacy-layout.sh --app app --pipeline pipeline
# pipeline host (pipeline deploys refuse to run until this is done)
scp ops/setup-dirs.sh ops/migrate-legacy-layout.sh <pipeline-host>:/tmp/
ssh -t <pipeline-host> 'sudo bash /tmp/migrate-legacy-layout.sh --pipeline <pipeline-dir>'
```

The app server already keeps its data in `/var/lib/open-swells` and its
config in `/etc/open-swells`, so it needs no migration.
