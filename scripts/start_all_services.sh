#!/usr/bin/env bash
# Manages the local AI-chat development stack. It owns only the processes it
# starts; MySQL, Redis, tokenizer and vector KVLite remain external services.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${ROOT_DIR}/runtime/services"
LOG_DIR="${STATE_DIR}/logs"
BIN_DIR="${STATE_DIR}/bin"
PID_FILE="${STATE_DIR}/services.tsv"
GRACE_SECONDS="${GRACE_SECONDS:-15}"

MOCK_DIR="${ROOT_DIR}/mock-openai-api"
PROXY_DIR="${ROOT_DIR}/openai-api-proxy"
KEYWORDS_DIR="${ROOT_DIR}/keywords-filter"
SERVICE_DIR="${ROOT_DIR}/ai-chat-service"
BACKEND_DIR="${ROOT_DIR}/ai-chat-backend"
WEB_DIR="${ROOT_DIR}/ai-chat-web"

declare -a SERVICE_NAMES=()
declare -a SERVICE_PIDS=()
declare -a SERVICE_BINS=()
STARTING=0

log() {
  printf '[%s] %s\n' "$(date '+%F %T')" "$*"
}

die() {
  log "错误: $*"
  return 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令: $1"
}

ensure_layout() {
  mkdir -p "$LOG_DIR" "$BIN_DIR"
  touch "$PID_FILE"
}

pid_command_contains() {
  local pid="$1" expected="$2"
  [[ -r "/proc/${pid}/cmdline" ]] || return 1
  tr '\0' ' ' < "/proc/${pid}/cmdline" | grep -Fq -- "$expected"
}

pid_is_owned() {
  local pid="$1" expected="$2"
  kill -0 "$pid" 2>/dev/null && pid_command_contains "$pid" "$expected"
}

rewrite_state() {
  local tmp="${PID_FILE}.tmp.$$"
  : > "$tmp"
  local index
  for index in "${!SERVICE_NAMES[@]}"; do
    printf '%s\t%s\t%s\n' "${SERVICE_NAMES[$index]}" "${SERVICE_PIDS[$index]}" "${SERVICE_BINS[$index]}" >> "$tmp"
  done
  mv "$tmp" "$PID_FILE"
}

load_live_state() {
  SERVICE_NAMES=()
  SERVICE_PIDS=()
  SERVICE_BINS=()
  [[ -f "$PID_FILE" ]] || return 0

  local name pid binary
  while IFS=$'\t' read -r name pid binary; do
    [[ -n "${name:-}" && -n "${pid:-}" && -n "${binary:-}" ]] || continue
    if pid_is_owned "$pid" "$binary"; then
      SERVICE_NAMES+=("$name")
      SERVICE_PIDS+=("$pid")
      SERVICE_BINS+=("$binary")
    else
      log "忽略过期或非本管理器进程: ${name} pid=${pid}"
    fi
  done < "$PID_FILE"
  rewrite_state
}

wait_for_port() {
  local host="$1" port="$2" timeout_seconds="$3"
  local deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    if (echo >/dev/tcp/"$host"/"$port") >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "等待 TCP ${host}:${port} 超时"
}

wait_for_http() {
  local url="$1" timeout_seconds="$2"
  local deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    if curl --noproxy '*' --fail --silent --show-error --max-time 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "等待 HTTP 健康检查超时: ${url}"
}

wait_for_grpc() {
  local probe="$1" address="$2" timeout_seconds="$3"
  [[ -f "$probe" ]] || die "缺少 gRPC health probe: ${probe}"
  local executable="${BIN_DIR}/$(basename "$probe")"
  if [[ ! -x "$executable" || "$probe" -nt "$executable" ]]; then
    install -m 0755 "$probe" "$executable"
  fi
  local deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    if "$executable" -addr="$address" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "等待 gRPC 健康检查超时: ${address}"
}

wait_for_pid_or_fail() {
  local pid="$1" name="$2"
  if ! kill -0 "$pid" 2>/dev/null; then
    die "${name} 在健康检查前已退出；查看日志: ${LOG_DIR}/${name}.log"
  fi
}

confirm_last_service_ownership() {
  local index=$(( ${#SERVICE_PIDS[@]} - 1 ))
  local pid="${SERVICE_PIDS[$index]}" binary="${SERVICE_BINS[$index]}" name="${SERVICE_NAMES[$index]}"
  if ! pid_is_owned "$pid" "$binary"; then
    die "${name} 未保持为本次启动的受管理进程；查看日志: ${LOG_DIR}/${name}.log"
  fi
}

build_go_service() {
  local name workdir package binary
  name="$1"
  workdir="$2"
  package="$3"
  binary="${BIN_DIR}/${name}"
  log "构建 ${name}" >&2
  (
    cd "$workdir"
    go build -o "$binary" "$package"
  )
  printf '%s' "$binary"
}

start_service() {
  local name workdir binary logfile pid
  name="$1"
  workdir="$2"
  binary="$3"
  shift 3
  logfile="${LOG_DIR}/${name}.log"

  log "启动 ${name}"
  # setsid makes the recorded PID the leader of a dedicated process group.
  # stop then signals the whole group, including any child process it creates.
  setsid bash -c 'cd "$1"; shift; exec "$@"' _ "$workdir" "$binary" "$@" >"$logfile" 2>&1 &
  pid="$!"
  SERVICE_NAMES+=("$name")
  SERVICE_PIDS+=("$pid")
  SERVICE_BINS+=("$binary")
  rewrite_state
  sleep 0.1
  wait_for_pid_or_fail "$pid" "$name"
  log "${name} 已启动, PID=${pid}, 日志=${logfile}"
}

terminate_group() {
  local pid="$1" signal="$2"
  kill -"$signal" -- "-$pid" 2>/dev/null || true
}

stop_loaded_services() {
  local index pid
  for index in "${!SERVICE_PIDS[@]}"; do
    pid="${SERVICE_PIDS[$index]}"
    if kill -0 "$pid" 2>/dev/null; then
      log "向 ${SERVICE_NAMES[$index]} (进程组 ${pid}) 发送 TERM"
      terminate_group "$pid" TERM
    fi
  done

  local deadline=$((SECONDS + GRACE_SECONDS)) remaining=1
  while (( SECONDS < deadline && remaining )); do
    remaining=0
    for pid in "${SERVICE_PIDS[@]}"; do
      if kill -0 "$pid" 2>/dev/null; then
        remaining=1
        break
      fi
    done
    (( remaining )) && sleep 1
  done

  for index in "${!SERVICE_PIDS[@]}"; do
    pid="${SERVICE_PIDS[$index]}"
    if kill -0 "$pid" 2>/dev/null; then
      log "${SERVICE_NAMES[$index]} 超过 ${GRACE_SECONDS}s 未退出，发送 KILL"
      terminate_group "$pid" KILL
    fi
  done
  rm -f "$PID_FILE"
}

rollback_start() {
  if (( STARTING )); then
    log "启动失败，回滚本次已启动服务"
    load_live_state
    stop_loaded_services
  fi
}

preflight() {
  require_command bash
  require_command curl
  require_command go
  require_command setsid
  ensure_layout
  load_live_state
  if (( ${#SERVICE_PIDS[@]} > 0 )); then
    die "已有受管理服务运行；先执行: $0 stop"
  fi
  # AI-chat uses startup_policy=reject, so this external vector dependency is
  # mandatory. AI-chat itself performs the stricter VINFO readiness check.
  wait_for_port "${KVLITE_VECTOR_HOST:-127.0.0.1}" "${KVLITE_VECTOR_PORT:-28080}" "${KVLITE_VECTOR_WAIT_SECONDS:-10}"
}

start_all() {
  preflight
  STARTING=1
  trap 'rollback_start; exit 130' INT TERM

  if ! start_stack; then
    rollback_start
    return 1
  fi

  STARTING=0
  trap - INT TERM
  log "全部服务已就绪。使用 '$0 status' 查看状态，使用 '$0 stop' 优雅退出。"
}

start_stack() {
  local binary
  binary="$(build_go_service mock-openai-api "$MOCK_DIR" .)" || return 1
  start_service mock-openai-api "$MOCK_DIR" "$binary" --config dev.config.yaml || return 1
  wait_for_http http://127.0.0.1:8083/health 60 || return 1
  confirm_last_service_ownership || return 1

  binary="$(build_go_service openai-api-proxy "$PROXY_DIR" .)" || return 1
  start_service openai-api-proxy "$PROXY_DIR" "$binary" --config dev.config.yaml || return 1
  wait_for_http http://127.0.0.1:8084/health 60 || return 1
  confirm_last_service_ownership || return 1

  binary="$(build_go_service keywords-filter-sensitive "$KEYWORDS_DIR" ./filter-server)" || return 1
  start_service keywords-filter-sensitive "$KEYWORDS_DIR" "$binary" --config dev.config.yaml --dict dict.txt || return 1
  wait_for_grpc "${KEYWORDS_DIR}/grpc_health_probe-linux-amd64" 127.0.0.1:50053 60 || return 1
  confirm_last_service_ownership || return 1

  start_service keywords-filter-keywords "$KEYWORDS_DIR" "$binary" --config dev.kw.config.yaml --dict keyword-dict.txt || return 1
  wait_for_grpc "${KEYWORDS_DIR}/grpc_health_probe-linux-amd64" 127.0.0.1:50054 60 || return 1
  confirm_last_service_ownership || return 1

  binary="$(build_go_service ai-chat-service "$SERVICE_DIR" ./chat-server)" || return 1
  start_service ai-chat-service "$SERVICE_DIR" "$binary" --config dev.config.yaml || return 1
  wait_for_grpc "${SERVICE_DIR}/grpc_health_probe-linux-amd64" 127.0.0.1:50055 90 || return 1
  confirm_last_service_ownership || return 1

  binary="$(build_go_service ai-chat-backend "$BACKEND_DIR" ./cmd)" || return 1
  start_service ai-chat-backend "$BACKEND_DIR" "$binary" --config dev.config.yaml || return 1
  wait_for_http http://127.0.0.1:7080/api/health 90 || return 1
  confirm_last_service_ownership || return 1

  if [[ "${START_WEB:-1}" == "1" ]]; then
    require_command pnpm || return 1
    start_service ai-chat-web "$WEB_DIR" pnpm dev || return 1
    wait_for_http http://127.0.0.1:5173 90 || return 1
    confirm_last_service_ownership || return 1
  fi
}

stop_all() {
  ensure_layout
  load_live_state
  if (( ${#SERVICE_PIDS[@]} == 0 )); then
    log "没有受本脚本管理的运行中服务"
    return 0
  fi
  stop_loaded_services
  log "服务已停止"
}

status_all() {
  ensure_layout
  load_live_state
  if (( ${#SERVICE_PIDS[@]} == 0 )); then
    log "没有受本脚本管理的运行中服务"
    return 1
  fi
  local index
  for index in "${!SERVICE_NAMES[@]}"; do
    printf '%-28s running pid=%s log=%s/%s.log\n' "${SERVICE_NAMES[$index]}" "${SERVICE_PIDS[$index]}" "$LOG_DIR" "${SERVICE_NAMES[$index]}"
  done
}

usage() {
  cat <<EOF
Usage: $0 {start|stop|restart|status}

Environment:
  START_WEB=1                 Also start ai-chat-web through pnpm.
  GRACE_SECONDS=15            TERM grace period before KILL.
  KVLITE_VECTOR_HOST=127.0.0.1
  KVLITE_VECTOR_PORT=28080    External required vector KVLite endpoint.
EOF
}

case "${1:-start}" in
  start) start_all ;;
  stop) stop_all ;;
  restart) stop_all; start_all ;;
  status) status_all ;;
  -h|--help|help) usage ;;
  *) usage; exit 2 ;;
esac
