# PxGo R50 — Exact-SHA CI goconst fix for R49 native socket-error test

Date: 2026-10-28 ICT. Parent PR #103 R49 immutable SHA `d8a1f0ff3b0fc7a535e907dfe17a12e7e178991c`, base PR #102 `fb2aa923a753309dae515fc8be38112f45fbddc8`. Research PRs remain DRAFT, no merge/release/default promotion.

## Actual R49 failure (preserved)

R49 native errno test-only addition successfully passed local 200x extracted-errno tests, full Go packages, vet and Windows amd64 crosscompile, but GitHub Actions `40717496364` exact SHA Ubuntu job `122919047532` FAILED at `golangci-lint` due two `goconst` issues reported in otherwise unchanged production runtime files: `internal/proxy/proxy.go:153` repeated string `tcp`, and `internal/proxy/supervisor_runtime.go:166` repeated `dial`. The new `&net.OpError{Op: "dial",Net: "tcp",Err: &os.SyscallError{...}}` test fixture increased their package-wide literal counts beyond configured lint threshold. This is a REAL source/CI-quality failure, not runner flake; do not rerun R49 to conceal it. Preserve exact original run/log.

## Narrow one-file fix

Change ONLY `internal/proxy/capacity_mixed_benchmark_test.go` (Go TEST file) to omit `Op` and `Net` string labels from constructed test `net.OpError`. The nested `Err: &os.SyscallError{Syscall: "bind", Err: syscall.Errno(10049)}` remains intact, so the code still tests `errors.As` correctly through standard-library nested wrappers. No runtime code, workload, CI YAML, dependencies, native errno extraction logic, positive metric assertions or other test changes. The test explanatory comment explicitly states these unrelated Op/Net labels were never part of the asserted semantic.

## Direct validation on exact isolated R49 parent

- Source HEAD exactly R49 SHA, original dirty project checkout untouched.
- `gofmt -l` clean; `git diff --check` PASS.
- `go test ./internal/proxy -run '^TestCapacityNativeSocketErrnoExtraction$' -count=200`: PASS.
- `golangci-lint run ./...` (host version 2.13.2, CI 2.12.2): **0 issues**, exit 0. Host cache was read-only, so linter emitted benign cache write warnings; analysis result still zero issues. Use GOLANGCI_LINT_CACHE=/tmp for future.
- `go test ./... -count=1`: **all package tests PASS**.
- `go vet ./...`: PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy`: PASS.
- Native exact-SHA R50 full GitHub Actions run still required after creating immutable commit; do not mark green from local checks.

Historical R31/R46 Windows WSAENOBUFS TIME_WAIT resource errors remain undiagnosed; R49/R50 numeric syscall extraction returns -1 if HTTP proxy 502 has already reduced the underlying error to text. No causal diagnosis or release authorization inferred. Independent separate strict READ-ONLY KitAuditor review of R48+R49+R50 remains required, enterprise protected AD/SSPI conditional skip, class-fair queue and organic Go memory pressure remain production blockers.
