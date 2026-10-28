# PxGo R48 — Keep connection class accounting budgets aligned with the effective physical admission cap

Date: 2026-10-28 ICT. Parent PR#103 exact immutable SHA `125770cb1788c9387ca143504ebc94b0c5af4245`; PR#102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`. Small **experimental AUTO class-accounting runtime correction**, with no change to physical admissions, R11 policy, emergency controller transitions, connection classification, legacy/shadow mode or shipped default. PRs remain OPEN DRAFT; no merge/release/tag authorization.

## Source-proven defect

`newCapacityGovernor(modeAuto, cfg)` created `connectionClassManager` and called `sampleBudgets(g.soft)` exactly once, where initial soft=256. Later changes to `g.effective` via `recomputeLocked()` did NOT update class budgets. Example: with manual `cfg.MaxConnections=16`, authoritative physical `EffectiveLimit=16` yet Doctor class pool advisory budget incorrectly reported `ShortBudget=64 LongBudget=192`. The split was stale and could be > physical cap after manual limits, OS/Job pressure or emergency shutdown. These are advisory counters, but misleading diagnostic data and an unsafe design starting point for future class-aware admission.

## Narrow fix

Only `internal/proxy/capacity.go` changes: remove the one-off startup call to `sampleBudgets(g.soft)`, and when `recomputeLocked` changes `g.effective`, atomically update `g.classes.sampleBudgets(n)` within the existing `g.mu` critical section before `notifyLocked`. Existing lock ordering `governor.mu → classManager.mu` matches `capacityGovernor.snapshot`; class methods do not acquire governor.mu while holding classManager.mu. Nil classes path stays unchanged, and new code cannot create or release an extra physical permit. Class accounting's 25/75 split is based on the **actual** effective window; it remains soft and has NO short-priority or hard reservation semantics.

Two tests appended to `internal/proxy/capacity_classes_test.go`:
1. `TestConnectionClassBudgetsFollowEffectiveLimit`: manual 16 → class budgets 4/12, OS shrink to 7 → 1/6, soft growth with manual cap stays 4/12, emergency FD zero → 0/0, recovery 16 → 4/12.
2. `TestConnectionClassBudgetsDoNotEvictExistingLongOnShrink`: 16 authenticated long class leases survive shrink to effective 8, class budget 2/6 and long debt 10; all leases finish idempotently with zero class count.

No actual fair scheduler was introduced; reserving four class *accounting slots* cannot force short progress when 16 physical sockets are held indefinitely as long. True class admission remains separate production work.

## Red→green, cross-platform evidence

**Original unchanged R47 exact SHA** with only the NEW test copied to a fresh disposable clone: failed as expected, `EffectiveLimit=16 ShortBudget=64 LongBudget=192`, expected 16/4/12. `BASELINE_R47_NEGATIVE_PROOF_PASS`. This proves the tests catch a pre-existing defect, not simply matching a changed output.

**Corrected R48 isolated staging** (original root WIP untouched):
- `gofmt -l`: clean.
- Targeted class budget and existing promotion/close tests: **20 repeats PASS**.
- `go test ./internal/proxy -count=1`: PASS (approximately 16 seconds).
- `go test ./... -count=1`: ALL packages PASS (including proxy and Guardian).
- `go vet ./...`: PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/proxy`: PASS.
- `git diff --check`: PASS.
- Native Windows exact SHA CI and independent separate READ-ONLY KitAuditor still **PENDING** until branch commit/CI, not implied by crossbuild.

## Risk and unresolved production blockers

The governor's `g.mu` and classManager `m.mu` lock order should be explicitly examined by independent reviewer for possible re-entrance and shutdown interleavings. Current direct code inspection finds no reverse lock acquisition. New class-budget split is telemetry only and may be zero temporarily when emergency gate closes. Manual ceiling can be below active after shrink without forced eviction; debt remains as before.

R47 full CI on exact parent SHA completed success, 130 jobs (128 SUCCESS, 2 conditional SKIPPED); native strict 2,560 held CONNECT +600 HTTP Job Object memory job-wide tests PASS, but protected enterprise AD/SSPI remains conditional SKIPPED. No real enforced class fairness, naturally exhausted Go heap under nested Job Object pressure, Linux PSI, WAN and longer field soak gate. Keep BOTH PRs DRAFT, no merge/release/default activation until true separate independent KitAuditor source+CI PASS and production authorization.
