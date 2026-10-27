# R23: TLS 1.3, HTTP/2 multiplexing and SSE with saturated physical admission

Status: **experimental research-only; production release gates remain OPEN**.
Parent exact PxGo PR #103 R22 SHA `9f730423f7f220367de8cc3bdc61d8671357f90a`.
No production runtime, governor, classifier, default mode or auth code changes.

## Purpose and acceptance

The existing Windows mixed suite covers HTTP/1.1 requests, live CONNECT tunnels and real echo payloads, but not encrypted HTTP/2 multiplexing or real Server-Sent Events. R23 adds opt-in `TestCapacityTLSHTTP2StreamingMixed` in `internal/proxy/capacity_tls_h2_mixed_test.go`, using only existing Go dependencies and a locally generated `httptest` TLS certificate rooted in an explicit trust pool. No `InsecureSkipVerify`, real external network destinations or corporate credentials.

The test requires **actual TLS 1.3**, ALPN `h2`, HTTP response protocol version 2 and exact response/event bytes, otherwise it fails. Six real SSE streams remain open on six distinct HTTP/2 TCP/TLS/CONNECT sessions, while 48 additional HTTP/2 streams share those six underlying sockets and 64 short HTTP/1.1 requests traverse the same proxy. Six separate held CONNECT echo tunnels must still echo correctly afterward.

`MaxConnections=16` is the shared, authoritative hard physical ceiling. The fixture checks six CONNECT echoes + six TLS/H2 connections = 12 authoritative downstream permits, and when classes are enabled all 12 must be `long` with no provisional/short residual permits. It then opens four more echo CONNECT tunnels to reach **exactly 16/16**; a new short HTTP request must NOT finish during 150ms of physical saturation. Closing one long tunnel must unblock the short, and a replacement tunnel must refill the single released physical permit without dropping the ongoing SSE sessions. The four extra echo tunnels are closed; the six original echoes survive verified. SSE final events are then released and verified, and all transient short HTTP permits must drain before the final 12-long permit conservation assertion.

The native `windows-latest` opt-in gate should run on seed 0, per controller policy (`r11`, `fast_saturation`) and class accounting on/off, **before** 2,560 tunnel stress to avoid TIME_WAIT interference. CI must check out the exact immutable head SHA and retain raw `benchmarks/tls-h2-streaming.log` on success and failure.

## Initial local source and test evidence (2026-10-27 ICT)

- Verified R22 handoff SHA256SUMS 2/2; original PxGo checkout WIP left unchanged.
- Isolated source extracted from immutable R22 Git object, then overlaid with the single proposed test file; code in a separately owned staging location.
- Initial real Linux test: classifier ON/OFF each PASS; after adding the saturated physical permit borrow/replenish proof: **10/10 Linux opt-in runs ON and 10/10 OFF PASS**.
- Linux `gofmt` and `go vet ./internal/proxy` PASS; full `go test ./... -count=1` PASS; `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy` PASS.
- Local Go `-race` unavailable because the KitMCP sandbox has `CGO_ENABLED=0` and no gcc. This is **unrun**, not PASS. Exact-SHA CI and a real Windows runner must be verified after branch update.

## Limitations and release hold

This is loopback TLS/HTTP2/SSE, not WAN-loss, multi-hour memory/CPU soak, a real enterprise AD/SSPI flow, Windows Job Object hard limit, or a production QoS/starvation guarantee. HTTP/2 is end-to-end **inside CONNECT**, not HTTP/2 as a client-to-proxy front-end protocol. The 25/75 short/long budgets remain advisory, with no class-aware prioritized scheduler. The borrowed permit is global physical admission under saturation, not a promise that a short queue can preempt already admitted long connections. No recommendation or evidence of fast-saturation superiority follows from this test. Earlier R22 order-balanced results are unchanged.

**Keep PR #103 and #102 DRAFT; no merge, release, tag, forced push or experimental default activation.** Exact-SHA native Windows CI, full paginated job status and independent strict read-only KitAuditor review remain required for R23.
