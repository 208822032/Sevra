#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

OUTPUT_DIR_RAW="${SEMANTIC_EVAL_OUTPUT_DIR:-artifacts/benchmarks/semantic_vs_lexical}"
if [[ "$OUTPUT_DIR_RAW" = /* ]]; then
	OUTPUT_DIR="$OUTPUT_DIR_RAW"
else
	OUTPUT_DIR="$ROOT_DIR/$OUTPUT_DIR_RAW"
fi

mkdir -p "$OUTPUT_DIR"

go test -timeout 10m ./chat-server/qa -run '^TestKVLiteStoreSemanticVsLexicalEvaluationReport$' -count=1 -v | tee "$OUTPUT_DIR/report.txt"