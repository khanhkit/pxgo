# Benchmarking

PxGo performance evidence has two layers:

1. Go micro-benchmarks for isolated proxy hot paths.
2. A reproducible end-to-end harness that compares the exact PxGo and Python Px
   binaries on the same machine and retains the raw evidence needed to audit any
   published claim.

Benchmark output is evidence, not source code. **Do not publish a summary number
without retaining the corresponding raw artifact directory.**

## Micro-benchmarks

```bash
make bench
```

This runs `BenchmarkHTTPProxy` and `BenchmarkCONNECTProxy` with allocation
statistics. For before/after comparisons, use repeated runs and `benchstat`:

```bash
go test -bench 'HTTPProxy|CONNECTProxy' -benchmem -count 10 -run '^$' ./internal/proxy > new.txt
benchstat old.txt new.txt
```

Back-to-back network benchmarks can accumulate sockets in `TIME_WAIT`. Keep
the raw runs, report the environment, and explain excluded outliers rather than
silently deleting them.

## End-to-end harness

Inspect the complete workload matrix without installing benchmark tools:

```bash
scripts/bench-e2e.sh --plan
```

Validate the harness's isolation/PID/peak-RSS primitives:

```bash
scripts/bench-e2e.sh --self-test
scripts/bench-e2e_test.sh
```

A real local run requires:

- a built PxGo binary (`make build`),
- Python Px (`pip install px-proxy`),
- `hey`,
- `iperf3`,
- `proxytunnel`,
- `curl`,
- Python 3.

Then run:

```bash
make build
scripts/bench-e2e.sh
```

By default the harness exercises both proxies at client counts **1, 10, 50,
and 200**. `REQUESTS` controls the 1 KiB workload (default 5000) while
`LARGE_REQUESTS` separately bounds the 10 MiB workload (default 100), so the
large-body scenario does not accidentally transfer tens of gigabytes per
matrix cell. The matrix covers:

- HTTP 1 KiB forwarding,
- HTTP 10 MiB forwarding,
- CONNECT throughput with parallel iperf3 streams,
- SSE first-byte latency,
- a sustained HTTP soak.

The local workloads intentionally do not pretend to be Windows corporate-auth
coverage.

## Isolation and process ownership

The harness removes inherited `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`,
`NO_PROXY`, and lowercase equivalents from benchmark child processes. This
prevents the developer machine's own proxy configuration from changing the
routing under test.

Every local benchmark child runs from an isolated empty working directory with
a dedicated HOME/TMPDIR and an `env -i` environment. This prevents CWD
`pxgo.ini`, user-home Px/PxGo config, `PXGO_*` values, and unrelated host
state from entering the local comparison.

Every origin/proxy/tunnel/server process is started in the foreground by the
harness, captured by exact PID, and terminated through the PID registry. It
does not use process-name matching and does not start an untracked
`iperf3 -D` daemon.

Ports are checked before use. Override them with `PXGO_PORT`, `PX_PORT`,
`ORIGIN_PORT`, and `IPERF_PORT` when necessary.

## Peak memory

On Linux, peak RSS is read from the kernel's per-PID `VmHWM` value, so the
measurement is the process high-water mark rather than one final current-RSS
sample. On platforms without `/proc/<pid>/status`, the harness falls back to
per-PID `ps` RSS and the artifact must be labelled as a sampled fallback.

Processes are identified by PID, so Python Px is measured even when the OS
reports its executable/command name as `python` rather than `px`.

## Raw artifacts and reproducibility

Each real run creates a new immutable-style directory:

```text
benchmark-artifacts/<UTC_RUN_ID>/
  metadata.txt
  peak-rss.tsv
  manifest.sha256
  raw/
  logs/
  windows_pac_wpad_auth.status
```

`metadata.txt` records the repository SHA, environment, scenario parameters,
and SHA-256 identity of the exact PxGo and Px entrypoint binaries. Raw `hey`,
`curl`, and `iperf3` outputs are retained per proxy/scenario/client count.
`manifest.sha256` hashes the artifact files after the run.

Use a unique `RUN_ID`; the harness refuses to overwrite an existing artifact
directory.

## Windows PAC/WPAD + authentication gate

Local unauthenticated traffic is **not** a substitute for the intended
corporate Windows hot path. Publication-quality comparison must also run on an
authorized Windows/domain environment that covers, as applicable:

- WinHTTP PAC and/or WPAD discovery,
- PAC evaluation and routing,
- upstream 407 authentication,
- NTLM/SSPI or Negotiate continuation,
- authenticated connection reuse,
- the same client-count matrix and long-running resource sampling.

That infrastructure is intentionally external to this repository. Attach an
authorized driver with:

```bash
WINDOWS_CORP_DRIVER=/path/to/driver scripts/bench-e2e.sh
```

The driver receives an artifact-directory argument and must place its raw
evidence there. Set `REQUIRE_WINDOWS_CORP=1` for a publication/release run so
the harness fails instead of silently skipping the corporate scenario.

Without that driver, the run records:

```text
REQUIRES_EXTERNAL_WINDOWS_DOMAIN_RUNNER
```

in `windows_pac_wpad_auth.status`.

## Publishing results

No canonical PxGo-vs-Px end-to-end result is checked in merely to fill a table.
A result is publishable only when all of the following are attached:

- the raw artifact directory and `manifest.sha256`,
- exact repository SHA and binary hashes,
- hardware/OS/tool versions,
- local HTTP/CONNECT/SSE/soak outputs,
- required Windows PAC/WPAD/auth artifacts for claims about corporate usage,
- an explanation of repetitions, variance, failures, and any excluded runs.

A summary table should be generated from those artifacts and should link back
to the run ID. Until an authorized Windows/domain run exists, corporate-auth
performance remains **unmeasured**, not inferred from the local benchmark.
