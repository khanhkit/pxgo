# PxGo R57 — Fix HTTP Accept quoted commas in experimental SSE classification

Date: 2026-10-29 ICT. Parent exact immutable PR #103 R56 SHA `bc8e2f51e3817ad8af1add2ea8234d7b96c1cf36`. PR #102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`. **Experimental source bugfix and tests; no default-mode change, merge, tag or release.**

## Root cause and exact pre-fix reproducibility

The experimental post-authentication class accounting treats the client's `Accept` explicit `text/event-stream` as long. The R56 `acceptsEventStream` helper used `strings.Split(headerValue, ",")` before `mime.ParseMediaType`. But in an HTTP quoted-string media parameter, **a comma is ordinary data** and does not delimit media ranges. This is a real parser bug, not a benchmark artifact.

Isolated exact R56 GitHub SHA clone, standalone `TestAcceptSSEQuotedMediaRangeAudit`, **pre-fix failure** in `R57_PRE_FIX_TEST.log`:
- False positive: `Accept: application/json; note="intro, text/event-stream; q=1, outro"` was incorrectly interpreted as explicit SSE (got true, want false). Under strict-long intake with existing long budget full, normal JSON may receive an unnecessary 503.
- False negative: `Accept: text/event-stream; note="left,right"` was incorrectly interpreted as non-SSE (got false, want true), skipping early post-auth long classification and relying only on actual upstream Content-Type later.
- Correct previous `q=0` and actual HTTP200 upstream response classifiers must remain intact.

## Narrow solution

Only `internal/proxy/capacity_classes.go`: split comma-separated media ranges while respecting quoted strings and a backslash-escaped quote inside quotes. Then leave each complete item to `mime.ParseMediaType` and existing strict RFC-style qvalue validation. Do not invent new media type parser or touch the single global physical permit or budget allocation.

`internal/proxy/capacity_sse_response_classification_test.go`: add quoted commas, escaped quotes, q0/positive qvalue variants, and actual on-wire JSON HTTP200 through the proxy at both 11/12 and 12/12 existing held long CONNECTs, proving no spurious 503 under a full long cap. Test also checks first SSE event, real SSE classification, denial and physical/class drain.

## Verified on isolated exact parent before R57 GitHub commit

- New standalone adversarial `TestAcceptSSEQuotedMediaRangeAudit`: pre-fix **2 FAIL / 2 PASS**, post-fix **4/4 PASS, 3 full test repeats**. Audit-only fixture remains in staging, not required for shipped Go test source because actual integration+unit regressions were added to the existing tracked test file.
- `TestAcceptsEventStreamQualitySemantics` expanded from 17 to 23 cases, **3 repeats PASS**.
- `TestStrictLongIntakeUsesActualSSEResponseAndAcceptQuality` real on-wire with 11 and 12 held true CONNECTs, including actual 200 JSON with quoted media-range-like false token at 12/12, **3 repeats PASS**.
- `go test -count=1 -timeout=300s ./...` FULL suite **PASS** (all packages). `go vet ./...` **PASS**.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy` and corresponding Windows `go vet ./internal/proxy` **PASS**.
- All tests ran in disposable isolated clone of the exact R56 GitHub SHA with separate Go module/build caches; original dirty `/home/coder/MCP/PxGo` WIP untouched.

## Remaining gates and honest claims

R57 is NOT an independent KitAuditor verdict. A genuine separate strict READ-ONLY review must evaluate the HTTP Accept tokenizer, RFC quoted-string escape semantics, malformed inputs, SSE reclassification, authorization, upstream body closure on 503, 503+Retry-After and exact physical permit invariants. GitHub Actions complete exact R57 SHA job pagination remains pending until commit+CI. Both PRs remain OPEN DRAFT, no merge/release/default AUTO promotion.

Protected native enterprise AD/SSPI remains conditionally skipped; class fairness, effective nested Windows Job Object COMMIT and natural Go memory allocation pressure, Windows outbound WSAENOBUFS-like native runner errors, WAN/soak/Linux PSI remain production release blockers.
