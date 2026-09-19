# Repository Protection Contract

This file documents the repository-admin controls required by AP-ISS-0024. Source changes cannot enforce these settings by themselves.

## Main branch

- Require pull requests before merge.
- Require the Linux and Windows CI matrix checks from `.github/workflows/ci.yml`.
- Require branches to be up to date before merge.
- Block force pushes and branch deletion.
- Require conversation resolution and at least one approving review for production changes.
- Do not permit administrators/bots to bypass required checks for release-bound commits except documented emergency recovery.

## Release tags

- Release publication must run from the exact tagged SHA.
- GoReleaser is gated by `verify-exact-sha`, `verify-windows-native`, and `windows-domain-sspi`.
- Do not manually publish artifacts when any of those jobs is absent, queued, skipped, cancelled, or failed.
- Protect `v*` tags/rulesets from unreviewed overwrite/deletion.

## Protected AD environment

- Create GitHub environment `pxgo-ad` with required reviewer approval.
- Attach only a domain-joined self-hosted Windows x64 runner carrying labels `self-hosted`, `windows`, `x64`, `pxgo-ad`.
- Define environment variable `PXGO_SSPI_AD_PROXY_HOST` as the controlled upstream proxy DNS host whose service account owns `HTTP/<proxy-host>`.
- Run the self-hosted runner under an authorized domain identity; do not place reusable domain passwords in repository variables.
- Network access from the runner must be limited to the controlled AD/proxy fixture and normal build dependencies.

## Manual verification

`CI` exposes `workflow_dispatch` for pre-release verification of an explicit `verification_ref`. `native_sspi=true` runs the generic Windows NTLM/handle fixture. `real_ad=true` schedules the protected `pxgo-ad` runner and requires the real AD testcase to exist in the selected ref.

Repository administrators must enforce the settings above on the authoritative upstream repository. A fork or local source checkout can validate the workflow contract but cannot prove upstream governance is enabled.
