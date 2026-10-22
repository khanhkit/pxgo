# AP-ISS-0035 — 2,560-connection concurrency study and capacity controller R6

Date: 2026-10-22. Status: research and **experimental opt-in implementation**, not production AUTO approval. This is an owner-directed extension of the independently approved Oct 15 baseline design: the trial starts AUTO at a resource-clamped target of 256 rather than the original provisional 32; reviewer validation of the modified startup assumption remains mandatory. `legacy` remains the unchanged shipped default.

## Workload and misleading benchmarks to avoid

When legacy is manually configured with exactly `workers=1, threads=256`, a valid overload test attempts **2,560 downstream connections simultaneously** (10× the admitted budget). It does not require legacy to *admit* all 2,560 at the same time: that would contradict its safety contract. Report attempted sockets, handshake successes **before the release barrier**, accepted/active snapshot, kernel backlog behavior, timeouts, errors, and completions **after the release barrier** separately. If 256 legacy slots are held, the extra 2,304 connections must either queue, time out, or fail according to OS backlog and client timeout; do not count a post-release handshake as a pre-release admission.

The opt-in test `TestCapacityBurst2560` opens 2,560 client goroutines on a start barrier, sends real CONNECT requests through PxGo into a local TCP origin, holds successful tunnels for 13 seconds, snapshots admission and resources, then releases all clients and drains the remaining pending ones. Mode-controlled runs test `legacy`, `shadow` and `auto`; `legacy` and `shadow` explicitly use 256 slots. It is intentionally opt-in because client, proxy, and upstream sockets are in one process and use several thousand FDs plus thousands of Go goroutines. The PR-scoped GitHub CI job runs the three modes on both Linux and Windows, recording raw logs; default unrelated PRs are not forced to run this load.

The co-located test provides comparable *functional admission* evidence, but its goroutine count, Go heap, RSS and CPU **cannot** be attributed solely to PxGo. Proper production capacity conclusions require separate PXGO and load-driver processes/hosts, native Windows handles and Unix FD counts, CPU, RSS/cgroup pressure, backlog/retransmissions, and true long-lived tunnel plateaus. Use fresh immutable SHAs, similar load-driver setups, 1/10/50/200/2560 loads, and repeated steady runs with confidence intervals.

## R5 baseline and R6 local evidence (Linux ARM64, 2026-10-22)

| Test | 2,560 attempted; pre-release admitted | End-of-run full-sample p95 CONNECT | Other evidence |
|---|---:|---:|---|
| Legacy 256, R6 harness | 256 | ~13.14 s | All client attempts eventually complete after release; never exceeded 256 admission |
| AUTO 256 target, original 1s sampling and +50% ramp | 2,560 | ~10.98 s | Peak 2,560 downstream, ~12,806 goroutines and ~214 MB Go heap in joint process |
| AUTO 256, 250ms sampling and 500ms growth cooldown | 2,560 | ~4.44 s | Peak 2,560 downstream, no timeouts/dial errors; ~12,806 goroutines and ~259 MB joint Go heap |

In the final R6 re-run using the corrected before/after reporting, legacy256 admitted 256 before release (p95 among those **256** admitted: ~114ms), shadow256 also admitted 256 (p95 ~110ms), while AUTO admitted all 2,560 before release (p95 among those **2,560** admitted: ~4.435s). All three modes had zero dial errors, and all 2,560 client attempts completed successfully **after** releasing the held tunnels. The two p95 values are over **different admitted populations** and are therefore not a like-for-like latency improvement claim; pre-release success rates are the actual overload differentiator.

The complete-run p95 includes **after-release** successes. The updated harness separately records *pre-release p95*, successful-before-release and completed-after-release numbers; new logs must supersede these initial results. Do not interpret test outcome, controlled local p95, or high connection counts as global throughput improvement; long-lived CONNECT admission is not HTTP RPS.

Existing 200-CONNECT sequential test recorded initial AUTO 32-target ramp ~34s versus explicitly tuned legacy256 ~0.15s. Starting opt-in AUTO at 256 avoids needlessly starving the first wave but **must** be clamped before listener startup on Linux to the measured safe process FD envelope. A Linux process with `RLIMIT_NOFILE=64/128/192` was separately tested using actual `setrlimit` subprocesses; the fixed 256 reserve had broken those limits and is now adaptive.

## Research and algorithm selection

1. **Netflix concurrency-limits / Vegas / Gradient2:** infer request queueing from completion latency, with workload arrival rate and Little's Law. Good for repeated short requests, but a long-lived idle CONNECT/SSE/WebSocket tunnel has no stable request completion RTT; upstream network latency and intentional connection holding are not valid local PXGO admission-pressure signals. Applying Gradient2 directly to tunnel lifetime would shrink during healthy long connections. Use this model for future *observability of short HTTP requests*, never as the sole proxy-wide connection admission governor. Source: https://github.com/Netflix/concurrency-limits
2. **Envoy gradient controller:** computes relative minRTT/sampleRTT plus a headroom allowance and probes baseline minRTT by temporarily lowering concurrency. It explicitly documents transient rejections during measurement. PxGo must preserve authenticated/connection-bound and useful CONNECT tunnels, so a periodic global minRTT clamping experiment is inappropriate for admission by itself. Source: https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/adaptive_concurrency_filter.html
3. **Linux cgroup v2:** `memory.current`, `memory.high`, `memory.max` and memory pressure distinguish memory reclaim from hard OOM. FD headroom alone misses socket buffers, goroutine stack growth, TLS memory and cgroup OOM. Before production AUTO, add a **calibrated resource-envelope term** with cgroup/job-object data that safely handles missing and stale sensors and preserves established connections. Do not use a global MemAvailable alone as a process budget. Source: https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html
4. **Go runtime metrics:** scheduler runnable latency, live goroutine count and Go memory classes can help explain contention. They are *advisory signals*, not independent proof that one more downstream socket is unsafe. Avoid new goroutine pools, custom Go scheduling or per-route admission counters. Source: https://pkg.go.dev/runtime/metrics

## Recommended controller: bounded resource-safe fast ramp

At opt-in AUTO startup, set soft target `S=256`. Before exposing listener sockets on Linux, read actual process FD headroom, compute finite `H_FD`, and clamp effective admission: `E=min(S,H_FD,M)` where M is the optional positive user-defined hard ceiling. Low RLIMIT is allowed to clamp E far below 256. Other platforms currently retain a finite 256 fallback, and **do not claim that Windows can grow or is safe under resource pressure without a verified native handles/memory envelope**.

Use the already owned maintenance loop, not a new scheduler or pool: sample OS FD every 250ms for opt-in AUTO only; keep PAC reload, Kerberos and Guardian on their established 1-second cadence. Increase **only after at least 3 consecutive valid healthy samples plus actual demand** (active near limit or admission waiter), respecting a 500ms growth cooldown. Candidate R6 trial law:

```
S_next = min(H_FD, S + max(32, min(S/2, 512)))
E      = min(S_next, H_FD, manual_ceiling_if_enabled)
```

On repeated independent FD pressure, halve S with a 3-second hysteretic cooldown; after EMFILE/ENFILE temporarily disable new admission and allow at most one post-cooldown probe within the existing soft/hard/manual envelopes when instrumentation is unavailable. Existing tunnels are never torn down to enforce a smaller limit. A lost or contradictory sensor never authorizes increased capacity. Avoid using queued wait time **by itself** as a negative pressure signal, since the gate necessarily causes that wait.

**Non-goals of R6:** do not turn on AUTO by default, do not invent an upper hard limit from CPU count or goroutine count, do not assume 4 FD per connection or 256 reserved FD are production-calibrated, do not add per-upstream governors. The proposed growth schedule intentionally values burst responsiveness at the cost of potential memory expansion; platform-specific memory safety must be independently benchmarked.

## Required gates before full AP-ISS-0035 closure / release

- PR-scoped 2,560 **simultaneous** connection test, legacy256/shadow256/AUTO, Linux+Windows; capture attempts, admitted-before-release, accepted-after-release, errors, p50/p95/p99 and safety invariants.
- Dedicated PxGo-server and load-client processes to attribute RSS, FD/Windows handles, live goroutines, memory/cgroup/Job Object pressure, CPU and scheduler latency separately. Repeat 1/10/50/200/2560 workloads including 1 KiB/10 MiB HTTP, long CONNECT, SSE and auth-sensitive TLS/SSPI.
- Independent stress/saturation with low nofile (64, 128, 192), real EMFILE/ENFILE, memory pressure, recovery, SOAK, no forced existing connection eviction, exact permit release, port/backlog behavior and CI race.
- Native Windows enterprise PAC/WPAD/AD/SSPI data and a calibrated native handle/memory sensor (current repository exposes no usable `pxgo-ad` self-hosted runner). CI on generic `windows-latest` is not a substitute for protected corporate AD tests.
- Security/architecture reviewer PASS tied to immutable SHA, complete release-blocking CI, explicit owner-approved capacity envelope/calibration, protected main and immutable artifact verification. Until then, PR #102 stays DRAFT and **no release**.
