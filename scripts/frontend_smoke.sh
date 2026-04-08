#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="${ROOT_DIR}/ai-chat-web"

log() {
  printf '[frontend-smoke] %s\n' "$*"
}

if ! command -v pnpm >/dev/null 2>&1; then
  log 'pnpm is required but was not found on PATH'
  exit 1
fi

if [[ ! -d "${WEB_DIR}" ]]; then
  log "frontend source directory not found: ${WEB_DIR}"
  exit 1
fi

log "running pnpm smoke in ${WEB_DIR}"
(cd "${WEB_DIR}" && pnpm smoke)

log 'smoke checks finished'