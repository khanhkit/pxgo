# PxGo AP-ISS-0035 — Native Windows process-isolated resource benchmark R12

**Status: research-only, experimental flags disabled by default; NOT merge/release proof.** Based on PR #103 immutable parent `8c98efee88b268eca53dea08d2be704b0fcb3970` (R11 Windows network CI `40068473870`, all 43 jobs enumerated: 41 success, 2 conditional skip). This continuation adds a new **process-isolated measurement gate** without modifying production code or relaxing any network success invariants.

## Measurement design and why it matters

Earlier 16 x 2,560 held-CONNECT, 128 native Windows mixed HTTP/CONNECT, competing admission, staggered lifetimes, shared-hard-cap borrowing, and active 10 MiB two-way payload tests run proxy, load clients and origins in the **same Go test process**. Shared heap/RSS/goroutine count from those results is **not attributable to PxGo alone**. It cannot establish a responsible per-tunnel memory/handle quota for production Windows AUTO.

`TestCapacityIsolatedProcessResourceBench` launches a **distinct child PID containing PxGo only**, using the exact same compiled Go test binary. The Go test parent contains all client TCP dialers plus a local HTTP origin and bidirectional TCP echo service. The child binds only loopback and receives control messages through an inherited anonymous pipe (`SNAP` and `QUIT`), without exposing a remote control endpoint.

The parent first samples idle child metrics, then establishes and holds **256/320/384 live CONNECT tunnels** (seed 0/1/2) with verified unique bidirectional echo. During the plateau it issues **400/500/600 real short HTTP GETs**, maximum 16 concurrent client dials, and samples the child PID every 100ms. Before closing held tunnels, it verifies every long connection's data plane again, captures the child's own capacity/class counters and enforces single-permit conservation, then closes all tunnels and asserts full drain. Any HTTP or CONNECT error or missing child-resource counter **FAILS** the job. Parent child exit must be normal; no silent success from a killed process.

### Measurements collected **from the child PID only**

- **Windows**: Win32 `GetProcessMemoryInfo(PROCESS_MEMORY_COUNTERS_EX)` working set and **PrivateUsage / process-private commit**, `GetProcessHandleCount`, and `GetProcessTimes` user+kernel CPU nanoseconds. The child process `OpenProcess` handle is explicitly closed each sample. No host-wide GlobalMemoryStatusEx or co-located client heap is substituted for these measurements.
- **Linux**: `/proc/PID/status` `VmRSS` (resident bytes) and `RssAnon` (resident anonymous bytes, a **different** definition than Windows process-private committed bytes), and `/proc/PID/fd` open descriptor count. Linux CPU delta is **unavailable** in this test and reported as zero; do not compare that with Windows CPU.
- Each run reports `held_long`, `completed_short`, short HTTP offered-load p95/p99 and throughput, child `baseline_*`, `held_*` and `peak_*` metrics, CPU delta on Windows, and the actual child resource hard envelope. Samples may undercount transient peaks shorter than the 100ms polling period.

This is still a Go **test executable** in the child, not a stripped release PxGo executable. The baseline-subtracted delta is useful for experimental trend comparison but includes test-binary overhead and any ancillary PXGO runtime work. The peak CPU/RSS numbers are not cross-VM matched; Actions runs use different shared-cloud hosts.

## Native Windows CI matrix

An additional **12 fresh windows-latest jobs** cover `R11` vs `fast_saturation`, class accounting ON vs OFF, seed 0/1/2; each job uses a new GitHub hosted Windows VM to avoid TIME_WAIT contamination across unrelated runs. Tests are opt-in and located in `internal/proxy/capacity_isolated_process_test.go` and two platform-specific resource readers. Each job verifies immutable PR HEAD SHA, native platform (not cross-compile), every socket and short body, and uploads its exact-SHA raw benchmark logs for audit. Existing 43 legacy/mixed research jobs remain unchanged.

## Production limitations and next gates

These native measurements can calibrate plausible CPU/handle/private-commit slopes, but **do not constitute an OS-enforced process budget**. Need Windows Job Object `ProcessMemoryLimit`, `JobMemoryLimit` and limited job CPU measurements under real pressure; Linux cgroup `memory.high`/OOM and PSI; separate-process WAN TLS/SSPI, HTTP/2 and long-duration soaks; and a protected AD/SSPI runner. Current 25/75 short/long pool budgets remain **advisory accounting**, not two independently fair admission semaphores. The native enterprise SSPI gate remains conditionally skipped. **Keep both PR #102 and PR #103 unmerged and unreleased** until independent review plus those protected gates are satisfied.


## R13 independent audit corrections

Independent strict read-only KitAuditor on immutable R12 commit `396d6c793d4c9a109323e3e07e598d28a2238225` found that the previous sampler began **only after** all long CONNECT sockets were established, so its `peak_*` numbers could undercount admission-ramp spikes. The new independent child-PID sampler starts before the first dial, samples every 25ms throughout ramp, mixed load and actual release/drain, and records counts and private-memory peaks **per phase**. It fails on telemetry errors and on missing samples for any measured phase. Polling still cannot prove instantaneous sub-25ms peaks do not occur.

The audit also raised a teardown deadlock scenario: close listeners and wait for echo goroutines before closing the held client connections. Although the original Go defers were LIFO and therefore closed held clients first, the R13 code uses ONE coordinated cleanup that explicitly closes every owned long client before stopping the TCP echo listener and joining workers. A nested real negative test injects `t.Fatal` after actual CONNECT establishment and requires prompt process exit with the exact expected failure marker, not a timeout. The two Windows `classes=on,seed=0` jobs run this fault injection automatically. Linux `/proc` status parsing now requires a present, well-formed `VmRSS` and `RssAnon` with `kB` units rather than silently coercing parsing errors to zero. No production governor/flag/runtime behavior changes.


## R14 Windows saturation sampling — eliminate accumulated VM-local resource contamination

Exact R13 SHA `f12a89d0c96a9bd75b7e0adaa1e51c0a4849cddb`, workflow `40082795761` had a real Windows research failure in the **fourth** consecutive 2,560 held-CONNECT repetition for R11 class OFF: the first **3/3** admitted all 2,560, but run 4 admitted 2,559/2,560 due one native `bind` error `WSAENOBUFS` (Windows reported insufficient buffer space/queue). The test gate correctly FAILED; do not treat that result as success or silently retry it. Reusing the same VM/ephemeral TCP endpoint pool across four heavyweight 2,560-CONNECT cycles is consistent with host TCP resource contamination, though the exact socket-state transition was not independently measured for that failed job. This is not proof of a PxGo admission bug or proof that Winsock alone is responsible.

R14 changes only CI test-runner topology and documentation: replace the four sequential heavy runs per policy/class with a `sample=[1,2,3,4]` matrix and a separate **fresh windows-latest VM** per sample (2 policies × 2 class modes × 4 samples = 16 independent native saturation jobs). Each sample still requires **exactly all 2,560 simultaneously admitted held CONNECTs** with post-plateau unique echo, and uploads its own immutable-SHA log, including failures. The existing 32 isolated mixed seed jobs, 12 process-PID resource jobs and all core platform/DoH gates remain. Total expected GitHub jobs changes from **55** to **67**, including the same two conditional skips; do not infer success until all 67 are enumerated and all 65 required jobs are green. The isolated resource measurements remain on the 25ms R13 sampler.
