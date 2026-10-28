# PxGo R45 — R44 10-minute native Windows soak test-validity fix (test-only, staging pending CI)

2026-10-28 ICT. Parent PR#103 R44 immutable SHA `f5f77e2c0cd90cd1055618117c817a22b95acf32`; base PR#102 `fb2aa923a753309dae515fc8be38112f45fbddc8`. Both OPEN DRAFT, no merge/release/default change.

## Real R44 failure (do not erase/waive)

R44 exact native GitHub run `40654286776`, job `122701458837` **FAILED** on its FIRST attempt, after cycle1 PASS but during cycle2 of `TestCapacityNativeWindowsTenMinuteSoak`. Raw test output:

```
SOAK_SAMPLE cycle=1 elapsed=20.1656553s held=2560 short_completed=20 test_process_private=434696192 test_process_handles=10425 heap_alloc=276945616 physical=2560
capacity_ten_minute_soak_windows_test.go:161: physical/class permit loss at cycle=2: {Mode:auto State:normal ActiveDownstream:2561 ... AdmissionWaits:7 ...}
--- FAIL: TestCapacityNativeWindowsTenMinuteSoak (44.10s)
```

Exact artifact `win-2560-ten-minute-soak-f5f77e2c0cd90cd1055618117c817a22b95acf32` ID `12800719374`; SHA256 of downloaded `windows-2560-10min-soak.log` is `839ec501e9b2682306e6304e7df43e563921993593b3805534cf1df51d632261` and is preserved in this staging. Whole R44 CI cannot be called GREEN until actual completed/success; R44 produced a real failing test, not an artifact/upload failure.

The previous test immediately asserted that 2,560 held CONNECT + *zero* ordinary short HTTP must be counted at the **exact instant** the short HTTP client returns after response/read/Close. But the server-side handler and connection goroutine may still be asynchronously releasing the ordinary short's physical/class permit; a snapshot with `ActiveDownstream=2561` and no readback 10ms later is NOT proof of a leaked permit, nor can it be waived as a known good race. The observation is ambiguous until a bounded retry distinguishes transient cleanup lag from persistent leakage. No production runtime source is changed.

## Narrow R45 correction

- Modify ONLY `internal/proxy/capacity_ten_minute_soak_windows_test.go` test code, based on exact immutable R44 Git blob `72cab7f469151ad99cf62fa9622163ff6f666b1a`.
- After every cycle's exactly 20/20 short HTTP clients have successfully closed, poll up to **3 real wall-clock seconds**, every 20ms, measuring `px.capacityDiagnosticSnapshot()`. At **every** observation, fail immediately if one of the 2,560 live LONG connections is missing (`ClassPools.Long !=2560` or `ActiveDownstream<2560`), or total active exceeds 2,560+20 (implausible accumulated short backlog). Require the final settled sample to be **exactly** `ActiveDownstream=2560, ClassPools.Total=2560, Long=2560, Short=0, Provisional=0`. If still not settled at deadline, **hard FAIL** with the final snapshot; NO SKIP, NO tolerance of actual leaks.
- Record `SOAK_SETTLE cycle=n max_transient_active=x settled_after=... physical=2560` whenever a transient short permit was observed. Keep original `SOAK_SAMPLE` positive evidence at each of the full 30 monotonic ~20-second cycles, 2,560 real payload heartbeats, full 600/600 HTTP request successes, native PrivateBytes/handles growth bounds and all-final-permit drain. This is a test timing fidelity fix only.
- The only new source import is internal diagnostic snapshot type; no external dependencies or production code.
- On a fresh clone of exact R44 HEAD, `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c`, `go vet ./internal/proxy` and `gofmt -l` all PASS. Native R45 soak test is **NOT YET PASS** until actual Windows run and strict full workflow verified.

## Architecture caution

If any R45 cycle fails after 3s with nonzero Short/Provisional, treat it as a candidate actual goroutine/permit leak, not infrastructure flakiness; further native instrumentation and independent source/security audit required. Positive R45 30-cycle evidence is 10-minute Windows **loopback single-host, no TLS/WAN** and does not prove natural Go OOM resilience, strict class-fair scheduling, enterprise AD/SSPI or Linux PSI. The original R44 failed result stays preserved historically even if R45 passes. Other R45 job-wide native test prototype from parallel isolated staging is kept unreferenced until this soak validity gate is resolved, to avoid cross-branch interference.

No independent KitAuditor read-only verdict after R22. KitControl `assess` dry-run to obtain actor-level review failed `lifecycle bridge rejected request (2)`, no actor/claim mutation was made. Release remains BLOCKED; both PRs DRAFT, user WIP preserved.
