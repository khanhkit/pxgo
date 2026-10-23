# PxGo AP-ISS-0035 — Mixed-duration native Windows stress matrix, audit v1

Status: **EXPERIMENTAL / unmerged / not production-ready**. This is a separate research expansion on PR #103, based on its exact previous HEAD `32e0e48ca98c7845deccfccdd4f91490cc404a79`. Legacy remains default. No real AD/SSPI environment or production Windows Job Object limit is claimed.

## Why the earlier benchmark was insufficient

R1–R3 `windows-latest` capacity jobs held **2,560 homogeneous CONNECT tunnels** for ~18 seconds. That proves simultaneous tunnel data-plane liveness and native Windows headroom, not the realistic contention among HTTP short requests, repeated keep-alive sessions, long-lived tunnels and short-to-long migration. The workload also used 32-SYN waves to avoid confusing native Winsock listen-backlog refusals with admission-governor limits.

This suite retains the existing **2,560 held-CONNECT** network gate, increases it from **2 to 4 repeated samples per cell**, and adds **4 mixed workloads × 8 seeded independent samples = 32 more samples per cell**. With R11/fast-saturation crossed with class accounting on/off (4 native `windows-latest` cells), this is **16 homogeneous tunnel runs plus 128 mixed-load runs plus 32 competing-admission runs** and 2 real-clock idle keepalive aging soaks (class-on cells only), each with named profile, actual network request counts, raw p50/p95/p99 and errors. Distinct GitHub-hosted VM runners are NOT paired experiments; higher sample count improves coverage, not causal guarantees across machines.

## Four realistic traffic profiles (each seed scales load 80–120%)

| Scenario | Baseline held CONNECT | Held short keepalive | HTTP keepalive promoted after 8 requests | Transient HTTP GET requests | Intent |
|---|---:|---:|---:|---:|---|
| short_heavy | 192 | 64 | 16 | 600 | Small long floor alongside high short-turnover pressure |
| balanced | 512 | 128 | 32 | 600 | Approximate sustained mixed fleet |
| long_heavy | 1,024 | 64 | 64 | 250 | Tunnel-dominated profile with short request fairness |
| churn | 384 | 48 | 48 | 1,200 | High short-socket turnover and classifier promotions |

Mechanism: a real loopback HTTP origin replies with a fixed payload, and an independent TCP echo server services real CONNECT tunnels. The client holds all admitted CONNECT and keepalive sockets while issuing short requests with maximum **32 concurrent new TCP dials**. Each CONNECT is challenged once before, once **after** the short workload, and every response must match. `HTTP/1.1` keep-alives that receive eight actual GETs must atomically shift short→long while retaining **one and only one** global physical permit. Before release, assert `active == longConnect + keptShort + promoted`, class `long == longConnect + promoted`, `short == keptShort`, `provisional == 0`; after close, verify both global and all class permits drain to zero. Short request completion `== attempted` is the work-conserving fairness gate (no queued-starvation success masking). The predicted CONNECT→long and aged `30s` migration remain covered by dedicated unit/integration tests.

## Measurement and limitations

- Output lines `MIXED_RESULT profile=… seed=…` report **both** client-service latency (measured after the local concurrency token) and offered-load end-to-end latency (measured before waiting for the local token), so load-generator queue delay is visible rather than hidden. Values report CONNECT concurrently held, completed short requests, transient HTTP request latency p50/p95/p99, throughput in requests/sec, promotions, current long debt, historical overflow events, global active sockets, process-shared heap, goroutine count and scenario duration. `RESULT platform=windows` logs from existing 2,560 test still report CONNECT p95/p99 and all echo errors.
- The Go test binary co-locates **proxy, load generator and origins**. Heap and goroutines are intentionally named `*_shared`, NOT PxGo-exclusive; they must not be used as per-proxy memory or CPU budgets.
- The tests exercise real HTTP/1.1, CONNECT, keep-alive and promotion under load; they do **not** yet exercise authenticated real AD/SSPI, Windows Job Object pressure, encrypted long HTTP/2 streams, real SSE/WebSocket frames, remote Internet packet loss, multi-hour tails, or 2,560 *instantaneous* TCP SYNs.
- A fail-closed test cannot silently skip native Windows mixed-load under inadequate native resources. All expected requests and sockets are counted. Raw stdout must be uploaded to immutable-SHA CI artifacts for failed as well as successful tests.
- Planned separate production gates: isolated load generator/process-private telemetry; resource pressure and cgroup/Job Object limits; long-v-short fairness under actual quota enforcement, predictable short grace even when long debt exists, classifier-based borrowing, production TLS/auth and cold-start/soak.

## Reproduction

```
PXGO_EXPERIMENTAL_CAPACITY_MODE=auto PXGO_EXPERIMENTAL_CONNECTION_CLASSES=1 \
PXGO_CAPACITY_MIXED_BENCH=1 PXGO_CAPACITY_MIX_SEED=3 \
PXGO_RESEARCH_CAPACITY_POLICY=fast_saturation \
go test ./internal/proxy -run '^TestCapacityMixedTrafficBench$' -count=1 -v -timeout=125s
```

All changes remain in the dedicated experimental PR. A separate independent KitAuditor must inspect the immutable SHA and actual Windows CI logs; this document alone is not a release gate.


### Live clock aging and failure-teardown audit enhancement

As a separate native Windows gate for classifier-ON cells, hold an actual ordinary HTTP/1.1 keepalive for >30s without issuing another request. Verify the maintenance ticker promotes the same physical socket from short to long exactly once, not only a mock-clock test. Also, mixed benchmark CONNECT establishment drains ALL goroutine results before reporting a cohort failure so failures do not leave successful, unowned upstream TCP sockets blocking test cleanup. Both are mandatory test quality safeguards and do not alter production runtime.


### R1 review: reproducibility and latency boundaries

The independent R1 audit identified a real error-path ownership bug: the CONNECT cohort previously abandoned successful sockets on first setup error, risking teardown hangs. The corrected fixture collects every result and closes every owned socket before asserting failure. It also records `short_offered_p95/p99` (from submission before the bounded client concurrency token) alongside `short_p95/p99` (socket and HTTP service after acquiring that token), making overload-queue measurement bias explicit. Per-seed keepalive counts and 8-request promotion volume now vary independently of long CONNECT and short transient intensity instead of merely scaling total connections.


### R1 warning follow-up: simultaneous class arrivals

An additional `TestCapacityMixedCompetingAdmission` sends actual CONNECT and short GET admission attempts **concurrently** behind separate bounded 16-dial producer queues, for eight seeds. It requires every long and short to complete (even though long remains held at the post-load barrier), sends unique post-load CONNECT echo, and asserts physical/class permits conserve and drain. Its p95 values include the client load-generator queue from submission, unlike client-service-only timing. This covers fairness under overlapping arrivals at the existing **single global gate**; no unsupported claim is made that advisory class budgets enforce strict class fairness.


### Windows R1 CI gate defect and R2 fix

Raw first-run exact-SHA Windows workflow 39885079509 confirms the four heavy 2,560 CONNECT load jobs passed their earlier homogeneous portion but **each mixed load failed immediately at setup**, since workflow had not explicitly set `PXGO_EXPERIMENTAL_CAPACITY_MODE=auto`. Homogeneous test hid this by `t.Setenv` internally. R2 explicitly sets AUTO in the research job and retains mixed-test fail-closed validation. Do not count R1's Windows mixed results as benchmark evidence; only an exact R2 green workflow with complete `MIXED_RESULT` lines qualifies.


### R2 Windows failure: ephemeral-port/TIME_WAIT contamination from preceding network loads

At exact SHA `479498b010bc60e54f9a40af4da8e1ed43a89154`, GitHub Windows workflow `39886080737` demonstrated the four homogeneous 2,560 CONNECT repetitions can complete; however the subsequent mixed seed 0 failed on ALL four Windows runner cells with actual Winsock `connectex: Only one usage of each socket address (protocol/network address/port) is normally permitted`. Previous saturation workload had consumed thousands of short-lived loopback TCP endpoints. Ephemeral port reuse / TIME_WAIT exhaustion is a strong explanation, but actual per-run OS `netstat` TCP-state counters were not collected, so do not claim proven root cause. This is a **Windows network load-generator operating envelope**, not a proven PXGO governor policy regression.

R3 separates homogeneous 4×2,560-CONNECT tests and mixed workload into different fresh `windows-latest` jobs, and runs **each of 8 mixed seeds on its own fresh VM** (policy × class × seed = 32 independent Windows mixed jobs, `max-parallel=8`). Each seed still performs four live profile subtests plus the competing short+long admissions; seed zero with classifier on also performs the 31-second real-clock idle promotion soak. This retains 16 homogeneous + 128 mixed + 32 competition trials, 2 real-time idle promotion soaks, and 14-day SHA-qualified artifact retention. It **does not ignore failed samples, force TCP RST, alter the OS ephemeral port range, or quietly reduce live connection counts**. The old R2 Windows mixed results are INVALID as capacity evidence; independently verify R3 raw logs.


### R3 Windows CI audit correction: lifecycle tests must precede socket saturation

Workflow `39886829077` confirmed separately isolated mixed seed runners began PASS, while four homogeneous 4×2,560 Windows jobs FAILED only AFTER all 2,560 tests completed: the subsequently scheduled `TestConnectionClassHTTPAndCONNECTLifecycle` used another loopback TCP port and received `WSAEADDRINUSE` (ephemeral-port pressure). R4 moves network lifecycle verification and benchmark microbenchmarks onto the **fresh seed 0 mixed-runner BEFORE** any socket stress, leaving the 4×2,560 job focused solely on the intended saturation gate. Never weaken the failed test or reinterpret an exhausted runner as a controller regression. Keep every raw failed job and distinguish OS test-host constraint from code safety.

## R5 extension: real variable connection lifetimes (2026-10-23)

R4 immutable GitHub HEAD `ee81b331d221dff9996c98d68de35a1638fb5e79` passed all required 43-job CI results at run `39887332409` (41 success, 2 conditional skipped). Although the 128 mixed profile samples varied admitted short/long *counts*, their held CONNECT sockets generally survived until a common release point. Such static profiles cannot prove correctness as long-lived streams expire and short requests opportunistically reclaim the released global physical slots.

R5 adds opt-in `TestCapacityMixedStaggeredLifetimes` and schedules it **before the existing mixed profile test**, on EACH freshly isolated `windows-latest` (policy, class flag, seed) research VM:

- Four real TCP CONNECT lifetime buckets with target end times **250ms, 950ms, 2.5s, 5.5s** measured from the initial cohort being established; each bucket contains `16+seed%6`, `24+seed%8`, `32+seed%10`, `40+seed%12` live tunnels respectively (112–139 initial held sockets depending on seed). Every tunnel is echo-probed on establishment and every surviving tunnel after each closure wave.
- At each phase the client offers **48–96 independent short HTTP/1.1 requests** through the SAME proxy, with capped 12-client dial concurrency. Record short request offered-load p95/p99 including client-side waiting, and fail if any HTTP response is missing, fails payload validation or times out.
- After a naturally closed group is released, require `global active == remaining CONNECT sockets`. With class accounting ON, `long==remaining`, `short==0`, `provisional==0`, no duplicated physical permits; with class OFF, no class tracker. All four close waves must drain **all physical and class permits to zero**, not just make a cumulative counter appear correct.
- The run has its own `LIFETIME_RESULT` for each phase and final `LIFETIME_SUMMARY`, verified in the immutable-SHA raw GitHub artifacts. It is a distinct **lifetime distribution correctness gate**, NOT proof of realistic multi-hour tail distributions, real WAN losses, Windows Job Object memory pressure or true fair scheduling from an advisory-only split.

This adds **32 additional real Windows lifetime scenario runs**, giving 16 homogeneous held-CONNECT +128 mixed profiles +32 competing-arrival +32 staggered-lifetime = **208 network scenario executions**, plus 2 real-clock 31s HTTP short→long aging soaks and repeat class unit/microbench. All trial policy/flags remain disabled by default; protected AD/SSPI and independent real resource failure calibration remain release blockers.


### R6 independent critic follow-up: actual permit reclamation and cleanup

An independent immutable-SHA R5 KitAuditor review found that, although the staged 250ms/950ms/2.5s/5.5s lifecycle test verified post-churn survivor echo and physical permit release, it issued short HTTP **before** releasing each long cohort. R6 changes the workload order to **release an expired CONNECT bucket first, then issue the next bounded short HTTP wave**, so every phase now directly tests whether short traffic reuses the physically freed admission capacity *while surviving long tunnels remain open*. `actual_close_elapsed` is logged alongside each nominal `lifetime_target`: the target is a lower-bound schedule, not a guaranteed exact lifetime on loaded Windows VMs. The partial cohort error path also explicitly closes all successful sockets before failing, in addition to the correct Go LIFO deferred teardown. This is benchmark-only; no shipping policy changed.


## R7 strict hard-envelope borrowing regression (2026-10-23)

R6 independent KitAuditor exact-SHA source verdict PASS (0 proven blockers), but correctly warned that successful *post-release* short requests do not by themselves establish **necessity** of a reclaimed permit when global admission never reached its ceiling. R7 adds `TestCapacityShortBorrowsReleasedLongPermit`, a real TCP+HTTP test using `cfg.MaxConnections=64` as an authoritative **hard manual cap**. It fills all 64 permits with real authenticated CONNECT tunnels and asserts `ActiveDownstream==EffectiveLimit==64`. A short HTTP/1.1 request is then issued via the same proxy; it MUST remain incomplete for at least 150ms while the global gate is saturated. Closing one long tunnel MUST allow the previously blocked short request to finish with an exact valid origin payload and return its permit. A replacement CONNECT then fills the 64th slot again. This is repeated **four times per isolated Windows policy/class/seed cell**, with exact class/global accounting and zero leaked tokens at shutdown.

This test verifies **real global physical permit recovery under saturation**, not hypothetical 25/75 class reservation or a separate short-class scheduler. The Windows research matrix retains 32 independent seed VMs: 32 strict borrowing scenarios × 4 guaranteed blocked/release rounds = 128 hard-saturated admission exercises, in addition to 16 held-2,560, 128 mixed profiles, 32 competing-arrival and 32 staggered lifetime scenarios, plus two real-time 31s idle promotion soaks. Total **240 separately named network scenario runs** and 128 inner borrowing transitions (not counted as distinct jobs). Original production legacy mode and optional AUTO policy remain unchanged.


### R8 expansion: actively transferring long tunnels versus real short HTTP payloads

Previous mixed profiles verified **idle held long CONNECT** with initial and post-churn echo, not concurrent relay throughput. `TestCapacityMixedActivePayload` now exercises both **actively streaming** and **idle** CONNECT tunnels under the same global hard cap while 120–168 transient HTTP GETs return varied 128 B / 1 KiB / 16 KiB / 64 KiB response bodies. Four workload seeds exercise 48–84 held long tunnels, half streaming and half idle, with 1 KiB / 16 KiB / 128 KiB / 256 KiB echo frames in 8 rounds and one 10 MiB transfer per seed. A 64 KiB chunked full-duplex echo test avoids test-fixture TCP send/receive deadlock for 10 MiB. Every chunk is content-verified; every original tunnel must still pass a final echo after short churn; short-body hashes are compared using exact full bytes. Deadline/permit-release failures are fatal, not silently retried or marked PASS. Local 16 combinations (4 seeds × 2 algorithms × classifier on/off) PASS, with expected whole-process load and host scheduling variability.

Each of the 32 fresh Windows mixed-seed jobs now includes one additional active-payload profile: **32 native Windows active cases**, raising the total named Windows network scenario executions from 240 to **272** (not 272 independent statistical replications). These runs capture `ACTIVE_PAYLOAD_RESULT` with payload-size, total bytes, active/idle long counts, successful short HTTP responses, p95/p99 of both HTTP offered-to-completion and per-frame tunnel echo, and the physical active connection count. This is not a production-only resource measurement; client, proxy and origin share a test process. No claim about separate short/long admission hard quotas or WAN/SSPI/TLS is made.


### R9 follow-up to independent R8 critic: full duplex and true transfer intervals

R8 critic (read-only, exact `0fe6466f915f6e6d78b05d55b811da8223ee0115`) noted that alternating 64-KiB write-then-read chunks show robust bidirectional echo but do not establish **independent simultaneous TCP reader and writer**; and `overlapped_worker_shorts` establishes concurrent goroutine lifetime, not necessarily active transfer. R9 adds a separate **10-MiB true full-duplex** path with concurrent `io.Copy` writer and `io.ReadFull` reader over the same physical tunnel; exact position-dependent bytes verified under bounded deadline. Smaller frames retain chunked round-trip stress. The benchmark now reports `short_starts_during_actual_transfer` and `short_finishes_during_actual_transfer` sampled while a tunnel is inside a real exchange, distinct from `overlapped_worker_shorts`. Long workers signal ready before releasing the short-load start barrier, avoiding scheduler-induced zero-overlap false positives; only worker overlap is a hard assertion because transfer-window sample overlap depends on host scheduling. This is still loopback data-plane verification, not an independent WAN congestion/half-close stress gate.
