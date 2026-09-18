# Configuration

pxgo configuration sources are applied in this order:

```text
defaults < pxgo.ini < .env < environment < command line
```

Environment variables use the `PXGO_` prefix. For example, `--proxy` maps to
`PXGO_PROXY`, and `--client-username` maps to `PXGO_CLIENT_USERNAME`.

## Config File Lookup

When `--config` is provided, pxgo reads that exact file.

Without `--config`, pxgo checks:

1. `./pxgo.ini`
2. the platform config directory
3. `pxgo.ini` next to the executable

Platform config directories:

- Windows: `%APPDATA%\pxgo`
- macOS: `~/Library/Application Support/pxgo`
- Linux/Unix: `$XDG_CONFIG_HOME/pxgo` or `~/.config/pxgo`

## Starter Config

Generate a config:

```bash
./pxgo --save --config=./pxgo.ini --proxy=proxy.company.com:8080
```

Use the commented repository sample [../pxgo.ini](../pxgo.ini) when you want a
human-edited config with explanations.

## Proxy Section

| Key / Flag | Default | Description |
| --- | --- | --- |
| `server`, `proxy` / `--proxy` | empty | Upstream proxy server list |
| `pac` / `--pac` | empty | PAC URL or local file |
| `pac_encoding` / `--pac-encoding` | `utf-8` | PAC source encoding: `utf-8`/`utf8`, `latin1`/`latin-1`, `cp1252`/`windows-1252`, `cp1251`/`windows-1251`, `utf-16`, `utf-16le`, `utf-16be`, or `auto` |
| `port` / `--port` | `3128` | Local listen port |
| `listen` / `--listen` | `127.0.0.1` | Local listen address list |
| `gateway` / `--gateway` | `0` | Bind all interfaces |
| `hostonly` / `--hostonly` | `0` | Bind all interfaces but allow local host IPs |
| `allow` / `--allow` | `*.*.*.*` | Client allow list |
| `noproxy` / `--noproxy` | empty | Direct-connect bypass list |
| `useragent` / `--useragent` | empty | Override or set `User-Agent` |
| `username` / `--username` | empty | Upstream auth username or Kerberos principal |
| `auth` / `--auth` | empty | Upstream auth selector |
| `kerberos` / `--kerberos` | `0` | Enable Kerberos ticket management |

## Automatic Upstream Proxy Discovery

When neither `--proxy` nor `--pac` is configured, pxgo resolves one authoritative routing source in this order:

1. On Windows, Internet Options / WinHTTP system proxy state.
2. Environment proxy variables.
3. Direct connection when neither source exists.

The selected source is authoritative. If Windows system PAC/WPAD is configured but resolution fails, pxgo returns that error instead of silently falling through to environment variables or DIRECT.

Environment discovery is per target scheme:

- HTTP: `http_proxy` / `HTTP_PROXY`, then `all_proxy` / `ALL_PROXY`.
- HTTPS: `https_proxy` / `HTTPS_PROXY`, then `all_proxy` / `ALL_PROXY`.
- Other schemes: `all_proxy` / `ALL_PROXY`.
- `no_proxy` / `NO_PROXY` supplies bypass rules while environment routing is active.

On Windows, protocol-specific manual mappings such as `http=proxy-a:8080;https=proxy-b:8443` remain protocol-specific. AutoDetect and AutoConfigURL may coexist and are passed together to WinHTTP.

Native system-proxy discovery is currently implemented only on Windows. On macOS/Linux, automatic discovery therefore starts with the environment family above; pxgo does not pretend to consume desktop/system proxy settings it cannot actually read.

## PAC Semantics

PAC source decoding is explicit. The default remains `utf-8`; `latin1` is an
alias for ISO-8859-1, and Windows-1252/Windows-1251 plus UTF-16 variants are
supported when selected. `auto` recognizes UTF BOMs, otherwise accepts valid
UTF-8 and falls back to Windows-1252.

One loaded PAC generation owns one JavaScript global state. Calls are
serialized at that generation boundary, so unusual PAC files that intentionally
use mutable global counters/caches behave deterministically instead of getting
independent state from a VM pool. Reloading the PAC creates a new generation
and therefore a new global state.

`myIpAddress()` snapshots local interfaces once when the generation is loaded.
Loopback, link-local, multicast and unspecified IPv4 addresses are ignored;
private IPv4 addresses are preferred, with deterministic address ordering.
Reload the PAC generation when network-interface selection must be refreshed.

PAC routing results are tokenized on semicolons. Supported directives are
`PROXY`/`HTTP`, `HTTPS`, `SOCKS`, `SOCKS4`, `SOCKS4A`, `SOCKS5`,
and `DIRECT`. Malformed/unknown directives fail explicitly in production PAC
routing. Results are limited to 16 KiB and 32 candidates so a pathological PAC
cannot multiply dial/auth fallback work without bound.

## Client Section

| Key / Flag | Default | Description |
| --- | --- | --- |
| `client_auth` / `--client-auth` | `NONE` | Local client auth selector |
| `client_username` / `--client-username` | empty | Local client auth username |
| `client_nosspi` / `--client-nosspi` | `0` | Compatibility flag retained from Python Px |

## Settings Section

| Key / Flag | Default | Description |
| --- | --- | --- |
| `workers` / `--workers` | `1` | Compatibility setting retained for config parity |
| `threads` / `--threads` | `32` | Compatibility setting retained for config parity |
| `idle` / `--idle` | `30` | CONNECT tunnel idle timeout in seconds |
| `socktimeout` / `--socktimeout` | `20.0` | Upstream socket timeout in seconds |
| `proxyreload` / `--proxyreload` | `60` | PAC/system proxy refresh interval |
| `foreground` / `--foreground` | `0` | Compatibility flag |
| `log` / `--log` | `0` | Debug log destination: `1`=script dir, `2`=cwd, `3`=unique file, `4`=stdout |

## Passwords

Store credentials interactively (prompts with no echo, saves to OS keyring):

```bash
./pxgo --username='DOMAIN\user' --password
./pxgo --client-username=client --client-password
```

On Windows this uses Credential Manager; on macOS, Keychain; on Linux,
libsecret. Once stored, pxgo loads the password automatically when the
matching username is supplied.

For non-interactive runs (Docker, CI), use environment variables instead:

```bash
PXGO_PASSWORD='upstream-secret' ./pxgo --username='DOMAIN\user'
PXGO_CLIENT_PASSWORD='client-secret' ./pxgo --client-username=client
```

For environments without an OS keyring, opt in to plaintext storage:

```bash
PXGO_KEYRING_PLAINTEXT=1 ./pxgo --username='DOMAIN\user' --password=secret
PXGO_KEYRING_PLAINTEXT=1 ./pxgo --client-username=client --client-password=secret
```

Use `PXGO_KEYRING_FILE=/path/to/keyring.json` to choose the plaintext keyring
file.
