package chat_context

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
)

func TestRealBenchmarkScenarioDeterministicWithSeed(t *testing.T) {
	spec := workloadSpec{
		Name:              "read_heavy",
		SessionCount:      6,
		TotalTurns:        24,
		Requests:          24,
		RecentWindow:      4,
		ReadRatio:         0.75,
		HotSessionCount:   2,
		WarmSessionCount:  2,
		HotWeight:         0.6,
		WarmWeight:        0.25,
		TurnMin:           2,
		TurnMax:           5,
		UserBytesMin:      48,
		UserBytesMax:      96,
		AssistantBytesMin: 48,
		AssistantBytesMax: 96,
	}
	fixturesA, requestsA, messageA, tokenA := buildRealBenchmarkScenario(spec, 123456789)
	fixturesB, requestsB, messageB, tokenB := buildRealBenchmarkScenario(spec, 123456789)
	if !reflect.DeepEqual(fixturesA, fixturesB) {
		t.Fatalf("fixtures should be deterministic with fixed seed")
	}
	if !reflect.DeepEqual(requestsA, requestsB) {
		t.Fatalf("requests should be deterministic with fixed seed")
	}
	if messageA != messageB || tokenA != tokenB {
		t.Fatalf("size summaries should be deterministic: %+v %+v %+v %+v", messageA, messageB, tokenA, tokenB)
	}
}

func TestBenchmarkAggregationUsesMedianAndStdDev(t *testing.T) {
	results := []benchmarkResult{
		{Workload: "read_heavy", Backend: "redis", RunID: "run-1", QPS: 100, RequestLatency: latencySummary{P99MS: 10}, ElapsedMS: 1000, Requests: 1000, TotalTurns: 1000},
		{Workload: "read_heavy", Backend: "redis", RunID: "run-2", QPS: 120, RequestLatency: latencySummary{P99MS: 12}, ElapsedMS: 900, Requests: 1000, TotalTurns: 1000},
		{Workload: "read_heavy", Backend: "kvlite", RunID: "run-1", QPS: 130, RequestLatency: latencySummary{P99MS: 9}, ElapsedMS: 820, Requests: 1000, TotalTurns: 1000},
		{Workload: "read_heavy", Backend: "kvlite", RunID: "run-2", QPS: 150, RequestLatency: latencySummary{P99MS: 8}, ElapsedMS: 760, Requests: 1000, TotalTurns: 1000},
	}
	aggregates := aggregateBenchmarkResults(results)
	if len(aggregates) != 2 {
		t.Fatalf("expected 2 aggregate rows, got %d", len(aggregates))
	}
	var redisAgg benchmarkAggregateResult
	for _, aggregate := range aggregates {
		if aggregate.Backend == "redis" {
			redisAgg = aggregate
			break
		}
	}
	if redisAgg.Backend != "redis" {
		t.Fatalf("expected redis aggregate row, got %+v", redisAgg)
	}
	if redisAgg.Samples != 2 {
		t.Fatalf("expected 2 samples, got %+v", redisAgg)
	}
	if math.Abs(redisAgg.QPSMedian-110) > 0.001 {
		t.Fatalf("unexpected qps median: %+v", redisAgg)
	}
	if math.Abs(redisAgg.QPSMean-110) > 0.001 {
		t.Fatalf("unexpected qps mean: %+v", redisAgg)
	}
	if redisAgg.QPSStdDev <= 0 {
		t.Fatalf("expected qps stddev to be positive: %+v", redisAgg)
	}
	report, err := buildBenchmarkReport(&realBenchConfig{Profile: "full", SeedBase: 42}, suiteManifest{GeneratedAt: time.Now().UTC(), RunID: "agg", Profile: "full"}, results)
	if err != nil {
		t.Fatalf("build benchmark report: %v", err)
	}
	if len(report.AggregateResults) != 2 {
		t.Fatalf("expected aggregate results in report: %+v", report.AggregateResults)
	}
	if len(report.ComparisonRows) != 1 {
		t.Fatalf("expected one comparison row, got %+v", report.ComparisonRows)
	}
	if report.ComparisonRows[0].Winner != "kvlite" {
		t.Fatalf("expected kvlite to win on median comparison: %+v", report.ComparisonRows[0])
	}
}

func TestWritePathReusesHotMetaCache(t *testing.T) {
	store := newSessionStore(newMemoryRawBackend("memory"), "ai:chat").(*sessionStore)
	sessionID := "sess-write-cache"
	turn1 := buildCompletedTurn(t, store, sessionID, "msg-1", "pid-0", "hello", "world", 10*time.Second)
	if _, err := store.LoadSessionMeta(context.Background(), sessionID); err != nil {
		t.Fatalf("warm session meta: %v", err)
	}
	baseline := store.hotPathStats()
	seq, err := store.AllocateTurnSeq(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("allocate turn seq from cache: %v", err)
	}
	if seq != turn1.Seq+1 {
		t.Fatalf("unexpected next seq: got %d want %d", seq, turn1.Seq+1)
	}
	turn := &TurnRecord{
		SessionID:        sessionID,
		RequestMessageID: "msg-2",
		ParentMessageID:  "assistant-msg-1",
		Seq:              seq,
		TurnStatus:       TurnStatusOpen,
		UserMessage: openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "new question",
		},
		UserTokens:  3,
		TotalTokens: 3,
	}
	if err := store.WriteOpenTurn(context.Background(), turn, 10*time.Second); err != nil {
		t.Fatalf("write open turn: %v", err)
	}
	assistantMsg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "new answer"}
	if err := store.CompleteTurn(context.Background(), sessionID, seq, "assistant-msg-2", assistantMsg, 5, 10*time.Second); err != nil {
		t.Fatalf("complete turn: %v", err)
	}
	after := store.hotPathStats()
	if after.MetaCacheHit <= baseline.MetaCacheHit {
		t.Fatalf("expected meta cache hit to increase on write path: before=%+v after=%+v", baseline, after)
	}
	meta, err := store.LoadSessionMeta(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load session meta after write: %v", err)
	}
	if meta == nil || meta.LastSeq != seq || meta.TurnCount != 2 {
		t.Fatalf("write path should keep meta consistent: %+v", meta)
	}
}

func TestRealRedisVsKVLiteBenchmarkAggregate(t *testing.T) {
	if os.Getenv("RUN_REAL_BENCH") != "1" {
		t.Skip("RUN_REAL_BENCH=1 not set")
	}
	if strings.ToLower(strings.TrimSpace(os.Getenv("REAL_BENCH_MODE"))) != string(realBenchModeAggregate) {
		t.Skip("REAL_BENCH_MODE=aggregate not set")
	}
	inputDirs := strings.Split(strings.TrimSpace(os.Getenv("REAL_BENCH_AGGREGATE_INPUT_DIRS")), ",")
	filtered := make([]string, 0, len(inputDirs))
	for _, dir := range inputDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		filtered = append(filtered, dir)
	}
	if len(filtered) == 0 {
		t.Skip("REAL_BENCH_AGGREGATE_INPUT_DIRS not set")
	}
	reports := make([]benchmarkReport, 0, len(filtered))
	for _, dir := range filtered {
		report, err := loadBenchmarkReport(filepath.Join(dir, "report.json"))
		if err != nil {
			t.Fatalf("load benchmark report from %s: %v", dir, err)
		}
		reports = append(reports, report)
	}
	combinedResults := make([]benchmarkResult, 0)
	for _, report := range reports {
		combinedResults = append(combinedResults, report.Results...)
	}
	benchCfg := &realBenchConfig{
		Profile:         reports[0].Profile,
		OutputDir:       getEnvDefault("REAL_BENCH_OUTPUT_DIR", realBenchDefaultOutputDir),
		RunID:           getEnvDefault("REAL_BENCH_RUN_ID", reports[0].RunID),
		SeedBase:        reports[0].SeedBase,
		WarmupRequests:  reports[0].WarmupRequests,
		RedisNamespace:  reports[0].RedisNamespace,
		KVLiteNamespace: reports[0].KVLiteNamespace,
	}
	if err := os.MkdirAll(benchCfg.OutputDir, 0o755); err != nil {
		t.Fatalf("create aggregate output dir: %v", err)
	}
	manifest := suiteManifest{
		GeneratedAt:     time.Now().UTC(),
		RunID:           benchCfg.RunID,
		Profile:         benchCfg.Profile,
		RedisNamespace:  "aggregate:redis",
		KVLiteNamespace: "aggregate:kvlite",
	}
	if err := writeBenchmarkReport(benchCfg.OutputDir, benchCfg, manifest, combinedResults); err != nil {
		t.Fatalf("write aggregate benchmark report: %v", err)
	}
}

func loadBenchmarkReport(path string) (benchmarkReport, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return benchmarkReport{}, err
	}
	var report benchmarkReport
	if err := json.Unmarshal(bytes, &report); err != nil {
		return benchmarkReport{}, err
	}
	return report, nil
}
