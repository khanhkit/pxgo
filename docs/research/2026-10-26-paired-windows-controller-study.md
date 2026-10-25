# PxGo AP-ISS-0035 — R17 same-runner paired native Windows controller comparison

Status: **research-only CI benchmark; no production behavior change**. Base source SHA `107aec51a747c2b62669a6a2bb1c915daf3a978a` (which preserved the audited R16 code `e3376665470a6c2aabd163824c13f8231cf288ad`); actual R17 immutable commit/CI and results must be recorded after execution. Both PR #102 and experimental PR #103 remain DRAFT, and legacy stays the default.

## Motivation

R16 produced a well-grounded 128-row mixed-load matrix and 16 separate-process Windows resource measurements. Those are **different VMs per governor**, and each seed corresponds to a different offered load. Comparing median p95 between R11 and fast-saturation across independent hosted VMs does not establish causal superiority. In fact, observed mixed short HTTP p95 medians often favor R11 with classification enabled, contradicting the earlier homogeneous 2,560-CONNECT fast ramp result. Test the policy contrast **within the same machine under the same workload**, and make any observed latency changes explicitly conditional on TCP port / TIME_WAIT state.

## Controlled design: sixteen fresh Windows VMs, thirty-two independent test processes

- Matrix: 4 real mixed profiles (`short_heavy`, `balanced`, `long_heavy`, `churn`) × classification OFF/ON × two seeds (0/1): 16 matched-host *pairs*, 32 successful workload trials required. These add to the existing 16×2,560 held-CONNECT, 128 mixed scenarios, 32 competing-admission, 32 concurrent active-payload, and 16 process-isolated capacity benchmarks. They are not substitutes for those functional gates.
- Each pair checks out exactly `github.event.pull_request.head.sha` and compiles the **same Go test binary once**, then runs separate fresh OS processes with `PXGO_RESEARCH_CAPACITY_POLICY=r11` and `fast_saturation`. Keep `PXGO_EXPERIMENTAL_CAPACITY_MODE=auto`, class flag, profile name, seed, HTTP/CONNECT counts and Go binaries identical within the pair.
- Order is **AB or BA** based on seed parity: even seed R11→fast, odd seed fast→R11. This balances a basic first-run/TIME_WAIT effect but does not eliminate carryover. Each pair gets a new Windows-hosted VM; there is no 2,560 bulk tunnel saturation on that VM first.
- Test `TestCapacityMixedTrafficBench` must log exactly ONE `MIXED_RESULT` row per process. Enforce `short_attempted == short_completed`, exact CONNECT payload integrity, global/class permit conservation and eventual drain; nonzero exit or missing/duplicated metric is a strict failure.
- Persist raw test logs, runner OS/Go/CPU, execution order, native Windows TCP TIME_WAIT counts before each policy, IPv4 port-range-before and grouped TCP states after, under SHA-qualified 14-day GitHub Actions artifacts. Never widen ephemeral port ranges, force TCP RST or discard a second-policy failure.

## Analysis requirements

Parse matched job logs by precise job ID and immutable SHA; check both policy rows have identical offered counts for the same profile/seed/class. Calculate per-pair **Δp95, Δp99, ΔRPS**, report median paired Δ and per-profile signed results, count positive/negative pairs. Show order effects separately. Median across only two seeds per class/profile is exploratory; do not invent a significance p-value or claim fleet-wide improvement. If TIME_WAIT or job variance confounds the second run, disclose it and require additional repeats or a fresh-host design. The main test co-locates proxy, client and upstream so its Go heap/goroutines remain **shared**, not PXGO-private; process-isolated R16 evidence remains the source for resource-envelope work.

## Separate unresolved acceptance gates

Native enterprise AD/SSPI protected runner, enforced Windows Job Object commit/memory limits with deny/recover semantics, production TLS/HTTP2/SSE/WebSocket/multi-hour tail tests, high-load WAN variance and **real class-aware admission fairness (25/75 are advisory soft counters)** remain unverified. This R17 workflow changes CI/documentation only: does not implement new quota admission or switch the default, and must independently pass audit and the complete exact-SHA CI before it is considered credible evidence.
