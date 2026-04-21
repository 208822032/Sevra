package qa

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-chat-service/chat-server/llm"
)

func seedSemanticRecordWithVector(t *testing.T, store *KVLiteStore, backend *memoryKVLiteBackend, record *llm.QARecord, vector []float32, model string) string {
	t.Helper()
	if store == nil || backend == nil || record == nil {
		t.Fatalf("invalid semantic seed inputs")
	}
	stored, err := normalizeQARecordForStore(record, time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("normalize semantic record: %v", err)
	}
	if len(vector) == 0 {
		vector = semanticVectorFromText(stored.Question, DefaultSemanticEmbeddingDim)
	}
	if strings.TrimSpace(model) == "" {
		model = "manual-embedding"
	}
	now := time.Now().Unix()
	stored.EmbeddingModel = model
	stored.EmbeddingStatus = EmbeddingStatusReady
	stored.EmbeddingDim = len(vector)
	stored.EmbeddingUpdatedAt = now
	stored.EmbeddingRef = semanticEmbeddingKey(stored.QuestionID)
	stored.SemanticReady = true
	if stored.Metadata == nil {
		stored.Metadata = map[string]string{}
	}
	stored.Metadata["embedding_model"] = model
	stored.Metadata["embedding_status"] = stored.EmbeddingStatus
	stored.Metadata["embedding_dim"] = "4"
	stored.Metadata["embedding_updated_at"] = time.Now().Format(time.RFC3339Nano)
	stored.Metadata["embedding_ref"] = stored.EmbeddingRef
	stored.Metadata["semantic_ready"] = "true"

	mainPayload, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal semantic record: %v", err)
	}
	writeTTL := ttlForWrite(stored, time.Minute, time.Minute)
	if err := backend.RSet(context.Background(), store.recordKey(stored.QuestionID), string(mainPayload), writeTTL); err != nil {
		t.Fatalf("write semantic main record: %v", err)
	}
	embeddingPayload, err := encodeSemanticEmbeddingPayload(semanticEmbeddingPayload{
		Model:     model,
		Provider:  model,
		Dim:       len(vector),
		Vector:    append([]float32(nil), vector...),
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("encode semantic payload: %v", err)
	}
	if err := backend.RSet(context.Background(), store.prefixed(semanticEmbeddingKey(stored.QuestionID)), embeddingPayload, writeTTL); err != nil {
		t.Fatalf("write semantic embedding: %v", err)
	}
	bucketPayload, err := encodeCandidateBucketIDs([]string{stored.QuestionID})
	if err != nil {
		t.Fatalf("encode semantic scope index: %v", err)
	}
	if err := backend.RSet(context.Background(), store.semanticScopeIndexKey(stored.Scope), bucketPayload, writeTTL); err != nil {
		t.Fatalf("write semantic scope index: %v", err)
	}
	return stored.QuestionID
}

func manualShadowIndexForVector(scope string, corpusLimit int, queryVector []float32, records map[string]*llm.QARecord, vectors map[string][]float32) *semanticANNIndex {
	index := newSemanticANNIndex(scope, corpusLimit, newSemanticEmbeddingSignature("", "manual-embedding", len(queryVector)))
	for questionID, record := range records {
		vector := vectors[questionID]
		if record == nil || len(vector) == 0 {
			continue
		}
		index.addRecord(record, vector, firstNonEmpty(record.EmbeddingModel, "manual-embedding"))
	}
	queryBucket := index.bucketKey(queryVector)
	if queryBucket != "" {
		ids := make([]string, 0, len(records))
		for questionID := range records {
			ids = append(ids, questionID)
		}
		index.Buckets[queryBucket] = ids
	}
	return index
}

func TestKVLiteStoreSemanticShadowLookupDiffCases(t *testing.T) {
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := []float32{1, 0, 0, 0}
	mainVector := []float32{1, 0, 0, 0}
	shadowVector := []float32{0.7, 0.7, 0, 0}

	t.Run("same top1", func(t *testing.T) {
		backend := newMemoryKVLiteBackend()
		store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.50)
		record := newCandidateQARecord("same top1 question", "same top1 answer", "deepseek", "deepseek-chat", scope)
		questionID := seedSemanticRecordWithVector(t, store, backend, record, mainVector, "manual-embedding")
		index := manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{questionID: record}, map[string][]float32{questionID: mainVector})
		store.semanticANNCacheMu.Lock()
		store.semanticANNCache[store.semanticANNCacheKey(scope, 16)] = index
		store.semanticANNCacheMu.Unlock()

		result, hit, err := store.lookupBySemanticWithVector(context.Background(), LookupRequest{
			Question:            "same top1 question",
			Provider:            "deepseek",
			Model:               "deepseek-chat",
			Scope:               scope,
			SemanticEnabled:     true,
			SemanticTopK:        2,
			SemanticThreshold:   0.50,
			SemanticCorpusLimit: 16,
			SemanticShadow: SemanticShadowConfig{
				Enabled:        true,
				TopK:           2,
				ScoreThreshold: 0.50,
				SampleRate:     1,
			},
		}, queryVector)
		if err != nil {
			t.Fatalf("same top1 lookup: %v", err)
		}
		if !hit || result.Answer != "same top1 answer" {
			t.Fatalf("unexpected same top1 result: %+v", result)
		}
		if !result.SemanticShadowTrace.ShadowSemanticEnabled || !result.SemanticShadowTrace.ShadowSemanticHit {
			t.Fatalf("expected enabled shadow hit: %+v", result.SemanticShadowTrace)
		}
		if !result.SemanticShadowTrace.ShadowSemanticSameTop1 {
			t.Fatalf("expected same top1 shadow result: %+v", result.SemanticShadowTrace)
		}
		if result.SemanticShadowTrace.ShadowSemanticDiffCategory != "bf_hit_ann_hit_same_top1" {
			t.Fatalf("unexpected diff category: %+v", result.SemanticShadowTrace)
		}
	})

	t.Run("different top1 and brute-force hit ann miss", func(t *testing.T) {
		backend := newMemoryKVLiteBackend()
		store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.50)
		mainRecord := newCandidateQARecord("main question", "main answer", "deepseek", "deepseek-chat", scope)
		shadowRecord := newCandidateQARecord("shadow question", "shadow answer", "deepseek", "deepseek-chat", scope)
		mainID := seedSemanticRecordWithVector(t, store, backend, mainRecord, mainVector, "manual-embedding")
		shadowID := seedSemanticRecordWithVector(t, store, backend, shadowRecord, shadowVector, "manual-embedding")
		combinedScopePayload, err := encodeCandidateBucketIDs([]string{mainID, shadowID})
		if err != nil {
			t.Fatalf("encode combined scope index: %v", err)
		}
		if err := backend.RSet(context.Background(), store.semanticScopeIndexKey(scope), combinedScopePayload, time.Minute); err != nil {
			t.Fatalf("write combined semantic scope index: %v", err)
		}
		index := manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{shadowID: shadowRecord}, map[string][]float32{shadowID: shadowVector})
		store.semanticANNCacheMu.Lock()
		store.semanticANNCache[store.semanticANNCacheKey(scope, 16)] = index
		store.semanticANNCacheMu.Unlock()

		result, hit, err := store.lookupBySemanticWithVector(context.Background(), LookupRequest{
			Question:            "different top1 question",
			Provider:            "deepseek",
			Model:               "deepseek-chat",
			Scope:               scope,
			SemanticEnabled:     true,
			SemanticTopK:        2,
			SemanticThreshold:   0.50,
			SemanticCorpusLimit: 16,
			SemanticShadow: SemanticShadowConfig{
				Enabled:        true,
				TopK:           2,
				ScoreThreshold: 0.80,
				SampleRate:     1,
			},
		}, queryVector)
		if err != nil {
			t.Fatalf("different top1 lookup: %v", err)
		}
		if !hit || result.QuestionID != mainID {
			t.Fatalf("expected main brute-force hit, got %+v", result)
		}
		if result.SemanticShadowTrace.ShadowSemanticHit {
			t.Fatalf("expected ANN miss due to threshold, got %+v", result.SemanticShadowTrace)
		}
		if result.SemanticShadowTrace.ShadowSemanticDiffCategory != "bf_hit_ann_miss" {
			t.Fatalf("unexpected diff category: %+v", result.SemanticShadowTrace)
		}
		if len(result.SemanticShadowTrace.ShadowSemanticTopHits) == 0 || result.SemanticShadowTrace.ShadowSemanticTopHits[0].RecordID != shadowID {
			t.Fatalf("unexpected shadow top hits: %+v", result.SemanticShadowTrace.ShadowSemanticTopHits)
		}
	})

	t.Run("brute-force miss ann hit", func(t *testing.T) {
		backend := newMemoryKVLiteBackend()
		store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.50)
		shadowRecord := newCandidateQARecord("shadow question hit", "shadow hit answer", "deepseek", "deepseek-chat", scope)
		shadowID := seedSemanticRecordWithVector(t, store, backend, shadowRecord, queryVector, "manual-embedding")
		index := manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{shadowID: shadowRecord}, map[string][]float32{shadowID: queryVector})
		store.semanticANNCacheMu.Lock()
		store.semanticANNCache[store.semanticANNCacheKey(scope, 16)] = index
		store.semanticANNCacheMu.Unlock()

		result, hit, err := store.lookupBySemanticWithVector(context.Background(), LookupRequest{
			Question:            "brute force miss ann hit",
			Provider:            "deepseek",
			Model:               "deepseek-chat",
			Scope:               scope,
			SemanticEnabled:     true,
			SemanticTopK:        2,
			SemanticThreshold:   1.01,
			SemanticCorpusLimit: 16,
			SemanticShadow: SemanticShadowConfig{
				Enabled:        true,
				TopK:           2,
				ScoreThreshold: 0.50,
				SampleRate:     1,
			},
		}, queryVector)
		if err != nil {
			t.Fatalf("brute-force miss ann hit lookup: %v", err)
		}
		if hit {
			t.Fatalf("expected brute-force miss, got %+v", result)
		}
		if !result.SemanticShadowTrace.ShadowSemanticHit {
			t.Fatalf("expected ANN hit, got %+v", result.SemanticShadowTrace)
		}
		if result.SemanticShadowTrace.ShadowSemanticDiffCategory != "bf_miss_ann_hit" {
			t.Fatalf("unexpected diff category: %+v", result.SemanticShadowTrace)
		}
		if result.SemanticShadowTrace.ShadowSemanticAnswerID != shadowID {
			t.Fatalf("unexpected shadow answer id: %+v", result.SemanticShadowTrace)
		}
	})
}

func TestKVLiteStoreSemanticShadowLookupIndexMissingFallback(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.50)
	result := LookupResult{
		Hit:        true,
		HitType:    HitTypeSemantic,
		QuestionID: "main-answer-id",
	}
	shadow := store.lookupBySemanticANNShadowWithVector(context.Background(), LookupRequest{
		Question:        "index missing question",
		Provider:        "deepseek",
		Model:           "deepseek-chat",
		Scope:           BuildScope("deepseek", "deepseek-chat", "v1"),
		SemanticEnabled: true,
		SemanticShadow:  SemanticShadowConfig{Enabled: true, TopK: 2, ScoreThreshold: 0.50, SampleRate: 1},
	}, []float32{1, 0, 0, 0}, result)
	if !shadow.ShadowSemanticEnabled {
		t.Fatalf("expected shadow execution to be enabled")
	}
	if !shadow.ShadowSemanticFallbackUsed || !strings.Contains(strings.ToLower(shadow.ShadowSemanticError), "index missing") {
		t.Fatalf("expected missing-index fallback error, got %+v", shadow)
	}
	if shadow.ShadowSemanticDiffCategory != "ann_index_missing" {
		t.Fatalf("unexpected diff category: %+v", shadow)
	}
}
