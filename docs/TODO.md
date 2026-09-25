# Engineering TODO

These items are intentionally deferred external validation/publication work. They are not blockers for normal CI, release, or canonical issue completion as of 2026-09-25.

## Real AD / Kerberos runtime validation

- [x] Owner manually validated the real AD / Kerberos SSPI scenario successfully on 2026-09-25 and closed GitHub issue #31 as completed.
- [x] Protected `pxgo-ad` workflow/environment and hosted main-history source preflight remain available for future reproducible reruns.
- [x] Manual validation is recorded as owner-confirmed external evidence; it is not represented as a new GitHub Actions protected-runner execution.

The real-AD validation blocker is closed. A domain-joined self-hosted runner is now optional reproducibility infrastructure, not a release or issue-completion requirement.

## Repository governance

- [x] Protect `main` with strict required `Test (ubuntu-latest)` + `Test (windows-latest)` checks and admin enforcement.
- [x] Keep required approving reviews at `0` for the current solo-maintainer model while preserving pull-request/conversation protection.
- [x] Protect release tags `v*` against update/deletion (`Protect release tags v*`).
- [x] Configure the protected `pxgo-ad` environment for the current ownership model.

## WinGet publication

Current upstream package: `microsoft/winget-pkgs#440461`, `KhanhKit.PxGo` v0.7.1. PR merged on 2026-09-25 as `f93e24c1b1a1775ca8328e8e891113f09c4c337c`; manifests are present on upstream `master`.

- [x] Microsoft technical validation stages 01-10 pass (`Azure-Pipeline-Passed`, `Validation-Completed`).
- [x] Account owner accepted the Microsoft Contributor License Agreement on 2026-09-25; `license/cla` is SUCCESS.
- [x] Microsoft/community moderator approved and merged the upstream PR (`Moderator-Approved`).
- [ ] Wait for Microsoft publish pipeline completion (`Publish-Pipeline-Succeeded`) and verify `KhanhKit.PxGo` v0.7.1 appears in the official WinGet CDN/catalog before advertising `winget install KhanhKit.PxGo`.

## Optional extended Guardian evidence (non-blocking)

Production/release readiness does not depend on the two checks below. They are preserved as optional long-horizon evidence and must not be represented as completed until run on suitable hardware.

- [ ] Optional: run a real Windows sleep/resume cycle with the Guardian parent + worker and preserve evidence that the scheduler gap grants one fresh grace window instead of killing a healthy resumed worker.
- [ ] Optional: run the Guardian recycle soak for 24h+ and record parent RSS/handle/process/goroutine/log growth plus orphan-worker count.
- [x] Native macOS Guardian process-lifecycle smoke completed on hosted `macos-15` arm64 CI, covering startup failure, ready-crash/hang/control-close recycle, graceful/forced stop, parent-death orphan cleanup, and a short recycle soak (PR #42 / run `36043844328`).

## Policy

- Normal CI remains gated by required hosted Ubuntu + Windows verification plus native Linux arm64/macOS arm64 execution.
- Release remains gated by exact-tag-SHA verification, native Windows SSPI verification, candidate execution, and immutable promotion.
- Real AD/Kerberos validation is owner-confirmed complete; the protected workflow remains available for future reproducible reruns.
- Windows sleep/resume and continuous 24h+ Guardian soak are optional extended evidence, not production/release blockers, and must not be represented as complete using deterministic short tests.
- These TODOs must not be silently reintroduced as mandatory release blockers without an explicit project decision.
