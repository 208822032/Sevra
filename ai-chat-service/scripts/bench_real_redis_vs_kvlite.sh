#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

check_port() {
  python3 - "$1" "$2" <<'PY'
import socket, sys
host = sys.argv[1]
port = int(sys.argv[2])
sock = socket.socket()
sock.settimeout(2)
try:
    sock.connect((host, port))
finally:
    sock.close()
PY
}

wait_port() {
  python3 - "$1" "$2" <<'PY'
import socket, sys, time
host = sys.argv[1]
port = int(sys.argv[2])
deadline = time.time() + 20
while time.time() < deadline:
    try:
        sock = socket.create_connection((host, port), timeout=1)
        sock.close()
        sys.exit(0)
    except Exception:
        time.sleep(0.2)
print(f"timeout waiting for {host}:{port}", file=sys.stderr)
sys.exit(1)
PY
}

start_local_kvlite() {
  local kv_dir="$ROOT_DIR/../KVLite"
  local kv_bin="$kv_dir/kvstore"
  TMP_DIR="$(mktemp -d)"
  local kv_log="$TMP_DIR/kvlite.log"
  local kv_conf="$TMP_DIR/kvlite.conf"

  cleanup() {
    if [[ -n "${KV_PID:-}" ]] && kill -0 "$KV_PID" 2>/dev/null; then
      kill -TERM "$KV_PID" 2>/dev/null || true
      wait "$KV_PID" 2>/dev/null || true
    fi
    rm -rf "$TMP_DIR"
  }
  trap cleanup EXIT

  if [[ ! -x "$kv_bin" ]]; then
    make -C "$kv_dir" kvstore
  fi

  local port sync_port
  port="${REAL_BENCH_KVLITE_PORT:-0}"
  if [[ "$port" == "0" ]]; then
    port="$(python3 - <<'PY'
import socket
sock = socket.socket()
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
)"
  fi
  sync_port="$((port + 1000))"

  cat > "$kv_conf" <<EOF
role = master
port = $port
sync_port = $sync_port
enable_aof = no
aof_fsync_mode = no
dump_filename = data/kvstore.dat
EOF

  "$kv_bin" --config "$kv_conf" > "$kv_log" 2>&1 &
  KV_PID=$!
  wait_port 127.0.0.1 "$port"
  export REAL_BENCH_KVLITE_ADDR="127.0.0.1:$port"
}

if [[ -z "${REAL_BENCH_REDIS_ADDR:-}" ]]; then
  export REAL_BENCH_REDIS_ADDR="127.0.0.1:6379"
fi

check_port "${REAL_BENCH_REDIS_ADDR%:*}" "${REAL_BENCH_REDIS_ADDR##*:}"

if [[ -z "${REAL_BENCH_KVLITE_ADDR:-}" ]]; then
  start_local_kvlite
else
  check_port "${REAL_BENCH_KVLITE_ADDR%:*}" "${REAL_BENCH_KVLITE_ADDR##*:}"
fi

export RUN_REAL_BENCH=1
export REAL_BENCH_MODE="${REAL_BENCH_MODE:-run}"
export REAL_BENCH_PROFILE="${REAL_BENCH_PROFILE:-full}"
export REAL_BENCH_OUTPUT_DIR="${REAL_BENCH_OUTPUT_DIR:-artifacts/benchmarks/real_redis_vs_kvlite}"
export REAL_BENCH_CLEAN_AFTER="${REAL_BENCH_CLEAN_AFTER:-0}"
export REAL_BENCH_CLEAN_BEFORE="${REAL_BENCH_CLEAN_BEFORE:-0}"

echo "[bench] mode=$REAL_BENCH_MODE profile=$REAL_BENCH_PROFILE"
echo "[bench] redis=$REAL_BENCH_REDIS_ADDR kvlite=$REAL_BENCH_KVLITE_ADDR"
echo "[bench] output=$REAL_BENCH_OUTPUT_DIR"

go test -timeout 30m ./chat-server/chat-context -run '^TestRealRedisVsKVLiteBenchmark$' -count=1 -v