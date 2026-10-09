#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/ai-chat-stack/compose.yaml"
WEB_CONFIG="${ROOT_DIR}/ai-chat-web/nginx.local.conf"
WEB_CONTAINER="ai-chat-web"
WEB_IMAGE="ai-chat-web:local"
WEB_PORT="${WEB_PORT:-5173}"
REDIS_PASSWORD="${REDIS_PASSWORD:-123456}"
MYSQL_USER="${MYSQL_USER:-ai_chat}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-123456}"
MYSQL_DB="${MYSQL_DB:-ai_chat}"
KVLITE_CLIENT="${KVLITE_CLIENT:-/home/zhengpan/share/Linux/KVLite/build/tests/bin/kvstore_client}"

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }
require() { command -v "$1" >/dev/null 2>&1 || die "缺少命令: $1"; }

start_system_service() {
  local service="$1"
  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files "${service}.service" >/dev/null 2>&1; then
    sudo systemctl enable --now "$service"
  else
    log "跳过 ${service}：请确认它已自行运行"
  fi
}

check_host_dependencies() {
  redis-cli -h 127.0.0.1 -p 6379 -a "$REDIS_PASSWORD" --no-auth-warning ping \
    | grep -qx PONG || die "Redis 检查失败"
  mysql --connect-timeout=3 -h127.0.0.1 -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" \
    -Nse "SELECT 1 FROM ${MYSQL_DB}.chat_records LIMIT 1;" >/dev/null \
    || die "MySQL 或 ${MYSQL_DB}.chat_records 检查失败"
}

check_kvlite() {
  (echo >/dev/tcp/127.0.0.1/28080) 2>/dev/null || die "KVLite 向量端口 28080 不可用"
  (echo >/dev/tcp/127.0.0.1/9300) 2>/dev/null || die "KVLite 会话端口 9300 不可用"
  if [[ -x "$KVLITE_CLIENT" ]]; then
    "$KVLITE_CLIENT" 127.0.0.1 28080 -c 'VINFO' \
      | grep -q '"index_ready:1"' || die "KVLite 向量索引未 ready"
  fi
}

check_tokenizer() {
  curl --noproxy '*' -fsS -X POST http://127.0.0.1:3002/tokenizer/deepseek-chat \
    -H 'Content-Type: application/json' \
    -d '{"role":"user","content":"healthcheck"}' \
    | grep -q '"code":200' || die "tokenizer 检查失败"
}

check() {
  require docker
  require curl
  require redis-cli
  require mysql
  [[ -f "$COMPOSE_FILE" ]] || die "找不到 $COMPOSE_FILE"
  [[ -f "$WEB_CONFIG" ]] || die "找不到 $WEB_CONFIG"
  check_host_dependencies
  check_kvlite
  check_tokenizer
  curl --noproxy '*' -fsS -o /dev/null http://127.0.0.1:7080/api/health || die "后端检查失败"
  curl --noproxy '*' -fsS -o /dev/null "http://127.0.0.1:${WEB_PORT}/" || die "前端检查失败"
  curl --noproxy '*' -fsS -o /dev/null "http://127.0.0.1:${WEB_PORT}/api/health" || die "前端 API 代理检查失败"
  local backend_container
  backend_container="$(docker compose -f "$COMPOSE_FILE" ps -q ai-chat-backend | head -n1)"
  [[ -n "$backend_container" ]] || die "找不到 ai-chat-backend 容器"
  docker exec "$backend_container" getent hosts ai-chat-service >/dev/null \
    || die "Docker DNS 无法解析 ai-chat-service"
  log "完整链路检查通过"
}

start_web() {
  docker rm -f "$WEB_CONTAINER" >/dev/null 2>&1 || true
  docker run -d \
    --name "$WEB_CONTAINER" \
    --restart unless-stopped \
    --add-host host.docker.internal:host-gateway \
    -p "${WEB_PORT}:80" \
    -v "${WEB_CONFIG}:/etc/nginx/conf.d/default.conf:ro" \
    "$WEB_IMAGE" >/dev/null
}

start_all() {
  start_system_service redis-server
  start_system_service mysql
  docker rm -f tokenizer >/dev/null 2>&1 || true
  docker compose -f "$COMPOSE_FILE" up -d --wait
  start_web
  check
  log "启动完成：前端 http://127.0.0.1:${WEB_PORT}"
}

stop_all() {
  docker rm -f "$WEB_CONTAINER" >/dev/null 2>&1 || true
  docker compose -f "$COMPOSE_FILE" down --remove-orphans
  if [[ "${STOP_SYSTEM_SERVICES:-0}" == "1" ]]; then
    sudo systemctl stop mysql redis-server || true
    log "Redis/MySQL 已停止"
  else
    log "Redis/MySQL 未停止；如需停止请设置 STOP_SYSTEM_SERVICES=1"
  fi
}

status() {
  docker compose -f "$COMPOSE_FILE" ps -a
  docker ps --filter "name=^/${WEB_CONTAINER}$" \
    --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'
}

case "${1:-status}" in
  start) start_all ;;
  stop) stop_all ;;
  restart) stop_all; start_all ;;
  status) status ;;
  check) check ;;
  *) echo "用法: $0 {start|stop|restart|status|check}"; exit 2 ;;
esac
