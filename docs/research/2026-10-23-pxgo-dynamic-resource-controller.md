# PxGo AP-ISS-0035 — Dynamic resource loading and PID/control-theory benchmark

**Research date:** 2026-10-23 (Asia/Ho_Chi_Minh). **State:** RESEARCH ONLY / no implementation promotion / no merge or release. Authoritative baseline: `khanhkit/pxgo` PR #102 immutable HEAD `ad326c3bbe8ff5e7840dc8ce2eb61dc0d8f74488` (verify SHA before repeating). Baseline mode `legacy` remains default. Reproducible source and raw measurements are saved in this sibling lab folder and `live/`; no production checkout or KitControl work/claim was mutated.

## Executive result

1. The first instinct to replace AIMD with generic PI/PID based on the observed `g.waiting` counter is **WRONG for the current PxGo admission pipeline**. The `Accept -> acquire` stage serializes admission, so the userspace counter cannot see most TCP backlog SYNs; an actual 2,560-CONNECT Linux benchmark with otherwise identical safety limits admitted only 1,120–1,184 connections by the 18-second gate for these naive variants. AIMD R11 admitted all 2,560. Code is test-only in the isolated lab.
2. An independent **fast-saturation** experiment retaining the same 256 startup, three-good-sample gates, 500ms growth cooldown, OS safety hard ceiling, half-on-pressure and EMFILE fail-closed paths replaces bounded R11 growth `+max(32,min(soft/2,512))` with **`+max(64,min(soft,1024))`** when saturated. Five repeated **2,560 simultaneous-SYN Linux** tests: median p95 accepted CONNECT handshake+initial echo **4.172s R11 versus 2.455s fast**, approximately **41.2% lower**, nonoverlapping observed ranges. Both 5/5 passed 2,560 held tunnels and two bidirectional echo probes, with no errors. This is promising for responsiveness, **NOT evidence of safety under memory/Job Object pressure**.
3. Synthetic control research (7 algorithms x 6 scenarios x 6 deterministic seeds, 250ms steps over 75s) initially favored HPA-style/PI with an **unrealistically omniscient** demand signal. Repeating the exact matrix with serialized-Accept-like weak demand observability reversed its ranking: R11 reached 2,560 in 3.5 *modeled* seconds while pure PI/PID/HPA with unobservable demand **never did**. Oracle simulation cannot substitute for implementation-signal fidelity.
4. Recommendation: keep R11 as the shipping **disabled-by-default experimental AUTO** reference; research fast-saturation in preview alongside independent resource monitoring. Add trustworthy *arrival intensity* and *admission pressure* sensors first; then trial a **guarded PI/PID on *resource pressure / ramp aggressiveness***, not a PID on CONNECT lifetime or raw goroutine count.

## Comparative algorithm research

| Controller | Feedback/actuator | Relevance | Main risk |
|---|---|---|---|
| R11 bounded AIMD | Saturated admission and healthy FD/Win32 sample; grows up to +512/500ms, halves on pressure | Known code, minimal state, correct demand fallback | Burst can take ~4s to 2,560 CONNECT |
| Naive PI/PID on userspace waiter count | e = desired demand - allowed capacity, integral gain, derivative-on-measurement + anti-windup | Works only when *true demand* is observable | Accept-stage serial queue hides demand; confirmed by actual test failure |
| HPA-style proportional | desired/current ratio, 10% deadband and scale-rate limits | Useful for controlled smoothing/stabilization | Needs robust desired demand metric; not directly an application-thread autoscaler |
| Netflix Vegas / Gradient2 | minRTT versus sampleRTT and short-/long-horizon latency trend | Appropriate for repeatable short request latencies | CONNECT lifetime is not latency; false queueing diagnosis and needless suppression |
| Envoy gradient | ((minRTT+buffer)/RTT)*old_limit+sqrt(limit) | Mature HTTP request control | minRTT probes intentionally reduce concurrency, harmful if applied to held tunnels |
| CoDel/PI2 queue-delay AQM | PI update of *actual queue sojourn*, not total connection lifetime | Inspiration for future *measured* admission wait latency control | Queue at OS SYN backlog is currently uninstrumented; packet AQM is not CONNECT admission |
| Fast-saturation / hybrid | Increased bounded rate when saturated **and** verified headroom exists; resource hard gate independent | Empirically faster in 5 live Linux bursts | Resource slope, Windows Job Object and noisy-stale telemetry unvalidated |
| BBR-style model | Estimated supply/capacity and pacing based on measured delivery | Useful mental model for decoupling capacity from demand | Proxy admission is not path bandwidth; no direct 1:1 transfer of TCP gains |

### Sources, with applicability limits

- Netflix concurrency-limits/README, Vegas and Gradient2: https://github.com/Netflix/concurrency-limits (queueing via request completion latency).
- Envoy adaptive concurrency filter gradient/minRTT: https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/adaptive_concurrency_filter.html
- Kubernetes HPA behavior and rate/stabilization/deadband controls: https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/
- Linux PSI CPU/memory/IO stalled-time indicators: https://www.kernel.org/doc/html/latest/accounting/psi.html
- Linux cgroup v2 memory.current, memory.high, memory.max and memory events: https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html
- Go runtime scheduler/memory metrics: https://pkg.go.dev/runtime/metrics
- Windows Job Objects / process memory and job memory limits: https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects and https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_extended_limit_information
- RFC 9332 DualPI2 AQM and PI controller on queue sojourn, **not** proxy CONNECT duration: https://www.rfc-editor.org/rfc/rfc9332.html
- Anti-Windup in PID Control (Caparroz et al, 2026) conditional integration/back-calculation: https://arxiv.org/abs/2606.01959

## Experimental methodology and provenance

Host: existing OCI Linux ARM64 PxGo coding host, **4 logical CPUs**, ~23 GiB host RAM, Go 1.26.10 toolchain per project, no external network during localhost benchmark. Test is `internal/proxy/capacity_burst_test.go`, activated by `PXGO_CAPACITY_BURST_2560=1`, `PXGO_CAPACITY_BENCH_MODE=auto`, `PXGO_CAPACITY_SYN_SPIKE=1`. The pre-release test asserts exactly 2,560 live CONNECT tunnels and verifies initial and post-hold unique bidirectional payloads. Legacy/shadow comparison maintains 256 permits (historical). Host was not emptied of other software loads, so results should not be extrapolated as an unshared dedicated host.

- PID lab based on immutable PR HEAD noted above. Only files in isolated folder `capacity-pid-lab-20261023` were changed for research: local-only algorithm selection `PXGO_RESEARCH_CAPACITY_POLICY` and synthetic safety test. These changes were **not** pushed to PR #102 or enabled in PXGO builds.
- PI/PID/proportional/HPA lab variants use identical 256 startup, existing hard FD/resource/manual limits, healthy-sample gating and cooldown. The only difference is growth increment computed from userspace waiter count or ratio. Source `internal/proxy/capacity.go` in the isolated lab checkout.
- Fast-saturation trial doubles growth more aggressively but remains inside independent hard envelope; strict resource-pressure *test* was synthetic, **not a calibrated native memory test**.
- Five sequential, interleaved repeats each of R11 and fast-saturation with all 2,560 client SYNs started together. Same host, Go test binary, origin and client colocated, 18-second held-tunnel barrier. Runs #1 and #2 use `go test`; #3–5 use compiled Go test binary and `os.wait4()` to measure per-run child CPU and maxRSS (no `/usr/bin/time` installed on host). Results under `live/`.
- The 7-controller *synthetic* matrix models 6 scenarios (2,560 step, pulse, hard resource cliff, sensor outage, mixed demand ramp, idle recovery), each with six deterministic seeds at dt=0.25s over 75s. It additionally sweeps 48 PI/PID gain combinations. It assumes an independent **perfect safety oracle**: results are architectural comparisons, NOT measured latency or actual production FD/memory safety. Results `results/*` (oracle) versus `observable_results/*` (serial-Accept-style partial visibility). Unit: integrated backlog in connection-seconds.

## Live 2,560-SYN burst: five identical-workload repetitions

| Metric | R11 current | Fast-saturation research | Interpretation |
|---|---:|---:|---|
| 2,560 live tunnels and two payload probes | **5/5 PASS** | **5/5 PASS** | Equal functional acceptance in this host trial |
| Pre-release p95 median | **4.172s** | **2.455s** | **41.2% lower for fast** in five interleaved runs |
| Pre-release p95 observed range | 3.938–4.441s | 2.237–2.496s | Small sample; does not establish fleet-wide confidence bounds |
| Pre-release p99 median | ~4.178s | ~2.457s | Burst admission, not per-packet relay latency |
| Max RSS median, compiled runs 3–5 | ~230.7 MiB | ~226.1 MiB | Whole single test process, client+proxy+origin, high uncertainty |
| CPU user+system median, compiled runs 3–5 | ~2.94s | ~2.95s | No demonstrated reliable CPU advantage |
| Resource failure / dropped established tunnel | 0 observed in these runs | 0 observed | **No** production resource pressure was applied |

### Native measured PI/PID failure under the *same* 2,560-client wave workload

| Growth policy (lab only) | Pre-release simultaneous admitted | p95 for ultimately completed CONNECTs | Gate |
|---|---:|---:|---|
| R11 AIMD baseline | 2,560 | ~35.6 ms | PASS |
| Simple proportional from waiters | 1,184 | ~14.0 s | **FAIL** |
| PI from waiters | 1,152 | ~14.1 s | **FAIL** |
| PID from waiters | 1,120 | ~13.7 s | **FAIL** |
| HPA-style from weak observed demand | 1,184 | ~14.0 s | **FAIL** |

These are *single* waves for prototype elimination, versus 5 repeats for final R11/fast comparison. The wave-mode p95 and all-at-once burst p95 are intentionally **NOT compared against each other**.

### Synthetic results: observational bias is a correctness issue

With fictional perfect per-tick demand knowledge, HPA/PI model reaches 2,560 in 2.5 modeled seconds and R11 in 2.75. With serialized-Accept signal visibility, model R11 reaches 2,560 in 3.5 modeled seconds while PI/PID/HPA **never** reach 2,560 during the 75-second horizon; direct Go tests confirm the direction of this failure. The other numerical simulator scores should NOT be treated as physically calibrated benchmarks because estimated client arrival rates, latency, resource costs and safety pressure are synthetic.

## What guarded PI/PID should actually control

Keep an independent **resource-hard-ceiling** evaluator. Admission `E = min(S, H_FD, H_cgroup, H_JobObject, H_manual)`, excluding absent/invalid limits rather than assuming infinity in unknown critical dimensions. The limits must be from the *same* process/cgroup/job, not global host memory. On uncertainty stop increasing and retain allowed existing sockets, with tested one-probe EMFILE recovery. Do not kill established authenticated or in-flight CONNECT tunnels to meet a smaller ceiling.

A possible SECONDARY (not production-approved) PI/PID actuator is **growth-rate modulation** conditioned on persistent saturation and healthy resource slack, rather than absolute soft-window replacement:

```
resource_pressure = max(normalized_fd_pressure,
                        normalized_cgroup_memory_pressure,
                        normalized_job_commit_pressure,
                        normalized_scheduler_stall)
error = target_resource_pressure - resource_pressure
I = clamp(I + error*dt, I_min, I_max)        # integrate only if not saturated
D = EMA((resource_pressure - prev_pressure)/dt)
growth_rate = clamp(base_AIMD_rate + Kp*error + Ki*I - Kd*D,
                    0, max_verified_growth_rate)
if healthy_samples>=3 AND admission_saturated AND sensor_fresh:
    S_new = min(H, S_old + growth_rate*dt)
if memory/FD pressure OR EMFILE/ENFILE:
    S_new = pressure_policy(S_old)          # independent mandatory backoff
```

**Anti-windup:** freeze/reset I when clamped by H, missing/stale telemetry, emergency FD, decreasing envelope or zero demand. `D` is filtered and based on measured pressure (not error) to avoid derivative kick on load steps; never use RTT of an intentionally long-lived tunnel as latency signal. A target CPU alone is invalid because low CPU does not imply memory or socket buffers are safe. Use deadband and downscale hysteresis (Kubernetes HPA ideas), but don't delay mandatory emergency backoff. A *measured* ingress/backlog signal requires independently reviewed changes to the listener/telemetry; do not infer all pending SYNs from `g.waiting`.

## Required further production gates (NOT SATISFIED)

1. Instrument **PxGo-only** process private committed memory, Linux cgroup memory.current/high/PSI, native Windows Job Object memory limit, process handle counts, CPU and scheduler delay (load generator in separate processes/hosts). Calibrate memory/FD/handle slope and safety reserve from 1/10/50/200/2560 workloads; HTTP payload 1 KiB/10 MiB, TLS/SSPI, long-lived CONNECT/SSE, mixed traffic, sustained SOAK.
2. Reproduce dynamic severe memory pressure, low nofile 64/128/192, Windows Job Object process/job-commit limits and sudden cgroup memory.high/CPU throttling; measure oscillation, settling time, p95/p99, admission drops, resource ceilings, existing connection survival and recovery, including sensor outage/malformed input.
3. Native Windows R11 CI confirmed 2,560 held tunnels, but **PID/fast alternative has not run native Windows runtime**. Protected real AD/SSPI needs `pxgo-ad` self-hosted runner (repository count 0 at this session), and cannot be represented as green by standard Windows CI. Audit/owner approval required before promoting any default/releasing.
4. Implement candidate behind a **separate disabled-by-default algorithm experiment flag**, no production default switch. Run full independent code/security review, race-enabled Go tests on toolchain with cgo/compiler, immutable-SHA Linux+Windows stress CI, rollback/backout check, and release contracts. Local `go test -race` could not run on this host because cgo/gcc toolchain is unavailable; do not mark that gate PASS.

## Lab commands (local-only; NOT installed release)

```
cd /home/coder/MCP/PxGo/.hotfix-work-20261022/capacity-dynamic-controller-research-20261023
python3 controller_matrix.py --observability oracle --out results
python3 controller_matrix.py --observability serial_accept --out observable_results
cd ../capacity-pid-lab-20261023
PXGO_CAPACITY_BURST_2560=1 PXGO_CAPACITY_SYN_SPIKE=1 PXGO_CAPACITY_BENCH_MODE=auto PXGO_RESEARCH_CAPACITY_POLICY=r11 go test ./internal/proxy -run '^TestCapacityBurst2560$' -count=1 -v -timeout=65s
PXGO_CAPACITY_BURST_2560=1 PXGO_CAPACITY_SYN_SPIKE=1 PXGO_CAPACITY_BENCH_MODE=auto PXGO_RESEARCH_CAPACITY_POLICY=fast_saturation go test ./internal/proxy -run '^TestCapacityBurst2560$' -count=1 -v -timeout=65s
```

All research artifacts are preserved; raw test outputs, scripts, CSV tables and SHA256 manifest are in this research directory. **No change to PR #102 HEAD, no merge or release.**
