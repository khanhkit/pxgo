# Installation

## Windows

Download the archive matching your machine from
[GitHub Releases](https://github.com/khanhkit/pxgo/releases):

- `pxgo_windows_amd64.zip` for x64 Windows
- `pxgo_windows_arm64.zip` for ARM64 Windows

The official WinGet package is `KhanhKit.PxGo`:

```powershell
winget install --id KhanhKit.PxGo --exact
```

The official PxGo Scoop bucket remains available as an alternative:

```powershell
scoop bucket add khanhkit https://github.com/khanhkit/scoop-bucket
scoop install khanhkit/pxgo
```

You can also extract `pxgo.exe` manually and place it on `PATH` if desired.

## macOS and Linux

Download the matching `pxgo_darwin_*.tar.gz` or `pxgo_linux_*.tar.gz`
archive from [GitHub Releases](https://github.com/khanhkit/pxgo/releases),
extract `pxgo`, and place it on `PATH` if desired.

## From Source

Requirements:

- Go 1.24 or newer
- Network access for first-time module download

Build:

```bash
go build -o pxgo .
```

Run:

```bash
./pxgo
```

Install somewhere on `PATH` if desired:

```bash
install -m 0755 pxgo ~/.local/bin/pxgo
```

## Updates

A versioned PxGo build can inspect the accepted release channel without changing the installation:

```bash
pxgo --check-update
```

Use `pxgo --update` for an explicit update. PxGo preserves installation ownership:

- WinGet installs delegate to `winget upgrade --id KhanhKit.PxGo --exact`;
- Scoop installs delegate to `scoop update pxgo`;
- Homebrew installs delegate to `brew upgrade pxgo`;
- direct/portable installs download the exact matching GitHub release archive, verify the release-asset SHA-256 when GitHub provides it, independently verify the exact filename in `checksums.txt`, verify the staged binary version, then activate it without rebuilding or patching the published bytes.

`install_provider=auto` is the default. Known package-manager paths are authoritative: configuring a conflicting provider such as `direct` for a detected Scoop/WinGet/Homebrew executable fails instead of overwriting manager-owned bytes. Use an explicit provider only when deployment tooling has a reliable installation-owner marker that path detection cannot express.

Automatic updates default to `auto_update=install`. `auto_update=notify` performs bounded periodic checks while leaving the worker running; `auto_update=install` stages the candidate first, then stops the Guardian-owned worker before apply. Set `auto_update=off` to opt out. Failed discovery/download/apply leaves the current runnable version in place or restarts it; direct replacement keeps a rollback copy until the replacement passes its version check. Periodic checks default to `24h` with startup jitter so a fleet does not synchronize every request against GitHub.

Stable releases are the default. `update_channel=prerelease` must be selected explicitly. Automatic flows never downgrade to an older semantic version.

## Docker

Build the default runtime image:

```bash
docker build -t pxgo .
```

Run with an upstream proxy using the least-privilege runtime contract:

```bash
docker run --rm -p 3128:3128 \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
  --cap-drop=ALL \
  --security-opt=no-new-privileges:true \
  pxgo --gateway --allow=192.168.1.0/24 --proxy=proxy.company.com:8080
```

Mount a config file:

```bash
docker run --rm -p 3128:3128 \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
  --cap-drop=ALL \
  --security-opt=no-new-privileges:true \
  -v "$PWD/pxgo.ini:/pxgo/pxgo.ini:ro" \
  pxgo --config=/pxgo/pxgo.ini --gateway --allow=192.168.1.0/24
```

The published image defaults to UID/GID `65532:65532`. Its root filesystem does not need to be writable for normal proxy operation; `/tmp` is the explicit writable scratch path for replay/Kerberos temporary state. If you intentionally persist user configuration, mount a writable directory at `/home/pxgo/.config`.

`--gateway` is fail-closed: choose a restrictive client `--allow` range or configure downstream authentication. Replace the example subnet with the client network visible to the container. Plaintext remote listeners do not permit `BASIC` or `ANY` because those modes advertise Basic credentials.

The runtime image retains Kerberos command-line tools because Linux/macOS `--kerberos` uses `kinit`/`klist` for the managed ccache and consumes that cache for upstream HTTP Negotiate/SPNEGO authentication.

## Windows Startup

The Go port can install the released `pxgo.exe` directly into the current
user's Windows Run registry key:

```powershell
pxgo.exe --install --config C:\path\to\pxgo.ini
pxgo.exe --uninstall
```

`--install` first persists the effective configuration atomically to the
selected config path, verifies both the running executable and saved config
exist, then writes a `PxGo` Run value. It does not depend on a separate
`pxgow.exe` artifact. Executable and `--config` arguments are encoded as
Windows command-line arguments, and the registry value is stored as a
non-expanding string so percent signs in paths remain literal.

A non-forced install preserves an existing `PxGo` entry; add `--force` to
replace it. `--uninstall` removes only the `PxGo` value and leaves legacy
`Px` entries untouched.

Startup registry operations are Windows-only. On non-Windows platforms the
commands return an unsupported-platform error.
