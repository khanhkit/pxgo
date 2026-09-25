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

WinGet metadata inherited from the previous repository identity is retired.
Fork-owned package `KhanhKit.PxGo` v0.7.1 was moderator-approved and merged in
`microsoft/winget-pkgs#440461` on 2026-09-25; the manifests are present on the
upstream `master` branch. Microsoft catalog publication is asynchronous after
merge. Until `Publish-Pipeline-Succeeded` is observed and the official WinGet
CDN/catalog index contains `KhanhKit.PxGo` v0.7.1, do not yet advertise
`winget install KhanhKit.PxGo` as a live install path.

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
