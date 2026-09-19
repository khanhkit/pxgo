# Architecture

The Go port is organized around one main binary and small internal packages.

## Runtime Flow

1. `main.go` parses configuration with `internal/config`.
2. One-shot CLI actions execute directly and exit without creating a Guardian worker.
3. Normal long-running mode starts an `internal/guardian` parent process, which
   launches exactly one internal worker using a private loopback control channel.
4. The worker creates the proxy server with `internal/proxy.New`; it reports READY
   only after listener readiness and reports process progress from Runtime Supervisor.
5. Upstream proxy selection is delegated to `internal/wproxy`.
6. PAC files are loaded and evaluated by `internal/pac`.
7. HTTP requests are forwarded directly or through an upstream proxy.
8. HTTPS `CONNECT` requests create a tunnel between client and target or client
   and upstream proxy.
9. Optional upstream and client authentication is handled in `internal/proxy`.
10. Optional Kerberos ticket management is handled by `internal/kerberos`.

## Packages

| Path | Responsibility |
| --- | --- |
| `main.go` | CLI actions, Guardian parent/worker dispatch, self-test, startup install/uninstall dispatch |
| `internal/guardian` | Independent parent/worker process lifecycle, authenticated READY/BEAT/STOP control, watchdog and restart backoff |
| `internal/config` | Defaults, CLI/env/INI parsing, config save, password storage |
| `internal/proxy` | HTTP proxy, CONNECT tunnels, auth, allow rules, reload behavior |
| `internal/wproxy` | Proxy discovery model, manual proxy parsing, bypass rules |
| `internal/pac` | PAC loading, JavaScript execution, Mozilla PAC helper functions |
| `internal/dnscache` | TTL cache in front of `net.LookupIP` (60 s hits, 5 s misses, 4096-entry cap), shared by noproxy matching and PAC `dnsResolve()` |
| `internal/kerberos` | `kinit`/`klist` orchestration and ticket refresh state |
| `internal/debug` | Debug logging |
| `internal/diagnostic` | Bounded/redacted operational snapshots and doctor state |
| `internal/supervisor` | In-worker runtime progress/outcome classification and owner-scoped recovery coordination |
| `internal/systemproxy` | Platform system proxy discovery |
| `internal/winstartup` | Windows startup command generation |

## Process Failure Boundary

Long-running PxGo uses two processes under normal operation:

```text
pxgo Guardian parent
  └── pxgo internal worker
       └── proxy.Server + Runtime Supervisor
```

The Guardian owns process liveness only. Each worker generation receives a fresh
256-bit token and a `127.0.0.1:0` control address through private internal
environment variables; the secret is never placed in argv or user configuration.
The authenticated control protocol is intentionally small: worker `READY` and
`BEAT`, plus bidirectional `STOP` lifecycle intent. The Guardian does not import
PAC, proxy routing, DNS, auth or Internet-health policy.

Pre-READY failure is terminal and is not restart-looped. Unexpected post-READY
crash, control loss or heartbeat timeout is restartable through capped backoff.
A clean worker-requested stop is explicit and terminates the parent without
respawn. Parent shutdown sends STOP, waits boundedly, force-terminates if needed
and reaps the child. Worker control loss triggers bounded worker shutdown so a
dead parent does not leave an unmanaged proxy process. The watchdog treats a
large parent scheduling gap as suspend/scheduler delay and grants a fresh grace
window before declaring a hang.

One-shot actions such as help/version/save/install/uninstall/password/test/doctor
and quit never recursively spawn a worker. `--restart` first terminates the old
process tree and then enters the normal single-Guardian launch path.

## State And Concurrency

The proxy server runs one `http.Server` over one or more listeners.

- listener/server/port state is guarded by `stateMu`
- the active wproxy is guarded by `wmu`; reloads rebuild outside the lock and
  swap under it, so requests never wait on a PAC download
- per-connection client auth state lives in a `sync.Map` of `clientState`
  entries keyed by remote address, dropped when the connection closes
- Kerberos check and renewal state is guarded by the Kerberos manager mutex
- hijacked CONNECT tunnels and HTTP Upgrade streams use one server-owned lifecycle
  registry. A reservation is taken before `Hijack`, converted to an active managed
  stream immediately after ownership transfer, and released only when both relay
  directions terminate. `Shutdown` prevents new reservations, closes registered
  endpoints, and waits for both pending handoffs and active streams to reach zero
  within its context.

## HTTP Intermediary Boundary

Plain HTTP forwarding is an explicit intermediary boundary rather than a request
clone pass-through:

- the absolute request-target authority is canonical for outbound `Host`;
- `Connection`-nominated fields and standard hop-by-hop fields are consumed on
  requests and responses;
- downstream `Proxy-*` credentials/metadata terminate locally. The explicit
  `Auth=NONE` parent-proxy compatibility path may forward client
  `Proxy-Authorization` and relay the parent's 407 `Proxy-Authenticate`;
- PxGo appends `Via: 1.1 pxgo` to forwarded requests, informational responses,
  and final responses;
- forward transports disable automatic compression so representation bytes and
  `Content-Encoding` are not silently transformed;
- response trailers are declared before the final status and populated after body
  EOF; informational 1xx responses such as 103 Early Hints are forwarded
  deliberately;
- HTTP `101 Switching Protocols` is a dedicated path: only Upgrade semantics are
  restored after generic hop-header stripping, downstream is hijacked, buffered
  client bytes are preserved, and the bidirectional stream joins the same managed
  lifecycle registry used by CONNECT.

Time-based housekeeping (proxy reload, Kerberos ticket refresh) runs on a
background one-second ticker owned by `Start`/`Shutdown`, not on the request
path. A failed reload is logged and the previous proxy config stays active.

## Performance Notes

- `http.Transport`s are cached per proxy candidate (`DIRECT` or
  `scheme://host:port`, capped at 64) so upstream connections are reused via
  keep-alive; the cache is dropped only when a reload changes the routing.
- PAC scripts are compiled once to a `goja.Program`; evaluation draws VMs from
  a `sync.Pool`, so lookups run in parallel without a shared-VM lock.
- DNS lookups for noproxy matching and PAC `dnsResolve()` go through
  `internal/dnscache`.
- Request bodies stay streaming when only one forwarding attempt is possible.
  Requests that need auth/fallback replay keep up to 1 MiB in memory, then spool
  to a temp file with a 256 MiB per-request replay cap and a 512 MiB
  process-wide disk-spool budget. Replay capture follows request cancellation;
  terminal cleanup zeroes in-memory data and removes temp files.
- CONNECT relays preserve TCP half-close (`CloseWrite`) so early EOF on one side
  does not truncate the other. With idle tracking disabled, raw `io.Copy` keeps
  the platform zero-copy path. With idle tracking enabled, a lightweight reader
  wrapper refreshes the read deadline and shared tunnel activity timestamp on
  every successful read, preventing long one-way transfers from being mistaken
  for idle tunnels.

The test suite includes race-detector coverage for the proxy and Kerberos
packages.

## Python Reference

The original Python implementation remains in `px-python/`. It is used as the
behavioral reference while the Go port replaces Python packaging, libcurl usage,
and Python-specific keyring integration with Go-native code.

