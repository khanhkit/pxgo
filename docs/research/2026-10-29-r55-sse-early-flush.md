# PxGo R55 — SSE first-event streaming flush fix (experimental PR #103)

Date: 2026-10-29 ICT. Grounded immutable parent R54 SHA `a0b1c0b647489ab26ae668ce3d5212034d7404c5`, open DRAFT PR #103, base PR #102 `fb2aa923a753309dae515fc8be38112f45fbddc8`. No config/default/admission policy change; patch touches only `internal/proxy/intermediary.go`, adds `internal/proxy/sse_first_event_flush_test.go`, and this research doc. Root checkout user WIP preserved. NO MERGE/RELEASE.

## Reproduced actual defect — before patch
A new strict on-wire `TestSSEFirstEventMustArriveBeforeUpstreamUnblocks` runs a real upstream `httptest.Server`, sends HTTP response `Content-Type: text/event-stream; charset=utf-8`, emits `data: ready\n\n` and calls upstream `http.Flusher.Flush()` while explicitly BLOCKING further upstream data. The client sends an ordinary `Accept: application/json` through real PxGo TCP HTTP proxy, requires downstream response headers and the first SSE event BEFORE the origin is unblocked. On exact immutable R54 source unmodified, this test **FAILED** consistently in an observed run after 1.31 seconds: `SSE headers were not flushed before upstream second event: ... i/o timeout`. Current proxy `writeHTTPResponse` calls `rw.WriteHeader(...)` then `io.Copy(rw, resp.Body)` without flushing, so the first event can be withheld until the origin sends additional data or closes.

## Small fix / bounded claims
For actual **upstream response Content-Type**, case-insensitive media type token `text/event-stream` (ignoring semicolon parameters), after `WriteHeader` call downstream `http.Flusher.Flush()` immediately, then copy upstream response through a tiny `io.Writer` that flushes after each successful `Write`. Non-SSE HTTP response copy is left unchanged; no Accept-header-based branch, no artificial timeout tuning, no content transformation, no forced body buffering or premature cancellation. The original trailer forwarding remains after the copy, no changes to upgrades/tunnels/auth/PAC/DoH/capacity.

New strict test confirms first frame+separator delivered while origin is BLOCKED, only then releases origin and confirms second frame intact, with 1300ms initial TCP read deadline. A buffered proxy cannot PASS merely by eventually delivering both frames after upstream EOF.

## Verified test evidence and limitations
- Separate owned exact-R54 SHA clone at `pxgo-autopilot-60m-20261029-1420/r55/shadow`, no mutations to dirty root.
- NEW on-wire test against unchanged R54 **FAIL** (1.31s timeout), then with isolated fix **15/15 consecutive PASS**; strict marker `SSE_EARLY_FLUSH_RESULT first_frame_before_upstream_release=true second_frame_after_release=true content_type_params=true real_tcp=true`.
- `go test -count=1 -timeout=150s ./...` **all packages PASS**, `go vet ./...` PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy` PASS, Windows `go vet ./internal/proxy` PASS; `gofmt -l` empty, `git diff --check` clean.
- **Known separate issue NOT FIXED**: optional R55 negative fixture `PXGO_RESEARCH_SSE_NEGATIVE=1` demonstrates opt-in strict-long intake's request-header preclassification mistakes: upstream truly returns SSE while client Accepts JSON, but proxy keeps it SHORT with 12/12 long occupied; a JSON GET with `Accept: application/json, text/event-stream;q=0` can be denied 503 because parser searches for SSE token without considering q=0. Exact local negative marker `SSE_MISCLASSIFICATION_NEGATIVE real_sse_short_class=true long_cap=12 live_long=12 false_503_q_zero=true http_status=503`. This is a separate design/release blocker and requires response-aware class accounting and secure authorization-aware behavior; no claim that flush resolves it.
- No separate independent KitAuditor verdict. Full new immutable R55 GitHub CI needs to complete with all jobs enumerated and raw logs at exact HEAD before a source+CI PASS is claimed. Protected Windows enterprise AD/SSPI conditionally SKIPPED, fair QoS long/short, natural Go heap resource pressure, Linux PSI and WAN soak still need gates.

## Release hold
Both PR #102 and #103 stay OPEN DRAFT, legacy remains default; no merge/release/tag or force push. This is a narrow data-plane correctness fix inside an experimental PR, NOT a production capacity fairness approval.
