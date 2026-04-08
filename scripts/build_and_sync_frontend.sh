#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="${ROOT_DIR}/ai-chat-web"
BACKEND_WWW_DIR="${ROOT_DIR}/ai-chat-backend/www"
DIST_DIR="${WEB_DIR}/dist"

log() {
  printf '[frontend-sync] %s\n' "$*"
}

if ! command -v pnpm >/dev/null 2>&1; then
  log 'pnpm is required but was not found on PATH'
  exit 1
fi

if ! command -v rsync >/dev/null 2>&1; then
  log 'rsync is required but was not found on PATH'
  exit 1
fi

if [[ ! -d "${WEB_DIR}" ]]; then
  log "frontend source directory not found: ${WEB_DIR}"
  exit 1
fi

mkdir -p "${BACKEND_WWW_DIR}"

log "source: ${WEB_DIR}"
log "build: pnpm build"
(cd "${WEB_DIR}" && pnpm build)

if [[ ! -d "${DIST_DIR}" ]]; then
  log "build output directory not found: ${DIST_DIR}"
  exit 1
fi

log "sync: ${DIST_DIR} -> ${BACKEND_WWW_DIR}"
rsync -a --delete "${DIST_DIR}/" "${BACKEND_WWW_DIR}/"

log "done: backend runtime frontend refreshed at ${BACKEND_WWW_DIR}"