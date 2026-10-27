# PxGo R25 — Real WebSocket binary frames and class-aware observation under a single physical cap

**Status:** opt-in test-only research. Baseline immutable R24 PR #103 `44910d78aaed02f4c1fd946b4c87c8f83446aa0d`; this does not promote opt-in AUTO or create a hard two-class scheduler.

## Why this gate

Earlier `TestHTTPUpgrade101RelaysBidirectionally` sends unframed `ping` text to exercise basic 101 tunneling; the classifier lifecycle unit test predicts WebSocket as long, but no native Windows mixed WebSocket-frame + short traffic test had been run while the authoritative global physical cap was exhausted. R24 added TLS1.3/H2/SSE under physical saturation, but did not cover WebSocket `Upgrade: websocket`.

`TestCapacityMixedWebSocketSaturated` adds a real local TCP origin implementing the minimal RFC 6455 handshake (Sec-WebSocket-Key/Accept) and strictly checked FIN binary frames: each of 8 real clients masks payload frames, local origin unmasks and returns unmasked binary response, client verifies exact bytes. The origin is **not an external WebSocket service** and does not implement full fragmentation/close/extension negotiation. Eight independent real HTTP/1.1 Upgrade requests through the proxy become 8 held WebSocket tunnels. Eight additional sustained physical CONNECT tunnels pass unique real echo. No new production code or dependency.

## Acceptance invariants

- Explicit `PXGO_CAPACITY_MODE=auto` (actual env `PXGO_EXPERIMENTAL_CAPACITY_MODE=auto`) and `PXGO_CAPACITY_WS_BENCH=1` required; with classifier ON set `PXGO_EXPERIMENTAL_CONNECTION_CLASSES=1`; test remains silently skipped outside this opt-in but Windows CI **rejects absence of mandatory marker**.
- Check 8 authenticated-as-per-local-fixture WebSocket 101 handshakes plus 8 CONNECTs physically active with `MaxConnections=16`, `ActiveDownstream == EffectiveLimit == 16`. If class tracking is ON, `Long==Total==16`, `Short==Provisional==0`. Class quotas are **advisory accounting**, not enforced class reservations.
- A new real short HTTP/1.1 request must complete the TCP dial and cause `AdmissionWaits` to increase at the actual 16/16 cap. It must stay blocked for another 150ms. Closing exactly one held CONNECT frees its global permit and permits that same short HTTP request to finish successfully, then a replacement CONNECT refills the cap. No previously admitted WebSocket is displaced. The second different WebSocket masked binary frame is echoed after borrowing; all CONNECT echoes are also reverified.
- Explicitly close all 16 held sockets and verify all global and (when enabled) class leases drain to ZERO with timeout; origin and echo worker goroutines are joined; error paths have finite per-socket deadlines.
- Native Windows `windows-latest` seed0 for r11/fast_saturation × class ON/OFF (4 cells), before intensive mixed load. Exact SHA checkout and SHA-scoped artifacts remain the workflow policy. CI checks raw `WS_MIXED_RESULT websocket=8 connect=8 short_borrow=1 wait_delta=[1-9]... global_active=0` marker and tests exit code. Failure must not be replaced with an expected skip or bypassed via a retry of individual workload.

## Local source-only evidence (2026-10-27)

Test source overlaid on a disposable Linux source tree archived from exact R24 immutable SHA. Ten real Linux runs (5 class ON, 5 OFF) PASS with wait_delta=1 and global_active=0. Full `go test ./... -count=1`, `go vet ./internal/proxy`, `gofmt`, golangci-lint v2.13.2 (0 issues), and Windows amd64 Go test crosscompile all PASS. The host lacks CGO compiler; local `go test -race` **NOT RUN**. Native Windows execution MUST be independently verified from R25 exact-SHA CI after immutable commit.

## Remaining independent production gates

Protected enterprise AD/SSPI runner unavailable; enforceable Windows Job Object private commit/handles pressure + recovery, Linux cgroup PSI, calibrated resource envelope, statistically valid randomized same-VM policy crossover controlling TIME_WAIT, real WAN latency/loss/soak, production auth/TLS termination, and actual **fair per-class admission scheduling** under the sole global physical safety permit remain OPEN. A single short reclaiming one released long permit proves global work conservation only, not class fairness under sustained adversarial traffic. Legacy mode remains default, both PRs DRAFT, no merge/release/tag.
