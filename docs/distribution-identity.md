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
PxGo does not claim an official WinGet package until a fork-owned package ID is
published through the external WinGet repository. Candidate package
`KhanhKit.PxGo` tracks the current v0.7.1 release in `microsoft/winget-pkgs#440461`.
It must not be advertised as published until that upstream PR is accepted;
Microsoft CLA acceptance and moderator review remain external requirements.

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
