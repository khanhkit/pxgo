# Changelog

All notable changes to pxgo will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0] - 2026-09-24

### Added
- Add end-to-end Linux/macOS Kerberos/SPNEGO upstream proxy authentication backed by the managed FILE ccache for HTTP and CONNECT flows
- Add live MIT KDC coverage that provisions `HTTP/proxy.pxgo.test` and requires real SPNEGO service-ticket acquisition
- Publish and automate the official Homebrew tap at `khanhkit/homebrew-tap`
- Publish a checksum-pinned direct-install Scoop manifest with release assets
- Add legacy Python px migration fallbacks for `PX_*` environment variables and `px.ini`

### Changed
- Prefer `PXGO_*` over legacy `PX_*`, and native `pxgo.ini` over fallback `px.ini`
- Raise the Go 1.25 patch floor and update security-sensitive dependencies and pinned GitHub Actions
- Expand reliability, CLI doctor/quit, HTTPS-parent proxy, and authentication regression coverage

### Fixed
- Make replay-body remove-failure retry tests deterministic across root, container, Linux, and Windows environments
- Keep Unix Kerberos authentication from silently falling back after a rejected SPNEGO exchange
- Restore the non-skipping Kerberos CI contract so SPNEGO integration cannot green by omitting its service-principal fixture
- Resolve the code-scanning findings present before the parity remediation

## [0.5.1] - 2026-09-22

### Added
- Add curated release notes and fork-owned distribution metadata

### Changed
- Build release candidates once, verify exact candidate archives on native runtimes, and promote unchanged verified bytes
- Align release, Homebrew, documentation, and module identity with `khanhkit/pxgo`
- Fix tagged-commit dry-run verification to use the existing tag version

## [0.5.0] - 2026-09-22

### Added
- Handle SIGINT/SIGTERM with a graceful, bounded shutdown that drains in-flight requests
- Add a benchmark harness: `make bench`, `scripts/bench-e2e.sh` (vs Python px), and `docs/benchmarking.md`

### Changed
- Migrate the authoritative repository and Go module identity to `github.com/khanhkit/pxgo`
- Use fork-owned release URLs throughout the docs and generated site; package-manager install commands are hidden until their fork-owned repositories are provisioned
- Reuse upstream connections via cached keep-alive transports keyed by proxy candidate
- Compile PAC scripts once and evaluate them on a pooled set of JavaScript VMs, removing the global PAC lock
- Cache DNS lookups used by `--noproxy` matching and PAC `dnsResolve()` (new `internal/dnscache`)
- Stream request bodies straight through unless an upstream auth retry could need a replay
- Rewrite the CONNECT relay to preserve the kernel `splice(2)` fast path and half-close each direction independently
- Run proxy reload and Kerberos ticket checks on a background ticker instead of per request; a failed reload now keeps the previous proxy config and logs the error instead of returning 502
- Reload the proxy configuration outside the routing lock and keep warm connections unless the routing actually changed

### Removed
- Retire the inherited WinGet package metadata and installation command until a fork-owned package identity is validated and published

### Fixed
- Forward client bytes pipelined behind a CONNECT request (fixes stalled TLS handshakes)
- Deliver upstream bytes that arrive together with the CONNECT response (fixes server-speaks-first protocols such as SMTP)
- Pin NTLM/Negotiate upstream authentication to a single connection
- Match IPv6 addresses and CIDRs in `--noproxy` and `--allow`
- Default CONNECT requests to bracketed IPv6 literals without a port to port 443
- Bypass the whole `127.0.0.0/8` loopback block for `<local>`
- Send an incrementing nonce count and random cnonce in upstream Digest authentication
- Reject replayed client Digest nonce/nc pairs and add PAC fetch timeouts with retry backoff

## [0.4.0] - 2026-05-23

### Added
- Add the initial WinGet manifest generation for Windows distribution

## [0.3.0] - 2026-05-23

### Fixed
- Keep active CONNECT downloads alive while the upload side is idle

## [0.2.0] - 2026-05-21

### Added
- Store and retrieve proxy credentials in the OS keyring (Windows Credential Manager, macOS Keychain, libsecret on Linux)
- Prompt for passwords interactively with no echo when `--password` or `--client-password` is used without a value
- Support Windows SSPI for transparent single-sign-on on domain-joined machines without explicit credentials
- Add `PXGO_KEYRING_PLAINTEXT=1` environment variable to use a plaintext credential file instead of the OS keyring (for Docker and CI)
- Buffer large request bodies without loading them fully into memory; uploads over 1 MiB spill to a temporary file
- Reload Windows system proxy settings on a configurable interval so VPN connect/disconnect changes take effect without restart

### Fixed
- Stop retrying upstream proxy candidates after the client has already disconnected
- Fix `--verbose` and `--log` flags not producing any proxy debug output

## [0.1.0] - 2026-05-16

### Added

- Initial Go rewrite of the Python Px proxy
- HTTP/HTTPS proxy with CONNECT tunnel support
- NTLM authentication via `go-ntlmssp`
- Kerberos authentication support (Linux/macOS)
- PAC file evaluation via `goja` JavaScript engine
- INI, environment variable, and dotenv configuration
- `--proxy`, `--pac`, `--port`, `--listen`, `--gateway`, `--hostonly` flags
- `--auth`, `--username`, `--client-auth`, `--client-username` auth flags
- `--noproxy` bypass list with IP ranges and CIDR support
- `--test` self-test mode with httpbin.org
- `--save` to persist config to pxgo.ini
- `--install`/`--uninstall` Windows registry startup integration
- `--quit` and `--restart` for running instances
- `--password`/`--client-password` keyring credential storage
- Docker support
- Multi-platform builds: Linux, macOS, Windows (amd64, arm64)

[Unreleased]: https://github.com/khanhkit/pxgo/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/khanhkit/pxgo/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/khanhkit/pxgo/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/khanhkit/pxgo/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/khanhkit/pxgo/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/khanhkit/pxgo/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/khanhkit/pxgo/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/khanhkit/pxgo/releases/tag/v0.1.0
