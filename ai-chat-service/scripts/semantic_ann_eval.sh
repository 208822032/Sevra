#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

INPUT_RAW="${SEMANTIC_ANN_SAMPLE_FILE:-artifacts/benchmarks/semantic_ann_samples.jsonl}"
TOP_N="${SEMANTIC_ANN_TOP_N:-5}"

if [[ "$INPUT_RAW" = /* ]]; then
	INPUT_PATH="$INPUT_RAW"
else
	INPUT_PATH="$ROOT_DIR/$INPUT_RAW"
fi

go run ./cmd/semantic-ann-eval --input "$INPUT_PATH" --top-n "$TOP_N"
