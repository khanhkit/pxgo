# Usage

## Basic Proxy

Start pxgo:

```bash
./pxgo
```

Configure applications to use `127.0.0.1:3128` as their HTTP and HTTPS proxy.

## Upstream Proxy

```bash
./pxgo --proxy=proxy.company.com:8080
```

Multiple upstream proxies can be comma-separated:

```bash
./pxgo --proxy=proxy-a.company.com:8080,proxy-b.company.com:8080
```

pxgo tries the returned proxy list in order and falls back when a proxy fails.

## PAC File

```bash
./pxgo --pac=http://proxy.company.com/proxy.pac
./pxgo --pac=/path/to/proxy.pac
```

PAC source encoding is auto-detected by default. To force a specific encoding:

```bash
./pxgo --pac=/path/to/proxy.pac --pac-encoding=latin1
./pxgo --pac=/path/to/proxy.pac --pac-encoding=cp1252
./pxgo --pac=/path/to/proxy.pac --pac-encoding=cp1251
./pxgo --pac=/path/to/proxy.pac --pac-encoding=utf-16
```

`--pac-encoding=auto` honors an HTTP `Content-Type` charset, detects UTF BOMs,
accepts valid UTF-8, then tries Windows-1252/Windows-1251 and Latin-1. PAC result
lists are bounded to 32 candidates and
16 KiB and malformed directives fail explicitly.

## Bypass Rules

`--noproxy` skips the upstream proxy for matching destinations:

```bash
./pxgo --proxy=proxy.company.com:8080 --noproxy=localhost,example.com,10.0.*.*
```

Supported values include exact IPs, wildcard IPv4 globs, IPv4 ranges, CIDR
ranges, and host/domain suffixes.

## Upstream Authentication

Set `--auth` to select upstream proxy authentication. Store the password
interactively in the OS keyring first:

```bash
./pxgo --username='DOMAIN\user' --password
```

Then run the proxy:

```bash
./pxgo \
  --proxy=proxy.company.com:8080 \
  --auth=NTLM \
  --username='DOMAIN\user'
```

Or supply the password via environment variable for non-interactive runs:

```bash
PXGO_PASSWORD='secret' ./pxgo \
  --proxy=proxy.company.com:8080 \
  --auth=NTLM \
  --username='DOMAIN\user'
```

Supported auth selectors:

- `ANY`: try `NEGOTIATE`, `NTLM`, `DIGEST`, then `BASIC`
- `ANYSAFE`: try `NEGOTIATE`, `NTLM`, then `DIGEST`
- `NEGOTIATE`, `NTLM`, `DIGEST`, `BASIC`: force one mode
- `NONE`: pass proxy authentication through from the client
- `ONLYNTLM`, `NOBASIC`, `SAFENONTLM`: selector forms matching the Python Px convention

When reusable upstream username/password credentials are configured and `--auth` is omitted, pxgo uses the `ANYSAFE` challenge set so a Basic-only parent cannot silently downgrade those credentials. Use explicit `--auth=ANY`, `--auth=BASIC`, or `--auth=ONLYBASIC` only when Basic fallback is intentionally accepted. Credentials are emitted only after a matching upstream challenge.

Upstream Digest supports legacy MD5 without qop and MD5 with `qop=auth`. Unsupported qop values such as `auth-int` and unsupported algorithms such as `MD5-sess` or `SHA-256` are rejected rather than being signed with an incompatible MD5 formula.

## Kerberos

On Linux and macOS, `--kerberos` uses the existing ticket manager to acquire and
refresh a per-process `FILE:` credential cache with `kinit`/`klist`. When an
upstream proxy answers with `407 Proxy Authentication Required` and a `Negotiate`
challenge, pxgo consumes that ccache to obtain a service ticket for
`HTTP/<proxy-host>` and emits an RFC 4178 SPNEGO token. Both ordinary HTTP proxy
requests and CONNECT tunnels use the same Kerberos-only path; a rejected Kerberos
exchange is not silently downgraded to NTLM.

`--kerberos` requires `--username`. `KRB5_CONFIG` may be set explicitly; standard
Linux/macOS Kerberos config locations are used when available. The first proxy-auth
attempt waits only for an already-running initial ticket refresh, with a bounded
wait, rather than starting a second `kinit`.

On Windows, upstream `Negotiate`/`NTLM` is handled separately through current-user
SSPI. Omit `--kerberos` and explicit upstream username/password credentials to use
that path. `Negotiate` is reported as Kerberos only when mechanism evidence is
available; otherwise it remains `Negotiate` or `NTLM`.

The build-tagged MIT/Heimdal integration harness covers ticket lifecycle and can
also exercise real `HTTP/<proxy-host>` SPNEGO acquisition by setting
`PXGO_KERBEROS_PROXY_HOST`.

## Client Authentication

By default local clients can use pxgo without authenticating. Require client auth:

```bash
PXGO_CLIENT_PASSWORD='client-secret' ./pxgo \
  --client-auth=DIGEST \
  --client-username=client
```

Supported client auth modes are `NEGOTIATE`, `NTLM`, `DIGEST`, `BASIC`, `ANY`,
`ANYSAFE`, and `NONE`. For **downstream client authentication**, `NEGOTIATE` is
a compatibility mode for NTLMSSP carried directly under the Negotiate scheme or
wrapped in SPNEGO. It does **not** accept Kerberos/GSSAPI tokens. This downstream
compatibility mode is separate from Windows upstream SSPI and from Linux/macOS
**upstream** Kerberos/SPNEGO authentication.

## Remote Clients

Default mode listens only on `127.0.0.1`.

Allow remote clients:

```bash
./pxgo --gateway --allow=192.168.1.*
```

Gateway mode is fail-closed. It starts only when at least one remote-admission policy is explicit: a restrictive `--allow` list, `--hostonly`, or downstream authentication. On plaintext remote listeners, `BASIC`, `ANY`, and explicit auth lists containing `BASIC` are rejected; use `ANYSAFE`, `DIGEST`, `NTLM`, or `NEGOTIATE` with `--client-username` and a stored/configured client password. The `/PxgoQuit` control request is accepted only as an exact origin-form request from an allowed loopback client, so a proxied absolute URL ending in `/PxgoQuit` is ordinary origin traffic.

Allow only IP addresses assigned to local interfaces:

```bash
./pxgo --hostonly
```

## Logging

pxgo supports four log destinations controlled by `--log=N`, `PXGO_LOG=N`, or `settings:log=N` in the config file:

```bash
./pxgo --log=1        # log to script directory (alias: --debug)
./pxgo --log=2        # log to working directory
./pxgo --log=3        # log to working directory with unique filename (alias: --uniqlog)
./pxgo --log=4        # log to stdout, implies --foreground (alias: --verbose)
```

## Self-Test

```bash
./pxgo --test
./pxgo --test=https://example.com
./pxgo --test=all:https://httpbin.org
```

`all` mode checks several HTTP methods through the proxy. Self-test startup and shutdown are deadline-bounded; malformed target URLs and shutdown/start failures are returned as errors instead of being ignored or panicking.
