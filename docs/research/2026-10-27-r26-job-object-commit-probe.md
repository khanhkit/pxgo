# PxGo R26 — Windows Job Object hard process-commit gate (bounded child probe)

State: experimental test-only candidate as of 2026-10-27 ICT; staged locally, pending exact-SHA native Windows CI and independent read-only audit. Exact parent candidate PR #103 R25 `cee96f5742f7772d2957d7af2917d464262dca3a`. Do not merge/release or call this a PxGo production resource policy.

## Why

R16 process-isolated Win32 sampling measured an independent PxGo child process holding 2,560 CONNECTs, but **measurement is not enforcement**. The existing AUTO Windows memory sensor is an absolute trial envelope, not a Job Object hard quota. To establish a safe precondition for a future guarded PxGo resource pressure test, this isolated Windows-only Go regression asks if a real native Job Object can enforce process *committed memory* on a specific child PID, deny a deliberate over-budget commit, and allow a smaller post-denial commit in the same still-live process. This research does NOT establish memory budgets for 2,560 tunnels, fairness, or graceful PxGo overload recovery.

Microsoft documents `JOB_OBJECT_LIMIT_PROCESS_MEMORY` as an enforced limit on process committed memory; `ProcessMemoryLimit` is a virtual memory commit quota, not a working-set or resident RSS limit. Assignment of an already-running process does NOT re-test its earlier allocations; the test snapshots baseline before installing the new limit, then probes **future** committed bytes only. Nested jobs can inherit tighter effective limits from the parent. Sources:
- https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_extended_limit_information
- https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_basic_limit_information
- https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-assignprocesstojobobject
- https://learn.microsoft.com/en-us/windows/win32/procthread/nested-jobs

## Exact acceptance

1. Only run opt-in `PXGO_CAPACITY_JOB_PRESSURE=1` and `TestCapacityWindowsJobObjectCommitPressure` on native Windows. Child is a separate instance of the same exact-SHA test binary and waits for parent `RUN` command; neither parent test process, runner, load generator nor original PxGo checkout is placed in the job.
2. Parent samples real child PID PrivateBytes with existing process-isolated GetProcessMemoryInfo helper, chooses a **finite hard cap = baseline + 128 MiB**. The Job Object is created with `JOB_OBJECT_LIMIT_PROCESS_MEMORY | JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, applies the limit and readbacks `QueryInformationJobObject`, then `AssignProcessToJobObject` ONLY to that child process.
3. Child attempts one `VirtualAlloc(MEM_RESERVE|MEM_COMMIT)` **256 MiB** which MUST fail with a recognized Win32 memory-limit error. The child is still alive; then a **4 MiB** MEM_COMMIT allocation succeeds (committed virtual memory, not necessarily resident RSS), is released, and logs a strict `JOB_RESULT` marker. Any unrelated error is NOT counted as pressure proof. Parent re-samples private-commit and checks it does not exceed installed cap. Child must exit normally via QUIT.
4. Native GitHub `windows-latest` test job isolated from 2,560 CONNECT stress, opt-in only for PR #103; requires exact immutable SHA checkout, strict positive log marker and uploads full logs even when failed. Set short finite timeouts on test and process, explicitly close job handle and child; no changes to OS pagefile, ephemeral ports, runner limits or Docker.
5. Fail closed if sandbox has a restrictive unexpected parent Job Object preventing nesting/assignment, if API or quotas are unavailable, or if any memory failure is unclassifiable. Record a blocked/failed gate instead of skipping or calling it pass.

## Limitations

This tests native hard process commit mechanics and a **surviving test helper**, not a complete workload-driven PxGo response under low memory. A Go proxy could terminate with runtime out-of-memory under such a limit; adding a production Job Object to PxGo is out of scope. Exact memory reservation values are bounded fixture values, NOT safe per-connection budgets or release thresholds. Job Objects offer hard committed-memory controls, not an arbitrary numeric count-of-handles cap; separate handle telemetry and admission safety remain needed. Separate native throughput/CPU/handles, Linux cgroup/PSI, long WAN soak, AD/SSPI and actual class-fair scheduler remain release blockers. Both PRs stay DRAFT, legacy default, no merge/tag/release.

## Current evidence

Go Windows amd64 CGO=0 cross-compilation and Windows `go vet ./internal/proxy` of the isolated new Windows test against immutable R22 Go runtime sources PASS; changed-lines Windows and Linux golangci-lint (vs R22) 0 new issues. Windows whole-package lint reports two **preexisting deprecation warnings** in unchanged `capacity_sensor_windows.go` and `sspi_windows_integration_test.go`, not in this test. **Native execution NOT YET DONE**. No independent KitAuditor verdict yet. This report must not claim R26 PASS without native exactly pinned CI and strict read-only source review.
