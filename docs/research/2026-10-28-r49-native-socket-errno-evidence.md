# PxGo R49 — Native Windows socket errno in failure-only mixed benchmark diagnostics

Date: 2026-10-28 (Asia/Ho_Chi_Minh). Source parent PR #103 R48 immutable SHA `3b05fc53f92ed2558fcb5b1e53540ebf3ec564ac`, PR #102 base SHA `fb2aa923a753309dae515fc8be38112f45fbddc8`. Both PRs OPEN DRAFT, main/legacy/default unchanged; no runtime code or Go production dependency added.

## Historical evidence, why this matters

R31 native Windows mixed test attempt1 had two failures due loopback socket bind error 'system lacked sufficient buffer space or because a queue was full' after 10,256 and 12,298 TIME_WAIT; same SHA failed-job retry on fresh hosted Windows VMs succeeded. R34 repeated failed workloads on eight fresh VMs 8/8 PASS; these results do not determine exactly if Windows 10055 WSAENOBUFS, 10049 WSAEADDRNOTAVAIL, socket buffer pressure, ephemeral local-port allocation or loopback artifact caused failures. R46 exact SHA first-policy fast-saturation short-heavy class OFF seed1 native failure after 557/558 short HTTP, TIME_WAIT=1,583 on same runner, warns high preexisting TIME_WAIT is not necessary for same diagnostic string. Do not claim all 502 errors are Windows socket failures.

## Narrow test-only change

Apply only `internal/proxy/capacity_mixed_benchmark_test.go` and `capacity_mixed_competing_test.go` on exact R48 head:
1. `capacityNativeSocketErrno(error) int` uses Go stdlib `errors.As(err, &syscall.Errno)` to return native errno, or **-1** when no typed native errno is present. No OS env or production traffic instrumentation, no new deps.
2. In benchmark **failure-only** `short request failed` diagnostics add `native_errno=NUM`, without changing Go test pass/fail, workload, offered concurrency, timeout, class policy, metrics or positive log markers.
3. In competing-admission **failure-only** terminal check add `native_errno=NUM` before the original first error. When the error is an HTTP 502 application string rather than an actual wrapped network `error`, numeric value is **-1**; this is an important limitation, not proof a native error did not occur upstream.
4. Deterministic table-driven test covers nil => -1, `fmt.Errorf("%w", syscall.Errno(10055))` => 10055, `net.OpError` wrapping `os.SyscallError("bind",syscall.Errno(10049))` => 10049, and application text => -1. Do not parse localized error strings or infer code 10055 from English text.

## Local validation on isolated exact R48 HEAD clone

- Exact source HEAD verified `3b05fc53f92ed2558fcb5b1e53540ebf3ec564ac`.
- Source-only test diff two files (no runtime/CI YAML).
- `gofmt -l` clean, `git diff --check` PASS.
- `go test ./internal/proxy -run '^TestCapacityNativeSocketErrnoExtraction$' -count=200`: **PASS 200 repeats**.
- `go test ./... -count=1`: **all packages PASS** including main and proxy.
- `go vet ./...`: **PASS**.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy`: **PASS**.
- Initial attempt using shared host module cache failed due read-only filesystem on module lock paths; reran with `GOMODCACHE=/tmp/pxgo-r49-gomod-20261028`, `GOCACHE=/tmp/pxgo-r49-gocache-20261028`, preserving original checkout and shared cache; successful tests above are authoritative.

## Scope and remaining gates

R49 only improves reliability of future failure logs. It does not fix Windows socket exhaustion, does not make R31/R46 historical failures green, and does not prove a causal bottleneck or class-aware fairness. Does not alter R48 runtime class budget update. All native Windows 2560, Job Object, protected enterprise AD/SSPI, WAN soak and Linux PSI gates must still be checked on immutable exact SHA. A separate strict READ-ONLY KitAuditor **actual independent reviewer** is still required; this report and author's checks cannot claim independent audit. Keep PRs DRAFT; no merge, release, tag or experimental default activation.
