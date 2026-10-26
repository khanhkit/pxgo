# PxGo R22 — Order-balanced Windows mixed-workload evidence audit

**Status:** independent research validation of previous immutable Windows R20 observations. Not production approval, not a runtime algorithm change. Data source is exactly 32 successful distinct jobs from GitHub Actions [run 40220215522](https://github.com/khanhkit/pxgo/actions/runs/40220215522), immutable source SHA `22d05d66d06538c1e00cd4b6d9474be6a36c7a49`. R21 exact-SHA source/CI `3d58b624b1852be76ea6e9b549073e372e1e06fd` had 103 jobs fully enumerated, 101 success, two conditional skips, with native R20 metric replay 64/64 accepted by the strict full-line parser. R22 does not replace that verification or infer anything about protected native AD/SSPI.

## Motivation and design

R20 crossed four real workloads (`short_heavy`, `balanced`, `long_heavy`, `churn`) × classification OFF/ON × seeds 0/1 × **both policy orders** (R11-first and fast-saturation-first). Each of the 32 independent jobs ran a matched pair of policy trials on one fresh Windows VM and collected offered HTTP short latency, matching long-CONNECT and HTTP load, and native TIME_WAIT-before counters. The pair's second policy always encounters a different TCP state. Crucially, the opposite order for an identical profile, classifier and seed ran on a **different** hosted Windows VM. Therefore order balance can be analyzed descriptively but cannot establish a randomized same-VM crossover causal effect.

For stratum `k = (profile, class, seed)` define measured difference in each execution order:

```
Delta(k, order) = offered_HTTP_short_p95_fast - offered_HTTP_short_p95_R11
balanced_delta(k) = (Delta(k, R11_first) + Delta(k, fast_first)) / 2
half_order_contrast(k) = (Delta(k, R11_first) - Delta(k, fast_first)) / 2
```

A positive policy difference favors **R11** on lower observed offered short HTTP p95. A positive half-order contrast suggests the fast-minus-R11 difference is larger when R11 is measured first. These differences mix actual controller effects, Windows host noise and TIME_WAIT/port-history carryover, so the method is *descriptive order balancing*, not an unbiased production estimate.

## R22 exact result

| Statistic | Measured |
|---|---:|
| Native Windows original jobs | 32 from one exact immutable R20 SHA |
| Strictly complete `(profile,class,seed)` strata with both orders | 16/16 |
| Raw unadjusted median of 32 per-host fast−R11 deltas | +7.866 ms |
| Median order-balanced delta across 16 strata | **+11.0637 ms** |
| Mean order-balanced delta | +15.3545 ms |
| Balanced strata with lower observed p95 for R11 | **13/16** |
| Balanced strata with lower observed p95 for fast-saturation | 3/16 |
| Median signed half-order contrast | **+12.9506 ms** |
| Mean signed half-order contrast | +17.1513 ms |
| Conditional descriptive 95% percentile-bootstrap sensitivity band for mean balanced delta | **[+4.8437, +26.9061] ms** |

The conditional **descriptive percentile-bootstrap sensitivity calculation** samples **10,000 deterministic replicates**, drawing from the four class×seed strata *within each profile* (preserves all four workload categories). Its band is a conditional resampling sensitivity summary of this fixed observational dataset: **it does not adjust nonrandom VM assignment, order carryover, temporal confounding or undocumented host differences**. Do not advertise the positive band as a statistical confidence interval for a causal deployment improvement or as permission to set R11/fast-saturation as the production default.

## Evidence integrity and reproduction

- Input: [`2026-10-26-r20-native-windows-paired-replay.csv`](2026-10-26-r20-native-windows-paired-replay.csv), previously backed by all 64 individual exact-R20-SHA native metric rows and immutable job IDs. The report now **pins this file's SHA256** `82b0a1c9c48a315c6ad6e0a60dfe3df15702f0737cedfec1f9f21ffc179e8386` and fails closed on any byte drift. A digest protects the committed replay from incidental substitution but **cannot itself authenticate job artifacts** if an actor can alter both code and source CSV; the original R21 immutable raw-log audit remains a separate mandatory provenance dependency.
- Validator: [`scripts/analyze-capacity-crossover.py`](../../scripts/analyze-capacity-crossover.py) uses stdlib only, requires the exact 13-column schema, correct GitHub run/job URL and full source SHA, 32 unique jobs and all 16 complete twice-ordered groups, no empty jobs, no nonfinite measurements, matching request intensity across both orders, and numerically consistent fast−R11 p95 deltas. Corrupt or missing data fail before reporting a result.
- Regression fixtures: [`scripts/analyze_capacity_crossover_test.py`](../../scripts/analyze_capacity_crossover_test.py) covers 23 positive/negative tests, including missing/duplicated cohorts, fake job URLs, wrong SHA, fabricated p95 deltas, NaN/Inf, invalid workload, mixed seed, and shuffled CSV order. Windows artifact provenance remains tied to R20, not the latest HEAD or a prior chat assertion.
- Output: [`2026-10-27-r22-order-balanced-study.json`](2026-10-27-r22-order-balanced-study.json). CI recomputes it exactly from the pinned CSV and compares byte-for-byte against committed output, refusing silent edits or calculation drift.

```
python3 -m unittest discover -s scripts -p 'analyze_capacity_crossover_test.py' -v
python3 scripts/analyze-capacity-crossover.py \
  --input docs/research/2026-10-26-r20-native-windows-paired-replay.csv \
  --expected-sha 22d05d66d06538c1e00cd4b6d9474be6a36c7a49 \
  --output /tmp/pxgo-r22-crossover.json
cmp /tmp/pxgo-r22-crossover.json docs/research/2026-10-27-r22-order-balanced-study.json
```

## Remaining gates before rollout

1. **Experimental** policy superiority remains undecided under order/VM confounding. Additional ideally randomized same-host session blocks with randomized AB/BA assignment, balanced OS port-state and resource instrumentation can improve evidence, but forced `SO_LINGER` RST or artificially expanded ephemeral port range would invalidate realistic traffic measurement.
2. Class short/long 25/75 quotas are advisory *accounting*, not actual independent fair admission or borrowing. Keep one physical resource governor; do not promise starvation freedom under an already-full long tunnel envelope.
3. Native enterprise Windows AD/SSPI, enforced Windows Job Object committed-memory limits and recovery, Linux cgroup pressure, representative TLS/HTTP2/SSE and multi-hour real deployment soaks are still unverified. Both PR #102 and PR #103 remain **DRAFT**, no merge, no release, no production algorithm promotion.
