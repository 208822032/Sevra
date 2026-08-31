// kvlite-benchmark exercises the AI-chat KVLiteVectorStore path with a
// disposable scope. It is a verification tool, not an ingestion path.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-chat-service/chat-server/retrieval/kvlite"
	"ai-chat-service/pkg/config"
)

type fixtureRow struct {
	Question string
	Answer   string
}

type queryCase struct {
	question   string
	expectedID string
}

type latencyReport struct {
	P50US int64 `json:"p50_us"`
	P95US int64 `json:"p95_us"`
	P99US int64 `json:"p99_us"`
}

type report struct {
	Dataset struct {
		Path      string `json:"path"`
		Kind      string `json:"kind"`
		Documents int    `json:"documents"`
		Queries   int    `json:"queries"`
	} `json:"dataset"`
	Model              string        `json:"model"`
	Dimension          int           `json:"dimension"`
	IndexType          string        `json:"index_type"`
	Metric             string        `json:"metric"`
	TopK               int           `json:"top_k"`
	Threshold          float64       `json:"threshold"`
	Concurrency        int           `json:"concurrency"`
	ElapsedMS          int64         `json:"elapsed_ms"`
	QPS                float64       `json:"qps"`
	Latency            latencyReport `json:"latency_us"`
	RecallAt1          float64       `json:"recall_at_1"`
	Errors             int           `json:"errors"`
	Scope              string        `json:"scope"`
	CleanupCompleted   bool          `json:"cleanup_completed"`
	EmbeddingCacheNote string        `json:"embedding_cache_note"`
}

func main() {
	var (
		configPath  = flag.String("config", "dev.config.yaml", "AI-chat configuration file")
		datasetPath = flag.String("dataset", "", "tab-separated Chinese QA fixture: question<TAB>answer")
		documents   = flag.Int("documents", 100, "number of unique-answer documents to seed")
		queries     = flag.Int("queries", 500, "number of fixture questions to search")
		concurrency = flag.Int("concurrency", 4, "simultaneous VSEARCH requests")
		output      = flag.String("output", "", "path for the JSON report (required)")
	)
	flag.Parse()
	if *datasetPath == "" || *output == "" || *documents <= 0 || *queries <= 0 || *concurrency <= 0 {
		fatalf("-dataset, -output, and positive -documents/-queries/-concurrency are required")
	}

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}
	rows, err := loadFixture(*datasetPath)
	if err != nil {
		fatalf("load fixture: %v", err)
	}
	docs, cases, err := selectCases(rows, *documents, *queries)
	if err != nil {
		fatalf("select fixture cases: %v", err)
	}

	retrieval := cfg.Retrieval.KVLite
	clientConfig := kvlite.DefaultConfig(retrieval.Address)
	timeout := time.Duration(retrieval.TimeoutMS) * time.Millisecond
	clientConfig.ConnectTimeout, clientConfig.ReadTimeout, clientConfig.WriteTimeout = timeout, timeout, timeout
	clientConfig.PoolSize, clientConfig.MaxIdle = *concurrency, *concurrency
	store, err := kvlite.NewStore(clientConfig)
	if err != nil {
		fatalf("create KVLite client: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := store.Health(ctx); err != nil {
		fatalf("KVLite health: %v", err)
	}

	scope := fmt.Sprintf("run-%d", time.Now().UnixNano())
	seeded := make([]seedDocument, 0, len(docs))
	cleanupCompleted := false
	defer func() {
		if cleanupCompleted {
			return
		}
		cleanupCompleted = cleanup(ctx, store, seeded, scope)
		if !cleanupCompleted {
			fmt.Fprintln(os.Stderr, "WARNING: benchmark scope cleanup was incomplete; use the scope printed in the report to remove it")
		}
	}()

	for _, doc := range docs {
		if err := store.Upsert(ctx, kvlite.Document{
			Question: doc.question,
			Scope:    benchmarkScope(scope),
			Answer:   kvlite.AnswerEnvelope{Version: kvlite.AnswerEnvelopeVersion, DocumentID: doc.id, Answer: doc.answer, Source: "benchmark_fixture", CreatedAt: time.Now().Unix()},
		}); err != nil {
			_ = cleanup(ctx, store, seeded, scope)
			fatalf("seed %q: %v", doc.id, err)
		}
		seeded = append(seeded, doc)
	}

	latencies, correct, failures, elapsed := runQueries(ctx, store, cases, scope, *concurrency)
	report := report{
		Model:              "bge-small-zh-v1.5 (server configuration)",
		Dimension:          512,
		IndexType:          "core_hnsw_v2",
		Metric:             "ip",
		TopK:               1,
		Threshold:          -1,
		Concurrency:        *concurrency,
		ElapsedMS:          elapsed.Milliseconds(),
		RecallAt1:          float64(correct) / float64(len(cases)),
		Errors:             failures,
		Scope:              scope,
		EmbeddingCacheNote: "KVLite embedding cache remains enabled by server configuration; benchmark measures end-to-end VSEARCH through the AI-chat RESP client.",
	}
	report.Dataset.Path = *datasetPath
	report.Dataset.Kind = "synthetic Chinese QA fixture; only a client-path smoke/throughput signal, not production quality calibration"
	report.Dataset.Documents = len(docs)
	report.Dataset.Queries = len(cases)
	if elapsed > 0 {
		report.QPS = float64(len(cases)) / elapsed.Seconds()
	}
	report.Latency = summarize(latencies)

	// Cleanup occurs before writing so the report truthfully records its result.
	cleanupCompleted = cleanup(ctx, store, seeded, scope)
	report.CleanupCompleted = cleanupCompleted
	if err := writeReport(*output, report); err != nil {
		fatalf("write report: %v", err)
	}
	if !cleanupCompleted {
		fatalf("benchmark completed but cleanup failed; report=%s", *output)
	}
	if failures > 0 {
		fatalf("benchmark had %d search failures; report=%s", failures, *output)
	}
	fmt.Printf("report=%s documents=%d queries=%d recall@1=%.4f qps=%.2f p50/p95/p99=%d/%d/%d us\n",
		*output, len(docs), len(cases), report.RecallAt1, report.QPS, report.Latency.P50US, report.Latency.P95US, report.Latency.P99US)
}

type seedDocument struct {
	id, question, answer string
}

func loadFixture(path string) ([]fixtureRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []fixtureRow
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1024*1024)
	for s.Scan() {
		parts := strings.SplitN(s.Text(), "\t", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			continue
		}
		rows = append(rows, fixtureRow{Question: parts[0], Answer: parts[1]})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("fixture contains no question<TAB>answer rows")
	}
	return rows, nil
}

func selectCases(rows []fixtureRow, documentLimit, queryLimit int) ([]seedDocument, []queryCase, error) {
	byAnswer := make(map[string]seedDocument)
	var docs []seedDocument
	for _, row := range rows {
		if _, exists := byAnswer[row.Answer]; exists {
			continue
		}
		doc := seedDocument{id: fmt.Sprintf("fixture-%05d", len(docs)+1), question: row.Question, answer: row.Answer}
		byAnswer[row.Answer] = doc
		docs = append(docs, doc)
		if len(docs) == documentLimit {
			break
		}
	}
	var cases []queryCase
	for _, row := range rows {
		if doc, ok := byAnswer[row.Answer]; ok {
			cases = append(cases, queryCase{question: row.Question, expectedID: doc.id})
			if len(cases) == queryLimit {
				break
			}
		}
	}
	if len(docs) == 0 || len(cases) == 0 {
		return nil, nil, errors.New("fixture cannot produce benchmark documents and queries")
	}
	return docs, cases, nil
}

func runQueries(ctx context.Context, store kvlite.Store, cases []queryCase, scope string, concurrency int) ([]time.Duration, int, int, time.Duration) {
	type result struct {
		latency time.Duration
		correct bool
		err     error
	}
	jobs := make(chan queryCase)
	results := make(chan result, len(cases))
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				started := time.Now()
				hits, err := store.Search(ctx, item.question, benchmarkScope(scope), 1, -1)
				result := result{latency: time.Since(started), err: err}
				if err == nil && len(hits) == 1 && hits[0].Answer.DocumentID == item.expectedID {
					result.correct = true
				}
				results <- result
			}
		}()
	}
	started := time.Now()
	go func() {
		for _, item := range cases {
			jobs <- item
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	latencies := make([]time.Duration, 0, len(cases))
	correct, failures := 0, 0
	for result := range results {
		latencies = append(latencies, result.latency)
		if result.correct {
			correct++
		}
		if result.err != nil {
			failures++
		}
	}
	return latencies, correct, failures, time.Since(started)
}

func summarize(latencies []time.Duration) latencyReport {
	if len(latencies) == 0 {
		return latencyReport{}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pick := func(p float64) int64 {
		index := int(math.Ceil(p*float64(len(latencies)))) - 1
		return latencies[index].Microseconds()
	}
	return latencyReport{P50US: pick(.50), P95US: pick(.95), P99US: pick(.99)}
}

func cleanup(ctx context.Context, store kvlite.Store, docs []seedDocument, scope string) bool {
	ok := true
	for _, doc := range docs {
		if err := store.Delete(ctx, doc.question, benchmarkScope(scope)); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup %q: %v\n", doc.id, err)
			ok = false
		}
	}
	return ok
}

func benchmarkScope(corpusVersion string) kvlite.LogicalScope {
	return kvlite.LogicalScope{
		Tenant:        "stage6-benchmark",
		KnowledgeBase: "qa-zh-fixture",
		CorpusVersion: corpusVersion,
	}
}

func writeReport(path string, value report) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0644)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "kvlite-benchmark: "+format+"\n", args...)
	os.Exit(1)
}
