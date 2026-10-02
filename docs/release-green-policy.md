# Green release policy

A stable PxGo release is **green** only when all release and distribution invariants below hold for the same version.

1. The PxGo remote branch inventory is exactly `main` at the release boundary.
2. The stable GitHub Release tag resolves to the exact protected `main` SHA that passed required CI and artifact verification.
3. Every live official distribution channel advertises the exact release version. Today the required channels are:
   - GitHub Releases
   - Homebrew (`khanhkit/homebrew-tap`)
   - Scoop (`khanhkit/scoop-bucket`)
   - WinGet (`KhanhKit.PxGo` in `microsoft/winget-pkgs` and the public WinGet source)
4. A submitted-but-unmerged WinGet manifest does not count as synchronized. Moderator/automation approval and public-source propagation are part of convergence.
5. A channel may not be silently skipped because credentials, publisher automation, or external approval are missing. The release remains non-green until that channel converges.
6. Publication must be monotonic: stale jobs must never downgrade a channel.

A GitHub Release may exist while external channels are converging, but it must not be reported as a green release until all live channels satisfy the exact-version check.
