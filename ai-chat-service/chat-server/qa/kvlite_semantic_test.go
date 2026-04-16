package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-chat-service/chat-server/llm"
)

type failingEmbeddingProvider struct {
	name string
	err  error
}

func (p *failingEmbeddingProvider) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (p *failingEmbeddingProvider) Capabilities() llm.CapabilitySet {
	return llm.CapabilitySet{SupportsEmbeddings: true}
}

func (p *failingEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p == nil {
		return nil, errors.New("embedding provider is nil")
	}
	if p.err == nil {
		return nil, errors.New("embedding provider failure")
	}
	return nil, p.err
}

func newSemanticTestStore(tb testing.TB, backend *memoryKVLiteBackend, enabled bool, model string, dim int, threshold float64) *KVLiteStore {
	tb.Helper()
	storeIface := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute,
		WithKVLiteSemanticConfig(SemanticConfig{
			Enabled:        enabled,
			TopK:           3,
			Threshold:      threshold,
			CorpusLimit:    16,
			EmbeddingModel: model,
			EmbeddingDim:   dim,
		}),
		WithKVLiteSemanticProvider(NewHashEmbeddingProvider(model, dim)),
	)
	store, ok := storeIface.(*KVLiteStore)
	if !ok {
		tb.Fatalf("expected *KVLiteStore, got %T", storeIface)
	}
	return store
}

func seedSemanticOnlyRecord(t *testing.T, store *KVLiteStore, backend *memoryKVLiteBackend, record *llm.QARecord, ttl time.Duration, model string, dim int) string {
	t.Helper()
	if store == nil || backend == nil || record == nil {
		t.Fatalf("invalid semantic seed inputs")
	}
	stored, err := normalizeQARecordForStore(record, ttl, ttl)
	if err != nil {
		t.Fatalf("normalize semantic record: %v", err)
	}
	now := time.Now().Unix()
	if dim <= 0 {
		dim = DefaultSemanticEmbeddingDim
	}
	if strings.TrimSpace(model) == "" {
		model = "placeholder-hash-embedding"
	}
	stored.EmbeddingModel = model
	stored.EmbeddingStatus = EmbeddingStatusReady
	stored.EmbeddingDim = dim
	stored.EmbeddingUpdatedAt = now
	stored.EmbeddingRef = semanticEmbeddingKey(stored.QuestionID)
	stored.SemanticReady = true
	if stored.Metadata == nil {
		stored.Metadata = map[string]string{}
	}
	stored.Metadata["embedding_model"] = model
	stored.Metadata["embedding_status"] = stored.EmbeddingStatus
	stored.Metadata["embedding_dim"] = fmt.Sprintf("%d", dim)
	stored.Metadata["embedding_updated_at"] = fmt.Sprintf("%d", now)
	stored.Metadata["embedding_ref"] = stored.EmbeddingRef
	stored.Metadata["semantic_ready"] = "true"

	mainPayload, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal semantic record: %v", err)
	}
	writeTTL := ttlForWrite(stored, ttl, ttl)
	if err := backend.RSet(context.Background(), store.recordKey(stored.QuestionID), string(mainPayload), writeTTL); err != nil {
		t.Fatalf("write semantic main record: %v", err)
	}
	vector := semanticVectorFromText(stored.Question, dim)
	embeddingPayload, err := encodeSemanticEmbeddingPayload(semanticEmbeddingPayload{
		Model:     model,
		Provider:  model,
		Dim:       dim,
		Vector:    vector,
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

func TestKVLiteStoreSemanticLookupHit(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	question := "How do I reset the cache safely?"
	record := newCandidateQARecord(question, "semantic answer", "deepseek", "deepseek-chat", scope)
	seedSemanticOnlyRecord(t, store, backend, record, time.Minute, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim)

	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
		SemanticEnabled:        true,
		SemanticTopK:           2,
		SemanticThreshold:      0.80,
	})
	if err != nil {
		t.Fatalf("semantic lookup: %v", err)
	}
	if !hit.Hit || hit.HitType != HitTypeSemantic {
		t.Fatalf("expected semantic hit, got %+v", hit)
	}
	if hit.Answer != "semantic answer" {
		t.Fatalf("unexpected semantic answer: %+v", hit)
	}
	if hit.SemanticCandidateCount != 1 || hit.SemanticTopK != 2 {
		t.Fatalf("unexpected semantic counts: %+v", hit)
	}
	if hit.SemanticSource != SemanticRecallSourceBruteForce {
		t.Fatalf("unexpected semantic source: %+v", hit)
	}
	if hit.Metadata["semantic_ready"] != "true" || hit.Metadata["semantic_source"] != SemanticRecallSourceBruteForce {
		t.Fatalf("unexpected semantic metadata: %+v", hit.Metadata)
	}
}

func TestKVLiteStoreSemanticLookupMissWhenEmbeddingMissing(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	question := "How do I reset the cache safely?"
	record := newCandidateQARecord(question, "semantic answer", "deepseek", "deepseek-chat", scope)
	stored, err := normalizeQARecordForStore(record, time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("normalize semantic record: %v", err)
	}
	stored.SemanticReady = true
	stored.EmbeddingStatus = EmbeddingStatusReady
	stored.EmbeddingRef = semanticEmbeddingKey(stored.QuestionID)
	stored.Metadata["semantic_ready"] = "true"
	stored.Metadata["embedding_status"] = EmbeddingStatusReady
	mainPayload, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal semantic record: %v", err)
	}
	if err := backend.RSet(context.Background(), store.recordKey(stored.QuestionID), string(mainPayload), time.Minute); err != nil {
		t.Fatalf("write semantic main record: %v", err)
	}
	bucketPayload, err := encodeCandidateBucketIDs([]string{stored.QuestionID})
	if err != nil {
		t.Fatalf("encode semantic scope index: %v", err)
	}
	if err := backend.RSet(context.Background(), store.semanticScopeIndexKey(stored.Scope), bucketPayload, time.Minute); err != nil {
		t.Fatalf("write semantic scope index: %v", err)
	}

	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
		SemanticEnabled:        true,
		SemanticTopK:           2,
		SemanticThreshold:      0.80,
	})
	if err != nil {
		t.Fatalf("semantic lookup with missing embedding: %v", err)
	}
	if hit.Hit {
		t.Fatalf("expected semantic miss when embedding payload is missing, got %+v", hit)
	}
}

func TestKVLiteStoreSemanticLookupDisabledByFeatureFlag(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	enabledStore := newSemanticTestStore(t, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	question := "How do I reset the cache safely?"
	record := newCandidateQARecord(question, "semantic answer", "deepseek", "deepseek-chat", scope)
	seedSemanticOnlyRecord(t, enabledStore, backend, record, time.Minute, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim)

	disabledStore := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute).(*KVLiteStore)
	hit, err := disabledStore.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
	})
	if err != nil {
		t.Fatalf("semantic-disabled lookup: %v", err)
	}
	if hit.Hit {
		t.Fatalf("expected miss when semantic feature flag is disabled, got %+v", hit)
	}
}

func TestKVLiteStoreSemanticLookupScopeIsolation(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scopeA := BuildScope("deepseek", "deepseek-chat", "v1")
	scopeB := BuildScope("openai", "gpt-4o-mini", "v1")
	question := "How do I reset the cache safely?"
	record := newCandidateQARecord(question, "semantic answer", "deepseek", "deepseek-chat", scopeA)
	seedSemanticOnlyRecord(t, store, backend, record, time.Minute, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim)

	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "openai",
		Model:                  "gpt-4o-mini",
		PromptVersion:          "v1",
		Scope:                  scopeB,
		SemanticEnabled:        true,
		SemanticTopK:           2,
		SemanticThreshold:      0.80,
	})
	if err != nil {
		t.Fatalf("semantic scope lookup: %v", err)
	}
	if hit.Hit {
		t.Fatalf("expected scope-isolated semantic miss, got %+v", hit)
	}
}

func TestKVLiteStoreSemanticLookupCandidatePrecedence(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	question := "How do I flush the Redis cache safely?"
	record := newCandidateQARecord(question, "candidate answer", "deepseek", "deepseek-chat", scope)
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert candidate record: %v", err)
	}
	backfiller, ok := interface{}(store).(SemanticBackfiller)
	if !ok {
		t.Fatalf("store does not implement semantic backfiller")
	}
	if err := backfiller.BackfillSemanticEmbedding(context.Background(), record); err != nil {
		t.Fatalf("backfill semantic embedding: %v", err)
	}
	if err := backend.Del(context.Background(), store.exactIndexKey(scope, ExactQuestionHash(question))); err != nil {
		t.Fatalf("remove exact index: %v", err)
	}
	if err := backend.Del(context.Background(), store.normalizedIndexKey(scope, QuestionHash(question))); err != nil {
		t.Fatalf("remove normalized index: %v", err)
	}

	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
		SemanticEnabled:        true,
		SemanticTopK:           2,
		SemanticThreshold:      0.80,
	})
	if err != nil {
		t.Fatalf("precedence lookup: %v", err)
	}
	if !hit.Hit || hit.HitType != HitTypeCandidate {
		t.Fatalf("expected candidate to outrank semantic, got %+v", hit)
	}
	if hit.CandidateCount == 0 || hit.SemanticCandidateCount != 0 {
		t.Fatalf("expected candidate path only, got %+v", hit)
	}
	if hit.Answer != "candidate answer" {
		t.Fatalf("unexpected precedence answer: %+v", hit)
	}
}

func TestKVLiteStoreSemanticBackfillPropagatesProviderError(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	storeIface := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute,
		WithKVLiteSemanticConfig(SemanticConfig{Enabled: true, TopK: 3, Threshold: 0.80, CorpusLimit: 16, EmbeddingDim: DefaultSemanticEmbeddingDim, EmbeddingModel: "failing"}),
		WithKVLiteSemanticProvider(&failingEmbeddingProvider{name: "failing", err: errors.New("embedding provider failed")}),
	)
	store := storeIface.(*KVLiteStore)
	record := newCandidateQARecord("How do I backfill embeddings?", "backfill answer", "deepseek", "deepseek-chat", BuildScope("deepseek", "deepseek-chat", "v1"))
	backfiller, ok := interface{}(store).(SemanticBackfiller)
	if !ok {
		t.Fatalf("store does not implement semantic backfiller")
	}
	if err := backfiller.BackfillSemanticEmbedding(context.Background(), record); err == nil {
		t.Fatalf("expected semantic backfill error")
	}
}

func BenchmarkKVLiteStoreSemanticLookupBruteForce(b *testing.B) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(b, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	for index := 0; index < 128; index++ {
		question := fmt.Sprintf("How do I reset the cache safely in scenario %d?", index)
		record := newCandidateQARecord(question, fmt.Sprintf("answer-%d", index), "deepseek", "deepseek-chat", scope)
		seedSemanticOnlyRecordNoTest(b, store, backend, record, time.Minute, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim)
	}
	hitQuestion := "How do I reset the cache safely in scenario 0?"
	missQuestion := "How do I provision a GPU cluster?"
	hitRequest := LookupRequest{
		Question:               hitQuestion,
		ExactQuestionHash:      ExactQuestionHash(hitQuestion),
		NormalizedQuestionHash: QuestionHash(hitQuestion),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
		SemanticEnabled:        true,
		SemanticTopK:           4,
		SemanticThreshold:      0.80,
	}
	missRequest := LookupRequest{
		Question:               missQuestion,
		ExactQuestionHash:      ExactQuestionHash(missQuestion),
		NormalizedQuestionHash: QuestionHash(missQuestion),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
		SemanticEnabled:        true,
		SemanticTopK:           4,
		SemanticThreshold:      0.999,
	}
	b.Run("hit", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			_, _ = store.Lookup(context.Background(), hitRequest)
		}
	})
	b.Run("miss", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			_, _ = store.Lookup(context.Background(), missRequest)
		}
	})
}

func seedSemanticOnlyRecordNoTest(b *testing.B, store *KVLiteStore, backend *memoryKVLiteBackend, record *llm.QARecord, ttl time.Duration, model string, dim int) string {
	b.Helper()
	stored, err := normalizeQARecordForStore(record, ttl, ttl)
	if err != nil {
		b.Fatalf("normalize semantic record: %v", err)
	}
	now := time.Now().Unix()
	if dim <= 0 {
		dim = DefaultSemanticEmbeddingDim
	}
	if strings.TrimSpace(model) == "" {
		model = "placeholder-hash-embedding"
	}
	stored.EmbeddingModel = model
	stored.EmbeddingStatus = EmbeddingStatusReady
	stored.EmbeddingDim = dim
	stored.EmbeddingUpdatedAt = now
	stored.EmbeddingRef = semanticEmbeddingKey(stored.QuestionID)
	stored.SemanticReady = true
	if stored.Metadata == nil {
		stored.Metadata = map[string]string{}
	}
	stored.Metadata["embedding_model"] = model
	stored.Metadata["embedding_status"] = stored.EmbeddingStatus
	stored.Metadata["embedding_dim"] = fmt.Sprintf("%d", dim)
	stored.Metadata["embedding_updated_at"] = fmt.Sprintf("%d", now)
	stored.Metadata["embedding_ref"] = stored.EmbeddingRef
	stored.Metadata["semantic_ready"] = "true"

	mainPayload, err := json.Marshal(stored)
	if err != nil {
		b.Fatalf("marshal semantic record: %v", err)
	}
	writeTTL := ttlForWrite(stored, ttl, ttl)
	if err := backend.RSet(context.Background(), store.recordKey(stored.QuestionID), string(mainPayload), writeTTL); err != nil {
		b.Fatalf("write semantic main record: %v", err)
	}
	vector := semanticVectorFromText(stored.Question, dim)
	embeddingPayload, err := encodeSemanticEmbeddingPayload(semanticEmbeddingPayload{
		Model:     model,
		Provider:  model,
		Dim:       dim,
		Vector:    vector,
		UpdatedAt: now,
	})
	if err != nil {
		b.Fatalf("encode semantic payload: %v", err)
	}
	if err := backend.RSet(context.Background(), store.prefixed(semanticEmbeddingKey(stored.QuestionID)), embeddingPayload, writeTTL); err != nil {
		b.Fatalf("write semantic embedding: %v", err)
	}
	bucketPayload, err := encodeCandidateBucketIDs([]string{stored.QuestionID})
	if err != nil {
		b.Fatalf("encode semantic scope index: %v", err)
	}
	if err := backend.RSet(context.Background(), store.semanticScopeIndexKey(stored.Scope), bucketPayload, writeTTL); err != nil {
		b.Fatalf("write semantic scope index: %v", err)
	}
	return stored.QuestionID
}
