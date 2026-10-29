# PxGo R33 — Native Windows child self-reported Job Object commit limit

Date 2026-10-28 ICT, research/test-only proposal; base R32 PR#103 immutable `c39eb7618de992e2c61231ba182bb3d5d65ec94a`, parent PR#102 DRAFT `fb2aa923a753309dae515fc8be38112f45fbddc8`. No production/runtime/admission default changes.

## Why this is needed

Existing native Windows AUTO capacity resource sample (`internal/proxy/capacity_sensor_windows.go`, unchanged) calculates provisional capacity from **process handle count + host-wide `GlobalMemoryStatusEx` physical/pagefile memory**. It does NOT read a job-specific hard process committed-memory limit and does not know if the PxGo process is hosted under an explicit Job Object cap. R29 and R32 successfully applied the parent Job Object to a separate PxGo test-child with only approximately 433MiB process commit limit while Windows host global commit space was much larger. A global memory sensor's apparent free capacity therefore cannot establish that local Job Object has safe headroom. No runtime fix or absolute budget is inferred from 2,560 tests; this is a genuine architecture/release gap.

Microsoft documents:
- `QueryInformationJobObject(hJob=NULL)` obtains current process's associated job, or the **immediate nested job** when multiple jobs exist: https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-queryinformationjobobject
- `JOBOBJECT_EXTENDED_LIMIT_INFORMATION.ProcessMemoryLimit` is committed virtual memory, only meaningful with `JOB_OBJECT_LIMIT_PROCESS_MEMORY` flag: https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_extended_limit_information
- In nested jobs, the effective limit may be the most restrictive of the entire job chain, so reading immediate job alone is **NOT a complete effective-limit proof**: https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_basic_limit_information.

## R33 narrow native test-only probe

During existing real PxGo child Job Object test:
1. Host parent creates and assigns only PxGo test child to limited Job Object, reads back exact memory cap, before any 2,560-CONNECT ramp.
2. Parent sends `JOBINFO\n` over private child stdin control channel; child uses native `windows.QueryInformationJobObject(0,...JobObjectExtendedLimitInformation)` to read its *own immediate* job, requires correct struct length, process-memory flag and nonzero cap, and prints raw `JOB_PROXY_OWN_JOBINFO process_commit_limit=... process_mem_flag=true`.
3. Parent exact-matches child's cap to the previously read parent handle cap; any failure is FATAL. Parent logs strict `JOB_PROXY_OWN_JOBINFO verified=true child_pid=... process_commit_limit=...`.
4. Existing 2,560 held CONNECT + 600 HTTP, deliberate 256MiB native allocation denied and 4MiB succeeded, telemetry and shutdown cleanup remain unchanged. Separate 8-cell policy/class trial matrix from R32 continues unmodified.
5. Native Windows workflow exact-SHA jobs (normal and matrix) additionally demand strict own-child-cap marker in raw test log, with artifact upload on any failure. A test that SKIPs cannot produce marker and cannot PASS.
6. R31 native nested subprocess negative test also passes through the new JOBINFO roundtrip prior to injecting t.Fatal after 2,560 actual connections.

## Validation / limits

Local `gofmt` PASS, Windows GOOS/GOARCH=windows/amd64 CGO=0 compile and `go vet` PASS, full workflow `ci-r33.yml` parsed with PyYAML, 14 static job definitions, strict child-limit assertion in normal and 8 matrix native test jobs. **Native immutable R33 CI is pending until new exact commit and all GitHub jobs.**

This demonstrates **queryability of the immediate Job Object cap from inside PxGo**, not automatic controller adaptation and NOT full ancestral effective limit in nested job trees. No guarantee that Go runtime can recover from organically exhausted commit budget; no production memory governor or process limit is added. Any future Windows sensor integration must have an auditable native-API error policy, handle `no job` / unknown nested limits safely, and use child private commit + kernel limit rather than host-wide availability alone. Protected AD/SSPI, WAN soak and real class-fair admission remain blocked, independent strict READ-ONLY KitAuditor needed. Both PRs stay DRAFT, no merge/release.
