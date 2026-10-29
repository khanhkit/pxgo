# PxGo R31 — Negative 2,560-live-CONNECT Job Object teardown gate (experimental)

Date: 2026-10-28 ICT. R31 source parent R29 exact SHA `56bfbf52d50fcb9b92a3afd7a0457745d5ee0939`, PR#103 base PR#102 `fb2aa923a753309dae515fc8be38112f45fbddc8`. NO production runtime/default changes, no release approval.

## Motivation

R29 ran native real PxGo test-child with 2,560 concurrently established bidirectional CONNECT tunnels + 600/600 short HTTP requests under an enforced Windows child process Job Object commit cap (baseline+384MiB), successfully denied a deliberately oversized 256MiB VirtualAlloc and allowed 4MiB in the same live PID, and drained all permits. R29's successful normal shutdown alone does not prove its cleanup after Go `t.Fatal` while all 2,560 CONNECTs are held. Early test fatal could strand echo worker goroutines, leaked child process, or block Wait indefinitely.

## R31 new negative gate

- Extend ONLY `internal/proxy/capacity_job_object_proxy_pressure_windows_test.go` on Windows.
- Existing real child-in-Job test accepts a TEST-ONLY `PXGO_CAPACITY_JOB_PROXY_INJECT_FAILURE=post_plateau`, which calls `t.Fatal("INJECTED_FAILURE_AFTER_2560_HELD_CONNECT")` only AFTER receiving all 2,560 CONNECT success results, snapshot showing physical active=2560, and Job Object exact cap readback. No silent skip/mocked connection counts.
- Its existing coordinated defer must close all held CONNECT clients, shut echo listener, join all echo workers, send `QUIT` to real PxGo child, wait for clean normal child exit, then close owned Job Object; only then print `JOB_PROXY_CLEANUP_PROOF held_closed=2560 echo_workers_joined=true child_exited=true`. If child exit/handle close fails, there is no positive marker and test fails; subprocess cancellation is bounded.
- A separate outer Windows Go test `TestCapacityWindowsJobProxyForcedFailureCleanup` spawns the above REAL test in a nested subprocess, with opt-in env and 40s hard timeout. Requires nested failure (as intended), exact injected-failure message, and positive normal teardown marker, rejects panic/fatal error/unexpected child exit, verifies no timeout, and logs `JOB_PROXY_CLEANUP_NEGATIVE expected_injected_failure=true clean_child_exit=true echo_joined=true elapsed=...`.
- Separate native fresh `windows-latest` CI job (avoids loopback TIME_WAIT from the normal 2,560 benchmark) checks exact immutable SHA and strict positive outer marker; uploads full raw logs even if failure.
- Stage source passed `gofmt -l` check; Windows amd64 CGO=0 crosscompile and `go vet` PASS; YAML entire file parsed with real PyYAML, 13 static jobs including original normal benchmark, one new 4-step negative gate.
- This is not an injected low-memory test. Real child Job Object is enforced and actual CONNECT plateau established, but actual test deliberately fails before memory pressure probe; purpose is deterministic *failure-teardown* validation.
- Do not claim independence: staged audit packet is a request; actual KitAuditor must separately inspect immutable GitHub R31 SHA plus native Windows raw job log and all paginated CI before reviewer verdict. Protected AD/SSPI remains SKIPPED, fair admission remains soft accounting, natural Go OOM still untested. PRs DRAFT, no merge/tag/release.

## Evidence rules

- Native Windows pipeline result is PENDING until exact SHA checkout, raw log with both messages, proof of nested test nonzero result, **outer test PASS**, and workflow full paginated jobs PASS. Do not mark green from local crosscompile alone.
- Candidate source and workflow Git blobs must exactly match local `git hash-object`; use nonforce expected-SHA CAS, preserve R29/R28 history and dirty checkout; require real YAML parser BEFORE GitHub push (R28 invalid YAML is a permanent negative fixture).
