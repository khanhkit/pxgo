#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SCRIPT="$ROOT/scripts/bench-e2e.sh"
DOC="$ROOT/docs/benchmarking.md"
fail=0

bad() { echo "FAIL: $*" >&2; fail=1; }
ok() { echo "PASS: $*"; }

plan=$(bash "$SCRIPT" --plan 2>&1) || bad "--plan must work without benchmark prerequisites"
for scenario in http_1k http_10m connect sse_first_byte soak windows_pac_wpad_auth; do
  grep -q "$scenario" <<<"$plan" || bad "plan missing scenario $scenario"
done
for clients in 1 10 50 200; do
  grep -q "clients=$clients" <<<"$plan" || bad "plan missing client count $clients"
done
[[ "$fail" -ne 0 ]] || ok "scenario matrix includes required workloads and client counts"

self=$(bash "$SCRIPT" --self-test 2>&1) || bad "--self-test must validate harness internals without external benchmark tools"
grep -q 'SELFTEST PASS' <<<"$self" || bad "self-test did not report PASS"
[[ "$fail" -ne 0 ]] || ok "behavioral self-test passes"

grep -q 'ps -o rss=,comm= -C' "$SCRIPT" && bad "benchmark still identifies RSS by process name"
grep -Eq 'iperf3 .* -D' "$SCRIPT" && bad "benchmark still starts detached untracked iperf3"
grep -Fq 'kill $(jobs -p)' "$SCRIPT" && bad "benchmark cleanup still kills shell jobs rather than tracked PIDs"
grep -q 'HTTP_PROXY' "$SCRIPT" || bad "benchmark does not explicitly sanitize inherited proxy environment"
grep -q 'ARTIFACT' "$SCRIPT" || bad "benchmark has no raw artifact contract"
grep -q 'sha256' "$SCRIPT" || bad "benchmark does not record binary identity"
[[ "$fail" -ne 0 ]] || ok "static isolation/artifact contract passes"

if grep -q '_run .*bench-e2e.sh' "$DOC"; then
  bad "benchmark docs still contain publication-table placeholder"
fi
grep -qi 'raw artifact' "$DOC" || bad "benchmark docs do not require raw artifacts"
grep -qi 'Windows' "$DOC" || bad "benchmark docs do not define Windows corporate-auth execution boundary"
[[ "$fail" -ne 0 ]] || ok "documentation publication contract passes"


run_fake_integration() {
  local tmp fakebin base run offset artifact log raw_count pid
  tmp=$(mktemp -d)
  fakebin="$tmp/bin"
  mkdir -p "$fakebin" "$tmp/artifacts"

  cat >"$fakebin/fake-proxy" <<'PY'
#!/usr/bin/env python3
import socket
import sys

port = None
for arg in sys.argv[1:]:
    if arg.startswith("--port="):
        port = int(arg.split("=", 1)[1])
if port is None:
    raise SystemExit("missing --port")

sock = socket.socket()
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(("127.0.0.1", port))
sock.listen()
while True:
    conn, _ = sock.accept()
    conn.close()
PY
  chmod +x "$fakebin/fake-proxy"
  ln -s fake-proxy "$fakebin/pxgo"
  ln -s fake-proxy "$fakebin/px"

  cat >"$fakebin/iperf3" <<'PY'
#!/usr/bin/env python3
import socket
import sys

args = sys.argv[1:]
if "-s" not in args:
    print('{"fake":"iperf3-client"}')
    raise SystemExit(0)

port = int(args[args.index("-p") + 1])
sock = socket.socket()
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(("127.0.0.1", port))
sock.listen()
while True:
    conn, _ = sock.accept()
    conn.close()
PY
  chmod +x "$fakebin/iperf3"

  cat >"$fakebin/proxytunnel" <<'PY'
#!/usr/bin/env python3
import socket
import sys

args = sys.argv[1:]
port = int(args[args.index("-a") + 1])
sock = socket.socket()
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(("127.0.0.1", port))
sock.listen()
while True:
    conn, _ = sock.accept()
    conn.close()
PY
  chmod +x "$fakebin/proxytunnel"

  cat >"$fakebin/hey" <<'SH'
#!/bin/sh
printf 'fake hey %s\n' "$*"
SH
  cat >"$fakebin/curl" <<'SH'
#!/bin/sh
printf '0.001\n'
SH
  chmod +x "$fakebin/hey" "$fakebin/curl"

  base=$((22000 + (RANDOM % 5000)))
  for run in run1 run2; do
    if [[ "$run" == "run1" ]]; then
      offset=0
    else
      offset=500
    fi
    artifact="$tmp/artifacts/$run"
    log="$tmp/$run.log"
    if ! env \
      PATH="$fakebin:$PATH" \
      PXGO_BIN="$fakebin/pxgo" \
      PX_BIN="$fakebin/px" \
      ARTIFACT_ROOT="$tmp/artifacts" \
      RUN_ID="$run" \
      CLIENT_COUNTS="1 10" \
      REQUESTS=2 \
      LARGE_REQUESTS=1 \
      SSE_SAMPLES=2 \
      SOAK_SECONDS=1 \
      PXGO_PORT=$((base + offset)) \
      PX_PORT=$((base + offset + 1)) \
      ORIGIN_PORT=$((base + offset + 2)) \
      IPERF_PORT=$((base + offset + 3)) \
      TUNNEL_PORT_BASE=$((base + offset + 100)) \
      bash "$SCRIPT" >"$log" 2>&1; then
      cat "$log" >&2
      bad "fake normal benchmark run $run failed"
      rm -rf "$tmp"
      return
    fi

    [[ -s "$artifact/metadata.txt" ]] || bad "$run missing metadata"
    [[ -s "$artifact/peak-rss.tsv" ]] || bad "$run missing peak RSS artifact"
    [[ -s "$artifact/manifest.sha256" ]] || bad "$run missing manifest"
    grep -q '^pxgo.sha256=' "$artifact/metadata.txt" || bad "$run missing PxGo hash"
    grep -q '^px.sha256=' "$artifact/metadata.txt" || bad "$run missing Px hash"
    grep -q 'REQUIRES_EXTERNAL_WINDOWS_DOMAIN_RUNNER' "$artifact/windows_pac_wpad_auth.status" \
      || bad "$run did not record external Windows gate"

    raw_count=$(find "$artifact/raw" -type f | wc -l)
    [[ "$raw_count" -eq 20 ]] || bad "$run raw artifact count=$raw_count want=20"

    while read -r pid; do
      if kill -0 "$pid" 2>/dev/null; then
        bad "$run leaked tracked process pid=$pid"
        kill "$pid" 2>/dev/null || true
      fi
    done < <(grep -oE '(origin_pid|pxgo_pid|px_pid|iperf_pid)=[0-9]+' "$log" | cut -d= -f2)
  done

  find "$tmp/artifacts/run1/raw" -type f -printf '%f\n' | sort >"$tmp/run1.files"
  find "$tmp/artifacts/run2/raw" -type f -printf '%f\n' | sort >"$tmp/run2.files"
  cmp "$tmp/run1.files" "$tmp/run2.files" >/dev/null \
    || bad "repeated runs produced different scenario artifact sets"

  rm -rf "$tmp"
  [[ "$fail" -ne 0 ]] || ok "fake-tool normal lifecycle is reproducible and leak-free"
}

run_fake_integration

exit "$fail"
