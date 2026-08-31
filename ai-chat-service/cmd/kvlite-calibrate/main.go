// kvlite-calibrate is a read-only threshold evaluation task. It never writes
// KVLite: reviewed documents must already have been ingested by kvlite-ingest.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-chat-service/chat-server/retrieval/kvlite"
	"ai-chat-service/pkg/config"
)

type dataset struct {
	Version int      `json:"version"`
	Cases   []qaCase `json:"cases"`
}

// ExpectedDocumentID is empty only for a reviewed negative query: it must
// fall back to the LLM rather than return any knowledge-base answer.
type qaCase struct {
	ID                 string `json:"id"`
	Query              string `json:"query"`
	ExpectedDocumentID string `json:"expected_document_id"`
}

type retrievedCase struct {
	qaCase
	Hits []kvlite.Hit
}

type thresholdResult struct {
	Threshold            float64 `json:"threshold"`
	DirectCount          int     `json:"direct_count"`
	CorrectDirectCount   int     `json:"correct_direct_count"`
	IncorrectDirectCount int     `json:"incorrect_direct_count"`
	Precision            float64 `json:"precision"`
	Coverage             float64 `json:"coverage"`
	NegativeFalseRate    float64 `json:"negative_false_direct_rate"`
}

type topKResult struct {
	TopK      int     `json:"top_k"`
	RecallAtK float64 `json:"recall_at_k"`
}

type report struct {
	Cases                 int               `json:"cases"`
	PositiveCases         int               `json:"positive_cases"`
	NegativeCases         int               `json:"negative_cases"`
	TopK                  []topKResult      `json:"top_k"`
	Thresholds            []thresholdResult `json:"thresholds"`
	Recommendation        *float64          `json:"recommended_threshold,omitempty"`
	RecommendationMessage string            `json:"recommendation_message"`
}

func main() {
	configPath := flag.String("config", "dev.config.yaml", "AI-chat configuration path")
	datasetPath := flag.String("dataset", "", "reviewed JSON evaluation dataset path")
	thresholdsRaw := flag.String("thresholds", "0.70,0.75,0.80,0.85,0.88,0.90,0.92,0.94,0.96", "ascending score thresholds")
	topKRaw := flag.String("top-k", "1,3,5", "Recall@K values to evaluate")
	minCases := flag.Int("min-cases", 100, "minimum labeled cases required to emit a recommendation")
	minPrecision := flag.Float64("min-precision", 0.99, "minimum direct-answer precision")
	maxNegativeFalseRate := flag.Float64("max-negative-false-rate", 0.005, "maximum negative-query false-direct rate")
	flag.Parse()
	if strings.TrimSpace(*datasetPath) == "" {
		log.Fatal("-dataset is required")
	}

	thresholds, err := parseFloatList(*thresholdsRaw, -1, 1)
	if err != nil {
		log.Fatal(err)
	}
	topK, err := parseIntList(*topKRaw, 1, 1024)
	if err != nil {
		log.Fatal(err)
	}
	data, err := loadDataset(*datasetPath)
	if err != nil {
		log.Fatal(err)
	}
	cnf, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if !cnf.Retrieval.KVLite.Enabled {
		log.Fatal("retrieval.kvlite.enabled must be true")
	}

	retrieval := cnf.Retrieval.KVLite
	clientConfig := kvlite.DefaultConfig(retrieval.Address)
	timeout := time.Duration(retrieval.TimeoutMS) * time.Millisecond
	clientConfig.ConnectTimeout, clientConfig.ReadTimeout, clientConfig.WriteTimeout = timeout, timeout, timeout
	clientConfig.PoolSize, clientConfig.MaxIdle = retrieval.PoolSize, retrieval.MaxIdle
	store, err := kvlite.NewStore(clientConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := store.Health(ctx); err != nil {
		log.Fatalf("KVLite vector health check failed: %v", err)
	}
	scope := kvlite.LogicalScope{Tenant: retrieval.Scope.Tenant, KnowledgeBase: retrieval.Scope.KnowledgeBase, CorpusVersion: retrieval.Scope.CorpusVersion, Provider: retrieval.Scope.Provider, Model: retrieval.Scope.Model, PromptVersion: retrieval.Scope.PromptVersion}
	maxTopK := topK[len(topK)-1]
	retrieved := make([]retrievedCase, 0, len(data.Cases))
	for _, item := range data.Cases {
		queryCtx, queryCancel := context.WithTimeout(context.Background(), timeout)
		hits, err := store.Search(queryCtx, item.Query, scope, maxTopK, -1)
		queryCancel()
		if err != nil {
			log.Fatalf("query %q: %v", item.ID, err)
		}
		retrieved = append(retrieved, retrievedCase{qaCase: item, Hits: hits})
	}
	result := evaluate(retrieved, topK, thresholds, *minCases, *minPrecision, *maxNegativeFalseRate)
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}

func loadDataset(path string) (dataset, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return dataset{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var data dataset
	if err := decoder.Decode(&data); err != nil {
		return dataset{}, fmt.Errorf("decode dataset: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return dataset{}, err
	}
	if data.Version != 1 || len(data.Cases) == 0 {
		return dataset{}, fmt.Errorf("dataset requires version=1 and non-empty cases")
	}
	seen := make(map[string]struct{}, len(data.Cases))
	for index := range data.Cases {
		item := &data.Cases[index]
		item.ID, item.Query, item.ExpectedDocumentID = strings.TrimSpace(item.ID), strings.TrimSpace(item.Query), strings.TrimSpace(item.ExpectedDocumentID)
		if item.ID == "" || item.Query == "" {
			return dataset{}, fmt.Errorf("case %d requires id and query", index)
		}
		if _, exists := seen[item.ID]; exists {
			return dataset{}, fmt.Errorf("duplicate case id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	return data, nil
}

func evaluate(cases []retrievedCase, topK []int, thresholds []float64, minCases int, minPrecision, maxNegativeFalseRate float64) report {
	result := report{Cases: len(cases)}
	for _, item := range cases {
		if item.ExpectedDocumentID == "" {
			result.NegativeCases++
		} else {
			result.PositiveCases++
		}
	}
	for _, k := range topK {
		found := 0
		for _, item := range cases {
			if item.ExpectedDocumentID == "" {
				continue
			}
			for _, hit := range item.Hits[:min(k, len(item.Hits))] {
				if hit.Answer.DocumentID == item.ExpectedDocumentID {
					found++
					break
				}
			}
		}
		recall := 0.0
		if result.PositiveCases > 0 {
			recall = float64(found) / float64(result.PositiveCases)
		}
		result.TopK = append(result.TopK, topKResult{TopK: k, RecallAtK: recall})
	}
	for _, threshold := range thresholds {
		metric := thresholdResult{Threshold: threshold}
		negativeFalse := 0
		for _, item := range cases {
			if len(item.Hits) == 0 || item.Hits[0].Score < threshold {
				continue
			}
			metric.DirectCount++
			if item.ExpectedDocumentID != "" && item.Hits[0].Answer.DocumentID == item.ExpectedDocumentID {
				metric.CorrectDirectCount++
			} else {
				metric.IncorrectDirectCount++
				if item.ExpectedDocumentID == "" {
					negativeFalse++
				}
			}
		}
		if metric.DirectCount > 0 {
			metric.Precision = float64(metric.CorrectDirectCount) / float64(metric.DirectCount)
		}
		if result.Cases > 0 {
			metric.Coverage = float64(metric.DirectCount) / float64(result.Cases)
		}
		if result.NegativeCases > 0 {
			metric.NegativeFalseRate = float64(negativeFalse) / float64(result.NegativeCases)
		}
		result.Thresholds = append(result.Thresholds, metric)
	}
	if result.Cases < minCases {
		result.RecommendationMessage = fmt.Sprintf("no recommendation: %d labeled cases is below min-cases=%d", result.Cases, minCases)
		return result
	}
	for _, metric := range result.Thresholds {
		if metric.DirectCount > 0 && metric.Precision >= minPrecision && metric.NegativeFalseRate <= maxNegativeFalseRate {
			value := metric.Threshold
			result.Recommendation = &value
			result.RecommendationMessage = "lowest threshold meeting direct precision and negative false-direct constraints"
			return result
		}
	}
	result.RecommendationMessage = "no evaluated threshold meets the requested quality constraints"
	return result
}

func parseFloatList(raw string, minValue, maxValue float64) ([]float64, error) {
	values := make([]float64, 0)
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || value < minValue || value > maxValue {
			return nil, fmt.Errorf("invalid threshold list %q", raw)
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("empty threshold list")
	}
	sort.Float64s(values)
	return values, nil
}

func parseIntList(raw string, minValue, maxValue int) ([]int, error) {
	values := make([]int, 0)
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < minValue || value > maxValue {
			return nil, fmt.Errorf("invalid top-k list %q", raw)
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("empty top-k list")
	}
	sort.Ints(values)
	return values, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("dataset contains multiple JSON values")
		}
		return fmt.Errorf("read dataset tail: %w", err)
	}
	return nil
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
