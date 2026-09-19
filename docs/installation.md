# Installation

## Windows with WinGet

After the WinGet manifest for a release passes Microsoft validation, install
pxgo with:

```powershell
winget install pavelsimo.pxgo
```

## macOS and Linux with Homebrew

Install pxgo from the Homebrew tap:

```bash
brew install pavelsimo/tap/pxgo
```

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

The runtime image retains Kerberos command-line tools for ticket-lifecycle integration tests and future GSSAPI work. The current user-facing `--kerberos` mode is fail-closed because those tickets are not yet consumed for upstream HTTP proxy authentication.

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
