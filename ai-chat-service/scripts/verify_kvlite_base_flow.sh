#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$ROOT_DIR"

bash scripts/test_kvlite_base.sh

if [[ "${RUN_KVLITE_INTEGRATION:-0}" == "1" ]]; then
  go test ./chat-server/chat-context -count=1 -run 'TestKVLiteContext_EndToEndWithRealServer|TestKVLiteContext_EndToEndFlow'
fi

echo "[OK] kvlite base flow verified"
