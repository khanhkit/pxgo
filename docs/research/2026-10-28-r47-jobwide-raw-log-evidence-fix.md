# PxGo R47 — FIX: export verified private child job-wide quota markers to native CI log

2026-10-28 16:27 ICT. **Experimental TEST-ONLY** change, no production runtime/default, no merge/release/tag. Parent PR #103 exact immutable R46 SHA `a085ce69f945b09c522fde765d877626fd948c4d`, PR #102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`; both OPEN DRAFT.

## R46 genuine CI failure

GitHub Actions exact R46 run `40663450793` attempt 1 and native Windows job `122731931200` are historical evidence. The job's new PowerShell step FAILS despite native Go test itself `--- PASS: TestCapacityWindowsJobProxy2560Pressure (4.09s)`, and success in later cleanup and artifact-upload step. The SHA-qualified raw artifact `win-real-pxgo-jobwide-a085ce69f945b09c522fde765d877626fd948c4d` (ID `12805081909`), log SHA256 `6578ef808d0b6c44455e826f06dc0caeeb223ad6170736697b83332d62a88a96`, proves:
- live PxGo child PID 2876, `job_cap=453763072`, `process_cap=520871936`, child readback verified.
- `JOB_PROXY_JOBWIDE_RESULT enforceable=true pid=2876 held_connect=2560 completed_short=600 job_total_limit=453763072 process_limit=520871936 job_only_denied=175177728 recovered_bytes=4194304 same_allocation_control_succeeded=true`.
- `JOB_PROXY_2560_RESULT ... held=2560 completed_short=600 denied_bytes=268435456 recovered_bytes=4194304 ... physical_after=0`, `JOB_PROXY_CLEANUP_PROOF ... child_exited=true`.

**The failure is the newly written CI strict raw-marker check, not a failed child Job-wide or socket data-plane test:** child emitted `JOB_PROXY_JOBWIDE_PROBE` and `JOB_PROXY_JOBWIDE_CONTROL` on its stdout, but parent consumed these messages via `capacityReadJobPrefix` and did not repeat them in its own Go `t.Log`. Only the parent test's stdout appears in `go test -v` CI artifact. The test already compares exact child marker strings in Go; CI could not see them. Strict workflow rejects absent markers correctly. Preserve full R46 attempt1 red status; no waiver/skip, do NOT silently delete strict marker requirements.

## R47 surgical source-only change

In `internal/proxy/capacity_job_object_proxy_pressure_windows_test.go`, add exactly two parent-side `t.Logf` statements **AFTER** successful strict child IPC comparison:
1. `JOB_PROXY_JOBWIDE_PROBE denied_bytes=<actual> recovered_bytes=4194304` following native allocation-denied+4MiB-recovered child marker.
2. `JOB_PROXY_JOBWIDE_CONTROL allowed_bytes=<SAME actual> cap_equal=true` following native same-allocation positive control under elevated JobMemoryLimit.

Parent still uses **one shared `jobWideProbeBytes`** before and after altering only JobMemoryLimit and requires exact child IPC positive markers, restore lower job-wide cap, and complete 2,560 held CONNECT, 600/600 short HTTP, full physical/class drain, and clean child exit. Nothing else changed; no workflow edits, no runtime/default changes. Existing R46 workflow 22 static jobs already contains strict regex for these markers, so a native exact-SHA R47 rerun can now prove positive evidence instead of merely internal test logic.

## Local verification

- R47 source created from exact R46 parent immutable source Git blob `452c204b8825afaf9a7d3f3bb888577921458a4a`; diff `5 insertions, 0 deletions`, `git diff --check PASS`.
- Isolated fresh clone exact R46 SHA `a085ce69f945b09c522fde765d877626fd948c4d`, Windows amd64 CGO disabled `go test -c`, Windows `go vet` and `gofmt -l` PASS.
- **Native R47 CI / independent strict read-only KitAuditor verdict PENDING**. Do not push PR branch until R46 full exact run terminal and recheck PR HEAD/base/DRAFT ownership (nonforce expected SHA CAS).
- Release remains BLOCKED for no truly independent KitAuditor, protected enterprise AD/SSPI SKIPPED, fair short/long scheduler absent, natural Go heap OOM/real effective nested job memory sensor, WAN multi-hour soak and Linux PSI missing.
