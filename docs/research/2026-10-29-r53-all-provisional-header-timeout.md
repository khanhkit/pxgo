# PxGo R53 — Native preauth header-timeout conditional short progress

Date 2026-10-29 ICT. Parent PR#103 exact R52 SHA ae5fcbd4b7be121ee97f5ab1dc33b046c36828b3, PR#102 base fb2aa923a753309dae515fc8be38112f45fbddc8. Both OPEN DRAFT, no merge/release/default change. One new Go TEST file and research note only, no runtime/scheduler/CI workflow change.

## Purpose

R52 demonstrated with 12 authenticated long CONNECT plus 4 silent provisional TCP connections filling the physical cap16 that an ordinary short HTTP request enters actual capacity AdmissionWaits and cannot complete until a provisional socket is released. R53 tests if existing real net/http ReadHeaderTimeout will *eventually* reclaim unclassified physical permits, allowing a waiting short to proceed without any manual test-issued close of silent TCP clients.

## Strict actual TCP test

1. Scope AUTO/class ON/strict long-intake experimental env flags using t.Setenv. Create actual PxGo listener cap16, SockTimeout=3 seconds, Idle=6 and ProxyReload disabled. The configured SocketTimeout maps both HTTP ReadHeaderTimeout and physical capacity acquire deadline to three seconds.
2. Dial and hold sixteen **real idle loopback TCP sockets** without ever sending any header bytes; poll actual capacity snapshot until ActiveDownstream=16 and provisional=16, long=0. This proves NO provisional-class quota smaller than physical limit.
3. Start short request 750ms after full plateau so older preauth sockets must expire before the short's own admission deadline. Send ordinary HTTP GET through PxGo to a real httptest HTTP origin; require actual AdmissionWaits increment and no response for 150ms.
4. Without manually closing a provisional socket in the test, rely on the real HTTP server header timeout to release a physical token. The GET MUST complete HTTP 200 with correct body within bounded wait. If it instead gets timeout/EOF/502, the test fails; no outcome waiver.
5. On all exits close test-owned client TCP sockets and verify physical active=0 and class total=0 BEFORE PxGo server shutdown; bounded shutdown. Strict Go positive marker STRICT_PROVISIONAL_TIMEOUT_RESULT initial_provisional=16 physical_cap=16 header_timeout_s=3 short_wait_proven=true short_completed_after_idle_expiry=true elapsed=....

## Exact isolated preflight evidence

- Parent clone SHA explicitly verified equal to R52; original user dirty PxGo checkout untouched, Go cache under isolated /tmp.
- gofmt clean.
- First Linux ARM64 opt-in test PASS: 3.02s test time, waiting short completed after 2.240515107s.
- Repeat 12/12 PASS (36.195s).
- Full go test ./... -count=1 PASS all packages.
- go vet ./... PASS.
- Windows amd64 CGO disabled full proxy test compile PASS.
- golangci-lint run ./... PASS, 0 issues.

## Interpretation & release gating

R53 proves *conditional* short progress when 3s preauth read deadline is earlier than a waiting short's remaining acquisition deadline. It is NOT strict fair admission or an unconditional bound. Shipped SockTimeout default is 20s; real clients may time out sooner, and competing preauth clients can refill the released slot. No new runtime Pmax or class-priority scheduler was implemented. Existing 25/75 class long-intake guard remains post-authorization only, and all 16 silent provisional sockets can fill the global physical cap. Do not conflate an eventually successful short with fairness.

Native GitHub R53 exact-SHA CI remains pending until commit and all 130+ jobs verify terminal; independent separate STRICT READ-ONLY KitAuditor verdict remains missing (lifecycle bridge dry-run exit2); protected enterprise AD/SSPI conditional SKIPPED, effective nested Windows Job Object sensor, natural Go OOM and WAN multi-hour soak remain open. Both PRs DRAFT, legacy remains default, release BLOCKED.
