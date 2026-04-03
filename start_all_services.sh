#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="${ROOT_DIR}/logs"
PID_FILE="${LOG_DIR}/start_all_services.pids"

mkdir -p "$LOG_DIR"
: > "$PID_FILE"

declare -a SERVICE_PIDS=()
declare -a SERVICE_NAMES=()

log() {
  printf '[%s] %s\n' "$(date '+%F %T')" "$*"
}

wait_for_port() {
  local host="$1"
  local port="$2"
  local timeout_seconds="${3:-60}"
  local start_ts
  start_ts="$(date +%s)"

  while true; do
    if (echo > "/dev/tcp/${host}/${port}") >/dev/null 2>&1; then
      return 0
    fi

    if (( $(date +%s) - start_ts >= timeout_seconds )); then
      log "等待 ${host}:${port} 超时"
      return 1
    fi

    sleep 1
  done
}

start_service() {
  local name="$1"
  local workdir="$2"
  local logfile="${LOG_DIR}/${name}.log"
  shift 2

  log "启动 ${name}"
  (
    cd "$workdir"
    "$@"
  ) >"$logfile" 2>&1 &

  local pid="$!"
  SERVICE_NAMES+=("$name")
  SERVICE_PIDS+=("$pid")
  printf '%s %s %s\n' "$name" "$pid" "$logfile" >> "$PID_FILE"
  log "${name} 已启动, PID=${pid}, 日志=${logfile}"
}

cleanup() {
  local pid
  for pid in "${SERVICE_PIDS[@]:-}"; do
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  done

  for pid in "${SERVICE_PIDS[@]:-}"; do
    wait "$pid" 2>/dev/null || true
  done
}

trap cleanup INT TERM EXIT

MOCK_DIR="${ROOT_DIR}/mock-openai-api"
PROXY_DIR="${ROOT_DIR}/openai-api-proxy"
KEYWORDS_DIR="${ROOT_DIR}/keywords-filter"
SERVICE_DIR="${ROOT_DIR}/ai-chat-service"
BACKEND_DIR="${ROOT_DIR}/ai-chat-backend"
WEB_DIR="${ROOT_DIR}/ai-chat-web"

log "开始按依赖顺序启动服务"

start_service "mock-openai-api" "$MOCK_DIR" go run ./main.go
wait_for_port 127.0.0.1 8083 60

start_service "openai-api-proxy" "$PROXY_DIR" go run ./main.go
wait_for_port 127.0.0.1 8084 60

start_service "keywords-filter-sensitive" "$KEYWORDS_DIR" go run ./filter-server/main.go --dict ./dict.txt --config dev.config.yaml
wait_for_port 127.0.0.1 50053 60

start_service "keywords-filter-keywords" "$KEYWORDS_DIR" go run ./filter-server/main.go --config dev.kw.config.yaml --dict keyword-dict.txt
wait_for_port 127.0.0.1 50054 60

start_service "ai-chat-service" "$SERVICE_DIR" go run ./chat-server/main.go
wait_for_port 127.0.0.1 50055 90

start_service "ai-chat-backend" "$BACKEND_DIR" go run cmd/main.go
wait_for_port 127.0.0.1 7080 90

if [[ "${START_WEB:-0}" == "1" ]]; then
  if command -v pnpm >/dev/null 2>&1; then
    start_service "ai-chat-web" "$WEB_DIR" pnpm dev
    wait_for_port 127.0.0.1 1002 90
  else
    log "未检测到 pnpm，跳过 ai-chat-web。设置 START_WEB=1 且安装 pnpm 后可启动前端。"
  fi
fi

log "全部服务已启动。按 Ctrl+C 退出时会自动清理进程。"
log "日志目录: ${LOG_DIR}"

while true; do
  if wait -n; then
    log "有服务已退出，开始清理其他进程"
  else
    log "有服务异常退出，开始清理其他进程"
  fi
  exit 1
done
