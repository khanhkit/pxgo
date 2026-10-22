# AP-ISS-0035 — Predictive short/long connection classification and native Windows policy benchmark

Status: **STAGED PREVIEW, NOT PRODUCTION**. Branch `experiment/capacity-two-class-windows-20261023` is based on immutable PxGo PR #102 head `fb2aa923a753309dae515fc8be38112f45fbddc8` (R11 algorithm remains default). No merge/release or KitControl ownership changes.

## The key lifecycle constraint

`net.Listener.Accept()` must take a bounded **global physical downstream permit before reading HTTP headers**. At that stage it cannot honestly know if this is a short request or a months-long CONNECT tunnel. Two fully independent semaphore gates at socket accept would either (a) count the same socket twice, (b) allow `shortBudget + longBudget > H_resource`, or (c) deadlock on a short->long migration when long is full. Do not change this invariant.

A global authoritative resource envelope remains:

```
physicalActive <= H_effective
H_effective = min(H_FD / H_JobObject / H_memory, H_manual, H_soft_global)
```

Two **mutually exclusive, soft-quota submanagers** live UNDER that single global gate. New sockets start `provisional`. At the first **authorized** HTTP request, classify HTTP GET/POST/etc as `short`, and predict `long` immediately for CONNECT, WebSocket Upgrade, and `Accept: text/event-stream`. The latter are rules, not a trained ML model. HTTP/1 keep-alive that survives 30s (maintenance sweep) or handles >=8 requests is promoted to `long`. Long is sticky until the underlying TCP connection closes, so it cannot oscillate with request dispatch. A future extension may use a bounded, privacy-preserving per-protocol duration EWMA after research, but source-IP identity/peer secrets must not be used to grant extra physical capacity.

On transition, the manager performs **one atomic decrement of short (or provisional) and one atomic increment of long**, with unchanged global physical admission. If long soft budget is full, classify anyway and account a `long_soft_overflow_events` cumulative events plus `long_current_debt` instantaneous overflow, preserving existing authenticated tunnels rather than blocking under a held global permit. Close is idempotent and releases the class lease and the original *single* physical permit. The `ConnContext` owner lives for the real TCP connection, including `StateHijacked`/CONNECT. The `/PxgoDoctor` capacity snapshot reports `{provisional,short,long,short_soft_budget,long_soft_budget,short_to_long_promotions,long_soft_overflow_events,total}` only when this opt-in experiment is enabled.

Trial target splits soft global window: **short 25%, long 75%**; this is a *hypothesis*, not an independent admission entitlement or finished quota scheduler. It must NOT yet prevent new short connections or allow new long connections beyond global safety. New per-class admission pacing/borrowing requires a separate passed security and fairness design: admission is TCP-first and classification occurs post-header, and global resource pressure must always take precedence. Starvation prevention must reserve handshake/classification budget (not a fixed class-specific hard gate) with bounded wait/fair queue behavior, and never block an existing authenticated CONNECT because a class counter overflows.

## Benchmark on GitHub Actions `windows-latest`

A **separate PR-scoped 4-cell Windows matrix** tests:

- R11 resource-bounded AIMD baseline vs lab-only `fast_saturation` (+max(64,min(soft,1024)), same global hard envelope, FD/Windows native pressure, emergency shutdown guard).
- Each policy with classification OFF and ON. Actual Windows 2,560-CONNECT held-tunnel plateau (32 SYNs every 100ms) twice per cell, with unique initial/post-plateau bidirectional echo. It must prove all 2,560 simultaneously live and `classPools.long=2,560, provisional=0` when classification is enabled; otherwise FAIL.
- Native Windows microbenchmarks `BenchmarkConnectionClassOpenPromoteClose` and `BenchmarkConnectionClassShortToLong`, `-benchmem -count=3`, deterministic migration tests repeated 5 times, and OS/runtime metadata. Upload raw log artifacts on success/failure pinned by immutable PR head SHA.
- Windows SYN backlog spike remains a separate distinct stress gate from held-tunnel plateau; a correct plateau does not prove 2,560 instantaneous SYNs can be accepted under `SOMAXCONN`.

Interpretation must not silently assume performance benefit: 32-SYN pacing can hide a faster controller's advantage. The Windows results serve as safety/correctness and cost evidence; compare against actual held-admission p50/p95/p99 and class-switch telemetry, not only a green workflow.

## Safety / fairness acceptance criteria before promotion

1. Atomic conservation: `provisional+short+long == number of class leases == physical connections on opted-in AUTO` (allow transient observation races between separate locks; use barrier snapshots for assertions).
2. Authenticated CONNECT immediately long, HTTP normal short; websocket/SSE predict long; aged/repeated HTTP short->long exactly once, no demotion, no duplicate permit.
3. Long full while short promotes: class-debt bookkeeping, no forced disconnection or global permit re-acquire. During resource shrink, existing sessions live; new admission respects authoritative global hard envelope.
4. Thread/race: concurrent classify/close/age sweep, duplicate close, server shutdown, HTTP/1 keepalive and hijack. Stress 2,560 tunnel data plane; test auth failure and protected doctor/quit handlers unaffected.
5. Production activation remains blocked by uncalibrated Windows private process/Job Object memory, Linux cgroup PSI, separate load process RSS/CPU/FD/handles, real enterprise AD/SSPI runner (currently missing), and an independent review of fair class-aware admission. Both new flags are strictly research/opt-in.

## Sources

- Go `http.Server.ConnContext` and ConnState lifecycle, including terminal `StateHijacked`: https://pkg.go.dev/net/http
- GitHub `windows-latest` VM specifications (public repo 4 CPU/16GB): https://docs.github.com/en/actions/reference/runners/github-hosted-runners
- Netflix concurrency-limits / Vegas / Gradient2 versus long CONNECT: https://github.com/Netflix/concurrency-limits
- Microsoft Windows Job Object memory quota: https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects
- Research baseline and live Linux 5x measurements: `docs/research/2026-10-23-pxgo-dynamic-resource-controller.md`


Audit R1 correction: when shutdown closes class registration between physical `acquire` and classifier `open`, an absent lease **fails admission** and immediately returns the sole global permit, never passing an untracked connection into `net/http`. `long_current_debt=max(0,long-long_soft_budget)` is distinct from cumulative `long_soft_overflow_events`; neither permits bypassing the global safety gate. Added deterministic shutdown-race and debt-drain tests.
