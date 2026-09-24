# Distribution identity

PxGo is published and maintained from `khanhkit/pxgo`.

## Authoritative identities

- GitHub repository: `https://github.com/khanhkit/pxgo`
- Go module path: `github.com/khanhkit/pxgo`
- Homebrew tap: `khanhkit/homebrew-tap` (published and updated by a tap-scoped write deploy key)

The Go module/import path was migrated with the v0.5.0 ownership cleanup so source,
build metadata, documentation, and release contracts use the same fork identity.

## Windows distribution

Every release generates `pxgo-scoop.json` from that release's immutable archive
checksums and publishes it alongside the archives. It can be installed directly
from the GitHub Release without provisioning a separate Scoop bucket.

WinGet metadata inherited from the previous repository identity is retired.
PxGo does not claim an official WinGet package until a fork-owned package ID is
separately validated and published through the external WinGet repository.

## Publication policy

GitHub Releases target `khanhkit/pxgo`. Homebrew publication targets
`khanhkit/homebrew-tap` and remains guarded by the repository variable
`PXGO_HOMEBREW_TAP_ENABLED=true`; cross-repository writes use a deploy key scoped
only to the tap repository rather than an account-wide token.

Historical copyright and attribution remain unchanged.
