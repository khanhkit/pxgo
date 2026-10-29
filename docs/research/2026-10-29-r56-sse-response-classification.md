# PxGo R56 — Actual upstream SSE response-aware class accounting and Accept q=0

Date 2026-10-29 ICT. **NONDEPLOYED staging only pending R55 exact-SHA CI**. Source exact immutable parent PR#103 R55 `0df0ea2f57b7f60ac09bdd635d14f63d53aedf18`, PR#102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`, both OPEN DRAFT. Candidate changes are limited to `internal/proxy/capacity_classes.go`, `internal/proxy/intermediary.go`, `internal/proxy/proxy.go`, new real-on-wire test and this report. No production legacy/default change, no merge/tag/release.

## Reproduced defects (strict experimental AUTO+classes+long intake ONLY)

The R55 actual SSE streaming flush fix passes and does not affect classification. A fresh strict full on-wire test against exact immutable R55 binary source reproduced two cases:
1. With 11 existing true authenticated long CONNECT and long budget12, downstream `Accept: application/json` goes to a real upstream that nonetheless returns **actual `Content-Type: text/event-stream`** and flushes first SSE event. Existing PxGo streamed it (R55 flush PASS) but tracked lease `Short=1, Long=11` while a long-lived SSE stream remained open. Baseline test FAILED at strict invariant expected Long=12, Short=0.
2. With 12/12 existing long CONNECT, same unsolicited actual upstream SSE was allowed HTTP200, bypassing strict new-long budget. Baseline test FAILED expected HTTP503.
3. A separate true upstream JSON GET with `Accept: application/json, text/event-stream;q=0` was incorrectly refused HTTP503 by substring-based preclassification, even though q=0 means unacceptable.

Raw TDD baseline local log `r56/baseline-r55-failing.log` records BOTH failures.

## Narrow candidate behavior

- Preserve early (post-auth) prediction for *requested* SSE to retain existing tests and fail-safe strict admission, but parse explicit `text/event-stream` Accept with `mime.ParseMediaType` and valid q in (0,1]. q=0, negative, >1, exponent/hex floats, over-precision and malformed values do NOT claim long capacity. This is not a full RFC Accept tokenizer for quoted commas.
- After a real successful HTTP RoundTrip, and BEFORE writing final HTTP response to downstream, inspect **actual upstream HTTP200 Content-Type** (text/event-stream, case-insensitive with media type parameters). Reclassify a valid short lease into long **without acquiring a second physical permit**; accounting is sticky once long.
- In opt-in strictLongIntake only, reject a newly revealed actual SSE long response with HTTP503+Retry-After+Connection close if the long class budget is already full. Close upstream response body as the handler returns; never evict an existing authenticated CONNECT/WebSocket/SSE long. Non-SSE status/Content-Type path remains identical, existing 101 Upgrade handling remains before response classification.
- Reuse one `isSSEMediaType` helper between downstream SSE flush logic and actual response classification. No changes to global physical accept H, dynamic window, DNS/PAC/DoH, connection admission order or default mode. This still cannot GUARANTEE short progress with all physical permits held provisionally/long; preauth slowloris and soft budget debt remain separate blockers.

## Validation

- Isolated R56 clone exact R55 SHA under owned `.hotfix-work-20261022/pxgo-autopilot-60m-20261029-1420/r56/shadow`; original checkout with user/worker WIP untouched.
- Existing R55 first-event flush test remains, R56 new two-case on-wire `TestStrictLongIntakeUsesActualSSEResponseAndAcceptQuality` now **PASS 10 consecutive iterations**, verifying 11-long actual SSE promoted to Long=12 and first frame flushed; 12-long actual SSE rejected 503 and LongDenied increments; q=0 JSON stays 200 and body unchanged in both.
- `TestAcceptsEventStreamQualitySemantics` tests 11 Accept cases: q=0, q=0.0, uppercase Q, invalid/negative/too high, valid weighted SSE, JSON only, bare SSE.
- `go test -count=1 ./...` all Go packages PASS, `go vet ./...` PASS, Windows GOOS=windows GOARCH=amd64 CGO_ENABLED=0 crosscompile PASS and Windows vet PASS.
- `golangci-lint run --new-from-rev=HEAD ./internal/proxy/...` zero new issues; `git diff --check` PASS. Need new exact-SHA CI, independent source+security audit **NOT PERFORMED**.

## Other unresolved matters

R55 exact full Actions run `40835771478` attempt1 had native Windows real 2,560 CONNECT job `123320086534` fail 1/600 short HTTP due Windows outbound loopback bind insufficient buffer/queue full after job-wide commit readback/denial/positive control succeeded. Do not waive historical failure; rerun only failed jobs on SAME SHA after workflow terminal and report attempts separately. This Windows issue occurs in R31/R53 and may reflect ephemeral host socket resource pressure rather than SSE code; exact mechanism unknown.

Independent strict READ-ONLY KitAuditor for R24–R56 absent; protected AD/SSPI conditional SKIPPED, user-requested true short/long prioritized queue not implemented, Windows nested effective job/Go heap natural pressure and WAN soak not production-validated. Release remains BLOCKED, PRs DRAFT.
