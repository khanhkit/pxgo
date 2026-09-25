# 🔀 pxgo

[![CI](https://github.com/khanhkit/pxgo/actions/workflows/ci.yml/badge.svg)](https://github.com/khanhkit/pxgo/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/khanhkit/pxgo)](https://github.com/khanhkit/pxgo/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/khanhkit/pxgo)](go.mod)
[![License](https://img.shields.io/github/license/khanhkit/pxgo)](LICENSE)

**pxgo** is a Go rewrite of [Px](https://github.com/genotrance/px) — a single binary that runs a local HTTP/HTTPS
proxy so applications can authenticate through corporate upstream proxies. On Windows, current-user SSPI supports NTLM and Negotiate. On Linux and macOS, `--kerberos` acquires/refreshes a Kerberos credential cache and uses it for upstream HTTP `Negotiate`/SPNEGO authentication.

By default pxgo listens on `127.0.0.1:3128`.

## Quick Start

Install a prebuilt binary by downloading the archive matching your platform
from the [GitHub Releases](https://github.com/khanhkit/pxgo/releases) page.

Homebrew users can install from the official PxGo tap:

```bash
brew tap khanhkit/tap
brew install pxgo
```

On Windows, install from the official WinGet catalog:

```powershell
winget install --id KhanhKit.PxGo --exact
```

Or install from the official PxGo Scoop bucket:

```powershell
scoop bucket add khanhkit https://github.com/khanhkit/scoop-bucket
scoop install khanhkit/pxgo
```

Each release also publishes a checksum-pinned direct-install manifest as a
bucket-independent fallback:

```powershell
scoop install https://github.com/khanhkit/pxgo/releases/latest/download/pxgo-scoop.json
```

> ☕ **Using the release binary?** If pxgo saves you time, you can support ongoing
> development via [GitHub Sponsors](https://github.com/sponsors/khanhkit).
> The full support options are also included in this README inside every release archive.

Build and run from this repository:

```bash
make build
./bin/pxgo
```

Configure your browser, package manager, or CLI tool to use:

```text
HTTP proxy:  127.0.0.1:3128
HTTPS proxy: 127.0.0.1:3128
```

Run with an explicit upstream proxy:

```bash
pxgo --proxy=proxy.company.com:8080
```

On domain-joined Windows machines pxgo authenticates to NTLM/Negotiate upstream proxies
with the logged-in user's credentials via SSPI — no `--username` or stored
password needed. A Negotiate challenge may resolve to Kerberos or NTLM according
to the Windows domain/SPN environment; pxgo does not label generic Negotiate as Kerberos without token evidence.

Run with a PAC file:

```bash
pxgo --pac=http://proxy.company.com/proxy.pac
pxgo --pac=/path/to/proxy.pac
```

Run a self-test through pxgo:

```bash
pxgo --test
pxgo --test=all:https://httpbin.org
```

Stop a running instance (sends `GET /PxgoQuit` to the listen address):

```bash
pxgo --quit
```

## Configuration

pxgo accepts command-line flags, `PXGO_*` environment variables, legacy `PX_*`
variables for Px migration, explicitly selected dotenv files, and `pxgo.ini`.
`PXGO_*` wins over the corresponding `PX_*` fallback. Precedence is:

```text
command line > PXGO_ environment > PX_ environment > explicit dotenv > pxgo.ini/px.ini > defaults
```

Current-working-directory `.env` files are not trusted implicitly. Use
`PXGO_DOTENV=/path/to/file` when dotenv loading is desired. Invalid known
values and unknown configuration keys fail startup rather than silently
falling back.

Create a starter config:

```bash
pxgo --save --config=./pxgo.ini --proxy=proxy.company.com:8080 --port=3128
```

Then run it with:

```bash
pxgo --config=./pxgo.ini
```

### Write `pxgo.ini` by hand

`--save` is optional. If you prefer to maintain the INI yourself, create a
plain-text `pxgo.ini` such as:

```ini
[proxy]
server = proxy.company.com:8080
listen = 127.0.0.1
port = 3128
auth = ANYSAFE
noproxy = localhost,127.0.0.1

[settings]
workers = 1
threads = 32
idle = 30
socktimeout = 20.0
proxyreload = 60
log = 0
```

On domain-joined Windows, omit `username` when you want pxgo to use the logged-in
user's SSPI credentials. For explicit credentials, add `username = DOMAIN\\user`
and store the password with the OS keyring rather than writing it into the INI.

You can place `pxgo.ini` next to the binary, in the platform config directory,
or anywhere you prefer when you pass its path explicitly. For migration from
Python Px, if no `pxgo.ini` exists in any normal search location, pxgo also
reads legacy `px.ini` from the same locations; `--save` still writes `pxgo.ini`:

```bash
pxgo --config=/path/to/pxgo.ini
```

The repository includes a fully commented sample config at [pxgo.ini](pxgo.ini).

Passwords stored with `--password`/`--client-password` go to the OS keyring
(Credential Manager, Keychain, or the Linux Secret Service D-Bus interface).
Headless Linux requires a working Secret Service session; backend failures are
reported instead of being mistaken for a missing password. Set
`PXGO_KEYRING_PLAINTEXT=1` to use a plaintext file instead (for Docker/CI), and
`PXGO_KEYRING_FILE=PATH` to choose where it lives — see
[docs/configuration.md](docs/configuration.md).

## Common Flags

| Flag | Purpose |
| --- | --- |
| `--proxy=HOST:PORT` | Upstream proxy server, or comma-separated servers |
| `--pac=URL_OR_PATH` | PAC file URL or local file |
| `--port=NUM` | Local listen port, default `3128` |
| `--listen=IP[,IP]` | Local listen address list, default `127.0.0.1` |
| `--gateway` | Bind all interfaces; requires a restrictive `--allow` policy or non-Basic downstream auth |
| `--hostonly` | Bind all interfaces but allow only local host interface IPs |
| `--allow=LIST` | Client allow list for `--gateway` mode |
| `--noproxy=LIST` | Hosts or IP ranges that bypass the upstream proxy |
| `--auth=TYPE` | Upstream auth mode: `ANY`, `ANYSAFE`, `NEGOTIATE`, `NTLM`, `DIGEST`, `BASIC`, `NONE`; omitted auth with reusable credentials behaves as `ANYSAFE`, while explicit `ANY` opts into Basic fallback |
| `--username=USER` | Explicit upstream proxy username |
| `--kerberos` | Linux/macOS: acquire/refresh a Kerberos ccache and use it for upstream HTTP `Negotiate`/SPNEGO (`HTTP/<proxy-host>`); Windows SSPI works without this flag |
| `--client-auth=TYPE` | Require client auth: `NONE`, `ANY`, `ANYSAFE`, `NEGOTIATE`, `NTLM`, `DIGEST`, `BASIC`; `BASIC`/`ANY` are loopback-only on plaintext listeners; downstream `NEGOTIATE` means NTLMSSP/NTLM-over-SPNEGO, not Kerberos/GSSAPI |
| `--log=N` | Debug log destination: `1`=script dir (`--debug`), `2`=cwd, `3`=unique file (`--uniqlog`), `4`=stdout (`--verbose`) |

Use `pxgo --help` for the current CLI help.

## Documentation

- [Installation](docs/installation.md)
- [Usage](docs/usage.md)
- [Configuration](docs/configuration.md)
- [Architecture](docs/architecture.md)
- [Build](docs/build.md)
- [Testing](docs/testing.md)
- [Benchmarking](docs/benchmarking.md)

## Docker

Build the runtime image:

```bash
docker build -t pxgo .
```

Run pxgo in Docker with a read-only root filesystem and no Linux capabilities:

```bash
docker run --rm -p 3128:3128 \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
  --cap-drop=ALL \
  --security-opt=no-new-privileges:true \
  pxgo --gateway --allow=192.168.1.0/24 --proxy=proxy.company.com:8080
```

See [docs/installation.md](docs/installation.md) and [docker/](docker/) for
more Docker details.

## Development

```bash
make tools       # install dev tools and Git hooks
make hooks       # install Git hooks only
make build       # build binary to bin/pxgo
make test        # run tests with race detector and coverage
make lint        # run golangci-lint
make fmt         # format code
make ci          # full CI gate: fmt-check + lint + test + build
make docs        # build docs site to dist/docs-site/
```

## ❤️ Support
One developer + too many over-nights + coffee = still shipping. If this repo or content has saved you some time or headaches, feel free to fuel me with a coffee. Appreciate it! ☕

<div align="center">

  <!-- GitHub Sponsors Card -->
  <div style="display: inline-block; vertical-align: top; margin: 10px; padding: 24px; border: 1px solid #d0d7de; border-radius: 12px; background: #f6f8fa; max-width: 320px; text-align: center; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #24292f;">🌟 GitHub Sponsors</h3>
    <a href="https://github.com/sponsors/khanhkit">
      <img src="https://img.shields.io/github/sponsors/khanhkit?label=Sponsor%20me%20a%20cup%20of%20coffee&logo=GitHub&style=for-the-badge&color=pink" alt="Sponsor me a cup of coffee" />
    </a>
  </div>

  <!-- TPBank Card -->
  <div style="display: inline-block; vertical-align: top; margin: 10px; padding: 24px; border: 1px solid #d0d7de; border-radius: 12px; background: #f6f8fa; max-width: 320px; text-align: center; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #24292f;">🏦 TPBank (Việt Nam)</h3>
    <p style="color: #57606a; margin: 12px 0;">STK: <strong>KHANHKIT</strong><br><span style="font-size: 1.1em; color: #0969da;"></span></p>
  </div>

  <!-- USDT Card -->
  <!-- USDT BEP20 Card -->
  <div style="display: inline-block; vertical-align: top; margin: 10px; padding: 24px; border: 1px solid #d0d7de; border-radius: 12px; background: #f6f8fa; max-width: 320px; text-align: center; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #24292f;">💰 USDT (BNB Smart Chain)</h3>
    <p style="color: #57606a; margin: 8px 0;"><strong>Network:</strong> BNB Smart Chain (BEP20)</p>
    <p style="margin: 12px 0;">
      <strong>Wallet Address:</strong><br>
      <code style="background: #fff; padding: 6px 10px; border-radius: 6px; font-size: 0.9em; word-break: break-all;">0x56e49EF13229E35b5ebea96cB85934d58459dE18</code>
    </p>
    <p style="color: #cf222e; font-size: 0.85em; margin-top: 8px;"><strong>⚠️ Only send on BEP20 network</strong></p>
  </div>

  <!-- USDT TRC20 Card -->
  <div style="display: inline-block; vertical-align: top; margin: 10px; padding: 24px; border: 1px solid #d0d7de; border-radius: 12px; background: #f6f8fa; max-width: 320px; text-align: center; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #24292f;">⚡ USDT (TRON)</h3>
    <p style="color: #57606a; margin: 8px 0;"><strong>Network:</strong> TRON (TRC20)</p>
    <p style="margin: 12px 0;">
      <strong>Wallet Address:</strong><br>
      <code style="background: #fff; padding: 6px 10px; border-radius: 6px; font-size: 0.9em; word-break: break-all;">THtezQttphE8ramQYeYemYfAWDVSKsZvbF</code>
    </p>
    <p style="color: #cf222e; font-size: 0.85em; margin-top: 8px;"><strong>⚠️ Only send on TRC20 network</strong></p>
  </div>

</div>
