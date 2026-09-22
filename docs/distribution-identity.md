# Distribution identity

PxGo is published and maintained from `khanhkit/pxgo`.

## Authoritative identities

- GitHub repository: `https://github.com/khanhkit/pxgo`
- Go module path: `github.com/khanhkit/pxgo`
- Homebrew tap: `khanhkit/homebrew-tap`

The Go module/import path was migrated with the v0.5.0 ownership cleanup so source,
build metadata, documentation, and release contracts use the same fork identity.

## Windows distribution

WinGet metadata inherited from the previous repository identity is retired.
PxGo does not advertise or generate a WinGet package until a fork-owned package ID
is separately validated and published. Windows users install release archives from
the GitHub Releases page.

## Publication policy

GitHub Releases target `khanhkit/pxgo`. Homebrew publication targets
`khanhkit/homebrew-tap` and remains guarded by the repository variable
`PXGO_HOMEBREW_TAP_ENABLED=true` plus its configured token.

Historical copyright and attribution remain unchanged.
