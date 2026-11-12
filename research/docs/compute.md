# Compute plan

Where to train and monitor our own forecast model, and what it should
cost. Written 2026-11-11. Prices are list prices gathered that month and
change often, so check them again before renting. GPU-hour figures are
estimates until the profiling run (step 3) replaces them with measured
ones.

## Recommended order

1. **Build the evaluation harness and GFS-Wave post-processing on CPU
   (approaches 0 and 1 in [approaches.md](approaches.md)). Cost: $0.**
   Both run on the existing 8-core machine. Post-processing
   (gradient-boosted trees trained on NDBC buoys) can ship in `pipeline/`,
   and its score is the bar any trained model has to beat. Without the
   harness we can't tell whether GPU spending helped.
2. **Apply for free compute now. Approvals take weeks.**
   - **Google TPU Research Cloud (TRC):** free TPU time for research.
     It's the best fit: GraphCast and `weathernext` are JAX, which runs
     natively on TPUs, and ERA5 already lives on GCS, so there's no data
     to move.
   - **NSF NAIRR Pilot:** GPU allocations for research and open
     projects. Being open source helps.
   - Startup credit programs (NVIDIA Inception, AWS, GCP), if OpenSwells
     is set up as a company.
3. **Profiling run, about $5–15.** Rent one H100 on Vast.ai or RunPod for
   2–4 hours and train the 1° mesh GNN for a few hundred steps. Measure
   time per epoch, memory use and dataloader throughput, then update the
   cost table below with real numbers.
4. **Train the 1° model on one rented H100 or A100.**
5. **Train at 0.25° only if the 1° model beats GFS-Wave at buoys.**

## Where to train

| Option | Price (Nov 2026) | Best for | Downsides |
| --- | --- | --- | --- |
| **Vast.ai** (marketplace) | H100 ~$1.33–2/hr median; A100 80GB ~$1.20/hr | Profiling, hyperparameter sweeps | Hosts vary in reliability, so checkpoint often |
| **RunPod** | H100 ~$2–4/hr (Community vs Secure); A100 ~$1.40/hr | Day-long runs; also serverless for inference | Secure Cloud prices went up in 2026 |
| **Lambda** | H100 ~$3–4/GPU-hr; A100 ~$2/hr | Long, reliable runs | Sometimes out of capacity |
| **Thunder / TensorDock** | A100 ~$0.80/hr | Cheapest A100s | Smaller providers |
| AWS / GCP / Azure | H100 up to ~$12/GPU-hr | Only with credits (GCP is close to ERA5) | 3–5× more expensive otherwise |
| Buying hardware | 4090/5090 box ~$3–5k | Not yet | Too little memory for 0.25°. Reconsider once usage is steady |

All of these bill by the minute. Check egress fees: pulling ~60 GB of
ERA5 once is cheap almost anywhere, but moving checkpoints and grids
around repeatedly adds up.

**Machine spec for 1°:** 1× H100 80GB (an A100 80GB also works), at
least 128 GB RAM so the whole ~60 GB ERA5 1° set fits in memory, and fast
NVMe. The current machine has 15 GB of RAM, which would need streaming
dataloaders. A rented box avoids that.

## Cost estimate

| Phase | GPU-hours | Cost |
| --- | --- | --- |
| Harness and GFS-Wave post-processing (approaches 0, 1) | 0 | $0 |
| Profiling run | 2–4 | ~$10 |
| 1° GNN or SFNO prototype, ~10–20 runs including failures | 200–600 | **~$400–1,500** |
| 0.25° model (multi-GPU, only if 1° works) | 2,000–8,000 | ~$5k–20k, or free with TRC/NAIRR |
| Joint atmosphere + wave fine-tune (approach 4) | far more | Grant scale only |

For scale: a 1° wave-only model has ~42k ocean nodes, about the size of
GraphCast_small, and the 2010–2022 training split has ~19k six-hourly
samples. That's small by weather-ML standards. Full 0.25° GraphCast took
about 4 weeks on 32 TPUv4, which is why 0.25° waits.

**Storage:** ~$5–10/month for ERA5 plus checkpoints on Cloudflare R2 (no
egress fees) or Backblaze B2. Keep the canonical copy there and pull it
onto each rented box.

## Inference is the ongoing cost

Training is paid once. Inference runs four times a day indefinitely.

- **Force the wave model with GFS winds**, which are free and already
  fetched by the pipeline, instead of running GraphCast or FourCastNet
  for wind. A small 1° wave model may then run on the CPU host with no
  GPU in production.
- If a GPU is needed, use serverless (RunPod Serverless, Modal) for about
  10 minutes per cycle: roughly $20–100/month, compared with $1,500+/month
  for an always-on GPU.

## Monitoring

Two separate things need watching.

1. **Training runs.** Weights & Biases (free personal/academic tier)
   gives loss curves, GPU use and run comparison from any rented box. To
   avoid depending on a SaaS, self-host MLflow or Aim on the pipeline host
   instead. Checkpoint to R2 every N steps, so losing a Vast.ai host costs
   minutes rather than a whole run.
2. **Forecast skill in production.** This matters more. Add a daily
   pipeline job that scores each published forecast against the latest
   NDBC observations: Hs bias and RMSE by lead time and region, next to
   GFS-Wave. Store the results and show them on an internal page in the
   app. This tells us when to switch the default model, and it reuses the
   approach 0 harness. Only 18% of spots have a buoy within 100 km (see
   [data.md](data.md)), so add satellite altimetry (CMEMS, free account)
   for global coverage.

## Summary

Spend $0 until the harness exists and GFS-Wave post-processing has
shipped. Apply to TRC and NAIRR now. Spend ~$10 on a profiling run, then
budget ~$1,000 on a rented H100 for the 1° prototype. Spend serious money
on 0.25° only if 1° clearly beats GFS-Wave at buoys.

## Sources

- [IntuitionLabs: H100 rental prices across 15 providers (2026)](https://intuitionlabs.ai/articles/h100-rental-prices-cloud-comparison)
- [IntuitionLabs: cloud GPU rental price comparison](https://intuitionlabs.ai/articles/cloud-gpu-rental-prices-comparison)
- [Spheron: Lambda Cloud H100 pricing 2026](https://www.spheron.network/blog/lambda-cloud-h100-pricing-2026/)
- [Spheron: RunPod vs Lambda 2026](https://www.spheron.network/blog/runpod-vs-lambda-labs-2026/)
- [Thunder Compute: Lambda alternatives and pricing](https://thundercompute.com/blog/lambda-labs-alternatives)
- [Jarvislabs: best cloud GPU providers 2026](https://jarvislabs.ai/ai-faqs/best-cloud-gpu-providers-2026)
- [Keenable: effective H100 price including egress](https://select.keenable.ai/r/effective-nvidia-h100-cloud-price/gOfLvQKXDuvoIXIJftVW5Q)
