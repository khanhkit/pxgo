# Distribution identity

PxGo releases from this fork are owned by `khanhkit/pxgo`.

Two inherited identifiers are intentionally retained for compatibility:

- Go module path: `github.com/pavelsimo/pxgo`. Changing it would alter every import path and downstream module identity, so it is not migrated without a separately validated compatibility plan.
- WinGet package identifier: `pavelsimo.pxgo`. Existing installs and upgrade continuity depend on the stable package ID; fork ownership is expressed by current publisher/support/release URLs instead.

Package-manager publishing is fail-closed until fork-owned staging repositories exist. WinGet manifests are generated but not uploaded (`skip_upload: true`) until `khanhkit/winget-pkgs` is provisioned. Homebrew targets `khanhkit/homebrew-tap` but its release job runs only when repository variable `PXGO_HOMEBREW_TAP_ENABLED` is explicitly set to `true` after that tap is provisioned and its token configured.

Historical copyright and attribution remain unchanged.
