#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

RUNS="${REAL_BENCH_REPEAT:-5}"
if [[ "$RUNS" -lt 1 ]]; then
	RUNS=1
fi

BASE_DIR_RAW="${REAL_BENCH_OUTPUT_DIR:-artifacts/benchmarks/real_redis_vs_kvlite_targets}"
if [[ "$BASE_DIR_RAW" = /* ]]; then
	BASE_DIR="$BASE_DIR_RAW"
else
	BASE_DIR="$ROOT_DIR/$BASE_DIR_RAW"
fi
SEED_BASE="${REAL_BENCH_SEED_BASE:-1774853392387908100}"
WARMUP_REQUESTS="${REAL_BENCH_WARMUP_REQUESTS:-5000}"
REQUESTS="${REAL_BENCH_REQUESTS:-100000}"
PROFILE="${REAL_BENCH_PROFILE:-quick}"
WORKLOADS="${REAL_BENCH_WORKLOADS:-read_heavy,hot_sessions,write_heavy}"
BASE_RUN_ID="${REAL_BENCH_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"

mkdir -p "$BASE_DIR/runs"

input_dirs=()
for idx in $(seq 1 "$RUNS"); do
	run_suffix=$(printf "%02d" "$idx")
	run_dir="$BASE_DIR/runs/run-$run_suffix"
	input_dirs+=("$run_dir")
	mkdir -p "$run_dir"
	export REAL_BENCH_PROFILE="$PROFILE"
	export REAL_BENCH_WORKLOADS="$WORKLOADS"
	export REAL_BENCH_REQUESTS="$REQUESTS"
	export REAL_BENCH_WARMUP_REQUESTS="$WARMUP_REQUESTS"
	export REAL_BENCH_SEED_BASE="$SEED_BASE"
	export REAL_BENCH_OUTPUT_DIR="$run_dir"
	export REAL_BENCH_RUN_ID="${BASE_RUN_ID}-target-$run_suffix"
	echo "[repeat] run=$idx/$RUNS output=$run_dir"
	bash "$ROOT_DIR/scripts/bench_real_redis_vs_kvlite.sh"
done

export RUN_REAL_BENCH=1
export REAL_BENCH_MODE="aggregate"
export REAL_BENCH_OUTPUT_DIR="$BASE_DIR"
export REAL_BENCH_AGGREGATE_INPUT_DIRS="$(IFS=,; echo "${input_dirs[*]}")"
export REAL_BENCH_RUN_ID="${REAL_BENCH_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}-target-aggregate"

echo "[repeat] aggregating ${#input_dirs[@]} runs into $BASE_DIR"
go test -timeout 30m ./chat-server/chat-context -run TestRealRedisVsKVLiteBenchmarkAggregate -count=1 -v

if [[ -n "${REAL_BENCH_COMPARE_BASELINE_REPORT_DIR:-}" ]]; then
	python3 - "$REAL_BENCH_COMPARE_BASELINE_REPORT_DIR" "$BASE_DIR" <<'PY'
import json
import os
import sys

baseline_dir = sys.argv[1]
target_dir = sys.argv[2]

with open(os.path.join(baseline_dir, "report.json"), "r", encoding="utf-8") as handle:
	baseline = json.load(handle)
with open(os.path.join(target_dir, "report.json"), "r", encoding="utf-8") as handle:
	target = json.load(handle)

baseline_rows = {row["workload"]: row for row in baseline.get("ComparisonRows", [])}
target_rows = {row["workload"]: row for row in target.get("ComparisonRows", [])}

print("[compare] workload | baseline qps | target qps | delta qps | baseline p99 | target p99 | delta p99")
for workload in sorted(set(baseline_rows) | set(target_rows)):
	base = baseline_rows.get(workload)
	cur = target_rows.get(workload)
	if not base or not cur:
		continue
	print(
		f"[compare] {workload} | {base['redis_qps']:.2f}/{base['kvlite_qps']:.2f} | "
		f"{cur['redis_qps']:.2f}/{cur['kvlite_qps']:.2f} | "
		f"{cur['kvlite_qps'] - base['kvlite_qps']:.2f} | "
		f"{base['redis_p99']:.3f}/{base['kvlite_p99']:.3f} | "
		f"{cur['redis_p99']:.3f}/{cur['kvlite_p99']:.3f} | "
		f"{cur['kvlite_p99'] - base['kvlite_p99']:.3f}"
	)
PY
fi