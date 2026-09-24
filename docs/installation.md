# Installation

## Windows

Download the archive matching your machine from
[GitHub Releases](https://github.com/khanhkit/pxgo/releases):

- `pxgo_windows_amd64.zip` for x64 Windows
- `pxgo_windows_arm64.zip` for ARM64 Windows

Extract `pxgo.exe` and place it on `PATH` if desired.

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
