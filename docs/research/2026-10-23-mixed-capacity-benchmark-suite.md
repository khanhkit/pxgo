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
