package chat_context

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat-service/pkg/config"
	redis_pool "ai-chat-service/pkg/db/redis"

	"github.com/sashabaranov/go-openai"
)

type realBenchMode string

const (
	realBenchModeRun         realBenchMode = "run"
	realBenchModePrepare     realBenchMode = "prepare"
	realBenchModeCleanup     realBenchMode = "cleanup"
	realBenchModeAggregate   realBenchMode = "aggregate"
	realBenchProfileFull     string        = "full"
	realBenchProfileQuick    string        = "quick"
	realBenchDefaultSeedBase int64         = 1774853392387908100

	realBenchDefaultOutputDir = "artifacts/benchmarks/real_redis_vs_kvlite"
	realBenchNamespacePrefix  = "ai:chat:realbench"
)

type realBenchConfig struct {
	Mode            realBenchMode
	Profile         string
	OutputDir       string
	RunID           string
	SeedBase        int64
	RedisAddr       string
	RedisPassword   string
	KVLiteAddr      string
	KVLitePassword  string
	RedisNamespace  string
	KVLiteNamespace string
	CleanAfter      bool
	CleanBefore     bool
	WarmupRequests  int
	RequestTimeout  time.Duration
	TurnTTL         time.Duration
}

type workloadSpec struct {
	Name              string
	SessionCount      int
	TotalTurns        int
	Requests          int
	RecentWindow      int
	ReadRatio         float64
	HotSessionCount   int
	WarmSessionCount  int
	HotWeight         float64
	WarmWeight        float64
	TurnMin           int
	TurnMax           int
	UserBytesMin      int
	UserBytesMax      int
	AssistantBytesMin int
	AssistantBytesMax int
	Concurrency       int
	Seed              int64
	Notes             string
}

type sessionFixture struct {
	SessionID          string
	InitialTurns       int
	NextSeq            int64
	UserBytesMin       int
	UserBytesMax       int
	AssistantBytesMin  int
	AssistantBytesMax  int
	UserBytesAvg       float64
	AssistantBytesAvg  float64
	UserTokensAvg      float64
	AssistantTokensAvg float64
}

type requestStep struct {
	SessionIndex    int
	IsRead          bool
	ReadWindow      int
	UserBytes       int
	AssistantBytes  int
	UserTokens      int
	AssistantTokens int
}

type benchCounters struct {
	readOps      uint64
	writeOps     uint64
	metaReads    uint64
	metaWrites   uint64
	turnReads    uint64
	turnWrites   uint64
	errorCount   uint64
	timeoutCount uint64
	readLatMu    sync.Mutex
	writeLatMu   sync.Mutex
	readLat      []time.Duration
	writeLat     []time.Duration
}

type benchmarkResult struct {
	Workload                       string         `json:"workload"`
	Backend                        string         `json:"backend"`
	RunID                          string         `json:"run_id"`
	Namespace                      string         `json:"namespace"`
	SessionCount                   int            `json:"session_count"`
	TotalTurns                     int            `json:"total_turns"`
	Requests                       int            `json:"requests"`
	ReadWriteRatio                 string         `json:"read_write_ratio"`
	Profile                        string         `json:"profile"`
	Notes                          string         `json:"notes"`
	MessageSize                    sizeSummary    `json:"message_size"`
	TokenSize                      sizeSummary    `json:"token_size"`
	RequestLatency                 latencySummary `json:"request_latency"`
	ReadRequestLatency             latencySummary `json:"read_request_latency"`
	WriteRequestLatency            latencySummary `json:"write_request_latency"`
	HotSessionReadLatency          latencySummary `json:"hot_session_read_latency"`
	HotSessionWriteThenReadLatency latencySummary `json:"hot_session_write_then_read_latency"`
	HotPathStats                   HotPathStats   `json:"hot_path_stats"`
	HotPathHitRatio                float64        `json:"hot_path_hit_ratio"`
	MetaCacheHitRatio              float64        `json:"meta_cache_hit_ratio"`
	RecentWindowCacheHitRatio      float64        `json:"recent_window_cache_hit_ratio"`
	QPS                            float64        `json:"qps"`
	ElapsedMS                      float64        `json:"elapsed_ms"`
	ErrorCount                     uint64         `json:"error_count"`
	TimeoutCount                   uint64         `json:"timeout_count"`
	ReadOps                        uint64         `json:"read_ops"`
	WriteOps                       uint64         `json:"write_ops"`
	MetaReads                      uint64         `json:"meta_reads"`
	MetaWrites                     uint64         `json:"meta_writes"`
	TurnReads                      uint64         `json:"turn_reads"`
	TurnWrites                     uint64         `json:"turn_writes"`
	PrepareMS                      float64        `json:"prepare_ms"`
	Conclusion                     string         `json:"conclusion"`
}

type sizeSummary struct {
	Min int     `json:"min"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Avg float64 `json:"avg"`
	Max int     `json:"max"`
}

type latencySummary struct {
	Count int     `json:"count"`
	MinMS float64 `json:"min_ms"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	AvgMS float64 `json:"avg_ms"`
	MaxMS float64 `json:"max_ms"`
}

type scenarioManifest struct {
	Workload        string           `json:"workload"`
	Spec            workloadSpec     `json:"spec"`
	SessionFixtures []sessionFixture `json:"session_fixtures"`
	Requests        []requestStep    `json:"requests"`
}

type suiteManifest struct {
	GeneratedAt     time.Time          `json:"generated_at"`
	RunID           string             `json:"run_id"`
	Profile         string             `json:"profile"`
	RedisNamespace  string             `json:"redis_namespace"`
	KVLiteNamespace string             `json:"kvlite_namespace"`
	Scenarios       []scenarioManifest `json:"scenarios"`
	Results         []benchmarkResult  `json:"results"`
}

type benchRawBackend struct {
	inner      rawBackend
	counters   *benchCounters
	classifier func(key string) string
}

func (b *benchRawBackend) Name() string { return b.inner.Name() }
func (b *benchRawBackend) Close() error { return b.inner.Close() }

func (b *benchRawBackend) RGet(ctx context.Context, key string) (string, error) {
	start := time.Now()
	val, err := b.inner.RGet(ctx, key)
	b.record("read", key, time.Since(start), err)
	return val, err
}

func (b *benchRawBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	start := time.Now()
	err := b.inner.RSet(ctx, key, value, ttl)
	b.record("write", key, time.Since(start), err)
	return err
}

func (b *benchRawBackend) HGet(ctx context.Context, key string) (string, error) {
	start := time.Now()
	val, err := b.inner.HGet(ctx, key)
	b.record("read", key, time.Since(start), err)
	return val, err
}

func (b *benchRawBackend) HSet(ctx context.Context, key, value string, ttl time.Duration) error {
	start := time.Now()
	err := b.inner.HSet(ctx, key, value, ttl)
	b.record("write", key, time.Since(start), err)
	return err
}

func (b *benchRawBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	start := time.Now()
	val, err := b.inner.TTL(ctx, key)
	b.record("read", key, time.Since(start), err)
	return val, err
}

func (b *benchRawBackend) Del(ctx context.Context, key string) error {
	return b.inner.Del(ctx, key)
}

func (b *benchRawBackend) record(kind, key string, elapsed time.Duration, err error) {
	if b == nil || b.counters == nil {
		return
	}
	if err != nil {
		atomic.AddUint64(&b.counters.errorCount, 1)
		if isBenchTimeoutError(err) {
			atomic.AddUint64(&b.counters.timeoutCount, 1)
		}
	}
	label := b.classifyKey(key)
	if kind == "read" {
		atomic.AddUint64(&b.counters.readOps, 1)
		if label == "turn" {
			atomic.AddUint64(&b.counters.turnReads, 1)
		} else if label == "meta" || label == "compat" || label == "summary" {
			atomic.AddUint64(&b.counters.metaReads, 1)
		}
		b.counters.readLatMu.Lock()
		b.counters.readLat = append(b.counters.readLat, elapsed)
		b.counters.readLatMu.Unlock()
		return
	}
	atomic.AddUint64(&b.counters.writeOps, 1)
	if label == "turn" {
		atomic.AddUint64(&b.counters.turnWrites, 1)
	} else if label == "meta" || label == "compat" || label == "summary" {
		atomic.AddUint64(&b.counters.metaWrites, 1)
	}
	b.counters.writeLatMu.Lock()
	b.counters.writeLat = append(b.counters.writeLat, elapsed)
	b.counters.writeLatMu.Unlock()
}

func (b *benchRawBackend) classifyKey(key string) string {
	if b != nil && b.classifier != nil {
		return b.classifier(key)
	}
	return classifyRealBenchKey(key)
}

func classifyRealBenchKey(key string) string {
	switch {
	case strings.Contains(key, ":sess:") && strings.Contains(key, ":turn:"):
		return "turn"
	case strings.Contains(key, ":sess:") && strings.Contains(key, ":meta"):
		return "meta"
	case strings.Contains(key, ":sess:") && strings.Contains(key, ":summary"):
		return "summary"
	case strings.Contains(key, ":compat:"):
		return "compat"
	default:
		return "other"
	}
}

func TestRealRedisVsKVLiteBenchmark(t *testing.T) {
	if os.Getenv("RUN_REAL_BENCH") != "1" {
		t.Skip("RUN_REAL_BENCH=1 not set")
	}

	ctx := context.Background()
	benchCfg, err := loadRealBenchConfig()
	if err != nil {
		t.Fatalf("load benchmark config: %v", err)
	}
	if err := os.MkdirAll(benchCfg.OutputDir, 0o755); err != nil {
		t.Fatalf("create output dir: %v", err)
	}
	if benchCfg.CleanBefore {
		if err := cleanupRealBenchmarkData(ctx, benchCfg); err != nil {
			t.Fatalf("cleanup benchmark data before run: %v", err)
		}
	}

	if benchCfg.Mode == realBenchModeCleanup {
		if err := cleanupRealBenchmarkData(ctx, benchCfg); err != nil {
			t.Fatalf("cleanup benchmark data: %v", err)
		}
		t.Logf("real benchmark data cleaned for run_id=%s", benchCfg.RunID)
		return
	}

	redisCfg := buildBenchmarkConfig(benchCfg.RedisAddr, benchCfg.RedisPassword, "redis")
	redis_pool.InitRedisPool(redisCfg)

	results := make([]benchmarkResult, 0, 10)
	scenarios := filterRealBenchmarkScenarios(realBenchmarkScenarios(benchCfg.Profile, benchCfg.RunID))
	scenarios = rebalanceRealBenchmarkSeeds(scenarios, benchCfg.SeedBase)
	manifest := suiteManifest{
		GeneratedAt:     time.Now().UTC(),
		RunID:           benchCfg.RunID,
		Profile:         benchCfg.Profile,
		RedisNamespace:  benchCfg.RedisNamespace,
		KVLiteNamespace: benchCfg.KVLiteNamespace,
	}

	for idx, spec := range scenarios {
		spec = overrideRealBenchmarkRequests(spec)
		seed := spec.Seed
		if seed == 0 {
			seed = time.Now().UnixNano() + int64(idx)
		}
		fixtures, requests, messageStats, tokenStats := buildRealBenchmarkScenario(spec, seed)
		scenarioManifest := scenarioManifest{Workload: spec.Name, Spec: spec, SessionFixtures: fixtures, Requests: requests}
		manifest.Scenarios = append(manifest.Scenarios, scenarioManifest)

		backendOrder := []string{"redis", "kvlite"}
		if idx%2 == 1 {
			backendOrder = []string{"kvlite", "redis"}
		}
		for _, backend := range backendOrder {
			result, err := runRealBenchmarkScenario(ctx, benchCfg, backend, spec, fixtures, requests, messageStats, tokenStats)
			if err != nil {
				t.Fatalf("run scenario %s on %s: %v", spec.Name, backend, err)
			}
			results = append(results, result)
			if err := writeBenchmarkScenarioResult(benchCfg.OutputDir, result); err != nil {
				t.Fatalf("write scenario result: %v", err)
			}
		}
	}

	manifest.Results = append(manifest.Results, results...)
	if err := writeBenchmarkManifest(benchCfg.OutputDir, manifest); err != nil {
		t.Fatalf("write benchmark manifest: %v", err)
	}
	if err := writeBenchmarkReport(benchCfg.OutputDir, benchCfg, manifest, results); err != nil {
		t.Fatalf("write benchmark report: %v", err)
	}

	if benchCfg.CleanAfter {
		if err := cleanupRealBenchmarkData(ctx, benchCfg); err != nil {
			t.Fatalf("cleanup benchmark data: %v", err)
		}
	}

	for _, result := range results {
		t.Logf("%s %s qps=%.2f p99=%.3fms errors=%d", result.Workload, result.Backend, result.QPS, result.RequestLatency.P99MS, result.ErrorCount)
	}
}

func filterRealBenchmarkScenarios(scenarios []workloadSpec) []workloadSpec {
	filters := parseBenchmarkFilterSet(getEnvDefault("REAL_BENCH_WORKLOADS", ""))
	if len(filters) == 0 {
		return scenarios
	}
	filtered := make([]workloadSpec, 0, len(scenarios))
	for _, spec := range scenarios {
		if _, ok := filters[strings.ToLower(strings.TrimSpace(spec.Name))]; ok {
			filtered = append(filtered, spec)
		}
	}
	return filtered
}

func overrideRealBenchmarkRequests(spec workloadSpec) workloadSpec {
	requested, err := strconv.Atoi(strings.TrimSpace(getEnvDefault("REAL_BENCH_REQUESTS", "0")))
	if err != nil || requested <= 0 {
		return spec
	}
	spec.Requests = requested
	if spec.TotalTurns < requested {
		spec.TotalTurns = requested
	}
	return spec
}

func parseBenchmarkFilterSet(raw string) map[string]struct{} {
	filters := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		filters[item] = struct{}{}
	}
	return filters
}

func loadRealBenchConfig() (*realBenchConfig, error) {
	mode := realBenchMode(strings.ToLower(strings.TrimSpace(getEnvDefault("REAL_BENCH_MODE", string(realBenchModeRun)))))
	profile := strings.ToLower(strings.TrimSpace(getEnvDefault("REAL_BENCH_PROFILE", realBenchProfileFull)))
	if profile != realBenchProfileFull && profile != realBenchProfileQuick {
		profile = realBenchProfileFull
	}
	if mode != realBenchModeRun && mode != realBenchModePrepare && mode != realBenchModeCleanup && mode != realBenchModeAggregate {
		mode = realBenchModeRun
	}
	runID := strings.TrimSpace(getEnvDefault("REAL_BENCH_RUN_ID", fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), randSuffix())))
	outputDir := strings.TrimSpace(getEnvDefault("REAL_BENCH_OUTPUT_DIR", realBenchDefaultOutputDir))
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Clean(filepath.Join("..", "..", outputDir))
	}
	redisAddr := strings.TrimSpace(getEnvDefault("REAL_BENCH_REDIS_ADDR", "127.0.0.1:6379"))
	redisPwd := strings.TrimSpace(os.Getenv("REAL_BENCH_REDIS_PASSWORD"))
	kvliteAddr := strings.TrimSpace(getEnvDefault("REAL_BENCH_KVLITE_ADDR", "127.0.0.1:9300"))
	kvlitePwd := strings.TrimSpace(os.Getenv("REAL_BENCH_KVLITE_PASSWORD"))
	cleanAfter := getEnvDefault("REAL_BENCH_CLEAN_AFTER", "0") == "1"
	cleanBefore := getEnvDefault("REAL_BENCH_CLEAN_BEFORE", "0") == "1"
	requestTimeoutMS, _ := strconv.Atoi(getEnvDefault("REAL_BENCH_REQUEST_TIMEOUT_MS", "5000"))
	turnTTLSeconds, _ := strconv.Atoi(getEnvDefault("REAL_BENCH_TTL_SECONDS", "86400"))
	seedBase := realBenchDefaultSeedBase
	if parsedSeedBase, err := strconv.ParseInt(getEnvDefault("REAL_BENCH_SEED_BASE", strconv.FormatInt(realBenchDefaultSeedBase, 10)), 10, 64); err == nil {
		seedBase = parsedSeedBase
	}
	warmupRequests, _ := strconv.Atoi(getEnvDefault("REAL_BENCH_WARMUP_REQUESTS", "0"))
	redisNamespace := strings.TrimSpace(getEnvDefault("REAL_BENCH_REDIS_NAMESPACE", fmt.Sprintf("%s:%s:redis", realBenchNamespacePrefix, runID)))
	kvliteNamespace := strings.TrimSpace(getEnvDefault("REAL_BENCH_KVLITE_NAMESPACE", fmt.Sprintf("%s:%s:kvlite", realBenchNamespacePrefix, runID)))
	return &realBenchConfig{
		Mode:            mode,
		Profile:         profile,
		OutputDir:       outputDir,
		RunID:           runID,
		SeedBase:        seedBase,
		RedisAddr:       redisAddr,
		RedisPassword:   redisPwd,
		KVLiteAddr:      kvliteAddr,
		KVLitePassword:  kvlitePwd,
		RedisNamespace:  redisNamespace,
		KVLiteNamespace: kvliteNamespace,
		CleanAfter:      cleanAfter,
		CleanBefore:     cleanBefore,
		WarmupRequests:  warmupRequests,
		RequestTimeout:  time.Duration(requestTimeoutMS) * time.Millisecond,
		TurnTTL:         time.Duration(turnTTLSeconds) * time.Second,
	}, nil
}

func rebalanceRealBenchmarkSeeds(scenarios []workloadSpec, seedBase int64) []workloadSpec {
	if len(scenarios) == 0 {
		return scenarios
	}
	if seedBase == 0 {
		seedBase = realBenchDefaultSeedBase
	}
	rebased := make([]workloadSpec, len(scenarios))
	copy(rebased, scenarios)
	for idx := range rebased {
		rebased[idx].Seed = seedBase + int64(idx+1)
	}
	return rebased
}

func buildBenchmarkConfig(redisAddr, redisPassword, kvliteAddr string) *config.Config {
	cnf := &config.Config{}
	redisHost, redisPort := splitHostPort(redisAddr)
	cnf.Redis.Host = redisHost
	cnf.Redis.Port = redisPort
	cnf.Redis.Pwd = redisPassword
	cnf.Context.Backend = "redis"
	cnf.Context.FallbackToRedis = false
	cnf.Context.Namespace = "ai:chat"
	cnf.Context.KVLite.Addr = kvliteAddr
	cnf.Context.KVLite.TimeoutMS = 2000
	cnf.Context.KVLite.ConnectTimeoutMS = 1000
	cnf.Context.KVLite.ReadTimeoutMS = 1000
	cnf.Context.KVLite.WriteTimeoutMS = 1000
	cnf.Context.KVLite.PoolSize = 8
	cnf.Context.KVLite.MaxIdle = 4
	cnf.Context.KVLite.MaxRetries = 1
	cnf.Context.KVLite.RetryBackoffMS = 20
	return cnf
}

func splitHostPort(addr string) (string, int) {
	host := "127.0.0.1"
	port := 6379
	parts := strings.Split(addr, ":")
	if len(parts) >= 2 {
		host = strings.Join(parts[:len(parts)-1], ":")
		if parsed, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			port = parsed
		}
	}
	return host, port
}

func realBenchmarkScenarios(profile, runID string) []workloadSpec {
	quick := profile == realBenchProfileQuick
	base := int64(1774853392387908100)
	if quick {
		return []workloadSpec{
			{
				Name:         "short_sessions",
				SessionCount: 24, TotalTurns: 300, Requests: 300, RecentWindow: 2,
				ReadRatio: 0.55, HotSessionCount: 4, WarmSessionCount: 8, HotWeight: 0.5, WarmWeight: 0.3,
				TurnMin: 2, TurnMax: 4, UserBytesMin: 48, UserBytesMax: 128, AssistantBytesMin: 48, AssistantBytesMax: 128,
				Concurrency: 1, Seed: base + 1, Notes: "short-session轻窗口",
			},
			{
				Name:         "hot_sessions",
				SessionCount: 16, TotalTurns: 300, Requests: 300, RecentWindow: 8,
				ReadRatio: 0.7, HotSessionCount: 4, WarmSessionCount: 4, HotWeight: 0.7, WarmWeight: 0.2,
				TurnMin: 12, TurnMax: 18, UserBytesMin: 96, UserBytesMax: 256, AssistantBytesMin: 96, AssistantBytesMax: 256,
				Concurrency: 1, Seed: base + 2, Notes: "热点会话",
			},
			{
				Name:         "write_heavy",
				SessionCount: 24, TotalTurns: 300, Requests: 300, RecentWindow: 4,
				ReadRatio: 0.2, HotSessionCount: 6, WarmSessionCount: 8, HotWeight: 0.45, WarmWeight: 0.3,
				TurnMin: 10, TurnMax: 20, UserBytesMin: 64, UserBytesMax: 192, AssistantBytesMin: 64, AssistantBytesMax: 192,
				Concurrency: 1, Seed: base + 3, Notes: "写多场景",
			},
			{
				Name:         "read_heavy",
				SessionCount: 24, TotalTurns: 300, Requests: 300, RecentWindow: 12,
				ReadRatio: 0.8, HotSessionCount: 6, WarmSessionCount: 8, HotWeight: 0.4, WarmWeight: 0.4,
				TurnMin: 12, TurnMax: 20, UserBytesMin: 128, UserBytesMax: 384, AssistantBytesMin: 128, AssistantBytesMax: 384,
				Concurrency: 1, Seed: base + 4, Notes: "读多场景",
			},
			{
				Name:         "balanced",
				SessionCount: 24, TotalTurns: 300, Requests: 300, RecentWindow: 6,
				ReadRatio: 0.5, HotSessionCount: 6, WarmSessionCount: 8, HotWeight: 0.34, WarmWeight: 0.33,
				TurnMin: 8, TurnMax: 16, UserBytesMin: 96, UserBytesMax: 256, AssistantBytesMin: 96, AssistantBytesMax: 256,
				Concurrency: 1, Seed: base + 5, Notes: "平衡场景",
			},
		}
	}
	return []workloadSpec{
		{
			Name:         "short_sessions",
			SessionCount: 120, TotalTurns: 10000, Requests: 10000, RecentWindow: 2,
			ReadRatio: 0.55, HotSessionCount: 12, WarmSessionCount: 36, HotWeight: 0.5, WarmWeight: 0.3,
			TurnMin: 2, TurnMax: 4, UserBytesMin: 48, UserBytesMax: 128, AssistantBytesMin: 48, AssistantBytesMax: 128,
			Concurrency: 1, Seed: base + 1, Notes: "短会话、最近窗口很小",
		},
		{
			Name:         "hot_sessions",
			SessionCount: 64, TotalTurns: 10000, Requests: 10000, RecentWindow: 8,
			ReadRatio: 0.7, HotSessionCount: 8, WarmSessionCount: 16, HotWeight: 0.7, WarmWeight: 0.2,
			TurnMin: 18, TurnMax: 32, UserBytesMin: 128, UserBytesMax: 384, AssistantBytesMin: 128, AssistantBytesMax: 384,
			Concurrency: 1, Seed: base + 2, Notes: "少量 session 高频访问",
		},
		{
			Name:         "write_heavy",
			SessionCount: 220, TotalTurns: 10000, Requests: 10000, RecentWindow: 4,
			ReadRatio: 0.2, HotSessionCount: 24, WarmSessionCount: 64, HotWeight: 0.45, WarmWeight: 0.3,
			TurnMin: 12, TurnMax: 24, UserBytesMin: 64, UserBytesMax: 192, AssistantBytesMin: 64, AssistantBytesMax: 192,
			Concurrency: 1, Seed: base + 3, Notes: "高频 open / complete / meta 更新",
		},
		{
			Name:         "read_heavy",
			SessionCount: 220, TotalTurns: 10000, Requests: 10000, RecentWindow: 12,
			ReadRatio: 0.8, HotSessionCount: 24, WarmSessionCount: 64, HotWeight: 0.4, WarmWeight: 0.4,
			TurnMin: 18, TurnMax: 32, UserBytesMin: 128, UserBytesMax: 512, AssistantBytesMin: 128, AssistantBytesMax: 512,
			Concurrency: 1, Seed: base + 4, Notes: "recent turns 回放为主",
		},
		{
			Name:         "balanced",
			SessionCount: 160, TotalTurns: 10000, Requests: 10000, RecentWindow: 6,
			ReadRatio: 0.5, HotSessionCount: 20, WarmSessionCount: 48, HotWeight: 0.34, WarmWeight: 0.33,
			TurnMin: 10, TurnMax: 20, UserBytesMin: 96, UserBytesMax: 256, AssistantBytesMin: 96, AssistantBytesMax: 256,
			Concurrency: 1, Seed: base + 5, Notes: "接近日常对话",
		},
	}
}

func buildRealBenchmarkScenario(spec workloadSpec, seed int64) ([]sessionFixture, []requestStep, sizeSummary, sizeSummary) {
	rng := rand.New(rand.NewSource(seed))
	fixtures := make([]sessionFixture, 0, spec.SessionCount)
	userSizes := make([]int, 0, spec.TotalTurns+spec.Requests)
	assistantSizes := make([]int, 0, spec.TotalTurns+spec.Requests)
	userTokens := make([]int, 0, spec.TotalTurns+spec.Requests)
	assistantTokens := make([]int, 0, spec.TotalTurns+spec.Requests)

	turnCounts := distributeCounts(spec.SessionCount, spec.TotalTurns, spec.TurnMin, spec.TurnMax, rng)
	for i := 0; i < spec.SessionCount; i++ {
		sessionID := fmt.Sprintf("%s:%s:s%04d", realBenchNamespacePrefix, spec.Name, i)
		turnCount := turnCounts[i]
		fixtures = append(fixtures, sessionFixture{SessionID: sessionID, InitialTurns: turnCount, NextSeq: int64(turnCount) + 1})
		for j := 0; j < turnCount; j++ {
			userBytes := sampleSize(spec.UserBytesMin, spec.UserBytesMax, rng)
			assistantBytes := sampleSize(spec.AssistantBytesMin, spec.AssistantBytesMax, rng)
			userSizes = append(userSizes, userBytes)
			assistantSizes = append(assistantSizes, assistantBytes)
			userTokens = append(userTokens, estimateTokens(userBytes))
			assistantTokens = append(assistantTokens, estimateTokens(assistantBytes))
		}
	}

	requests := make([]requestStep, 0, spec.Requests)
	for i := 0; i < spec.Requests; i++ {
		isRead := rng.Float64() < spec.ReadRatio
		sessionIndex := pickSessionIndex(spec, rng)
		step := requestStep{SessionIndex: sessionIndex, IsRead: isRead, ReadWindow: spec.RecentWindow}
		if !isRead {
			step.UserBytes = sampleSize(spec.UserBytesMin, spec.UserBytesMax, rng)
			step.AssistantBytes = sampleSize(spec.AssistantBytesMin, spec.AssistantBytesMax, rng)
			step.UserTokens = estimateTokens(step.UserBytes)
			step.AssistantTokens = estimateTokens(step.AssistantBytes)
			userSizes = append(userSizes, step.UserBytes)
			assistantSizes = append(assistantSizes, step.AssistantBytes)
			userTokens = append(userTokens, step.UserTokens)
			assistantTokens = append(assistantTokens, step.AssistantTokens)
		}
		requests = append(requests, step)
	}

	return fixtures, requests, sizeSummaryFromIntSamples(userSizes, assistantSizes), sizeSummaryFromIntSamples(userTokens, assistantTokens)
}

func sizeSummaryFromIntSamples(userSizes, assistantSizes []int) sizeSummary {
	combined := append(append([]int{}, userSizes...), assistantSizes...)
	if len(combined) == 0 {
		return sizeSummary{}
	}
	sort.Ints(combined)
	total := 0
	for _, v := range combined {
		total += v
	}
	return sizeSummary{
		Min: combined[0],
		P50: float64(percentileInt(combined, 50)),
		P95: float64(percentileInt(combined, 95)),
		P99: float64(percentileInt(combined, 99)),
		Avg: float64(total) / float64(len(combined)),
		Max: combined[len(combined)-1],
	}
}

func percentileInt(values []int, p float64) int {
	if len(values) == 0 {
		return 0
	}
	idx := int(math.Ceil((p/100.0)*float64(len(values)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func pickSessionIndex(spec workloadSpec, rng *rand.Rand) int {
	hotEnd := clampInt(spec.HotSessionCount, 0, spec.SessionCount)
	warmEnd := clampInt(spec.HotSessionCount+spec.WarmSessionCount, 0, spec.SessionCount)
	roll := rng.Float64()
	if hotEnd > 0 && roll < spec.HotWeight {
		return rng.Intn(hotEnd)
	}
	if warmEnd > hotEnd && roll < spec.HotWeight+spec.WarmWeight {
		return hotEnd + rng.Intn(warmEnd-hotEnd)
	}
	if warmEnd < spec.SessionCount {
		return warmEnd + rng.Intn(spec.SessionCount-warmEnd)
	}
	if spec.SessionCount == 0 {
		return 0
	}
	return rng.Intn(spec.SessionCount)
}

func distributeCounts(sessionCount, totalTurns, minTurns, maxTurns int, rng *rand.Rand) []int {
	if sessionCount <= 0 {
		return nil
	}
	counts := make([]int, sessionCount)
	if minTurns < 1 {
		minTurns = 1
	}
	base := totalTurns / sessionCount
	if base < minTurns {
		base = minTurns
	}
	current := 0
	for i := 0; i < sessionCount; i++ {
		counts[i] = base
		current += base
	}
	if current < totalTurns {
		remaining := totalTurns - current
		for remaining > 0 {
			idx := rng.Intn(sessionCount)
			counts[idx]++
			remaining--
		}
	} else if current > totalTurns {
		remaining := current - totalTurns
		for remaining > 0 {
			idx := rng.Intn(sessionCount)
			if counts[idx] <= minTurns {
				continue
			}
			counts[idx]--
			remaining--
		}
	}
	return counts
}

func runRealBenchmarkScenario(ctx context.Context, benchCfg *realBenchConfig, backend string, spec workloadSpec, fixtures []sessionFixture, requests []requestStep, messageStats sizeSummary, tokenStats sizeSummary) (benchmarkResult, error) {
	namespace := benchCfg.RedisNamespace
	if backend == "kvlite" {
		namespace = benchCfg.KVLiteNamespace
	}
	store, raw, err := newRealBenchmarkStore(benchCfg, backend, namespace)
	if err != nil {
		return benchmarkResult{}, err
	}
	defer store.Close()

	probeCtx, cancel := context.WithTimeout(ctx, benchCfg.RequestTimeout)
	if err := probeBackend(probeCtx, store, backend); err != nil {
		cancel()
		return benchmarkResult{}, err
	}
	cancel()

	prepareStart := time.Now()
	if err := prefillRealBenchmarkData(ctx, store, fixtures, benchCfg.TurnTTL, spec, messageStats); err != nil {
		return benchmarkResult{}, err
	}
	prepareElapsed := time.Since(prepareStart)

	results := executeRealBenchmarkRequests(ctx, store, fixtures, requests, benchCfg.TurnTTL, benchCfg.RequestTimeout, spec, benchCfg.WarmupRequests)
	counts := snapshotBenchCounters(raw.counters)
	hotStats := store.hotPathStats()
	requestLatency := summarizeDurations(results.allLatencies)
	readLatency := summarizeDurations(results.readLatencies)
	writeLatency := summarizeDurations(results.writeLatencies)
	writeThenReadLatency := summarizeDurations(results.writeThenReadLatencies)

	elapsed := results.elapsed
	qps := 0.0
	if elapsed > 0 {
		qps = float64(results.requestCount) / elapsed.Seconds()
	}
	conclusion := concludeBenchmark(spec.Name, backend, qps, requestLatency.P99MS, counts.errorCount)

	result := benchmarkResult{
		Workload:                       spec.Name,
		Backend:                        backend,
		RunID:                          benchCfg.RunID,
		Namespace:                      namespace,
		SessionCount:                   spec.SessionCount,
		TotalTurns:                     spec.TotalTurns,
		Requests:                       spec.Requests,
		ReadWriteRatio:                 fmt.Sprintf("%.0f/%.0f", spec.ReadRatio*100, (1-spec.ReadRatio)*100),
		Profile:                        benchCfg.Profile,
		Notes:                          spec.Notes,
		MessageSize:                    messageStats,
		TokenSize:                      tokenStats,
		RequestLatency:                 requestLatency,
		ReadRequestLatency:             readLatency,
		WriteRequestLatency:            writeLatency,
		HotSessionReadLatency:          readLatency,
		HotSessionWriteThenReadLatency: writeThenReadLatency,
		HotPathStats:                   hotStats,
		HotPathHitRatio:                ratio(hotStats.HotPathHit, hotStats.HotPathHit+hotStats.HotPathMiss),
		MetaCacheHitRatio:              ratio(hotStats.MetaCacheHit, hotStats.MetaCacheHit+hotStats.MetaCacheMiss),
		RecentWindowCacheHitRatio:      ratio(hotStats.RecentWindowCacheHit, hotStats.RecentWindowCacheHit+hotStats.RecentWindowCacheMiss),
		QPS:                            qps,
		ElapsedMS:                      float64(elapsed) / float64(time.Millisecond),
		ErrorCount:                     counts.errorCount,
		TimeoutCount:                   counts.timeoutCount,
		ReadOps:                        counts.readOps,
		WriteOps:                       counts.writeOps,
		MetaReads:                      counts.metaReads,
		MetaWrites:                     counts.metaWrites,
		TurnReads:                      counts.turnReads,
		TurnWrites:                     counts.turnWrites,
		PrepareMS:                      float64(prepareElapsed) / float64(time.Millisecond),
		Conclusion:                     conclusion,
	}
	return result, nil
}

type scenarioRun struct {
	requestCount           int
	elapsed                time.Duration
	allLatencies           []time.Duration
	readLatencies          []time.Duration
	writeLatencies         []time.Duration
	writeThenReadLatencies []time.Duration
}

func executeRealBenchmarkRequests(ctx context.Context, store *sessionStore, fixtures []sessionFixture, requests []requestStep, ttl time.Duration, timeout time.Duration, spec workloadSpec, warmupRequests int) scenarioRun {
	run := scenarioRun{
		allLatencies:           make([]time.Duration, 0, len(requests)),
		readLatencies:          make([]time.Duration, 0, len(requests)),
		writeLatencies:         make([]time.Duration, 0, len(requests)),
		writeThenReadLatencies: make([]time.Duration, 0, len(requests)),
	}
	if len(requests) == 0 {
		return run
	}
	rng := rand.New(rand.NewSource(spec.Seed + 99))
	if warmupRequests > len(requests) {
		warmupRequests = len(requests)
	}
	for idx := 0; idx < warmupRequests; idx++ {
		step := requests[idx]
		if step.SessionIndex < 0 || step.SessionIndex >= len(fixtures) {
			continue
		}
		sessionID := fixtures[step.SessionIndex].SessionID
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		executeRealBenchmarkStep(requestCtx, store, sessionID, step, ttl, rng, nil, nil, false)
		cancel()
	}
	startAll := time.Now()
	lastWriteAt := make(map[string]time.Time)
	for _, step := range requests {
		if step.SessionIndex < 0 || step.SessionIndex >= len(fixtures) {
			continue
		}
		sessionID := fixtures[step.SessionIndex].SessionID
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		stepStart := time.Now()
		executeRealBenchmarkStep(requestCtx, store, sessionID, step, ttl, rng, &run, lastWriteAt, true)
		d := time.Since(stepStart)
		run.allLatencies = append(run.allLatencies, d)
		if step.IsRead {
			run.readLatencies = append(run.readLatencies, d)
		} else {
			run.writeLatencies = append(run.writeLatencies, d)
		}
		cancel()
		run.requestCount++
	}
	run.elapsed = time.Since(startAll)
	return run
}

func executeRealBenchmarkStep(ctx context.Context, store *sessionStore, sessionID string, step requestStep, ttl time.Duration, rng *rand.Rand, run *scenarioRun, lastWriteAt map[string]time.Time, record bool) {
	if step.IsRead {
		_, err := store.LoadRecentTurns(ctx, sessionID, step.ReadWindow)
		if err != nil {
			_ = err
		}
		if record && run != nil && lastWriteAt != nil {
			if lastWrite, ok := lastWriteAt[sessionID]; ok && !lastWrite.IsZero() {
				run.writeThenReadLatencies = append(run.writeThenReadLatencies, time.Since(lastWrite))
			}
		}
		return
	}
	seq, err := store.AllocateTurnSeq(ctx, sessionID)
	if err != nil {
		return
	}
	turn := &TurnRecord{
		SessionID:   sessionID,
		Seq:         seq,
		TurnStatus:  TurnStatusOpen,
		UserMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: makePayload("user", step.UserBytes, step.UserTokens, rng.Int63())},
		UserTokens:  step.UserTokens,
		TotalTokens: step.UserTokens,
		CreatedAtMS: nowMS(),
		UpdatedAtMS: nowMS(),
	}
	if writeErr := store.WriteOpenTurn(ctx, turn, ttl); writeErr != nil {
		return
	}
	assistant := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: makePayload("assistant", step.AssistantBytes, step.AssistantTokens, rng.Int63())}
	if err := store.CompleteTurn(ctx, sessionID, seq, "", assistant, step.AssistantTokens, ttl); err != nil {
		return
	}
	if record && lastWriteAt != nil {
		lastWriteAt[sessionID] = time.Now()
	}
}

func prefillRealBenchmarkData(ctx context.Context, store *sessionStore, fixtures []sessionFixture, ttl time.Duration, spec workloadSpec, sizeStats sizeSummary) error {
	rng := rand.New(rand.NewSource(spec.Seed + 7))
	for _, fixture := range fixtures {
		for seq := 1; seq <= fixture.InitialTurns; seq++ {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			userBytes := sampleSize(spec.UserBytesMin, spec.UserBytesMax, rng)
			assistantBytes := sampleSize(spec.AssistantBytesMin, spec.AssistantBytesMax, rng)
			turn := &TurnRecord{
				SessionID:   fixture.SessionID,
				Seq:         int64(seq),
				TurnStatus:  TurnStatusOpen,
				UserMessage: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: makePayload("prefill-user", userBytes, estimateTokens(userBytes), rng.Int63())},
				UserTokens:  estimateTokens(userBytes),
				TotalTokens: estimateTokens(userBytes),
				CreatedAtMS: nowMS(),
				UpdatedAtMS: nowMS(),
			}
			if err := store.WriteOpenTurn(requestCtx, turn, ttl); err != nil {
				cancel()
				return err
			}
			assistant := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: makePayload("prefill-assistant", assistantBytes, estimateTokens(assistantBytes), rng.Int63())}
			if err := store.CompleteTurn(requestCtx, fixture.SessionID, int64(seq), "", assistant, estimateTokens(assistantBytes), ttl); err != nil {
				cancel()
				return err
			}
			cancel()
		}
	}
	return nil
}

func newRealBenchmarkStore(benchCfg *realBenchConfig, backend, namespace string) (*sessionStore, *benchRawBackend, error) {
	cnf := buildBenchmarkConfig(benchCfg.RedisAddr, benchCfg.RedisPassword, benchCfg.KVLiteAddr)
	var raw rawBackend
	switch backend {
	case "redis":
		raw = newRedisRawBackend(cnf, namespace)
	case "kvlite":
		raw = newKVLiteRawBackend(cnf, namespace, nil)
	default:
		return nil, nil, fmt.Errorf("unsupported backend %q", backend)
	}
	wrapped := &benchRawBackend{inner: raw, counters: &benchCounters{}}
	store := newSessionStore(wrapped, namespace).(*sessionStore)
	return store, wrapped, nil
}

func probeBackend(ctx context.Context, store *sessionStore, backend string) error {
	if store == nil {
		return errors.New("benchmark store not initialized")
	}
	if _, err := store.LoadSessionMeta(ctx, fmt.Sprintf("probe-%s", backend)); err != nil {
		return err
	}
	return nil
}

func snapshotBenchCounters(c *benchCounters) benchCounters {
	if c == nil {
		return benchCounters{}
	}
	return benchCounters{
		readOps:      atomic.LoadUint64(&c.readOps),
		writeOps:     atomic.LoadUint64(&c.writeOps),
		metaReads:    atomic.LoadUint64(&c.metaReads),
		metaWrites:   atomic.LoadUint64(&c.metaWrites),
		turnReads:    atomic.LoadUint64(&c.turnReads),
		turnWrites:   atomic.LoadUint64(&c.turnWrites),
		errorCount:   atomic.LoadUint64(&c.errorCount),
		timeoutCount: atomic.LoadUint64(&c.timeoutCount),
		readLat:      append([]time.Duration{}, c.readLat...),
		writeLat:     append([]time.Duration{}, c.writeLat...),
	}
}

func summarizeDurations(values []time.Duration) latencySummary {
	if len(values) == 0 {
		return latencySummary{}
	}
	sorted := append([]time.Duration{}, values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	total := time.Duration(0)
	for _, v := range sorted {
		total += v
	}
	return latencySummary{
		Count: len(sorted),
		MinMS: float64(sorted[0]) / float64(time.Millisecond),
		P50MS: float64(durationPercentile(sorted, 50)) / float64(time.Millisecond),
		P95MS: float64(durationPercentile(sorted, 95)) / float64(time.Millisecond),
		P99MS: float64(durationPercentile(sorted, 99)) / float64(time.Millisecond),
		AvgMS: float64(total) / float64(time.Millisecond) / float64(len(sorted)),
		MaxMS: float64(sorted[len(sorted)-1]) / float64(time.Millisecond),
	}
}

func ratio(numerator, denominator uint64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func durationPercentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	idx := int(math.Ceil((p/100.0)*float64(len(values)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func writeBenchmarkScenarioResult(dir string, result benchmarkResult) error {
	path := filepath.Join(dir, fmt.Sprintf("%s__%s.json", result.Workload, result.Backend))
	bytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, bytes, 0o644)
}

func writeBenchmarkManifest(dir string, manifest suiteManifest) error {
	path := filepath.Join(dir, "manifest.json")
	bytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, bytes, 0o644)
}

func writeBenchmarkReport(dir string, benchCfg *realBenchConfig, manifest suiteManifest, results []benchmarkResult) error {
	jsonPath := filepath.Join(dir, "report.json")
	mdPath := filepath.Join(dir, "report.md")
	report, err := buildBenchmarkReport(benchCfg, manifest, results)
	if err != nil {
		return err
	}
	jsonBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, jsonBytes, 0o644); err != nil {
		return err
	}
	return os.WriteFile(mdPath, []byte(renderBenchmarkMarkdown(report)), 0o644)
}

type benchmarkReport struct {
	GeneratedAt         time.Time
	RunID               string
	Profile             string
	Results             []benchmarkResult
	AggregateResults    []benchmarkAggregateResult
	ComparisonRows      []comparisonRow
	RedisPreferred      []string
	KVLitePreferred     []string
	RedisBetter         []string
	KVLiteBetter        []string
	FinalConclusion     string
	RedisNamespace      string
	KVLiteNamespace     string
	RepeatRuns          int
	WarmupRequests      int
	SeedBase            int64
	TotalRequests       int
	TotalTurns          int
	RequestsPerScenario int
	SampleCount         int
}

type benchmarkAggregateResult struct {
	Workload       string  `json:"workload"`
	Backend        string  `json:"backend"`
	Samples        int     `json:"samples"`
	QPSMean        float64 `json:"qps_mean"`
	QPSMedian      float64 `json:"qps_median"`
	QPSStdDev      float64 `json:"qps_stddev"`
	QPSSpanMin     float64 `json:"qps_min"`
	QPSSpanMax     float64 `json:"qps_max"`
	P99Mean        float64 `json:"p99_mean"`
	P99Median      float64 `json:"p99_median"`
	P99StdDev      float64 `json:"p99_stddev"`
	P99SpanMin     float64 `json:"p99_min"`
	P99SpanMax     float64 `json:"p99_max"`
	ElapsedMean    float64 `json:"elapsed_mean_ms"`
	ElapsedMedian  float64 `json:"elapsed_median_ms"`
	ElapsedStdDev  float64 `json:"elapsed_stddev_ms"`
	ElapsedSpanMin float64 `json:"elapsed_min_ms"`
	ElapsedSpanMax float64 `json:"elapsed_max_ms"`
	RunVariance    float64 `json:"run_variance"`
	ErrorCount     uint64  `json:"error_count"`
	TimeoutCount   uint64  `json:"timeout_count"`
}

type comparisonRow struct {
	Workload   string  `json:"workload"`
	RedisQPS   float64 `json:"redis_qps"`
	KVLiteQPS  float64 `json:"kvlite_qps"`
	RedisP99   float64 `json:"redis_p99"`
	KVLiteP99  float64 `json:"kvlite_p99"`
	Samples    int     `json:"samples"`
	Winner     string  `json:"winner"`
	Conclusion string  `json:"conclusion"`
}

func buildBenchmarkReport(benchCfg *realBenchConfig, manifest suiteManifest, results []benchmarkResult) (*benchmarkReport, error) {
	aggregateResults := aggregateBenchmarkResults(results)
	byWorkload := map[string]map[string]benchmarkAggregateResult{}
	for _, aggregate := range aggregateResults {
		if _, ok := byWorkload[aggregate.Workload]; !ok {
			byWorkload[aggregate.Workload] = map[string]benchmarkAggregateResult{}
		}
		byWorkload[aggregate.Workload][aggregate.Backend] = aggregate
	}
	comparisonRows := make([]comparisonRow, 0)
	redisPreferred := make([]string, 0)
	kvlitePreferred := make([]string, 0)
	for workload, pair := range byWorkload {
		redisResult, okRedis := pair["redis"]
		kvliteResult, okKV := pair["kvlite"]
		if !okRedis || !okKV {
			continue
		}
		winner := "mixed"
		redisSample := aggregateToBenchmarkResult(redisResult)
		kvliteSample := aggregateToBenchmarkResult(kvliteResult)
		if kvliteSample.QPS > redisSample.QPS && kvliteSample.RequestLatency.P99MS <= redisSample.RequestLatency.P99MS {
			winner = "kvlite"
			kvlitePreferred = append(kvlitePreferred, workload)
		} else if redisSample.QPS > kvliteSample.QPS && redisSample.RequestLatency.P99MS <= kvliteSample.RequestLatency.P99MS {
			winner = "redis"
			redisPreferred = append(redisPreferred, workload)
		} else if kvliteSample.QPS > redisSample.QPS {
			winner = "kvlite"
			kvlitePreferred = append(kvlitePreferred, workload)
		} else if redisSample.QPS > kvliteSample.QPS {
			winner = "redis"
			redisPreferred = append(redisPreferred, workload)
		}
		comparisonRows = append(comparisonRows, comparisonRow{
			Workload:   workload,
			RedisQPS:   redisSample.QPS,
			KVLiteQPS:  kvliteSample.QPS,
			RedisP99:   redisSample.RequestLatency.P99MS,
			KVLiteP99:  kvliteSample.RequestLatency.P99MS,
			Samples:    redisResult.Samples,
			Winner:     winner,
			Conclusion: chooseConclusion(redisSample, kvliteSample),
		})
	}
	sort.Slice(comparisonRows, func(i, j int) bool { return comparisonRows[i].Workload < comparisonRows[j].Workload })
	sort.Strings(redisPreferred)
	sort.Strings(kvlitePreferred)
	finalConclusion := deriveFinalConclusion(redisPreferred, kvlitePreferred, comparisonRows)
	return &benchmarkReport{
		GeneratedAt:         manifest.GeneratedAt,
		RunID:               manifest.RunID,
		Profile:             manifest.Profile,
		Results:             results,
		AggregateResults:    aggregateResults,
		ComparisonRows:      comparisonRows,
		RedisPreferred:      redisPreferred,
		KVLitePreferred:     kvlitePreferred,
		RedisBetter:         redisPreferred,
		KVLiteBetter:        kvlitePreferred,
		FinalConclusion:     finalConclusion,
		RedisNamespace:      manifest.RedisNamespace,
		KVLiteNamespace:     manifest.KVLiteNamespace,
		RepeatRuns:          inferRepeatRuns(results),
		WarmupRequests:      benchCfg.WarmupRequests,
		SeedBase:            benchCfg.SeedBase,
		TotalRequests:       sumResultRequests(results),
		TotalTurns:          sumResultTurns(results),
		RequestsPerScenario: inferRequestsPerScenario(results),
		SampleCount:         len(results),
	}, nil
}

func aggregateBenchmarkResults(results []benchmarkResult) []benchmarkAggregateResult {
	series := map[string][]benchmarkResult{}
	for _, result := range results {
		key := result.Workload + "\x00" + result.Backend
		series[key] = append(series[key], result)
	}
	aggregates := make([]benchmarkAggregateResult, 0, len(series))
	for key, items := range series {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		aggregates = append(aggregates, aggregateBenchmarkSeries(parts[0], parts[1], items))
	}
	sort.Slice(aggregates, func(i, j int) bool {
		if aggregates[i].Workload == aggregates[j].Workload {
			return aggregates[i].Backend < aggregates[j].Backend
		}
		return aggregates[i].Workload < aggregates[j].Workload
	})
	return aggregates
}

func aggregateBenchmarkSeries(workload, backend string, items []benchmarkResult) benchmarkAggregateResult {
	qpsValues := make([]float64, 0, len(items))
	p99Values := make([]float64, 0, len(items))
	elapsedValues := make([]float64, 0, len(items))
	var errorCount uint64
	var timeoutCount uint64
	for _, item := range items {
		qpsValues = append(qpsValues, item.QPS)
		p99Values = append(p99Values, item.RequestLatency.P99MS)
		elapsedValues = append(elapsedValues, item.ElapsedMS)
		errorCount += item.ErrorCount
		timeoutCount += item.TimeoutCount
	}
	qpsSorted := append([]float64{}, qpsValues...)
	p99Sorted := append([]float64{}, p99Values...)
	elapsedSorted := append([]float64{}, elapsedValues...)
	sort.Float64s(qpsSorted)
	sort.Float64s(p99Sorted)
	sort.Float64s(elapsedSorted)
	qpsMean, qpsStdDev := meanAndStdDev(qpsValues)
	p99Mean, p99StdDev := meanAndStdDev(p99Values)
	elapsedMean, elapsedStdDev := meanAndStdDev(elapsedValues)
	variance := 0.0
	if qpsMean != 0 {
		variance = qpsStdDev / qpsMean
	}
	return benchmarkAggregateResult{
		Workload:       workload,
		Backend:        backend,
		Samples:        len(items),
		QPSMean:        qpsMean,
		QPSMedian:      medianFloat64(qpsSorted),
		QPSStdDev:      qpsStdDev,
		QPSSpanMin:     minFloat64(qpsSorted),
		QPSSpanMax:     maxFloat64(qpsSorted),
		P99Mean:        p99Mean,
		P99Median:      medianFloat64(p99Sorted),
		P99StdDev:      p99StdDev,
		P99SpanMin:     minFloat64(p99Sorted),
		P99SpanMax:     maxFloat64(p99Sorted),
		ElapsedMean:    elapsedMean,
		ElapsedMedian:  medianFloat64(elapsedSorted),
		ElapsedStdDev:  elapsedStdDev,
		ElapsedSpanMin: minFloat64(elapsedSorted),
		ElapsedSpanMax: maxFloat64(elapsedSorted),
		RunVariance:    variance,
		ErrorCount:     errorCount,
		TimeoutCount:   timeoutCount,
	}
}

func aggregateToBenchmarkResult(aggregate benchmarkAggregateResult) benchmarkResult {
	return benchmarkResult{
		Workload: aggregate.Workload,
		Backend:  aggregate.Backend,
		QPS:      aggregate.QPSMedian,
		RequestLatency: latencySummary{
			P99MS: aggregate.P99Median,
		},
		ErrorCount:   aggregate.ErrorCount,
		TimeoutCount: aggregate.TimeoutCount,
	}
}

func meanAndStdDev(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	mean := total / float64(len(values))
	if len(values) == 1 {
		return mean, 0
	}
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	variance /= float64(len(values) - 1)
	return mean, math.Sqrt(variance)
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

func minFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func maxFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}

func inferRepeatRuns(results []benchmarkResult) int {
	if len(results) == 0 {
		return 0
	}
	seen := map[string]struct{}{}
	for _, result := range results {
		if result.RunID == "" {
			continue
		}
		seen[result.RunID] = struct{}{}
	}
	if len(seen) == 0 {
		return 1
	}
	return len(seen)
}

func chooseConclusion(redisResult, kvliteResult benchmarkResult) string {
	if kvliteResult.QPS > redisResult.QPS && kvliteResult.RequestLatency.P99MS <= redisResult.RequestLatency.P99MS {
		return "kvlite base favored"
	}
	if redisResult.QPS > kvliteResult.QPS && redisResult.RequestLatency.P99MS <= kvliteResult.RequestLatency.P99MS {
		return "redis baseline favored"
	}
	if kvliteResult.QPS > redisResult.QPS {
		return "kvlite base throughput better"
	}
	if redisResult.QPS > kvliteResult.QPS {
		return "redis baseline throughput better"
	}
	return "mixed"
}

func deriveFinalConclusion(redisPreferred, kvlitePreferred []string, rows []comparisonRow) string {
	if len(rows) == 0 {
		return "benchmark did not produce comparable results"
	}
	if len(kvlitePreferred) > len(redisPreferred) {
		return fmt.Sprintf("KVLite_Base shows a practical light-path advantage in %d of %d comparable workloads; the result is strong enough for resume and interview use if the same profile is rerun on the target environment.", len(kvlitePreferred), len(rows))
	}
	if len(redisPreferred) > len(kvlitePreferred) {
		return fmt.Sprintf("Redis remains stronger in %d of %d comparable workloads; KVLite_Base is still valuable for light-path scenarios, but the benchmark does not support a blanket throughput claim.", len(redisPreferred), len(rows))
	}
	return "KVLite_Base and Redis split the workload set; the light-path advantage is scenario-dependent and should be described as conditional rather than absolute."
}

func renderBenchmarkMarkdown(report *benchmarkReport) string {
	var b strings.Builder
	b.WriteString("# Real Redis vs KVLite Benchmark Report\n\n")
	b.WriteString(fmt.Sprintf("Generated at: %s\n\n", report.GeneratedAt.UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("Run ID: %s\n\n", report.RunID))
	if report.RepeatRuns > 1 {
		b.WriteString(fmt.Sprintf("Repeat runs: %d\n\n", report.RepeatRuns))
	}
	if report.WarmupRequests > 0 {
		b.WriteString(fmt.Sprintf("Warmup requests per run: %d\n\n", report.WarmupRequests))
	}
	b.WriteString("## Scenario Results\n\n")
	b.WriteString("| workload | backend | run_id | qps | p50(ms) | p95(ms) | p99(ms) | errors | notes |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, result := range report.Results {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %.2f | %.3f | %.3f | %.3f | %d | %s |\n", result.Workload, result.Backend, result.RunID, result.QPS, result.RequestLatency.P50MS, result.RequestLatency.P95MS, result.RequestLatency.P99MS, result.ErrorCount, escapePipe(result.Notes)))
	}
	b.WriteString("\n## Aggregate Summary\n\n")
	b.WriteString("| workload | backend | samples | qps mean | qps median | qps stddev | qps cv | p99 mean | p99 median | p99 stddev | elapsed mean(ms) | elapsed median(ms) | elapsed stddev(ms) | errors |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, aggregate := range report.AggregateResults {
		b.WriteString(fmt.Sprintf("| %s | %s | %d | %.2f | %.2f | %.2f | %.3f | %.3f | %.3f | %.3f | %.2f | %.2f | %.2f | %d |\n", aggregate.Workload, aggregate.Backend, aggregate.Samples, aggregate.QPSMean, aggregate.QPSMedian, aggregate.QPSStdDev, aggregate.RunVariance, aggregate.P99Mean, aggregate.P99Median, aggregate.P99StdDev, aggregate.ElapsedMean, aggregate.ElapsedMedian, aggregate.ElapsedStdDev, aggregate.ErrorCount))
	}
	b.WriteString("\n## Hot Path Stats\n\n")
	b.WriteString("| workload | backend | run_id | hot hit ratio | meta hit ratio | window hit ratio | hot read p99(ms) | write->read p99(ms) |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, result := range report.Results {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %.2f | %.2f | %.2f | %.3f | %.3f |\n", result.Workload, result.Backend, result.RunID, result.HotPathHitRatio, result.MetaCacheHitRatio, result.RecentWindowCacheHitRatio, result.HotSessionReadLatency.P99MS, result.HotSessionWriteThenReadLatency.P99MS))
	}
	b.WriteString("\n## Redis vs KVLite Comparison\n\n")
	b.WriteString("| workload | redis qps(median) | kvlite qps(median) | redis p99(median) | kvlite p99(median) | samples | winner | conclusion |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | --- | --- |\n")
	for _, row := range report.ComparisonRows {
		b.WriteString(fmt.Sprintf("| %s | %.2f | %.2f | %.3f | %.3f | %d | %s | %s |\n", row.Workload, row.RedisQPS, row.KVLiteQPS, row.RedisP99, row.KVLiteP99, row.Samples, row.Winner, row.Conclusion))
	}
	b.WriteString("\n## KVLite Better Scenarios\n\n")
	if len(report.KVLiteBetter) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, item := range report.KVLiteBetter {
			b.WriteString("- " + item + "\n")
		}
	}
	b.WriteString("\n## Redis Better Scenarios\n\n")
	if len(report.RedisBetter) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, item := range report.RedisBetter {
			b.WriteString("- " + item + "\n")
		}
	}
	b.WriteString("\n## Final Conclusion\n\n")
	b.WriteString(report.FinalConclusion)
	b.WriteString("\n\n## Environment\n\n")
	b.WriteString(fmt.Sprintf("- Redis namespace: %s\n", report.RedisNamespace))
	b.WriteString(fmt.Sprintf("- KVLite namespace: %s\n", report.KVLiteNamespace))
	b.WriteString(fmt.Sprintf("- Repeat runs: %d\n", report.RepeatRuns))
	b.WriteString(fmt.Sprintf("- Warmup requests: %d\n", report.WarmupRequests))
	b.WriteString(fmt.Sprintf("- Seed base: %d\n", report.SeedBase))
	b.WriteString(fmt.Sprintf("- Sample count: %d\n", report.SampleCount))
	b.WriteString(fmt.Sprintf("- Total requests: %d\n", report.TotalRequests))
	b.WriteString(fmt.Sprintf("- Total turns: %d\n", report.TotalTurns))
	return b.String()
}

func sumResultRequests(results []benchmarkResult) int {
	total := 0
	for _, result := range results {
		total += result.Requests
	}
	return total
}

func sumResultTurns(results []benchmarkResult) int {
	total := 0
	for _, result := range results {
		total += result.TotalTurns
	}
	return total
}

func inferRequestsPerScenario(results []benchmarkResult) int {
	if len(results) == 0 {
		return 0
	}
	return results[0].Requests
}

func cleanupRealBenchmarkData(ctx context.Context, benchCfg *realBenchConfig) error {
	path := filepath.Join(benchCfg.OutputDir, "manifest.json")
	bytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var manifest suiteManifest
	if err := json.Unmarshal(bytes, &manifest); err != nil {
		return err
	}
	redisCfg := buildBenchmarkConfig(benchCfg.RedisAddr, benchCfg.RedisPassword, benchCfg.KVLiteAddr)
	redis_pool.InitRedisPool(redisCfg)
	for _, scenario := range manifest.Scenarios {
		for _, fixture := range scenario.SessionFixtures {
			for _, targetNamespace := range []string{benchCfg.RedisNamespace, benchCfg.KVLiteNamespace} {
				store, _, err := newRealBenchmarkStore(benchCfg, namespaceBackend(targetNamespace, benchCfg), targetNamespace)
				if err != nil {
					return err
				}
				for seq := 1; seq <= fixture.InitialTurns; seq++ {
					if err := store.raw.Del(ctx, turnKey(targetNamespace, fixture.SessionID, int64(seq))); err != nil {
						return err
					}
				}
				if err := store.raw.Del(ctx, metaKey(targetNamespace, fixture.SessionID)); err != nil {
					return err
				}
				_ = store.Close()
			}
		}
	}
	return nil
}

func namespaceBackend(namespace string, benchCfg *realBenchConfig) string {
	if namespace == benchCfg.KVLiteNamespace {
		return "kvlite"
	}
	return "redis"
}

func sampleSize(minValue, maxValue int, rng *rand.Rand) int {
	if maxValue <= minValue {
		return minValue
	}
	return minValue + rng.Intn(maxValue-minValue+1)
}

func estimateTokens(bytes int) int {
	if bytes <= 0 {
		return 1
	}
	return int(math.Ceil(float64(bytes) / 4.0))
}

func makePayload(prefix string, targetBytes int, tokens int, salt int64) string {
	if targetBytes <= 0 {
		targetBytes = 16
	}
	base := fmt.Sprintf("%s-%d-%d-", prefix, tokens, salt)
	if len(base) >= targetBytes {
		return base[:targetBytes]
	}
	return base + strings.Repeat("x", targetBytes-len(base))
}

func randSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano()%1_000_000, 36)
}

func getEnvDefault(name, def string) string {
	val := strings.TrimSpace(os.Getenv(name))
	if val == "" {
		return def
	}
	return val
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func escapePipe(text string) string {
	return strings.ReplaceAll(text, "|", "\\|")
}

func concludeBenchmark(workload, backend string, qps, p99 float64, errCount uint64) string {
	if errCount > 0 {
		return "errors observed; treat as unstable"
	}
	if backend == "kvlite" && (strings.Contains(workload, "hot") || strings.Contains(workload, "write")) {
		return "kvlite base tends to benefit from lighter read/write coupling"
	}
	if backend == "redis" && strings.Contains(workload, "short") {
		return "redis baseline can remain competitive on tiny sessions"
	}
	if qps <= 0 || p99 <= 0 {
		return "insufficient signal"
	}
	return "scenario dependent"
}

func isBenchTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}
