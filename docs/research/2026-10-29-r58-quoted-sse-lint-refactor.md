# PxGo R58 — Resolve R57 gocyclo test-only CI blocker without suppressions

2026-10-29 ICT. Parent exact immutable R57 PR #103 SHA `f3f2b8785702f89f8aba59ba5ba94a8225177fad`, base PR #102 SHA `fb2aa923a753309dae515fc8be38112f45fbddc8`, both DRAFT.

## Original R57 failure retained

R57 native Go suite, vet and Windows crossbuild all PASSED in isolated staging; the source fix for quoted HTTP Accept comma was verified pre-fix FAIL (two independently observed true regression cases) and post-fix PASS.

However exact R57 GitHub Actions run `40878636776`, Ubuntu `Test (ubuntu-latest)` job `123463898892`, FAILED in `golangci-lint v2.12.2` at `internal/proxy/capacity_sse_response_classification_test.go:21`: `gocyclo cyclomatic complexity 51 (>50)` in `runStrictSSEActualResponse`. The added on-wire JSON quoted-media check contained multiple branches and pushed the existing test helper one point beyond repo lint threshold. The source bugfix itself was NOT proven to have failed Go tests. Preserve this exact CI failure and do not waive it or claim R57 overall green.

## R58 narrow correction

Change ONLY existing `internal/proxy/capacity_sse_response_classification_test.go`: move the quoted-Accept JSON200/503 on-wire check into a dedicated `assertQuotedAcceptJSONResponse(t, client, origin.URL)` helper, called at the original point in the test sequence. The helper verifies the same request URL, the same quoted `Accept` value, HTTP200, JSON body and body read/close errors. The 11/12 and 12/12 long quota, initial genuine SSE first frame, full physical/class drain, q=0 unit variants, and production parsing remain UNCHANGED. No `//nolint`, skipped test, weakened condition or deadline change. Commit no production runtime source.

## Locally verified against exact R57 parent in isolated clone

- `go test -count=3 -run 'TestAcceptsEventStreamQualitySemantics|TestStrictLongIntakeUsesActualSSEResponseAndAcceptQuality' ./internal/proxy`: PASS.
- `go test -count=1 -timeout=300s ./...`: full suite PASS.
- `go vet ./...`: PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy`: PASS.
- `golangci-lint v2.13.2 run --new-from-rev=HEAD --timeout=180s ./internal/proxy`: **0 new lint issues**, without suppressions. CI pinned v2.12.2 must still run and PASS on new exact immutable SHA.
- Original dirty root remained untouched. Only isolated test staged.

## Gates

R58 exact SHA CI is pending until new GitHub commit and full job pagination; previous R57 workflow's failure must remain noted. A distinct genuine strict READ-ONLY KitAuditor is still required for PR #102 and R24–R58. Protected enterprise Windows SSPI SKIPPED, true physical class fairness, effective nested Windows COMMIT/Go runtime natural pressure, WAN/Linux PSI and historical WSAENOBUFS remain release blockers. Both PRs DRAFT; no merge/tag/release/default promotion.
