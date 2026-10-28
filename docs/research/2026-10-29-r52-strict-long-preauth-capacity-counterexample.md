# PxGo R52 — Falsifiable real-TCP proof that opt-in long-intake guard cannot guarantee short liveness under preauth saturation

2026-10-29 Asia/Ho_Chi_Minh. PR#103 exact baseline R51 immutable HEAD `09826ddfedc1b462ca23b04ae2137d932ce81175` (open DRAFT), PR#102 exact base `fb2aa923a753309dae515fc8be38112f45fbddc8` (open DRAFT). This R52 adds ONE new Go TEST file only plus this research/evidence document. **No production runtime, scheduler, governor, class predictor, shipped legacy/default or CI workflow changes. No merge/tag/release.**

## Why this test matters

R51 introduced guarded opt-in `PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE=1`, valid only in AUTO mode with connection classes enabled. After HTTP authentication, new explicit long CONNECT/WebSocket/SSE classification is denied HTTP 503 when tracked long admissions already reach 75% of the current real physical effective window. It does not reserve physical short slots *before TCP Accept*, because `capacityGovernor.acquire` takes one global physical token on accept, and HTTP class prediction and authentication occur only later. R51 documentation correctly warned it does **not** guarantee short progress under all-long/provisional saturation, but most real on-wire tests cover 12 long + next short succeeding while the remaining four physical slots are empty.

This new TEST-only negative gate makes the limitation reproducible on genuine TCP+HTTP with no source runtime change.

## Exact native falsifiable protocol

1. Opt-in modes scoped with `t.Setenv`; AUTO, class tracking ON, strict long intake ON, `MaxConnections=16`. On a real listening PxGo server, echo upstream origin and authenticated loopback CONNECT handshake, hold **12 concurrently established** long CONNECT TCP sockets. Require class snapshot `Long=12, LongSoftBudget=12, ShortSoftBudget=4`.
2. Open **4 additional raw real TCP connections** to PxGo that send no headers whatsoever (deliberate pre-auth slowloris). Wait for a snapshot **ActiveDownstream=16, Long=12, Provisional=4** before issuing next request. All 16 active physical slots are now occupied though nominal short class budget is four.
3. On a separate real TCP connection, send a legitimate ordinary short HTTP GET via proxy to a running `httptest.Server`. Require authoritative `AdmissionWaits` counter to increase and **no HTTP response** for at least 150ms, not merely a sleeping producer goroutine.
4. Close exactly **one provisional** TCP socket; without disconnecting any authenticated long tunnel, require the short GET to return HTTP 200 with correct origin body within four seconds.
5. Verify all twelve long leases remained counted and `LongIntakeDenied=0` (ordinary short was never incorrectly rejected by long quota). Single coordinated cleanup closes all clients, probes **physical=0 + class total=0** *before* server shutdown, then bounded server shutdown and echo worker join. Test has no custom capacity semaphore or simulated kernel permits.
6. Positive Go marker `STRICT_LONG_PREAUTH_RESULT capacity=16 long_held=12 provisional_before=4 short_admission_wait_proven=true short_blocked_before_release=true short_succeeded_after_release=true long_unchanged=12`. This marker proves a **negative short-liveness counterexample**, not fair progress.

## Test development and host verification

- Initial isolated staging fixture created exactly on R51 source but its cleanup attempted `Close()` on a deliberately set-nil released provisional, resulting in a recovered panic despite the expected counterexample marker. This was a real **test harness bug**, not a PxGo runtime failure. Retained here as historical local test development evidence, and corrected with explicit nil-safe cleanup.
- Added pre-shutdown conservation check so a post-failure cleanup path must drain all native 16 physical/class permits to zero. The test now PASS.
- On disposable exact-R51-sha `git clone` with all caches isolated to /tmp and original dirty checkout untouched:
  - `gofmt -l`: PASS.
  - `go test ./internal/proxy -run ^TestStrictLongIntakeProvisionalSaturationDocumentsNoShortLiveness$ -count=75`: **75/75 PASS**, 14.635s total on host Linux ARM64 (CGO disabled).
  - Existing/new strict suite `go test ./internal/proxy -run ^TestStrictLongIntake -count=5`: **PASS five repeated groups**, 1.156s.
  - `go test ./... -count=1 -timeout=240s`: **all packages PASS**, including main/guardian/DoH/proxy.
  - `go vet ./...`: PASS.
  - `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy`: PASS.
  - `git diff --check`: PASS. All source is test-only.
- Host checked at 2026-10-29 06:52 ICT: dirty original `/home/coder/MCP/PxGo` WIP unchanged; disk ~14GiB free (~93% used). Official KitControl `./bin/kit-safe-disk-cleanup audit --scope-root /home/coder/MCP` returned **no authorized candidates**, so no cleanup/prune/Docker mutation. Current board watcher STOPPED; no actor/claim/lease was taken.

## Scope and governance

R52 confirms the existing R51 strict *long-class admission* is NOT a strict physical short reservation. Only after an existing provisional TCP client relinquished a physical token did the new short complete. This does NOT establish a practical short-fair design, nor does it prove liveness in the presence of untrusted slowloris or long debt. A future production design must provide bounded preauth intake, a real physical reservation/scheduler that accounts for socket resources BEFORE knowing class, timeout/fail closed, non-eviction of authenticated longs, single global permit, and tests for multiple authentication/upstream transports.

GitHub exact-SHA R52 full CI and independent separate strictly READ-ONLY KitAuditor source/security verdict are REQUIRED before marking new test gate complete. Official machine-bound KitControl Architect lifecycle bridge returned exit2 even in `dry_run:true` while requesting an independent KitAuditor, so **no independent reviewer was spawned**; don't impersonate or substitute this self-analysis for auditor.

Protected enterprise AD/SSPI still conditionally SKIPPED, naturally exhausted Go heap under effective nested Job Object limits, 2560 WAN multi-hour soak, Linux cgroup PSI and real class-aware physical fair scheduler remain production blockers. Both PRs stay DRAFT; release BLOCKED.
