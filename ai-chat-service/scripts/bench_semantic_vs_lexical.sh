#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

DATASET_PATH_RAW="${RETRIEVAL_EVAL_DATASET:-chat-server/qa/testdata/retrieval_eval_suite.v1.json}"
OUTPUT_DIR_RAW="${RETRIEVAL_EVAL_OUTPUT_DIR:-${SEMANTIC_EVAL_OUTPUT_DIR:-artifacts/benchmarks/semantic_vs_lexical}}"
CONFIG_PATH_RAW="${RETRIEVAL_EVAL_CONFIG:-dev.config.yaml}"
EMBEDDING_MODE="${RETRIEVAL_EVAL_EMBEDDING_MODE:-placeholder}"
EMBEDDING_PROFILE="${RETRIEVAL_EVAL_EMBEDDING_PROFILE:-}"

if [[ "$DATASET_PATH_RAW" = /* ]]; then
	DATASET_PATH="$DATASET_PATH_RAW"
else
	DATASET_PATH="$ROOT_DIR/$DATASET_PATH_RAW"
fi

if [[ "$OUTPUT_DIR_RAW" = /* ]]; then
	OUTPUT_DIR="$OUTPUT_DIR_RAW"
else
	OUTPUT_DIR="$ROOT_DIR/$OUTPUT_DIR_RAW"
fi

if [[ "$CONFIG_PATH_RAW" = /* ]]; then
	CONFIG_PATH="$CONFIG_PATH_RAW"
else
	CONFIG_PATH="$ROOT_DIR/$CONFIG_PATH_RAW"
fi

mkdir -p "$OUTPUT_DIR"

CMD=(go run ./cmd/retrieval-eval --dataset "$DATASET_PATH" --output-dir "$OUTPUT_DIR" --config "$CONFIG_PATH" --embedding-mode "$EMBEDDING_MODE")

if [[ -n "$EMBEDDING_PROFILE" ]]; then
	CMD+=(--embedding-profile "$EMBEDDING_PROFILE")
fi

"${CMD[@]}" | tee "$OUTPUT_DIR/report.txt"