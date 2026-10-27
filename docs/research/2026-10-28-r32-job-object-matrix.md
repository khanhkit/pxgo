# PxGo R32 — Fresh-VM 2×2×2 Windows real-proxy Job Object replication (experimental)

Date: 2026-10-28 ICT. Proposed parent immutable R31 PR#103 SHA `d5313c1dabd4af6b2472807392a2df74a2df9b25`, base PR#102 `fb2aa923a753309dae515fc8be38112f45fbddc8`, both OPEN DRAFT. Experimental test/workflow/doc only, no production runtime/default changes, no release.

## Goal and scope
R29/R31 proved on two separate native Windows VMs that the real PxGo child test process could hold 2,560 real TCP CONNECT tunnels, process 600/600 short HTTP while active, and survive a deliberately denied 256MiB native commit within a child-only enforced Job Object, then 4MiB successful commit, and zero physical/class leaks. R31 separate negative job intentionally t.Fatal'ed after all 2,560 were held and proved deterministic child+echo cleanup. These are few samples, not reliable cross-policy comparative data.

R32 runs a *factorial independent native runner replication*: 2 capacity policies (R11, experimental fast_saturation) × 2 classifier accounting states (ON/OFF) × 2 fresh-VM repetitions each = **8** native Windows jobs. Every runner independently performs 2,560 simultaneously ESTABLISHED bidirectional held CONNECT tunnels (paced 16-token ramp, not instantaneous SYN burst), 600/600 short HTTP, enforced child-only committed memory baseline+384MiB, deliberate 256MiB denied native VirtualAlloc, 4MiB successful recovery, valid child process snapshot, preserved held payload echoes, zero final permits and normal shutdown. Numeric runner telemetry emitted in strict `JOB_PROXY_2560_RESULT`.

Each fresh VM avoids cross-trial loopback TIME_WAIT carryover; samples remain **independent unpaired hosted VMs** and must NOT be treated as same-VM randomized crossover or causal R11 vs fast_saturation superiority. Matrix logs include explicit policy/class/trial provenance. OFF configuration verifies no class pool object exists; ON verifies long=total=2560 and short=provisional=0 at plateau.

## Concrete source changes
- Test-only `internal/proxy/capacity_job_object_proxy_pressure_windows_test.go`: require class ON fully conserved and class OFF no pool, reject invalid optional trial marker, emit explicit `JOB_PROXY_MATRIX_META policy=... classes=... trial=...`. Existing R31 cleanup negative fixture unchanged.
- Workflow-only dedicated `windows-job-proxy-2560-matrix` matrix 2×2×2, `fail-fast:false`, fresh `windows-latest` runner per combination with exact GitHub PR HEAD checkout SHA. Validate non-skipped positive marker plus metadata and Go test exit, always upload SHA+matrix-qualified raw logs; unchanged 106+ existing workflow jobs remain.
- No heuristic skip after native Winsock failures, no timer/port/system tuning. Preserve R31 host-resource failure exact raw evidence.

## Known R31 host-resource failure
R31 full workflow run `40552653412` attempt1 failed mixed Windows `r11/classes=off/seed=6` job `122360263178` in `churn`: 448/449 real CONNECT setup succeeded, next Winsock bind failed *"system lacked sufficient buffer space or because a queue was full"*. Read-only net diagnostics from SAME VM after failure: **10,256 TIME_WAIT** TCP connections, only ~15 ESTABLISHED, 40 LISTEN at diagnostic instant. This strongly implicates Windows ephemeral/socket allocation pressure after previous heavy churn, but not a formally proven causal root. The R31 **new negative cleanup** job 122360262170 and normal Job Object job 122360262481 both PASSED. Do not erase or waive R31 failure because unrelated jobs pass.

## Gates, no release
- `gofmt` PASS, native Windows GOOS=windows/amd64 CGO=0 crosscompile+vet PASS, whole workflow parsed with temporary PyYAML 14 static jobs; matrix cells=8. Exact Git SHA and native CI **PENDING until pushed and verified**.
- Protected corporate AD/SSPI remains conditional SKIPPED, no true fair admission scheduler (25/75 is advisory), no organically exhausted Go runtime recovery or long WAN/soak. Independent separate strict READ-ONLY KitAuditor exact SHA required. Both PRs DRAFT; no merge/release/default activation.
