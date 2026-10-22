# AP-ISS-0035: experimental downstream capacity

Status: **implementation preview; legacy remains the shipped default; no release/default-change approval**. The canonical design is `docs/superpowers/specs/2026-10-15-pxgo-dynamic-capacity-governor-design.md`.

## Configuration

The canonical `max-connections` INI/CLI key (or `PXGO_MAX_CONNECTIONS`, compatible `PX_MAX_CONNECTIONS`) is a **hard manual ceiling**, not a concurrency target. `0` means no manual ceiling. **Manual configuration is NOT recommended** except while diagnosing/mitigating a PxGo resource/performance problem. Legacy `workers`/`threads` remain parsed and round-tripped; they control the existing admission budget **only** in legacy or shadow mode. In AUTO, valid legacy values are ignored for capacity, without weakening numeric parsing.

The only opt-in switch is the **temporary internal** environment variable:

- `PXGO_EXPERIMENTAL_CAPACITY_MODE=legacy` (or unset): existing single shared `workers × threads` admission channel, unchanged by `max-connections`.
- `PXGO_EXPERIMENTAL_CAPACITY_MODE=shadow`: legacy channel remains authoritative; capacity controller samples demand/resources and exposes advisory fields without taking another permit.
- `PXGO_EXPERIMENTAL_CAPACITY_MODE=auto`: one shared dynamic downstream admission gate with a positive manual ceiling if specified. There is no additional per-upstream or transport limit. Use only in controlled tests with rollback to `legacy`.

No new normal-user INI setting exposes the experimental mode. Each admitted downstream socket, including idle keep-alive and hijacked CONNECT, retains a permit until the socket closes. Shrinkage never evicts established connections. A full gate accepts at most one waiting socket per listener, bounded by the existing socket timeout, and uses Go's `net/http.Server.Serve` temporary-Accept backoff. Saturation is *not* an HTTP 503.

## Sensor and controller preview limitations

On Linux, a low-frequency sample uses `RLIMIT_NOFILE` and `/proc/self/fd` as an independent pressure signal, not CPU or goroutine count. The experimental controller reserves at most 256 file descriptors (clamped to half of small RLIMIT_NOFILE values, with a minimum of 16) and estimates four FDs per incremental CONNECT socket. Opt-in AUTO now targets 256 initial permits (owner-requested trial, not a production-calibrated default), subject to an immediate Linux FD-headroom preflight that can clamp it below 256 on constrained hosts. Under consecutive healthy resource samples and queued demand, the soft window increases by max(32, min(soft/2, 512)) each eligible 1-second control tick; independent pressure still halves soft capacity. It does not directly resize goroutine pools. Windows R8 now has an **experimental native process-handle plus available physical/commit-memory sensor** and a finite policy envelope, rather than interpreting these metrics as an FD-equivalent quota. It is not Job Object or enterprise-auth calibrated. Other non-Linux platforms retain the cold-start window without unverified growth. Unsupported/invalid samples inhibit growth, not healthy connections. EMFILE/ENFILE closes *new* admission with recovery hysteresis rather than restarting Guardian.

**These numeric reservations are provisional experimental assumptions, not validated release thresholds.** Hardware, OS, upstream transport/auth mode, environment, FD/socket mix, memory and resource limits still require measurement. No switch to AUTO by default until the full AP-ISS-0035 Linux/Windows benchmarks and review gates establish calibrated fallback limits, admission wait bound, hysteresis and envelope constants. Long-lived tunnel pressure may be substantially different from short HTTP keep-alive workloads. Do not claim a performance gain or corporate Windows auth parity from a local benchmark.

Doctor's existing restricted `/PxgoDoctor` adds an ordinary `capacity` snapshot. During admission saturation a *new* Doctor connection can also be blocked; no bypass or privileged diagnostic port exists.

See [the 2,560-connection research, comparable benchmark methodology and R6 trial algorithm](capacity-2560-research-20261022.md). The R6 owner-requested trial starts AUTO at 256 (subject to Linux startup FD preflight) and samples every 250ms while keeping non-capacity housekeeping at 1s. Healthy sustained demand causes bounded proportional growth every 500ms; this is **not** a production-calibrated operating policy. Linux/Windows 2560-connection comparative tests are required before considering a production default.

## Required evidence and rollback

Use the existing `docs/benchmarking.md` harness, retained raw artifacts and binary hashes. Run the same immutable baseline versus `legacy`, `shadow`, and opt-in `auto` with at least 1, 10, 50, 200 downstream clients, HTTP 1 KiB/10 MiB, long-lived CONNECT, SSE, and soak. Record p50/p95/p99 wait, throughput/success, peak RSS/FD/handles/goroutines and recovery time, including resource-pressure injection. Windows WinHTTP/PAC/WPAD, NTLM/SSPI and authorized AD endpoints are separate mandatory platform gates for corporate release claims. Measure raw variance; micro-benchmark noise alone is not evidence of improved performance. Existing TCP/proxy auth and Guardian semantics must remain unchanged.

Rollback: unset `PXGO_EXPERIMENTAL_CAPACITY_MODE` (or set `legacy`) and restart the test instance. This re-enables the exact previous admission budget; do not modify/roll back authenticated upstream transport or saved user configurations.
