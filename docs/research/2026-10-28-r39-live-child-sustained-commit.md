# PxGo R39 — Sustained native 64MiB committed-memory ballast inside real child under 2,560 held CONNECT + 600 short HTTP

2026-10-28 ICT. Parent PR#103 exact immutable R35 SHA `22e2e22f3b72a3d2156da255c5e61fb7f426125a`, PR#102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`, both OPEN DRAFT. Opt-in TEST-ONLY native Windows change, new dedicated workflow job and report. No runtime governor/default production changes, no OS-wide pagefile/physical-memory/port-range modification, no merge/release.

## Why R39 is needed

R29/R31/R32/R33 tests demonstrated the real PxGo *test-child process* can hold 2,560 authenticated real CONNECT tunnels and complete 600/600 short HTTP through child-specific enforced Windows Job Object committed-memory baseline+384MiB while rejecting one deliberately huge 256MiB Win32 VirtualAlloc and accepting 4MiB. But the huge allocation failed while native process private commit still had >100MiB free, so evidence was OS quota enforcement, not a **sustained near-hard-cap committed-memory workload during data plane activity**.

R35 separately demonstrated a stricter nested ancestor Job Object cap (baseline+96MiB) can be hidden from a child's immediate NULL-Job query (leaf baseline+192MiB). Production memory sensor still uses GlobalMemoryStatusEx host-wide and does NOT account for true effective nested job limits; no runtime change here.

## New falsifiable scenario

Add child-only opt-in `PXGO_CAPACITY_JOB_PROXY_HOLD_COMMIT=1` to existing exact R35 Go file `capacity_job_object_proxy_pressure_windows_test.go`, without changing original behavior when env unset. Once all 2,560 real TCP CONNECT + roundtrip echo tunnels are simultaneously held, read independent child PID private commit. Before creating ballast enforce **at least 128MiB free committed headroom** under child-only Job Object, and still satisfy strict original denial margin for the 256MiB probe.

Parent sends `HOLD_COMMIT` over private child stdin. Child executes `VirtualAlloc(MEM_COMMIT|MEM_RESERVE)` **64MiB** in the real PxGo process, stores allocation pointer alive through the rest of workload; emits `JOB_PROXY_HOLD committed_bytes=67108864`. Parent must observe native PID `PrivateBytes` rising at least 48MiB versus immediate plateau, below enforced hard cap. Allocation remains live WHILE original child executes 256MiB rejection/4MiB successful native recovery and all 600 short HTTP requests, with each existing long CONNECT data path verified after HTTP load.

AFTER 600/600 HTTP and CONNECT payload integrity while all tunnels still held, independent PID PrivateBytes must remain at least 48MiB above original plateau. Parent sends `FREE_COMMIT`, child frees the 64MiB native allocation with `VirtualFree(MEM_RELEASE)` and emits `JOB_PROXY_UNHOLD released_bytes=67108864`. Parent readback must show PrivateBytes fell by >=48MiB (with tolerances for concurrent normal Go runtime allocations), then logs `JOB_PROXY_SUSTAINED_COMMIT_RESULT held_bytes=67108864 completed_short=600 enforced=true private_with_ballast=... private_after_release=... hard_commit=...`.

Finally, unchanged existing success gate closes all 2,560 long tunnels, samples three phases ramp/mixed/drain, verifies zero active physical/class permits, normal child shutdown and Job Object closing; emits strict `JOB_PROXY_2560_RESULT enforceable=true ... held=2560 completed_short=600 denied_bytes=268435456 recovered_bytes=4194304 ... physical_after=0`. The R31 separate negative early t.Fatal test and the existing eight-cell R32/R33 positive matrix run without new env and must preserve old invariants.

Dedicated *fresh* `windows-latest` exact-SHA R39 job requires all three positive markers: child self-limit, sustained +64MiB/released native commit, 2,560 CONNECT+600 HTTP final drain; tests fail on any missing marker/nonzero Go exit, raw artifact retained on success AND failure. Host/runner/parent unaffected, only sub-process Job Object cap. Native Windows gofmt/amd64 CGO-disabled crosscompile, go vet, full PyYAML 17 static job definitions, original 16 preserved, PASSED locally before Git push.

## Safety / scope limitations

- **This does NOT test natural Go heap OOM recovery, PID resource governor adaptation, or automatic backpressure**. Ballast is explicit Win32 memory allocation inside real test-child held in an independent pointer, and original 256MiB failed allocation is deliberate; no production memory sensor or controller added.
- The child is a full Go test executable running actual PxGo server logic, not the optimized production binary.
- 2,560 CONNECT are concurrent held ESTABLISHED tunnels opened in limited 16-at-a-time ramp, NOT 2,560 simultaneous SYNs.
- No guarantee in 64MiB ballast case that memory headroom is calibrated universally across fresh Windows runners. Hard fail any insufficient-margin/ENOMEM, never silently waive.
- Protected enterprise AD/SSPI unavailable/condition-skipped, true short/long priority class fairness not implemented (25/75 advisory), WAN/soak Linux cgroup PSI and full independent strict read-only KitAuditor source/CI verdict still missing. Release remains BLOCKED, PRs DRAFT, no merge/tag/release.
