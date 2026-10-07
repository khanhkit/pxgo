# Configuration

pxgo configuration sources are applied in this order:

```text
defaults < pxgo.ini/px.ini < explicitly selected dotenv < PX_ environment < PXGO_ environment < command line
```

Environment variables use the `PXGO_` prefix. For example, `--proxy` maps to
`PXGO_PROXY`, and `--client-username` maps to `PXGO_CLIENT_USERNAME`. For drop-in
Px migration, the corresponding legacy `PX_*` name is accepted only when its
`PXGO_*` counterpart is unset.

Configuration is strict: malformed typed values, unsupported `PXGO_*` options,
unknown INI keys, unreadable explicit config files, and missing explicit local
PAC files fail startup instead of silently falling back to defaults. An
explicitly present empty environment value is still an override, so string
settings can intentionally clear lower-precedence values.

A `.env` file in the current working directory is **not** loaded implicitly.
Select one explicitly when needed:

```bash
PXGO_DOTENV=/path/to/pxgo.env pxgo
```

For installed/portable deployments, pxgo still checks for a `.env` next to
the executable as its compatibility fallback. Set `PXGO_DOTENV=` to disable
dotenv loading entirely.

When pxgo is consumed as a Go package, the returned `Config` records the
winning source for effective values. `cfg.SourceOf("port")`, for example,
returns values such as `default`, `ini:/path/pxgo.ini`, `env:PXGO_PORT`, or
`cli`.

## Config File Lookup

When `--config` is provided, pxgo reads that exact file.

Without `--config`, pxgo checks all native `pxgo.ini` locations first:

1. `./pxgo.ini`
2. the platform config directory
3. `pxgo.ini` next to the executable

If none exists, it checks legacy `px.ini` in those same three locations. On normal startup, PxGo materializes the selected legacy file as a new `pxgo.ini`, preserving `px.ini` untouched and adding PxGo-specific settings/guidance. If neither file exists, PxGo detects the operating-system proxy state and generates a documented canonical INI; fixed PAC takes precedence over a fixed manual HTTP proxy, while dynamic WPAD/AutoDetect and protocol-specific mappings remain OS-owned and dynamic.

Existing canonical INIs carry a `# pxgo-config-schema:` marker. When a newer binary introduces config fields, PxGo appends only the missing new settings/guidance, preserves explicit user values/comments, creates a timestamped `.bak.*` copy first, and writes atomically. `pxgo --apply-system-proxy` is the explicit escape hatch that imports current OS routing into an existing canonical INI; it changes routing fields only and also requires a successful backup before mutation.

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
| `pac_encoding` / `--pac-encoding` | `auto` | PAC source encoding: `auto`, `ascii`/`us-ascii`, `utf-8`/`utf8`, `latin1`/`latin-1`, `cp1252`/`windows-1252`, `cp1251`/`windows-1251`, `utf-16`, `utf-16le`, `utf-16be`, `utf-32`, `utf-32le`, or `utf-32be` |
| `port` / `--port` | `3128` | Local listen port |
| `listen` / `--listen` | `0.0.0.0` | Local listen address list; the default binds all interfaces, so restrict `allow` and/or enable client authentication on untrusted networks |
| `gateway` / `--gateway` | `0` | Bind all interfaces; requires restrictive `allow`, `hostonly`, or strong downstream auth |
| `hostonly` / `--hostonly` | `0` | Bind all interfaces but allow local host IPs |
| `allow` / `--allow` | `*.*.*.*` | Client allow list |
| `noproxy` / `--noproxy` | empty | Direct-connect bypass list |
| `dns` / `--dns` | empty (`system`) | Outbound DNS resolver. Repeated INI `dns` entries create ordered resolver rules; comma-separated endpoints inside one entry remain failover endpoints |
| `dns_only` / `--dns-only` | empty | Apply the immediately preceding `dns` rule only to matching domains |
| `dns_bypass` / `--dns-bypass` | empty | Resolve matching domains with the operating-system DNS instead of the immediately preceding `dns` rule |
| `useragent` / `--useragent` | empty | Override or set `User-Agent` |
| `username` / `--username` | empty | Explicit upstream auth username |
| `auth` / `--auth` | empty | Upstream auth selector; empty + reusable credentials uses `ANYSAFE`, while explicit `ANY` includes Basic fallback |
| `kerberos` / `--kerberos` | `0` | Linux/macOS Kerberos ccache + upstream HTTP SPNEGO authentication; requires `username`; Windows current-user SSPI is separate and does not use this flag |

### DNS resolver policy

Leaving `dns` empty, or setting it to `system`, preserves the operating-system resolver. Configured resolver endpoints use these forms:

- `1.1.1.1` or `1.1.1.1:5353` — UDP DNS;
- `udp://1.1.1.1:53` — UDP DNS with TCP retry when the response is truncated;
- `tcp://1.1.1.1:53` — DNS over TCP;
- `https://resolver.example/dns-query` — DNS-over-HTTPS using normal TLS certificate validation.

Multiple non-system endpoints inside one `dns` value may be comma-separated. They are attempted in configured order with bounded timeouts. Classic DNS endpoints require an IP literal so their own hostname cannot create an implicit bootstrap lookup. A DoH endpoint may use a hostname; only that endpoint bootstrap uses the OS resolver, and its HTTP transport bypasses proxy environment settings so it cannot recursively route through PxGo.

INI files may repeat `dns` to create ordered resolver rules. `dns_only` limits the immediately preceding rule to matching domains; if it does not match, evaluation continues to the next rule and ultimately the OS resolver if no rule matches. `dns_bypass` is terminal for matching domains and sends them directly to the OS resolver. `*.example.com` matches subdomains only; `example.com` matches both the apex and its subdomains. If both selectors are present on one rule, `dns_bypass` wins.

Remote HTTP(S) PAC source loading is a control-plane bootstrap and always uses the OS resolver, so an internal PAC URL remains reachable on split-DNS/VPN networks. PAC `dnsResolve()`, ordinary destinations, upstream-proxy hostnames, and noproxy address lookups use the ordered DNS policy above. A configured rule itself remains fail-closed; there is no implicit system-DNS fallback after that rule has been selected.

Examples:

```sh
pxgo --dns=udp://10.0.0.53:53
PXGO_DNS='https://dns.example/dns-query' PXGO_DNS_ONLY='example.com' pxgo
pxgo --dns='udp://10.0.0.53:53,https://dns.example/dns-query'
```

```ini
[proxy]
dns = https://1.1.1.1/dns-query
dns_only = *.google.com

dns = udp://9.9.9.9:53
dns_bypass = *.bosch.com,bosch.com
```

The example sends Google subdomains to the first DoH rule, all other non-Bosch names to the second resolver, and Bosch domains to system/VPN DNS.

`pxgo --doctor` reports the resolver mode, safe endpoint identity, any system-bootstrap requirement, source provenance, and a bounded failure class. It does not report DNS query payloads or credential-bearing endpoint URLs.

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

PAC source decoding defaults to `auto`, matching current upstream Px behavior.
For HTTP PAC sources, a valid `Content-Type` charset takes priority. Otherwise
`auto` recognizes UTF-8/UTF-16/UTF-32 BOMs, accepts valid UTF-8, then tries
Windows-1252 and Windows-1251 before the Latin-1 fallback. An explicit
`--pac-encoding` continues to override detection.

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
| `client_auth` / `--client-auth` | `NONE` | Client auth selector; enabled modes require username/password, and Basic-capable modes are loopback-only on plaintext listeners |
| `client_username` / `--client-username` | empty | Local client auth username |
| `client_nosspi` / `--client-nosspi` | `0` | Compatibility flag retained from Python Px |

## Settings Section

| Key / Flag | Default | Description |
| --- | --- | --- |
| `workers` / `--workers` | `1` | Connection-admission multiplier; `workers × threads` is the global accepted-connection cap |
| `threads` / `--threads` | `32` | Connection-admission multiplier; must be positive |
| `idle` / `--idle` | `30` | CONNECT tunnel and downstream HTTP keep-alive idle timeout in seconds |
| `socktimeout` / `--socktimeout` | `20.0` | Upstream socket plus downstream header/read timeout in seconds; downstream write window is bounded to 2× this value |
| `proxyreload` / `--proxyreload` | `60` | PAC/system proxy refresh interval |
| `foreground` / `--foreground` | `0` | Compatibility flag |
| `log` / `--log` | `0` | Debug log destination: `1`=script dir, `2`=cwd, `3`=unique file, `4`=stdout |
| `auto_update` / `--auto-update` | `install` | Long-running update policy: `off`, `notify`, or unattended `install` |
| `update_interval` / `--update-interval` | `24h` | Periodic check interval as a positive Go duration; initial check is jittered |
| `update_channel` / `--update-channel` | `stable` | Accepted release channel: `stable` or explicit `prerelease` |
| `install_provider` / `--install-provider` | `auto` | Installation owner: `auto`, `direct`, `winget`, `scoop`, or `homebrew` |

### Update lifecycle

`--check-update` is read-only and reports the current version, latest accepted version, resolved provider, channel, and whether an update is available. `--update` performs one update using the resolved installation owner.

Package-manager ownership is authoritative. Auto-detection recognizes official WinGet, Scoop, and Homebrew installation paths; a conflicting explicit provider is rejected instead of letting direct self-replacement mutate manager-owned bytes. Portable paths resolve to `direct` unless deployment configuration provides an explicit manager marker.

Direct updates use the pinned `khanhkit/pxgo` GitHub release identity. PxGo selects only the exact `pxgo_<os>_<arch>.zip|tar.gz` archive, requires the exact filename in `checksums.txt`, cross-checks GitHub's SHA-256 asset digest when present, bounds download/extraction sizes, validates redirect origins, and executes the staged binary with `--version` before activation. Published archives are never rebuilt or patched locally.

`auto_update=notify` checks without stopping the proxy worker. `auto_update=install` downloads and verifies first, then asks the existing Guardian lifecycle to stop the worker before apply; failures restart the known-good runtime. Update state is written to the platform config directory as `update-state.json` and is surfaced through local diagnostics without release URL/query payloads or credentials.

Stable releases are the default and automatic flows do not downgrade. Prereleases are considered only when `update_channel=prerelease` is explicitly configured.

## Passwords

Store credentials interactively (prompts with no echo, saves to OS keyring):

```bash
./pxgo --username='DOMAIN\user' --password
./pxgo --client-username=client --client-password
```

On Windows this uses Credential Manager; on macOS, Keychain; on Linux,
the Secret Service D-Bus interface (typically GNOME Keyring). Once stored,
pxgo loads the password automatically when the matching username is supplied.

On headless Linux, a working session D-Bus plus a Secret Service provider is
required. `gnome-keyring-daemon` 48+ also expects `~/.local/share/keyrings` to
be owned by the current user with mode `0700`; incorrect ownership or
permissions can surface as a dismissed prompt or unavailable keyring. Password
store operations surface the backend error with the explicit plaintext fallback
option. Optional startup lookup remains best-effort so an unavailable keyring
does not block flows that already have usable credentials (for example, an
existing Kerberos ticket cache). If no OS keyring is available, use the
explicit plaintext fallback below.

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
