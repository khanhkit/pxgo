# Engineering TODO

These items are intentionally deferred external validation/governance work. They are not blockers for normal CI, release, or canonical issue completion as of 2026-09-19.

## Real AD / Kerberos runtime validation

- [ ] Provision one disposable Windows Server x64 VM for the `pxgo-ad` lab.
- [ ] Run `scripts/bootstrap-pxgo-ad.ps1 -Stage Promote -RebootAfterPromote`.
- [ ] After reboot, configure the domain runner with a one-time GitHub runner token and run `-Stage Configure` then `-Stage Validate`.
- [ ] Confirm the runner is online with labels `self-hosted`, `windows`, `x64`, `pxgo-ad`.
- [ ] Run `scripts/dispatch-real-ad-verification.sh main` to execute `TCSSPIWINAD010` through the manual protected workflow.
- [ ] Preserve evidence that `HTTP/<proxy-host>` obtains a Kerberos service ticket and that Negotiate does not fall back to direct NTLMSSP.

## Authoritative upstream governance

- [ ] Obtain admin/maintainer permission on `pavelsimo/pxgo`.
- [ ] Apply the repository-appropriate `main` branch protection policy on the authoritative upstream repository: strict Linux/Windows checks always; `0` required approvals for a solo-maintained repository, or a distinct approval when multi-maintainer governance requires it.
- [ ] Apply release-tag protection for `v*` on the authoritative upstream repository.
- [ ] If the manual real-AD workflow is enabled upstream, configure `pxgo-ad` for the repository ownership model: solo repositories may allow self-approval; multi-maintainer upstreams should require a distinct reviewer and keep self-review disabled.

## Policy

- Normal CI remains gated by Linux + Windows hosted verification and the native Windows SSPI gate where explicitly requested.
- Release remains gated by exact-tag-SHA verification and native Windows SSPI verification.
- Real AD/Kerberos validation is manual-only until the disposable lab exists.
- These TODOs must not be silently reintroduced as mandatory release blockers without an explicit project decision.

## Guardian external validation

- [ ] Run a real Windows sleep/resume cycle with the Guardian parent + worker and preserve evidence that the scheduler gap grants one fresh grace window instead of killing a healthy resumed worker.
- [ ] Run the Guardian recycle soak for 24h+ and record parent RSS/handle/process/goroutine/log growth plus orphan-worker count.
- [ ] Run a macOS Guardian smoke test when macOS CI/infrastructure is available.

These are extended platform/duration validation items. Deterministic scheduler-gap simulation, process fault injection, parent/child death handling, restart/backoff tests, local full race, and hosted Linux/Windows compile/test gates remain mandatory for canonical implementation completion.
