#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$ROOT_DIR"

go test ./chat-server/chat-context -count=1
go test ./chat-server/server -count=1

echo "[OK] kvlite base tests passed"
