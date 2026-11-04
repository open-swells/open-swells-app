# Model approaches

A working list of ideas, ordered by when to try them. Update it as
experiments come in; record results in the Log at the bottom.

## Framing

GraphCast and FourCastNet are **atmosphere** models. Their outputs are
wind, pressure, temperature and so on, not waves. There are two ways to
use them for a surf forecast:

1. **As the wind source.** A wave model is forced by surface wind, so a
   learned wave model can take 10 m wind from GraphCast, FourCastNet or
   GFS. This keeps the wave model small and lets us swap or ensemble wind
   sources.
2. **As the architecture.** Reuse their designs to learn wave dynamics
   from ERA5's wave fields. GraphCast uses an icosahedral multi-mesh GNN;
   FourCastNet uses spherical Fourier neural operators (SFNO in FCN2,
   local plus spectral spherical convolutions in FCN3).

Both architectures suit swell, which crosses whole ocean basins over days.
GraphCast's long mesh edges and SFNO's global spectral layers each carry
information thousands of km in a single step. The main difference is land.
A GNN can simply leave land cells out. SFNO's spherical harmonics see a
field with sharp coastline edges, which tends to cause ringing near
coasts, and coasts are exactly where surf spots are.

Whatever we build has to output what the pipeline publishes, so the app
can use it unchanged:

- `spot_forecasts.json.gz`: per-spot series of `hs`, `h1..3`/`p1..3`/`d1..3`,
  `ws`/`wd` and `hw`/`pw`/`dw`.
- The map's run-scoped bundles: `swell_<run>.bin` (`h1..d3`) and
  `wind_<run>.bin` (`s`/`d`/`u`/`v`), int16 grids listed under `bundles` in
  `metadata.json`. `products.write_run` writes these through
  `pipeline/bundle.py`, and `products.read_run` reads a published run back
  for comparison. Every cell becomes a map arrow, so write at about the
  pipeline's 1.67° spacing (`products.MAP_SPACING_DEG`). A partition is
  shown wherever it isn't NODATA, so mask weak ERA5-style partitions to NaN
  first.

## 0. Evaluation harness (do first)

Nothing else can be judged without it.

- Score GFS-Wave forecasts (f000–f168) against NDBC buoys for 2024–2025:
  Hs bias, RMSE and correlation by lead time, region and Hs bin, plus
  period and direction errors.
- Score ERA5 the same way. It is the ceiling for anything trained to
  mimic ERA5.
- Later: altimetry for global Hs, and CDIP spectra for partitions.

**Needs:** `fetch-gfswave` point extraction (to write), `fetch-ndbc`.
**Runs on:** CPU.

## 1. Post-processing GFS-Wave at buoys and spots (quick win)

Learn a correction for the forecast the pipeline already serves, using
gradient-boosted trees or a small MLP. Inputs: GFS-Wave at the spot (all
partitions), lead time, season, spot `facing`/`open_water`. Target:
buoy-observed Hs/Tp/Dp.

- Learns the systematic biases of GFS-Wave and of each location.
- Cheap and CPU-only, and it can ship within the existing pipeline.
- Only covers places with buoys; spots elsewhere need a transfer
  assumption.
- Its score is also the bar approaches 2 and 3 have to clear.

## 2. Mesh GNN wave model ("WaveCast", GraphCast-style)

An encoder–processor–decoder like GraphCast, with **ocean-only grid
nodes** on an icosahedral multi-mesh. It is autoregressive with a 6 h step.

- **Inputs:** wave state at t and t−6h; `u10`/`v10` over t−6h…t+6h
  (forcing, with the future window included because waves respond to the
  wind that's coming); `siconc`; static `depth`, `lsm`, lat/lon features.
- **Output:** the change in wave state over the next 6 h.
- **Training:** ERA5 forcing. **Inference:** forecast wind from
  GraphCast, FCN or GFS. Measure how much skill drops when ERA5 winds are
  replaced with forecast winds; that is the main risk of decoupling.
- Start at 1° (~42k ocean nodes, mesh refinement 5, about the scale of
  GraphCast_small), then move to 0.25°.
- Building blocks are in `weathernext.utils` (`icosahedral_mesh`,
  `deep_gnn`, `autoregressive`) in `envs/graphcast`.

## 3. SFNO / FCN3 wave model (FourCastNet-style)

Same inputs, outputs and data as approach 2, but with
`torch-harmonics`/`makani` blocks from `envs/earth2`. Land handling:
fill land with a smooth extension, and mask the loss to ocean.

Running approaches 2 and 3 on identical data and the same harness gives
a fair GNN-vs-spectral comparison for waves.

## 4. Joint atmosphere + wave model (later)

Fine-tune pretrained GraphCast or FCN with extra wave output channels, so
the model learns how waves and wind interact. This is the most powerful
option and also needs the most compute. Only worth it if approaches 2 and
3 show that forecast-wind error is the dominant problem.

## 5. Nearshore downscaling (later)

Map the offshore wave state to each spot using `facing`/`open_water`,
bathymetry, NWPS (SWAN) where it exists, and CDIP buoys for training.
This is where surf-specific value lives, but it needs approach 0's
verification first.

## Target design for swell partitions

Partition slots are ordered by height, so labels swap when swells cross
(see data.md). Options:

- **(a)** Predict integrated quantities (`hs`, wind-sea/swell energy
  split, mean period and direction of each), and derive partitions
  afterwards. Stable to train, but loses multi-swell detail.
- **(b)** Predict partitions with a permutation-invariant loss: match
  predicted to target partitions by direction and period (Hungarian
  matching), then regress. Keeps the multi-swell detail surfers care
  about.
- **(c)** Predict a coarse directional spectrum (e.g. 12 directions × 10
  frequency bins) and partition it the same way WW3 does. Most faithful,
  but ERA5 2-D spectra are not in ARCO; they come from MARS, which is
  slow and huge.

Start with (a) for approaches 2 and 3, and try (b) once the harness exists.

## Compute

| task | where |
| --- | --- |
| Data prep, approaches 0 and 1 | This machine (CPU). |
| GraphCast_small (1°) inference | CPU, slow. |
| GraphCast 0.25°, SFNO, FCN3 inference | One large GPU. Earth2Studio documents ≥60 GB for FCN3. |
| Training approaches 2 and 3 at 1° | One rented A100/H100. Estimate after a short profiling run. |
| Training at 0.25° | Multiple GPUs. Decide after the 1° results. |

## Log

| date | experiment | result |
| --- | --- | --- |
| 2026-10-28 | Data access check, ERA5 vs GFS-Wave on one analysis time | See data.md: `hs`/winds agree (r=0.96/0.98); partition periods and the wind-sea split don't. |
