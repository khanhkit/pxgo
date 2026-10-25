# PxGo AP-ISS-0035 — R16 native Windows mixed short/long observed-latency audit

**Evidence date:** 2026-10-25 (ICT). **Exact source SHA under analysis:** `e3376665470a6c2aabd163824c13f8231cf288ad`. **Native Windows Actions:** [40101711262](https://github.com/khanhkit/pxgo/actions/runs/40101711262), all 71 jobs enumerated (69 SUCCESS, 2 conditionally skipped). The following is **read-only analysis, not a source or production algorithm modification**.

## Evidence integrity

Independently read raw GitHub Actions logs for **all 32** `Windows mixed` jobs (R11 / fast-saturation × classes on/off × seeds 0–7), each on a separate fresh VM. Each log contains **exactly four** `MIXED_RESULT` profile rows, **one** `MIXED_COMPETE_RESULT` and **one** `ACTIVE_PAYLOAD_RESULT`. All 128 short profile rows had `short_attempted == short_completed`; all 32 competing test rows had full long/short completion; all 32 active payload rows completed each short request and showed **some actual overlap** of successful short requests with long frame transfer. **Do not claim 100% short requests overlapped frames**: e.g. one seed measured 118 actual overlaps out of 120 shorts (all 120 completed), while its test correctly passed. This is a required distinction between functional completion and *temporal* overlap evidence.

Each policy/class set is preserved as an immutable typed CSV with exact GitHub job IDs, seeds, scenario names and counts: [R11/off](2026-10-25-native-windows-r16-mixed-r11-off.csv), [R11/on](2026-10-25-native-windows-r16-mixed-r11-on.csv), [fast/off](2026-10-25-native-windows-r16-mixed-fast-off.csv), [fast/on](2026-10-25-native-windows-r16-mixed-fast-on.csv). Grouped median summary is [CSV](2026-10-25-native-windows-r16-mixed-median-summary.csv).

## Eight-seed mixed-traffic medians

**p95 is end-to-end offered-load latency**, starting *before* the load generator's bounded concurrency token, so it includes local producer waiting and HTTP/CONNECT handling rather than only service time. RPS is successfully completed short HTTP requests per second. `n=8` are **eight different deterministic load intensities (and eight different Windows hosted VMs)** per group, *not* repeated identical samples on the same host.

| Profile | Governor | Tracking | Seeds | Median offered p95 (ms) | Median short RPS |
|---|---|---|---:|---:|---:|
| balanced | r11 | off | 8 | 182.4 | 3042.4 |
| balanced | r11 | on | 8 | 128.2 | 4826.2 |
| balanced | fast_saturation | off | 8 | 214.6 | 2404.4 |
| balanced | fast_saturation | on | 8 | 207.8 | 2668.5 |
| churn | r11 | off | 8 | 501.7 | 2174.5 |
| churn | r11 | on | 8 | 337.8 | 3149.9 |
| churn | fast_saturation | off | 8 | 520.2 | 2564.1 |
| churn | fast_saturation | on | 8 | 521.0 | 1993.3 |
| long_heavy | r11 | off | 8 | 96.3 | 2395.1 |
| long_heavy | r11 | on | 8 | 74.7 | 3345.6 |
| long_heavy | fast_saturation | off | 8 | 96.3 | 2221.2 |
| long_heavy | fast_saturation | on | 8 | 97.2 | 2213.9 |
| short_heavy | r11 | off | 8 | 216.7 | 2601.7 |
| short_heavy | r11 | on | 8 | 151.6 | 3995.1 |
| short_heavy | fast_saturation | off | 8 | 217.1 | 2568.9 |
| short_heavy | fast_saturation | on | 8 | 178.6 | 2988.7 |

## Interpretation: do not overfit homogeneous saturation

The earlier 2,560-CONNECT homogeneous benchmark favored fast-saturation for connection-ramp p95. Under mixed HTTP/CONNECT loads these windows-hosted measurements **do not establish a universal fast advantage**. In fact, with class tracking on, R11 had lower *observed* median short-request p95 than fast-saturation in balanced (128.2 vs 207.8 ms), churn (337.8 vs 521.0 ms), long-heavy (74.7 vs 97.2 ms) and short-heavy (151.6 vs 178.6 ms). These are **descriptive observations**, NOT verified causal policy improvements, because each matrix job uses its own Windows VM and the seed varies offered load. No fleet-wide significance, variance-matched control or stable-memory safety is implied.

In all 128 mixed profiles the short request success gate remained strict, with physical permit conservation and class-promotion accounting checked by the test. This does **not** prove a short-reservation guarantee when all global permits are occupied: `TestCapacityShortBorrowsReleasedLongPermit` appropriately verifies shorts wait until a held long CONNECT closes, then borrow its released **global** permit. The separate 25/75 pools are still **soft counters and migration debt**, not two independent fair admission semaphores.

## Recommendation and remaining gates

- **Preserve R11 as the default opt-in AUTO comparison baseline**; keep fast-saturation an explicit preview flag, do not promote either to the production default based on these numbers.
- A robust fast-vs-R11 conclusion needs **paired/comparable hosts**, repeated *identical* offered load and controlled measurements of per-process commit/handles, CPU, resource pressure, p50/p95/p99, and burst vs steady state. Windows hosted VM variation and TCP TIME_WAIT contamination must be modeled rather than excluded without evidence.
- Production Windows Job Object hard-limits/failure and recovery, real corporate AD/SSPI, TLS/HTTP2/long SSE/WebSocket, long soaks, Linux cgroup PSI pressure and code/auditor approval remain **unmet release requirements**. The two PRs must remain DRAFT / unmerged / unreleased.

## Related sources

- [R16 process-isolated native resource analysis](2026-10-25-native-windows-r16-isolated-analysis.md) with 16 child-PID baselines/peaks, including four 2,560-CONNECT + 600 HTTP native samples.
- [R16 source and CI](https://github.com/khanhkit/pxgo/actions/runs/40101711262) tie metrics to the precise immutable SHA above; 71 jobs enumerated via GitHub REST jobs?per_page=100, not a partial first page.
