# PxGo AP-ISS-0035 — Independent R16 native Windows process-private resource analysis

**Date:** 2026-10-25 (Asia/Ho_Chi_Minh). **Immutable source commit:** `e3376665470a6c2aabd163824c13f8231cf288ad`. **Workflow:** [40101711262](https://github.com/khanhkit/pxgo/actions/runs/40101711262) (completed SUCCESS; 71/71 jobs: 69 SUCCESS, two conditionally skipped). **Scope:** research and release-readiness analysis only; the production PXGO controller and experimental feature flags are unchanged.

## Directly inspected evidence

This analysis fetched each of **16 successful fresh-VM `windows-latest` “Windows isolated proxy” raw job logs** and required an `ISOLATED_RESULT platform=windows` row with `held_long`, `completed_short`, baseline/peak process-private commit, RSS, process handles, CPU nanoseconds, and sample counts. The data are preserved verbatim as typed numerical columns with exact job IDs in [R16 isolated native Windows metrics CSV](2026-10-25-native-windows-r16-isolated-metrics.csv). `seed=0,1,2,3` has respectively 256/320/384/2,560 live CONNECT and 400/500/600/600 completed short HTTP requests. **All 16 rows match the requested held and completed counts.** Every configuration runs on a separate GitHub VM, so cross-policy comparisons are **not paired controls**.

## High-density, 2,560 held CONNECT + 600 completed short HTTP

| Native Windows policy / classification | CONNECT held | HTTP completed | HTTP throughput (RPS) | Offered-load HTTP p95 (ms) | Peak private commit Δ vs idle (MiB) | Peak process handles Δ vs idle | Child CPU Δ (s) |
|---|---:|---:|---:|---:|---:|---:|---:|
| fast_saturation/off | 2560 | 600 | 1857.54 | 306.79 | 264.7 | 5181 | 2.95 |
| fast_saturation/on | 2560 | 600 | 2371.62 | 237.83 | 267.0 | 5170 | 2.17 |
| r11/off | 2560 | 600 | 1936.27 | 297.04 | 253.8 | 5175 | 3.05 |
| r11/on | 2560 | 600 | 3327.50 | 170.25 | 265.8 | 5171 | 1.81 |

Across the four high-density fresh VMs: median **baseline-subtracted peak private commit 265.28 MiB** (observed range 253.79–267.02 MiB); median **peak process-handle delta 5173.00** (range 5170–5181). Dividing those deltas by 2,560 gives a **combined incremental upper-path estimate of approximately 106.11 KiB and 2.02 handles per held tunnel** for this exact test, **not a production-calibrated per-tunnel marginal cost**: the delta includes request bursts, Go allocations, listener, ancillary work, and some fixed resources. Note that these are **peak-minus-idle** values, not `held-minus-idle` estimates; the released Go test child PID is not a production-slim standalone binary.

The explicit 25 ms child-PID resource sampler covers connection ramp, mixed HTTP and drain and all 16 jobs recorded nonzero samples in each phase; transients shorter than 25 ms are not guaranteed observable. Windows `PrivateUsage`/private committed bytes are distinct from Linux `RssAnon`; do not compare those byte-for-byte.

## Audit interpretation and limits

- A complete 2,560/600 pass is materially stronger than homogeneous 2,560 CONNECT and demonstrates simultaneous long+short delivery at high density in a **separate PXGO PID**, but no production-process Job Object limit is actually enforced.
- The four large-load points represent **one sample per policy/class** and show large shared-host timing variability. Compare p95 or CPU trends cautiously; no confidence interval, causality, or universal fast-vs-R11 speedup can be justified from one high-density run per group. For the smaller three loads there are 12 more rows, also on different hosts and different cohorts.
- The provisional Windows governor estimates **2 MiB per extra connection** from host-wide memory and a separate handle reserve; the measured ~100 KiB high-density private commit delta does **not** justify reducing that conservative estimate. TLS/SSPI, HTTP/2, high packet-buffer occupancy, OS/Job commit quotas and production application behavior were not stressed in this sample.
- The class pool remains **advisory 25/75 soft accounting** with short-to-long migration and no independent class-specific fair admission semaphore. The observed short success rate does not establish latency guarantees under fully saturated long quotas. The separate hard-cap borrow test proves short progress *after* release, not a permanent 25% short reservation.
- The native Windows SSPI/enterprise AD conditional job was SKIPPED; a real enterprise runner, Windows Job Object `ProcessMemoryLimit` and `JobMemoryLimit` pressure/denial/recovery, TLS/SSPI and long soaks, and production-binary process-private metrics remain **unmet release gates**.
- No release, merge, default activation, or unreviewed algorithm change is authorized by these measurements. Keep PR #102 and #103 DRAFT until those gates are independently verified.

## Reproducibility

- Open exact-SHA workflow [40101711262](https://github.com/khanhkit/pxgo/actions/runs/40101711262); inspect **all 16** jobs named `Windows isolated proxy (POLICY, classes=MODE, seed=SEED)` and their raw `ISOLATED_RESULT platform=windows` lines. The CSV includes the job ID in every row.
- Per-run artifacts are SHA-qualified and retained by CI for 14 days. CSV is preserved in Git beyond temporary artifact retention.
- All other 71 workflows were enumerated via GitHub REST `jobs?per_page=100`, **not** a first-page-only connector summary.
