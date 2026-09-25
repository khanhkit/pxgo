# Engineering TODO

These items are intentionally deferred external validation/publication work. They are not blockers for normal CI, release, or canonical issue completion as of 2026-09-25.

## Real AD / Kerberos runtime validation

Current GitHub-side state is complete: the protected `pxgo-ad` environment is configured and `PXGO_SSPI_AD_PROXY_HOST=proxy.pxgo.test` is present. The remaining prerequisite is an actual domain-joined Windows x64 runner plus reachable AD/KDC/SPN fixture. Current self-hosted runner inventory is 0.

- [ ] Provision one disposable Windows Server x64 VM for the `pxgo-ad` lab.
- [ ] Run `scripts/bootstrap-pxgo-ad.ps1 -Stage Promote -RebootAfterPromote`.
- [ ] After reboot, configure the domain runner with a one-time GitHub runner token and run `-Stage Configure` then `-Stage Validate`.
- [ ] Confirm the runner is online with labels `self-hosted`, `windows`, `x64`, `pxgo-ad`.
- [ ] Dispatch `.github/workflows/real-ad-verification.yml` against stable v0.7.1 exact SHA `8a592c647efe6eee7f566a077466222678e169fa` to execute `TCSSPIWINAD010RealADNegotiateUsesKerberos` through the protected workflow.
- [ ] Preserve `klist` plus `HTTP/<proxy-host>` service-ticket evidence showing Negotiate obtains Kerberos and does not silently fall back to direct NTLMSSP.

## Repository governance

- [x] Protect `main` with strict required `Test (ubuntu-latest)` + `Test (windows-latest)` checks and admin enforcement.
- [x] Keep required approving reviews at `0` for the current solo-maintainer model while preserving pull-request/conversation protection.
- [x] Protect release tags `v*` against update/deletion (`Protect release tags v*`).
- [x] Configure the protected `pxgo-ad` environment for the current ownership model.

## WinGet publication

Current submission: `microsoft/winget-pkgs#440461`, `KhanhKit.PxGo` v0.7.1, head `2ddc5fe5acc6ea25e884acfc427cedac8422d9cd`.

- [x] Microsoft technical validation stages 01-10 pass (`Azure-Pipeline-Passed`, `Validation-Completed`).
- [ ] Account owner accepts the Microsoft Contributor License Agreement. This is a user-owned legal attestation and must never be performed autonomously.
- [ ] Microsoft/community moderator reviews and merges the upstream PR.
- [ ] Verify `KhanhKit.PxGo` is visible in the public WinGet catalog before advertising `winget install KhanhKit.PxGo` as an official install path.

## Guardian external validation

- [ ] Run a real Windows sleep/resume cycle with the Guardian parent + worker and preserve evidence that the scheduler gap grants one fresh grace window instead of killing a healthy resumed worker.
- [ ] Run the Guardian recycle soak for 24h+ and record parent RSS/handle/process/goroutine/log growth plus orphan-worker count.
- [x] Run a native macOS Guardian process-lifecycle smoke on hosted `macos-15` arm64 CI, covering startup failure, ready-crash/hang/control-close recycle, graceful/forced stop, parent-death orphan cleanup, and a short recycle soak (PR #42 / run `36043844328`).

## Policy

- Normal CI remains gated by required hosted Ubuntu + Windows verification plus native Linux arm64/macOS arm64 execution.
- Release remains gated by exact-tag-SHA verification, native Windows SSPI verification, candidate execution, and immutable promotion.
- Real AD/Kerberos validation is manual-only until the disposable domain-joined lab exists.
- Windows sleep/resume and continuous 24h+ Guardian soak remain extended external validation and must not be represented as complete using deterministic short tests.
- These TODOs must not be silently reintroduced as mandatory release blockers without an explicit project decision.
