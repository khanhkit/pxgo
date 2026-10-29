# PxGo R54 — Slowloris trickle headers cannot extend absolute preauthentication timeout

Date 2026-10-29 ICT. Exact immutable parent PR #103 R53 `4d79e5ef152c7933ad678504a35e0d3689506cef`, PR #102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`, both DRAFT. **New test-only Go file and this research report**, no shipped/runtime/config/proxy scheduler changes, no release.

## Why this adds evidence beyond R53

R52 showed 12 valid authenticated CONNECT + 4 silent/no-header provisional TCP connections physically saturate 16 permits even when R51 strict long-intake guard is enabled. An ordinary short HTTP request waits for a physical slot and cannot complete until a provisional is closed. R53 then showed 16 completely silent provisional connections eventually time out via real `net/http.ReadHeaderTimeout` at configured `SockTimeout=3`, allowing an ordinary HTTP short to complete. R53 cannot by itself exclude a malicious client periodically sending a byte to reset an *idle* timeout. User wants bounded preauth slowloris behavior before claims of short/long admission fairness.

## R54 actual on-wire test

`TestStrictLongIntakeAllProvisionalSlowHeadersTimesOut` creates real PxGo TCP listener with experimental AUTO+class tracking+strict long-intake **scoped only to the test**, hard global physical cap16, `SockTimeout=3s`, Idle=6s, ProxyReload disabled. Dials 16 client TCP connections, each sends syntactically plausible but **unfinished** HTTP/1.1 headers ending at `X-Slow: `, never terminates request with CRLFCRLF. Polls until 16/16 physical permits are occupied and class Provisional=16, Long=Short=0. Starts a private goroutine PER connection that writes a real `x` byte every 200ms. Writers do not voluntarily close connections to release capacity. Atomic success count must show at least 16 genuine drip payload writes.

750ms after 16/16 plateau, starts a REAL valid short HTTP GET through PxGo to a real httptest upstream; asserts global `AdmissionWaits` rose and request did not finish for at least 150ms while all physical slots occupied. With ongoing trickle bytes, verifies the server's existing **absolute** `ReadHeaderTimeout=3s` closes the slow headers and releases physical tokens; the waiting short returns HTTP 200 and correct origin body within bounded timeout, with no test-issued manual close before that response. Test result marker: `STRICT_PROVISIONAL_TRICKLE_RESULT initial_provisional=16 physical_cap=16 read_header_timeout_s=3 drip_writes=N short_wait_proven=true short_recovered_without_manual_close=true elapsed=T`.

On success or failure, coordinated defer stops all writer goroutines, closes all 16 owned client sockets and joins the drippers; polls physical+class drained to ZERO **before** bounded PxGo Shutdown. No goroutine leak/false claim from service shutdown. This is active header *trickle*, not an idle socket.

## Local isolated preflight (not independent audit)

- Entire candidate was staged under owned `.hotfix-work-20261022/pxgo-r54-slowheader-20261029-1130`, separate full Git clone `r54-shadow` verified `git rev-parse HEAD=4d79e5ef152c7933ad678504a35e0d3689506cef`. Original root dirty checkout untouched.
- `gofmt -l` clean.
- Linux ARM64 test: **9/9** exact test invocations PASS (1 original, 4 with `GOMAXPROCS=1`, 4 with `GOMAXPROCS=8`), ~3.05 seconds per run.
- Entire `go test -count=1 -timeout=180s ./...` PASS all packages; `go vet ./...` PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c` and `go vet ./internal/proxy` PASS.
- `golangci-lint run ./...` **0 issues**; benign read-only global lint cache warnings did not prevent analysis. No new dependencies.
- Author source/static review is NOT a genuine separate independent KitAuditor verdict.

## Limitations and production decisions

This proves only **conditional liveness** for existing TCP slow-header sockets that expire within the waiting short client's own timeout; the configured test `SockTimeout=3s` is NOT the shipped/default production 20s. A continuous burst of *new* malicious preauth sockets can capture freed permits before an ordinary short (the physical admission queue currently has no class fairness). Nothing in R54 introduces a bounded preauth cohort below H, strict short physical reservation, or prioritization after auth. The R51 opt-in guard still runs after physical accept and only refuses new explicit long requests after HTTP auth; it cannot guarantee short success at cap. Keep both PRs DRAFT and release blocked by actual separate post-R22 KitAuditor source/security+exact CI, protected enterprise AD/SSPI native gate (conditional skipped), Windows effective nested Job Object/Go heap pressure sensor and WAN/Linux soak.

Historical R53 Actions `40791969138` attempt1 native Windows paired long_heavy/ON/seed0/fast-first FAILED 199/200 short with WinSock outbound bind insufficient buffer; exact SHA attempt2 reran only failed job and completed success 130 jobs 128 SUCCESS, 2 conditional SKIPPED. Preserve both attempts; no claim root cause solved. R54 needs its own complete exact SHA Go/Windows CI and raw on-wire marker before it can be called green. No merge/tag/release/default promotion.
