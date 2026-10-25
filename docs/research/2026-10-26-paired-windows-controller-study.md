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

## R17 audit→fix R18, native test CLI and strict measurement integrity

The first native Windows CI run of new R17 paired jobs `40217239922` failed during **test startup**, before any HTTP/CONNECT load. Windows PowerShell split unquoted dotted Go test flags into an invalid `-test` token: exact raw job output `flag provided but not defined: -test`, followed by test usage; this is **workflow invocation**, not a governor regression. R18 passes complete quoted `-test.run=...`, `-test.v=true`, `-test.count=1`, and `-test.timeout=95s` string arguments via PowerShell array splatting and runs a compiled-binary `-test.list=...` preflight before workloads. No failure is silently waived.

The separate independent read-only KitAuditor [R17 critic](https://chatgpt.com/g/g-p-6abb7c46ecfc8191b18944f17d81ada2-pxgo/c/6ade3e39-3168-83ec-a658-5b41c707ac9c) returned REVISE due to underspecified benchmark result checks. R18 adds `policy=` and `classes=` provenance **to the native Go test's own MIXED_RESULT row**, plus a stdlib-only `scripts/verify-capacity-pair.py` enforcing exact profile, seed, both policy identities, class mode, single complete row, positive and conserved workload, all p50/p95/p99 and offered p95/p99, RPS, physical permit conservation, and strictly equal offered load in the two policy runs. It emits typed, machine-readable matched deltas without making causal claims. At least 19 positive/negative parser unit fixtures run on Windows before paired execution; local Linux compiled Go test exercised R11 and fast against the **same real** balanced-profile workload and the strict parser successfully compared equal cohort counts.

This is a **test/CI/evidence-only** change; it is not a runtime algorithm modification. Follow-up exact-SHA Windows CI and another independent audit are required before any benchmark conclusion. The order/TIME_WAIT effect remains an explicit limitation even on same-host pairs.

## R19 same-seed factorial order replication

R18's 16/16 Windows pairs verified identical cohort and complete HTTP/CONNECT data planes, with a descriptive median fast-minus-R11 offered p95 of **+13.09ms**, 11 paired profiles favoring R11 and 5 favoring fast. That result is **NOT CAUSAL**, and the independent auditor's order warning remains meaningful: seed 0 always ran R11 first and seed 1 always fast first. R19 crosses each of the **same two seeds with BOTH orders on different fresh Windows VMs**: 4 profiles × 2 classification modes × 2 seeds × 2 policy orders = **32 matched pairs, 64 native policy test processes**. Every pair still runs only two workloads per VM to minimize TIME_WAIT carryover, and no task/production code or default is changed.

The fail-closed paired validator now checks the actual execution manifest (`PAIR_POLICY` entries with profile, seed, class, policy, numeric TIME_WAIT-before count) against the requested order, separately from each native Go test's own policy provenance, and preserves both exact raw logs and typed JSON. Three new negative/positive test fixtures verify reversed order, rejected incorrect order and rejected missing baseline telemetry. Comparisons must be stratified by **policy execution order** as well as by profile/seed/class. A different run/commit MUST not be silently pooled without an explicit immutable-source equivalence check.


## R20 audit correction: ambiguous native execution-order manifest

Independent read-only critic replayed malformed PAIR_POLICY data against R19 immutable source `ced504de7cc44c8e8f1793f81a6ddb0ee0004bad` and demonstrated a **false validator PASS** with two duplicate `policy` keys per line and impossible `timewait_before=-2`. R19 used `dict(TOKEN.findall(line))`, silently keeping the *last* duplicate, and checked only that TIME_WAIT matched a signed integer, not that the value represented a real count or its documented `-1` unavailable sentinel. This is a **benchmark evidence integrity** defect, not evidence that real Windows runner logs are forged or that PXGO runtime is unsafe.

R20 fail-closes duplicate/missing/ambiguous native order fields, permits only >=0 actual TIME_WAIT or explicit -1 sensor-unavailable, and persists **observed policy order plus both typed per-policy native TIME_WAIT counts** in paired-evidence.json. Four deterministic positive/negative fixtures reproduce and regress the error. The study's 32×2 crossed-order workload and controller source remain unchanged. Native Windows CI must execute on immutable R20 SHA and pass again; do not inherit R19's workflow outcome.


### R20 independent critic revision: full-record parsing

Independent staging KitAuditor critic first returned **REVISE**: despite duplicate-key protection, `TOKEN.findall` silently ignored arbitrary non-token text embedded inside a PAIR_POLICY record. This was an additional false-PASS risk. The corrected source now **full-matches exactly the five fields emitted by the fixed PowerShell workflow**, with no extra token, garbage or ambiguous provenance. Additional negative fixtures reject inserted fragments and malformed fields; a positive CRLF fixture ensures native Windows line-endings remain supported. Re-audit this revised staging source and require exact immutable Windows CI again before accepting the validator.

## R21 evidence-integrity audit and full native R20 paired replay (2026-10-26)

**Authoritative benchmark source:** immutable PR #103 SHA `22d05d66d06538c1e00cd4b6d9474be6a36c7a49`, [GitHub Actions run 40220215522](https://github.com/khanhkit/pxgo/actions/runs/40220215522), **103 jobs enumerated with REST pagination, 101 success, two conditionally skipped** (native AD/SSPI and unrelated previous burst). All **32 same-host matched Windows pair jobs SUCCESS**, crossed by profile (4) × classification (2) × seed (2) × order (2); 64 policy trials.

R20's full-line `PAIR_POLICY` parser and Windows CI passed, but independent local adversarial replay against R20 `scripts/verify-capacity-pair.py` proved FOUR false-PASS cases in the `MIXED_RESULT` parser: embedded arbitrary garbage, trailing garbage, non-field prefixes, and malformed field delimiters. Earlier R20 `TOKEN.findall` scanned metric tokens while skipping invalid substrings, even as R20 had already fixed the separate order-manifest parser. The R21 fix requires a **full-record, anchored 21-field native Go `capacity_mixed_benchmark_test.go:<line>: MIXED_RESULT`** match before accepting any metrics. It additionally rejects internally contradictory percentiles `p50 greater than p95`, `p95 greater than p99`, offered p95 greater than offered p99, or offered p95/p99 lower than service p95/p99. Regression fixtures cover corrupted fields, injected fragments and native CRLF. R21 changes are **benchmark validation only**, not runtime governor, quota policy, test workload or default mode.

**Independent replay of R20 raw Windows log data:** Retrieved both original `MIXED_RESULT` lines from each of all **32 exact-source-successful Windows paired Actions job logs**, normalized the GitHub log timestamp prefix, and parsed all **64 native output rows** using the stricter R21 staged parser. Every record was accepted; matched profiles/classes/seeds and all four offered-load counts remained equal across the two policy runs. The full original Go test PASS and genuine observed order manifest were already release-gated by the successful R20 CI; this replay validates metric parser compatibility, and does NOT independently reconstruct `PAIR_POLICY` TIME_WAIT records from downloaded job artifacts. A reviewer must separately inspect the original artifacts to independently confirm TIME_WAIT carryover and order; do not substitute an invented manifest. Downloaded native metric excerpts, test reproduction script and sha256s were preserved in isolated host staging under `.hotfix-work-20261022/capacity-r21-evidence-20261026/`, not in a production artifact. Persisted normalized rows with exact **job ID, job URL and SHA**: [`2026-10-26-r20-native-windows-paired-replay.csv`](2026-10-26-r20-native-windows-paired-replay.csv), and [`summary.json`](2026-10-26-r20-native-windows-paired-summary.json).

### 32 actual Windows pairs: fast-minus-R11 short offered-p95

| Stratum | N pairs | Median delta (ms) | Pairs favor fast | Pairs favor R11 |
|---|---:|---:|---:|---:|
| All matched pairs | 32 | **+7.866** | 12 | 20 |
| R11 first | 16 | **+23.631** | 3 | 13 |
| fast first | 16 | **−4.495** | 9 | 7 |
| classes ON | 16 | +0.361 | 8 | 8 |
| classes OFF | 16 | +15.356 | 4 | 12 |
| short_heavy | 8 | +3.052 | 3 | 5 |
| balanced | 8 | +6.265 | 3 | 5 |
| long_heavy | 8 | +14.762 | 3 | 5 |
| churn | 8 | +15.838 | 3 | 5 |

Delta=fast-saturation p95 minus R11 p95; positive favors R11. The large **change of sign conditional on policy execution order** is essential. These are descriptive paired comparisons on different GitHub-hosted Windows VMs, with at most two runs per VM and nonzero TIME_WAIT/caching carryover; they **do not prove a causal or statistically significant R11 advantage**. In particular **do not promote fast-saturation** based on homogeneous 2,560-CONNECT burst p95 or this exploratory pair study. The class budgets remain advisory, and neither Windows Job Object pressure nor protected AD/SSPI has been independently passed.

**Gates:** R21 staging Python 33/33 PASS, actionlint, Go full suite/fmt/vet/lint 0 issues PASS. Independent R21 critic source audit returned PASS, with warnings to replay actual native Windows rows (completed as above) and obtain fresh exact-SHA Actions (still required after R21 commit). Keep PR #102 and PR #103 DRAFT, do not merge/release.
