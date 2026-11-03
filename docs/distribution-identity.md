# Distribution identity

PxGo is published and maintained from `khanhkit/pxgo`.

## Authoritative identities

- GitHub repository: `https://github.com/khanhkit/pxgo`
- Go module path: `github.com/khanhkit/pxgo`
- Homebrew tap: `khanhkit/homebrew-tap` (published and updated by a tap-scoped write deploy key)
- Scoop bucket: `khanhkit/scoop-bucket` (published from the exact staged release manifest using a bucket-scoped write deploy key)

The Go module/import path was migrated with the v0.5.0 ownership cleanup so source,
build metadata, documentation, and release contracts use the same fork identity.

## Windows distribution

Every release generates `pxgo-scoop.json` from that release's immutable archive
checksums and publishes it alongside the archives. Stable releases also copy that
exact staged manifest into the official `khanhkit/scoop-bucket`; the release asset
remains available as a bucket-independent direct-install fallback.

Legacy GoReleaser-owned WinGet metadata from the previous repository identity is retired.
Fork-owned package `KhanhKit.PxGo` was established by moderator-approved
`microsoft/winget-pkgs#440461` on 2026-09-25. WinGet is a live official distribution
channel and every stable PxGo release is submitted from CI with the pinned
WingetCreate client. `WINGET_CREATE_GITHUB_TOKEN` is mandatory for stable release
submission; the token is consumed through WingetCreate's environment variable and
is never placed on the command line. Submission is idempotent when the exact
version is already upstream or an exact-version PR is still open:

```powershell
winget install --id KhanhKit.PxGo --exact
```

## Retry WinGet after GitHub Release promotion

A failed **Submit WinGet update** job can occur after the immutable GitHub
Release has already been published. **Do not rerun the entire Release
workflow** to repair this distribution-only failure: that would repeat
release candidate/promotion stages unnecessarily.

Use the already-installed [WinGet Sync workflow](https://github.com/khanhkit/pxgo/actions/workflows/winget-sync.yml)
to retry the exact published stable tag. For example:

```bash
gh workflow run winget-sync.yml -R khanhkit/pxgo --ref main -f tag=v0.9.0
```

Replace `v0.9.0` with the **exact already-published release tag** requiring
synchronization; do not create a new release to repair WinGet. The workflow
checks out the trusted workflow SHA, fetches the specified tag and requires
that SHA to be an ancestor of the repository's current `main`. It then runs
the same pinned/checksummed WingetCreate publisher with repository-scoped
credentials. The publisher checks GitHub Release stability and artifact
availability, and short-circuits if that exact version has already landed
in `microsoft/winget-pkgs` or has an open upstream submission.

A successful synchronization workflow only proves that submission was
accepted or already converged at the publisher. As specified by the green
release policy, a pending unmerged WinGet PR does **not** make the release
green. Verify the exact version is published to the public WinGet source.

This independent path means a post-promotion WinGet failure remains visible
as a failure while a retry never rebuilds or re-promotes the immutable
GitHub Release.

## Green release convergence

The canonical green-release invariant is defined in `docs/release-green-policy.md`.
A release is not green merely because GitHub promotion succeeded: every live official
distribution channel, including WinGet, must advertise the exact same stable version.
An open WinGet submission is pending convergence, not success.

## Publication policy

GitHub Releases target `khanhkit/pxgo`. Homebrew publication targets
`khanhkit/homebrew-tap` and remains guarded by `PXGO_HOMEBREW_TAP_ENABLED=true`.
Scoop bucket publication targets `khanhkit/scoop-bucket` and is guarded by
`PXGO_SCOOP_BUCKET_ENABLED=true`. Each cross-repository publisher uses its own
write deploy key scoped only to the destination repository rather than an
account-wide token.

Both distribution repositories protect `main` with administrator enforcement,
linear history, and force-push/deletion disabled. They intentionally do not
require pull requests or status checks on `main`: the only automated writer is
the repository-scoped release deploy key, which must be able to publish an
ordinary release commit directly after PxGo's immutable-promotion gate succeeds.
Release workflows are serialized, and each downstream publisher refuses to
replace a newer package version with an older stable tag, preventing stale runs
or re-runs from rolling the Homebrew tap or Scoop bucket backward.

Historical copyright and attribution remain unchanged.
