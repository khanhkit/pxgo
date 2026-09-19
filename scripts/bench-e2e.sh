#!/usr/bin/env bash
# bench-e2e.sh — reproducible PxGo-vs-Px end-to-end benchmark harness.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PXGO_BIN=${PXGO_BIN:-"$ROOT/bin/pxgo"}
PX_BIN=${PX_BIN:-px}
PXGO_PORT=${PXGO_PORT:-3128}
PX_PORT=${PX_PORT:-3129}
ORIGIN_PORT=${ORIGIN_PORT:-8000}
IPERF_PORT=${IPERF_PORT:-5201}
TUNNEL_PORT_BASE=${TUNNEL_PORT_BASE:-15000}
REQUESTS=${REQUESTS:-5000}
LARGE_REQUESTS=${LARGE_REQUESTS:-100}
SSE_SAMPLES=${SSE_SAMPLES:-100}
SOAK_SECONDS=${SOAK_SECONDS:-60}
CLIENT_COUNTS=${CLIENT_COUNTS:-"1 10 50 200"}
ARTIFACT_ROOT=${ARTIFACT_ROOT:-"$ROOT/benchmark-artifacts"}
RUN_ID=${RUN_ID:-"$(date -u +%Y%m%dT%H%M%SZ)"}
WINDOWS_CORP_DRIVER=${WINDOWS_CORP_DRIVER:-}
REQUIRE_WINDOWS_CORP=${REQUIRE_WINDOWS_CORP:-0}
BENCH_CWD=${BENCH_CWD:-"$ROOT"}
BENCH_HOME=${BENCH_HOME:-"${HOME:-/tmp}"}
BENCH_TMPDIR=${BENCH_TMPDIR:-"${TMPDIR:-/tmp}"}

declare -a TRACKED_PIDS=()
declare -a TRACKED_LABELS=()
LAST_PID=""

usage() {
  cat <<'EOF'
Usage:
  scripts/bench-e2e.sh
  scripts/bench-e2e.sh --plan
  scripts/bench-e2e.sh --self-test

Environment:
  PXGO_BIN, PX_BIN, PXGO_PORT, PX_PORT, ORIGIN_PORT, IPERF_PORT
  TUNNEL_PORT_BASE, REQUESTS, LARGE_REQUESTS, SSE_SAMPLES, SOAK_SECONDS, CLIENT_COUNTS
  ARTIFACT_ROOT, RUN_ID
  WINDOWS_CORP_DRIVER=/path/to/authorized/windows-domain-driver
  REQUIRE_WINDOWS_CORP=1   fail if the external corporate scenario is unavailable
EOF
}

clean_env() {
  (
    cd "$BENCH_CWD"
    exec env -i       "PATH=$PATH"       "HOME=$BENCH_HOME"       "TMPDIR=$BENCH_TMPDIR"       "LC_ALL=C"       "TZ=UTC"       "BENCH_PROXY=${BENCH_PROXY:-}"       "BENCH_ORIGIN=${BENCH_ORIGIN:-}"       "BENCH_SAMPLES=${BENCH_SAMPLES:-}"       "$@"
  )
}

clean_corporate_env() {
  env     -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u NO_PROXY     -u http_proxy -u https_proxy -u all_proxy -u no_proxy     "$@"
}

plan() {
  local clients
  for clients in $CLIENT_COUNTS; do
    printf 'scenario=http_1k clients=%s platform=local\n' "$clients"
    printf 'scenario=http_10m clients=%s platform=local\n' "$clients"
    printf 'scenario=connect clients=%s platform=local\n' "$clients"
    printf 'scenario=sse_first_byte clients=%s platform=local\n' "$clients"
    printf 'scenario=soak clients=%s platform=local duration_s=%s\n' "$clients" "$SOAK_SECONDS"
    printf 'scenario=windows_pac_wpad_auth clients=%s platform=windows-external\n' "$clients"
  done
}

start_tracked() {
  local label=$1
  local logfile=$2
  shift 2
  (
    cd "$BENCH_CWD"
    exec env -i       "PATH=$PATH"       "HOME=$BENCH_HOME"       "TMPDIR=$BENCH_TMPDIR"       "LC_ALL=C"       "TZ=UTC"       "$@"
  ) >>"$logfile" 2>&1 &
  LAST_PID=$!
  TRACKED_PIDS+=("$LAST_PID")
  TRACKED_LABELS+=("$label")
}

untrack_pid() {
  local target=$1
  local -a next_pids=()
  local -a next_labels=()
  local i
  for i in "${!TRACKED_PIDS[@]}"; do
    if [[ "${TRACKED_PIDS[$i]}" != "$target" ]]; then
      next_pids+=("${TRACKED_PIDS[$i]}")
      next_labels+=("${TRACKED_LABELS[$i]}")
    fi
  done
  TRACKED_PIDS=("${next_pids[@]}")
  TRACKED_LABELS=("${next_labels[@]}")
}

stop_pid() {
  local pid=$1
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL "$pid" 2>/dev/null || true
  fi
  wait "$pid" 2>/dev/null || true
}

cleanup_processes() {
  local i
  for ((i=${#TRACKED_PIDS[@]}-1; i>=0; i--)); do
    stop_pid "${TRACKED_PIDS[$i]}"
  done
  TRACKED_PIDS=()
  TRACKED_LABELS=()
}

on_exit() {
  cleanup_processes
}

wait_port() {
  local host=$1 port=$2 timeout_s=${3:-10}
  local deadline=$((SECONDS + timeout_s))
  while (( SECONDS < deadline )); do
    if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then
      exec 3>&-
      exec 3<&-
      return 0
    fi
    sleep 0.1
  done
  return 1
}

assert_port_free() {
  local host=$1 port=$2
  if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then
    exec 3>&-
    exec 3<&-
    echo "port already in use: $host:$port" >&2
    return 1
  fi
}

read_peak_rss_kb() {
  local pid=$1
  if [[ -r "/proc/$pid/status" ]]; then
    awk '/^VmHWM:/ {print $2; found=1} END {if (!found) exit 1}' "/proc/$pid/status"
    return
  fi
  ps -o rss= -p "$pid" | awk 'NF {print $1}'
}

hash_file() {
  local path=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  else
    shasum -a 256 "$path" | awk '{print $1}'
  fi
}

resolve_command() {
  local cmd=$1
  if [[ "$cmd" == */* ]]; then
    [[ -x "$cmd" ]] || return 1
    printf '%s\n' "$cmd"
    return
  fi
  command -v "$cmd"
}

record_binary() {
  local label=$1 cmd=$2 out=$3
  local path
  path=$(resolve_command "$cmd")
  {
    printf '%s.path=%s\n' "$label" "$path"
    printf '%s.sha256=%s\n' "$label" "$(hash_file "$path")"
  } >>"$out"
}

write_manifest() {
  local dir=$1
  local out="$dir/manifest.sha256"
  local file rel
  : >"$out"
  while IFS= read -r file; do
    [[ "$file" == "$out" ]] && continue
    rel=${file#"$dir/"}
    printf '%s  %s\n' "$(hash_file "$file")" "$rel" >>"$out"
  done < <(find "$dir" -type f | LC_ALL=C sort)
}

write_origin() {
  local path=$1 data_dir=$2
  cat >"$path" <<PY
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from functools import partial
import sys

DATA = r"$data_dir"
PORT = int(sys.argv[1])

class H(SimpleHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.path == "/sse":
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            self.wfile.write(b"data: ready\\n\\n")
            self.wfile.flush()
            return
        return super().do_GET()

handler = partial(H, directory=DATA)
ThreadingHTTPServer(("127.0.0.1", PORT), handler).serve_forever()
PY
}

self_test() {
  local tmp child peak got
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"; cleanup_processes' RETURN

  mkdir -p "$tmp/work" "$tmp/home" "$tmp/tmp"
  BENCH_CWD="$tmp/work"
  BENCH_HOME="$tmp/home"
  BENCH_TMPDIR="$tmp/tmp"

  export HTTP_PROXY=http://poison.invalid:1
  export HTTPS_PROXY=http://poison.invalid:2
  export PXGO_CONFIG=/poison/pxgo.ini
  export PX_CONFIG=/poison/px.ini
  got=$(clean_env bash -c 'printf "%s/%s/%s/%s/%s/%s" "${HTTP_PROXY-unset}" "${HTTPS_PROXY-unset}" "${PXGO_CONFIG-unset}" "${PX_CONFIG-unset}" "$PWD" "$HOME"')
  [[ "$got" == "unset/unset/unset/unset/$BENCH_CWD/$BENCH_HOME" ]] || {
    echo "SELFTEST FAIL: clean_env leaked state or cwd: $got" >&2
    return 1
  }

  start_tracked selftest "$tmp/child.log" sleep 30
  child=$LAST_PID
  kill -0 "$child" 2>/dev/null || {
    echo "SELFTEST FAIL: tracked child did not start" >&2
    return 1
  }
  peak=$(read_peak_rss_kb "$child")
  [[ "$peak" =~ ^[0-9]+$ ]] && (( peak > 0 )) || {
    echo "SELFTEST FAIL: peak RSS unavailable for tracked PID" >&2
    return 1
  }
  stop_pid "$child"
  untrack_pid "$child"
  if kill -0 "$child" 2>/dev/null; then
    echo "SELFTEST FAIL: tracked child survived stop_pid" >&2
    return 1
  fi
  TRACKED_PIDS=()
  TRACKED_LABELS=()

  echo "SELFTEST PASS"
}

require_tools() {
  local tool
  for tool in hey iperf3 proxytunnel python3 curl seq xargs awk ps git find sort; do
    command -v "$tool" >/dev/null 2>&1 || {
      echo "missing prerequisite: $tool" >&2
      return 1
    }
  done
  resolve_command "$PXGO_BIN" >/dev/null || {
    echo "missing prerequisite: $PXGO_BIN" >&2
    return 1
  }
  resolve_command "$PX_BIN" >/dev/null || {
    echo "missing prerequisite: $PX_BIN" >&2
    return 1
  }
}

run_http() {
  local name=$1 proxy_port=$2 blob=$3 clients=$4 out=$5
  local requests=$REQUESTS
  if [[ "$blob" == "10m.bin" ]]; then
    requests=$LARGE_REQUESTS
  fi
  printf 'scenario=http_%s proxy=%s clients=%s requests=%s\n' "$blob" "$name" "$clients" "$requests"
  clean_env hey -n "$requests" -c "$clients" -x "http://127.0.0.1:$proxy_port"     "http://127.0.0.1:$ORIGIN_PORT/$blob" >"$out" 2>&1
}

run_sse() {
  local name=$1 proxy_port=$2 clients=$3 out=$4
  printf 'scenario=sse_first_byte proxy=%s clients=%s\n' "$name" "$clients"
  export BENCH_PROXY="http://127.0.0.1:$proxy_port"
  export BENCH_ORIGIN="http://127.0.0.1:$ORIGIN_PORT/sse"
  export BENCH_SAMPLES="$SSE_SAMPLES"
  seq 1 "$SSE_SAMPLES" | clean_env xargs -P "$clients" -I{} bash -c     'curl --silent --show-error --output /dev/null --max-time 10 --proxy "$BENCH_PROXY" --write-out "%{time_starttransfer}\\n" "$BENCH_ORIGIN"'     >"$out" 2>&1
}

run_connect() {
  local name=$1 proxy_port=$2 clients=$3 local_port=$4 out=$5 log=$6
  printf 'scenario=connect proxy=%s clients=%s\n' "$name" "$clients"
  start_tracked "proxytunnel-$name-$clients" "$log"     proxytunnel -p "127.0.0.1:$proxy_port" -d "127.0.0.1:$IPERF_PORT" -a "$local_port"
  local tunnel_pid=$LAST_PID
  wait_port 127.0.0.1 "$local_port" 5
  clean_env iperf3 -c 127.0.0.1 -p "$local_port" -P "$clients" -J >"$out" 2>&1
  stop_pid "$tunnel_pid"
  untrack_pid "$tunnel_pid"
}

run_soak() {
  local name=$1 proxy_port=$2 clients=$3 out=$4
  printf 'scenario=soak proxy=%s clients=%s duration_s=%s\n' "$name" "$clients" "$SOAK_SECONDS"
  clean_env hey -z "${SOAK_SECONDS}s" -c "$clients" -x "http://127.0.0.1:$proxy_port"     "http://127.0.0.1:$ORIGIN_PORT/1k.bin" >"$out" 2>&1
}

run_windows_corporate() {
  local artifact_dir=$1
  local status="$artifact_dir/windows_pac_wpad_auth.status"
  if [[ -n "$WINDOWS_CORP_DRIVER" && -x "$WINDOWS_CORP_DRIVER" ]]; then
    clean_corporate_env "$WINDOWS_CORP_DRIVER" "$artifact_dir/windows-corporate" >"$artifact_dir/windows-corporate-driver.log" 2>&1
    printf 'PASS driver=%s\n' "$WINDOWS_CORP_DRIVER" >"$status"
    return 0
  fi
  printf 'REQUIRES_EXTERNAL_WINDOWS_DOMAIN_RUNNER\n' >"$status"
  if [[ "$REQUIRE_WINDOWS_CORP" == "1" ]]; then
    echo "Windows PAC/WPAD + NTLM/SSPI benchmark requires WINDOWS_CORP_DRIVER" >&2
    return 1
  fi
}

main_run() {
  require_tools
  trap on_exit EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  assert_port_free 127.0.0.1 "$PXGO_PORT"
  assert_port_free 127.0.0.1 "$PX_PORT"
  assert_port_free 127.0.0.1 "$ORIGIN_PORT"
  assert_port_free 127.0.0.1 "$IPERF_PORT"

  local artifact_dir="$ARTIFACT_ROOT/$RUN_ID"
  if [[ -e "$artifact_dir" ]]; then
    echo "artifact directory already exists: $artifact_dir" >&2
    return 1
  fi
  mkdir -p "$artifact_dir/raw" "$artifact_dir/logs"
  local tmp="$artifact_dir/tmp"
  BENCH_CWD="$tmp/work"
  BENCH_HOME="$tmp/home"
  BENCH_TMPDIR="$tmp/tmp"
  mkdir -p "$tmp/data" "$BENCH_CWD" "$BENCH_HOME" "$BENCH_TMPDIR"

  head -c 1024 /dev/zero >"$tmp/data/1k.bin"
  head -c $((10 * 1024 * 1024)) /dev/zero >"$tmp/data/10m.bin"
  write_origin "$tmp/origin.py" "$tmp/data"

  local metadata="$artifact_dir/metadata.txt"
  {
    printf 'run_id=%s\n' "$RUN_ID"
    printf 'utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'git_sha=%s\n' "$(git -C "$ROOT" rev-parse HEAD)"
    printf 'uname=%s\n' "$(uname -a)"
    printf 'client_counts=%s\n' "$CLIENT_COUNTS"
    printf 'requests_1k=%s\n' "$REQUESTS"
    printf 'requests_10m=%s\n' "$LARGE_REQUESTS"
    printf 'sse_samples=%s\n' "$SSE_SAMPLES"
    printf 'soak_seconds=%s\n' "$SOAK_SECONDS"
  } >"$metadata"
  record_binary pxgo "$PXGO_BIN" "$metadata"
  record_binary px "$PX_BIN" "$metadata"
  record_binary hey hey "$metadata"
  record_binary iperf3 iperf3 "$metadata"
  record_binary proxytunnel proxytunnel "$metadata"
  record_binary curl curl "$metadata"
  record_binary python3 python3 "$metadata"

  start_tracked origin "$artifact_dir/logs/origin.log" python3 "$tmp/origin.py" "$ORIGIN_PORT"
  local origin_pid=$LAST_PID
  wait_port 127.0.0.1 "$ORIGIN_PORT" 10

  start_tracked pxgo "$artifact_dir/logs/pxgo.log" "$PXGO_BIN" --port="$PXGO_PORT"
  local pxgo_pid=$LAST_PID
  start_tracked px "$artifact_dir/logs/px.log" "$PX_BIN" --port="$PX_PORT"
  local px_pid=$LAST_PID
  wait_port 127.0.0.1 "$PXGO_PORT" 10
  wait_port 127.0.0.1 "$PX_PORT" 10

  start_tracked iperf-server "$artifact_dir/logs/iperf-server.log" iperf3 -s -p "$IPERF_PORT"
  local iperf_pid=$LAST_PID
  wait_port 127.0.0.1 "$IPERF_PORT" 10

  local clients name port local_port
  for clients in $CLIENT_COUNTS; do
    for name in pxgo px; do
      if [[ "$name" == "pxgo" ]]; then
        port=$PXGO_PORT
      else
        port=$PX_PORT
      fi
      run_http "$name" "$port" 1k.bin "$clients" "$artifact_dir/raw/${name}-http_1k-c${clients}.txt"
      run_http "$name" "$port" 10m.bin "$clients" "$artifact_dir/raw/${name}-http_10m-c${clients}.txt"
      run_sse "$name" "$port" "$clients" "$artifact_dir/raw/${name}-sse-c${clients}.txt"
      if [[ "$name" == "pxgo" ]]; then
        local_port=$((TUNNEL_PORT_BASE + clients))
      else
        local_port=$((TUNNEL_PORT_BASE + 1000 + clients))
      fi
      run_connect "$name" "$port" "$clients" "$local_port"         "$artifact_dir/raw/${name}-connect-c${clients}.json"         "$artifact_dir/logs/${name}-proxytunnel-c${clients}.log"
      run_soak "$name" "$port" "$clients" "$artifact_dir/raw/${name}-soak-c${clients}.txt"
    done
  done

  {
    printf 'proxy\tpid\tpeak_rss_kb\n'
    printf 'pxgo\t%s\t%s\n' "$pxgo_pid" "$(read_peak_rss_kb "$pxgo_pid")"
    printf 'px\t%s\t%s\n' "$px_pid" "$(read_peak_rss_kb "$px_pid")"
  } >"$artifact_dir/peak-rss.tsv"

  run_windows_corporate "$artifact_dir"

  write_manifest "$artifact_dir"

  printf 'artifact_dir=%s\n' "$artifact_dir"
  printf 'origin_pid=%s pxgo_pid=%s px_pid=%s iperf_pid=%s\n' "$origin_pid" "$pxgo_pid" "$px_pid" "$iperf_pid"
  printf 'Raw artifacts retained. Do not publish summary numbers without this directory.\n'
}

case "${1:-}" in
  --plan)
    plan
    ;;
  --self-test)
    self_test
    ;;
  --help|-h)
    usage
    ;;
  "")
    main_run
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
