# PxGo R40 — Native counterexample: host-global AUTO memory estimate versus enforced child Job Object budget

2026-10-28 ICT. Parent PR#103 exact immutable R39 SHA `1b85efab6882a83c2e24e651e6e5d58314f65dd5`, PR#102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`. Both OPEN DRAFT. **TEST-ONLY**, no shipped Windows capacity sensor, admission/runtime, default, OS pagefile/TCP or Docker changes. No merge/tag/release.

## Safety problem to characterize

The existing opt-in Windows AUTO resource sensor `readCapacitySampleWindows` in `internal/proxy/capacity_sensor_windows.go` bases `freeSlots` on process handles + **host-global** `GlobalMemoryStatusEx` physical/pagefile availability (assumed 1MiB per extra downstream slot, 4 handles/slot, provisional ceilings 32,768 handles and 4,096 slots), not on the process-specific `JOB_OBJECT_LIMIT_PROCESS_MEMORY` committed virtual memory budget. It can report a large global free slot count while the actual PxGo process is constrained by a small Job Object. R35 independently proved a stricter nested Job Object ancestor limit can also be invisible to `QueryInformationJobObject(NULL)` on the child immediate job.

R39 new native real PxGo test-child holds 2,560 authenticated CONNECT and serves 600 HTTP while sustaining a 64MiB Win32 native MEM_COMMIT ballast, then releases and verifies PrivateBytes delta under process-only Job Object cap baseline+384MiB. R40 adds a **live child read-only native sensor probe** at the point the ballast is held, before 600 short HTTP begins. No native OS or production state is modified beyond the already bounded R39 test-child.

## R40 exact falsifiable test-only scenario

In optional `PXGO_CAPACITY_JOB_PROXY_SENSOR_GAP=1` with `PXGO_CAPACITY_JOB_PROXY_HOLD_COMMIT=1` and regular real 2,560-CONNECT test enabled:
1. Parent verifies all 2,560 long CONNECT established, hard child Job Object cap in both parent and child, and child `PrivateBytes` rising by >=48MiB with 64MiB native ballast live.
2. Parent sends `SENSOR` over private child test stdin. The child calls EXACT existing `readCapacitySample()` (no production modification), rejects unavailable/invalid Windows sensor and prints `JOB_PROXY_GLOBAL_SENSOR resource=windows-memory-handles available=true reported_free_slots=N`.
3. Parent parses native reported `freeSlots=N` exactly. Compute `jobEquivalentSlots=floor((job_hard_commit-child_private_bytes) / 1MiB)`. **This is a SAME 1MiB heuristic convention as the current sensor**, not an authoritative actual number of additional sockets allowed: real per-connection commit differs and can spike. If `reported_free_slots > jobEquivalentSlots >=1`, emit `JOB_PROXY_SENSOR_GAP_RESULT child_job_commit_cap=... child_private_bytes=... job_equivalent_slots=... global_sensor_reported_slots=... global_exceeds_job=true`. If not, the native job FAILS; no synthetic marker or skip. It explicitly demonstrates host-global memory headroom calculation could exceed a process-local hard cap on a fresh hosted Windows runner.
4. The SAME living child continues to deliberately deny a 256MiB Win32 allocation under cap and accept 4MiB; serves **600/600** HTTP short requests while 64MiB ballast and 2,560 CONNECT tunnels remain held, preserves held tunnel echoes, releases the 64MiB ballast (private commit must fall >=48MiB), finally drains all physical/class permits to zero, child exits clean.
5. New separate native Windows-latest exact-SHA CI gate requires strict `JOB_PROXY_SENSOR_GAP_RESULT`, R39 sustained memory marker, and original 2,560+600/no-leak positive marker. Upload raw artifact always; all prior 17 static GitHub CI job definitions remain unchanged, 18 total after insertion.

## Interpretation and limits

The relation `reported_free_slots > jobEquivalentSlots` validates a **difference between two existing estimation conventions** on a host running the test. It does NOT prove that exactly `jobEquivalentSlots` further sockets are safe or that the native OS would refuse the next live Go outbound Dial at that point. It does not implement a production process budget sensor, ancestor effective limit enumerator, class-fair governor or natural Go heap OOM handling. Both R39 ballast and R40 global sensor observation are test-only. No independent post-R22 KitAuditor yet; protected corporate AD/SSPI remains conditional SKIPPED, WAN and Linux PSI soak untested.

## Preflight and operational protections

Exact R39 parent SHA checked against PR#103 DRAFT; isolated stage source Windows amd64 CGO-disabled crosscompile `go test -c`, `go vet`, `gofmt` PASS. New CI workflow built from verified R39 17-job YAML via Python literal replacement, real PyYAML parse confirms 18 static jobs and original 17 all present; SHA-qualified raw artifact and strict markers. No branch commit or native Windows claim until full exact-SHA CI and actual independent reviewer. Original root WIP preserved.
