# Repository Protection Contract

This file documents the repository-admin controls required by AP-ISS-0024. Source changes cannot enforce these settings by themselves.

## Main branch

- Require pull requests before merge.
- Require the Linux and Windows CI matrix checks from `.github/workflows/ci.yml`.
- Require branches to be up to date before merge.
- Block force pushes and branch deletion.
- GitHub squash-merge commits on `main` are server-signed/verified. A branch-level required-signatures rule is intentionally not enabled until a durable maintainer signing identity is provisioned; otherwise unsigned local PR commits cannot merge.
- Require conversation resolution.
- On the solo-maintained `khanhkit/pxgo` repository, set required approving reviews to `0`; the hosted CI matrix is the mandatory merge gate and must not be bypassed.
- On a multi-maintainer authoritative upstream, a distinct approving review may be required as an additional governance gate.
- Do not permit administrators/bots to bypass required checks for release-bound commits except documented emergency recovery.

## Release tags

- Release publication must run from the exact tagged SHA.
- GoReleaser is gated by `verify-exact-sha` and `verify-windows-native`.
- Do not manually publish artifacts when either required release gate is absent, queued, skipped, cancelled, or failed.
- Protect `v*` tags/rulesets from unreviewed overwrite/deletion.
- Owner-confirmed real-AD/Kerberos manual validation is complete; the hardened workflow remains optional reproducibility infrastructure and is not a release blocker.

## Protected AD environment

- Create GitHub environment `pxgo-ad`. On the solo-maintained repository, self-approval may be enabled for this manual verification environment; on a multi-maintainer authoritative upstream, require a distinct reviewer and keep self-review disabled.
- Attach only a domain-joined self-hosted Windows x64 runner carrying labels `self-hosted`, `windows`, `x64`, `pxgo-ad`.
- Define environment variable `PXGO_SSPI_AD_PROXY_HOST` as the controlled upstream proxy DNS host whose service account owns `HTTP/<proxy-host>`.
- Run the self-hosted runner under an authorized domain identity; do not place reusable domain passwords in repository variables.
- Network access from the runner must be limited to the controlled AD/proxy fixture and normal build dependencies.

## Manual verification

`CI` exposes `workflow_dispatch` for pre-release verification of an explicit `verification_ref`. `native_sspi=true` runs the generic Windows NTLM/handle fixture.

Real-AD/Kerberos validation has its own manual-only `Real AD Verification` workflow. Owner-confirmed manual validation is complete; the workflow remains intentionally excluded from normal CI/release dependencies and is available for optional reproducible reruns. Hosted preflight validates an exact SHA from `main` history and stages canonical source before any protected runner executes it.

Repository administrators must enforce the settings above on the authoritative upstream repository. A fork or local source checkout can validate the workflow contract but cannot prove upstream governance is enabled.

## Single-VM real-AD fixture

The protected AD gate can be satisfied with one disposable Windows Server x64 VM; a second proxy VM is not required.

Prerequisites:

- Windows Server 2022/2025 x64 VM with a stable IPv4 address and normal outbound access to GitHub/build dependencies.
- Elevated local Administrator access for the initial forest promotion.
- Repository admin permission only long enough to obtain a one-time self-hosted runner registration token.
- The protected GitHub environment variable `PXGO_SSPI_AD_PROXY_HOST` must match the bootstrap `ProxyHost` value. The default contract uses `proxy.pxgo.test`.

Stage 1, before domain promotion:

```powershell
.\scripts\bootstrap-pxgo-ad.ps1 -Stage Promote -RebootAfterPromote
```

After reboot, sign in as the new forest Administrator. Generate a one-time repository runner token from an authorized administration workstation:

```bash
gh api --method POST repos/khanhkit/pxgo/actions/runners/registration-token --jq .token
```

Pass that token only through the process environment, then configure the VM:

```powershell
$env:PXGO_RUNNER_TOKEN = '<one-time-token>'
.\scripts\bootstrap-pxgo-ad.ps1 -Stage Configure
.\scripts\bootstrap-pxgo-ad.ps1 -Stage Validate
Remove-Item Env:PXGO_RUNNER_TOKEN
```

The bootstrap creates the `pxgo.test` forest/DNS zone, a dedicated `PXGO\pxgo-runner` service identity, the `proxy.pxgo.test` DNS record, and the unique `HTTP/proxy.pxgo.test` SPN. It installs a SHA-256-pinned GitHub Actions runner under `C:\actions-runner`, labels it `pxgo-ad`, runs it as the domain service identity, and disables automatic runner updates so the verification binary cannot drift without review.

The real-AD testcase does not trust a generic `Negotiate` success as Kerberos proof. It requires a domain UPN, a real KDC service ticket for the HTTP SPN, rejects direct NTLMSSP tokens, completes the native SSPI exchange, and verifies the authenticated domain username.

For an optional reproducible rerun, once the runner reports online, dispatch the exact 40-character `main`-history commit SHA from an authenticated admin workstation:

```bash
./scripts/dispatch-real-ad-verification.sh 8a592c647efe6eee7f566a077466222678e169fa
```

The helper validates the protected runner labels and environment variable, dispatches the canonical workflow from `main` for the exact SHA, relies on hosted `main`-history validation/source staging before protected execution, approves the protected environment on the single-user verification fork, and waits for the workflow result. On an authoritative multi-user upstream repository, keep self-review disabled and require a distinct reviewer instead.
