## OpenSwells research

Experiments toward an OpenSwells wave forecast model. Nothing here is
deployed: the deploy workflow only watches `app/` and `pipeline/`. Once a
model works, its inference code gets rewritten into `pipeline/` with tests.
The pipeline never imports from here.

- [docs/data.md](docs/data.md): data sources, volumes, measured findings
- [docs/approaches.md](docs/approaches.md): model ideas, plan, experiment log

### Layout

| Path | What |
| --- | --- |
| `src/openswells_research/` | Core package: data sources, variable catalog, spots, CLI, and `products` (read/write the pipeline's `swell`/`wind` bundles) |
| `envs/earth2/` | Separate uv project: NVIDIA Earth2Studio (FourCastNet FCN/SFNO/FCN3, GraphCast inference). GPU, Linux x86_64. |
| `envs/graphcast/` | Separate uv project: DeepMind GraphCast (`weathernext`) for inference and its mesh/GNN code |
| `data/` | Downloaded datasets, git-ignored (`$OPENSWELLS_DATA` overrides) |

The model stacks are separate projects because their pins conflict with
the core environment and with each other (for example, `weathernext`
requires `xarray<=2026.2`, and both require CUDA wheels). Exchange data
between them as zarr files in `data/`.

### Setup

```sh
cd research
uv sync
uv run pytest
uv run openswells-research inventory   # checks every data source is reachable
```

Model environments, on a GPU machine:

```sh
cd envs/earth2 && uv sync
cd envs/graphcast && uv sync --extra cuda
```

### Fetching data

```sh
uv run openswells-research fetch-era5 --start 2024-01 --end 2024-12 --resolution 1.0
uv run openswells-research fetch-gfswave --cycle 2025-01-01T00 --hours 0-168:6
uv run openswells-research fetch-ndbc --years 2021-2025
```

Fetches are resumable: finished months, files and buoy-years are skipped.
ERA5 writes one zarr per month, so several month ranges can run in
parallel. ERA5 commands may print an `attached to a different loop`
traceback on exit. It's gcsfs closing its HTTP session and is harmless.

### Conventions

- Keep large data out of git. Commit only small fixtures under `tests/`.
- Clear notebook outputs before committing (`uv run --with nbstripout nbstripout *.ipynb`).
- Record every experiment's result in the Log in `docs/approaches.md`, including failures.
