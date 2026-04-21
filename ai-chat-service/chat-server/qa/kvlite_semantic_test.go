package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	return newSemanticTestStoreWithProfile(tb, backend, enabled, "", model, dim, threshold)
}

func newSemanticTestStoreWithProfile(tb testing.TB, backend *memoryKVLiteBackend, enabled bool, profile string, model string, dim int, threshold float64) *KVLiteStore {
	tb.Helper()
	storeIface := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute,
		WithKVLiteSemanticConfig(SemanticConfig{
			Enabled:          enabled,
			TopK:             3,
			Threshold:        threshold,
			CorpusLimit:      16,
			EmbeddingProfile: profile,
			EmbeddingModel:   model,
			EmbeddingDim:     dim,
		}),
		WithKVLiteSemanticProvider(NewHashEmbeddingProvider(model, dim)),
	)
	store, ok := storeIface.(*KVLiteStore)
	if !ok {
		tb.Fatalf("expected *KVLiteStore, got %T", storeIface)
	}
	return store
}

func newSemanticTestStoreWithANN(tb testing.TB, backend *memoryKVLiteBackend, enabled bool, profile string, model string, dim int, threshold float64, annTopK int, annMinScoreGap float64) *KVLiteStore {
	tb.Helper()
	storeIface := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute,
		WithKVLiteSemanticConfig(SemanticConfig{
			Enabled:          enabled,
			TopK:             3,
			Threshold:        threshold,
			CorpusLimit:      16,
			ANN:              SemanticANNConfig{TopK: annTopK, MinScoreGap: annMinScoreGap},
			EmbeddingProfile: profile,
			EmbeddingModel:   model,
			EmbeddingDim:     dim,
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
	return seedSemanticOnlyRecordWithProfile(t, store, backend, record, ttl, "", model, dim)
}

func seedSemanticOnlyRecordWithProfile(tb testing.TB, store *KVLiteStore, backend *memoryKVLiteBackend, record *llm.QARecord, ttl time.Duration, profile string, model string, dim int) string {
	tb.Helper()
	if store == nil || backend == nil || record == nil {
		tb.Fatalf("invalid semantic seed inputs")
	}
	stored, err := normalizeQARecordForStore(record, ttl, ttl)
	if err != nil {
		tb.Fatalf("normalize semantic record: %v", err)
	}
	now := time.Now().Unix()
	if dim <= 0 {
		dim = DefaultSemanticEmbeddingDim
	}
	if strings.TrimSpace(model) == "" {
		model = "placeholder-hash-embedding"
	}
	profile = strings.TrimSpace(profile)
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
	if profile != "" {
		stored.Metadata["embedding_profile"] = profile
	}
	stored.Metadata["embedding_status"] = stored.EmbeddingStatus
	stored.Metadata["embedding_dim"] = fmt.Sprintf("%d", dim)
	stored.Metadata["embedding_updated_at"] = fmt.Sprintf("%d", now)
	stored.Metadata["embedding_ref"] = stored.EmbeddingRef
	stored.Metadata["semantic_ready"] = "true"

	mainPayload, err := json.Marshal(stored)
	if err != nil {
		tb.Fatalf("marshal semantic record: %v", err)
	}
	writeTTL := ttlForWrite(stored, ttl, ttl)
	if err := backend.RSet(context.Background(), store.recordKey(stored.QuestionID), string(mainPayload), writeTTL); err != nil {
		tb.Fatalf("write semantic main record: %v", err)
	}
	vector := semanticVectorFromText(stored.Question, dim)
	embeddingPayload, err := encodeSemanticEmbeddingPayload(semanticEmbeddingPayload{
		Model:     model,
		Provider:  model,
		Profile:   profile,
		Dim:       dim,
		Vector:    vector,
		UpdatedAt: now,
	})
	if err != nil {
		tb.Fatalf("encode semantic payload: %v", err)
	}
	if err := backend.RSet(context.Background(), store.prefixed(semanticEmbeddingKey(stored.QuestionID)), embeddingPayload, writeTTL); err != nil {
		tb.Fatalf("write semantic embedding: %v", err)
	}
	bucketPayload, err := encodeCandidateBucketIDs([]string{stored.QuestionID})
	if err != nil {
		tb.Fatalf("encode semantic scope index: %v", err)
	}
	if err := backend.RSet(context.Background(), store.semanticScopeIndexKey(stored.Scope), bucketPayload, writeTTL); err != nil {
		tb.Fatalf("write semantic scope index: %v", err)
	}
	return stored.QuestionID
}

func semanticANNUnitVector(cosine float64) []float32 {
	if cosine > 1 {
		cosine = 1
	}
	if cosine < -1 {
		cosine = -1
	}
	sine := math.Sqrt(math.Max(0, 1-cosine*cosine))
	return []float32{float32(cosine), float32(sine), 0, 0}
}

func writeSemanticScopeIndex(tb testing.TB, backend *memoryKVLiteBackend, store *KVLiteStore, scope string, ids []string, ttl time.Duration) {
	tb.Helper()
	payload, err := encodeCandidateBucketIDs(ids)
	if err != nil {
		tb.Fatalf("encode semantic scope index: %v", err)
	}
	if err := backend.RSet(context.Background(), store.semanticScopeIndexKey(scope), payload, ttl); err != nil {
		tb.Fatalf("write semantic scope index: %v", err)
	}
}

func installSemanticANNIndex(tb testing.TB, store *KVLiteStore, scope string, corpusLimit int, index *semanticANNIndex) {
	tb.Helper()
	store.semanticANNCacheMu.Lock()
	store.semanticANNCache[store.semanticANNCacheKey(scope, corpusLimit)] = index
	store.semanticANNCacheMu.Unlock()
}

func seedSemanticOnlyRecordNoTest(b *testing.B, store *KVLiteStore, backend *memoryKVLiteBackend, record *llm.QARecord, ttl time.Duration, model string, dim int) string {
	return seedSemanticOnlyRecordNoTestWithProfile(b, store, backend, record, ttl, "", model, dim)
}

func seedSemanticOnlyRecordNoTestWithProfile(b *testing.B, store *KVLiteStore, backend *memoryKVLiteBackend, record *llm.QARecord, ttl time.Duration, profile string, model string, dim int) string {
	b.Helper()
	if store == nil || backend == nil || record == nil {
		b.Fatalf("invalid semantic seed inputs")
	}
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
	profile = strings.TrimSpace(profile)
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
	if profile != "" {
		stored.Metadata["embedding_profile"] = profile
	}
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
		Profile:   profile,
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

func TestKVLiteStoreSemanticANNUsesDefaultGateConfigWhenUnset(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStoreWithProfile(t, backend, true, "", "manual-embedding", 4, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)
	top1Vector := semanticANNUnitVector(1.0)
	top2Vector := semanticANNUnitVector(0.88)

	top1Record := newCandidateQARecord("default ann top1 question", "default ann answer", "deepseek", "deepseek-chat", scope)
	top2Record := newCandidateQARecord("default ann top2 question", "default ann distractor", "deepseek", "deepseek-chat", scope)
	top1ID := seedSemanticRecordWithVector(t, store, backend, top1Record, top1Vector, "manual-embedding")
	top2ID := seedSemanticRecordWithVector(t, store, backend, top2Record, top2Vector, "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, scope, []string{top1ID, top2ID}, time.Minute)
	installSemanticANNIndex(t, store, scope, 16, manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{
		top1ID: top1Record,
		top2ID: top2Record,
	}, map[string][]float32{
		top1ID: top1Vector,
		top2ID: top2Vector,
	}))

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "default ann query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("default ann lookup: %v", err)
	}
	if !hit || !result.Hit {
		t.Fatalf("expected default ANN hit, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionHit {
		t.Fatalf("expected ANN hit decision, got %+v", result)
	}
	if result.SemanticANNTopK != DefaultSemanticANNTopK || result.SemanticTopK != DefaultSemanticANNTopK {
		t.Fatalf("expected default ANN topK %d, got %+v", DefaultSemanticANNTopK, result)
	}
	if result.SemanticANNMinScoreGap != DefaultSemanticANNMinScoreGap {
		t.Fatalf("expected default ANN min gap %.2f, got %+v", DefaultSemanticANNMinScoreGap, result)
	}
	if result.Metadata["semantic_ann_top_k"] != fmt.Sprintf("%d", DefaultSemanticANNTopK) {
		t.Fatalf("unexpected ANN topK metadata: %+v", result.Metadata)
	}
	if result.Metadata["semantic_ann_min_score_gap"] != fmt.Sprintf("%.4f", DefaultSemanticANNMinScoreGap) {
		t.Fatalf("unexpected ANN min gap metadata: %+v", result.Metadata)
	}
	if result.Metadata["semantic_ann_decision"] != semanticANNDecisionHit {
		t.Fatalf("unexpected ANN decision metadata: %+v", result.Metadata)
	}
}

func TestKVLiteStoreSemanticANNUsesConfiguredGateValues(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStoreWithANN(t, backend, true, "", "manual-embedding", 4, 0.80, 5, 0.20)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)
	top1Vector := semanticANNUnitVector(1.0)
	top2Vector := semanticANNUnitVector(0.75)

	top1Record := newCandidateQARecord("configured ann top1 question", "configured ann answer", "deepseek", "deepseek-chat", scope)
	top2Record := newCandidateQARecord("configured ann top2 question", "configured ann distractor", "deepseek", "deepseek-chat", scope)
	top1ID := seedSemanticRecordWithVector(t, store, backend, top1Record, top1Vector, "manual-embedding")
	top2ID := seedSemanticRecordWithVector(t, store, backend, top2Record, top2Vector, "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, scope, []string{top1ID, top2ID}, time.Minute)
	installSemanticANNIndex(t, store, scope, 16, manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{
		top1ID: top1Record,
		top2ID: top2Record,
	}, map[string][]float32{
		top1ID: top1Vector,
		top2ID: top2Vector,
	}))

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "configured ann query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("configured ann lookup: %v", err)
	}
	if !hit || !result.Hit {
		t.Fatalf("expected configured ANN hit, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionHit {
		t.Fatalf("expected ANN hit decision, got %+v", result)
	}
	if result.SemanticANNTopK != 5 || result.Metadata["semantic_ann_top_k"] != "5" {
		t.Fatalf("expected configured ANN topK, got %+v", result)
	}
	if result.SemanticANNMinScoreGap != 0.20 || result.Metadata["semantic_ann_min_score_gap"] != "0.2000" {
		t.Fatalf("expected configured ANN min gap, got %+v", result)
	}
}

func TestKVLiteStoreSemanticANNConfiguredGapTriggersFallback(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStoreWithANN(t, backend, true, "", "manual-embedding", 4, 0.80, 3, 0.20)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)
	top1Vector := semanticANNUnitVector(1.0)
	top2Vector := semanticANNUnitVector(0.90)

	top1Record := newCandidateQARecord("gap configured top1 question", "gap configured top1 answer", "deepseek", "deepseek-chat", scope)
	top2Record := newCandidateQARecord("gap configured top2 question", "gap configured top2 distractor", "deepseek", "deepseek-chat", scope)
	top1ID := seedSemanticRecordWithVector(t, store, backend, top1Record, top1Vector, "manual-embedding")
	top2ID := seedSemanticRecordWithVector(t, store, backend, top2Record, top2Vector, "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, scope, []string{top1ID, top2ID}, time.Minute)
	installSemanticANNIndex(t, store, scope, 16, manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{
		top1ID: top1Record,
		top2ID: top2Record,
	}, map[string][]float32{
		top1ID: top1Vector,
		top2ID: top2Vector,
	}))

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "gap configured query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("configured gap lookup: %v", err)
	}
	if !hit || !result.Hit {
		t.Fatalf("expected fallback hit via brute-force, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionFallback {
		t.Fatalf("expected fallback ANN decision, got %+v", result)
	}
	if result.SemanticANNMinScoreGap != 0.20 || result.Metadata["semantic_ann_min_score_gap"] != "0.2000" {
		t.Fatalf("expected configured ANN min gap, got %+v", result)
	}
	if !result.SemanticFallbackUsed || !strings.Contains(result.SemanticFallbackReason, semanticANNReasonScoreGapTooSmall) {
		t.Fatalf("expected gap fallback reason, got %+v", result)
	}
}

func TestSemanticANNIndexSignatureBinding(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	storeA := newSemanticTestStoreWithProfile(t, backend, true, "profile-a", "model-a", 64, 0.80)
	storeB := newSemanticTestStoreWithProfile(t, backend, true, "profile-b", "model-b", 128, 0.80)

	keyA := storeA.semanticANNCacheKey(scope, 16)
	keyB := storeB.semanticANNCacheKey(scope, 16)
	if keyA == keyB {
		t.Fatalf("expected semantic ANN cache keys to differ, got %q", keyA)
	}

	versionA := buildSemanticANNIndexVersion(scope, 16, storeA.semanticEmbeddingSignature(), semanticANNProjectionBits, semanticANNProbeRadius)
	versionB := buildSemanticANNIndexVersion(scope, 16, storeB.semanticEmbeddingSignature(), semanticANNProjectionBits, semanticANNProbeRadius)
	if versionA == versionB {
		t.Fatalf("expected semantic ANN versions to differ, got %q", versionA)
	}
	if !strings.Contains(versionA, "embedding_profile=profile-a") || !strings.Contains(versionA, "embedding_model=model-a") || !strings.Contains(versionA, "embedding_dim=64") {
		t.Fatalf("unexpected semantic ANN version: %q", versionA)
	}
}

func TestKVLiteStoreSemanticANNCacheRebuildsOnSignatureChange(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	store := newSemanticTestStoreWithProfile(t, backend, true, "profile-a", "model-a", 64, 0.80)

	firstRecord := newCandidateQARecord("How do I reset the cache safely?", "answer-a", "deepseek", "deepseek-chat", scope)
	seedSemanticOnlyRecordWithProfile(t, store, backend, firstRecord, time.Minute, "profile-a", "model-a", 64)
	firstIndex, err := store.ensureSemanticANNIndex(context.Background(), scope, 16)
	if err != nil {
		t.Fatalf("build first ann index: %v", err)
	}
	if firstIndex == nil || len(firstIndex.Entries) != 1 {
		t.Fatalf("expected first ANN index with one entry, got %+v", firstIndex)
	}
	firstKey := store.semanticANNCacheKey(scope, 16)
	if _, ok := store.semanticANNCache[firstKey]; !ok {
		t.Fatalf("expected first ANN cache entry at key %q", firstKey)
	}

	store.semanticEmbeddingProfile = "profile-b"
	store.semanticModel = "model-b"
	store.semanticEmbeddingDim = 128
	secondRecord := newCandidateQARecord("How do I clear the Redis cache safely?", "answer-b", "deepseek", "deepseek-chat", scope)
	seedSemanticOnlyRecordWithProfile(t, store, backend, secondRecord, time.Minute, "profile-b", "model-b", 128)
	secondIndex, err := store.ensureSemanticANNIndex(context.Background(), scope, 16)
	if err != nil {
		t.Fatalf("build second ann index: %v", err)
	}
	if secondIndex == nil || len(secondIndex.Entries) != 1 {
		t.Fatalf("expected second ANN index with one entry, got %+v", secondIndex)
	}
	if secondIndex == firstIndex {
		t.Fatalf("expected ANN cache to rebuild after signature change")
	}
	secondKey := store.semanticANNCacheKey(scope, 16)
	if secondKey == firstKey {
		t.Fatalf("expected ANN cache key to change after signature change")
	}
	if _, ok := store.semanticANNCache[secondKey]; !ok {
		t.Fatalf("expected rebuilt ANN cache entry at key %q", secondKey)
	}
}

func TestKVLiteStoreSemanticANNFiltersMismatchedEmbeddingMetadata(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		model   string
		dim     int
	}{
		{name: "model mismatch", profile: "profile-a", model: "model-b", dim: 64},
		{name: "dim mismatch", profile: "profile-a", model: "model-a", dim: 32},
		{name: "profile mismatch", profile: "profile-b", model: "model-a", dim: 64},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := newMemoryKVLiteBackend()
			scope := BuildScope("deepseek", "deepseek-chat", "v1")
			store := newSemanticTestStoreWithProfile(t, backend, true, "profile-a", "model-a", 64, 0.80)
			record := newCandidateQARecord("How do I reset the cache safely?", "answer", "deepseek", "deepseek-chat", scope)
			seedSemanticOnlyRecordWithProfile(t, store, backend, record, time.Minute, tc.profile, tc.model, tc.dim)

			index, err := store.ensureSemanticANNIndex(context.Background(), scope, 16)
			if err != nil {
				t.Fatalf("build ann index: %v", err)
			}
			if index != nil && len(index.Entries) > 0 {
				t.Fatalf("expected mismatched embedding metadata to be filtered, got %+v", index)
			}
		})
	}
}

func TestKVLiteStoreSemanticLookupANNModeAndCache(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	question := "How do I reset the cache safely?"
	record := newCandidateQARecord(question, "ann answer", "deepseek", "deepseek-chat", scope)
	seedSemanticOnlyRecord(t, store, backend, record, time.Minute, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim)

	index, err := store.ensureSemanticANNIndex(context.Background(), scope, 16)
	if err != nil {
		t.Fatalf("ensure ann index: %v", err)
	}
	if index == nil || len(index.Entries) != 1 {
		t.Fatalf("expected one ANN entry, got %+v", index)
	}
	cachedIndex, err := store.ensureSemanticANNIndex(context.Background(), scope, 16)
	if err != nil {
		t.Fatalf("ensure cached ann index: %v", err)
	}
	if cachedIndex != index {
		t.Fatalf("expected cached ANN index to be reused")
	}

	hit, _, err := store.lookupBySemantic(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
		SemanticEnabled:        true,
		SemanticMode:           SemanticRecallModeANN,
		SemanticTopK:           2,
		SemanticThreshold:      0.80,
		SemanticCorpusLimit:    16,
	})
	if err != nil {
		t.Fatalf("ann semantic lookup: %v", err)
	}
	if !hit.Hit || hit.SemanticSource != SemanticRecallSourceANNBucketedLSH {
		t.Fatalf("expected ANN semantic hit, got %+v", hit)
	}
	if hit.SemanticModeRequested != SemanticRecallModeANN || hit.SemanticModeUsed != SemanticRecallModeANN {
		t.Fatalf("expected ANN mode fields to be populated, got %+v", hit)
	}
	if hit.SemanticFallbackUsed {
		t.Fatalf("did not expect ANN fallback on a healthy lookup, got %+v", hit)
	}
	if hit.Answer != "ann answer" {
		t.Fatalf("unexpected ann answer: %+v", hit)
	}
	if hit.SemanticCandidateCount == 0 || hit.SemanticTopScore <= 0 {
		t.Fatalf("expected ANN candidate stats to be populated: %+v", hit)
	}
	if hit.Metadata["semantic_mode_requested"] != SemanticRecallModeANN || hit.Metadata["semantic_mode_used"] != SemanticRecallModeANN {
		t.Fatalf("expected ANN metadata to be populated, got %+v", hit.Metadata)
	}
	if hit.Metadata["semantic_fallback_used"] != "false" {
		t.Fatalf("expected ANN metadata to record no fallback, got %+v", hit.Metadata)
	}
}

func TestKVLiteStoreSemanticLookupANNFallsBackToBruteForce(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	question := "How do I reset the cache safely?"
	record := newCandidateQARecord(question, "fallback answer", "deepseek", "deepseek-chat", scope)
	seedSemanticOnlyRecord(t, store, backend, record, time.Minute, "placeholder-hash-embedding", DefaultSemanticEmbeddingDim)

	cacheKey := store.semanticANNCacheKey(scope, 16)
	store.semanticANNCacheMu.Lock()
	store.semanticANNCache[cacheKey] = &semanticANNIndex{
		Scope:        normalizeScope(scope),
		CorpusLimit:  16,
		EmbeddingDim: DefaultSemanticEmbeddingDim,
		Entries: map[string]semanticANNIndexEntry{
			record.QuestionID: {
				Record:         cloneQARecord(record),
				Vector:         semanticVectorFromText(question, DefaultSemanticEmbeddingDim),
				EmbeddingModel: "placeholder-hash-embedding",
			},
		},
		Buckets: map[string][]string{
			"1": []string{record.QuestionID},
		},
	}
	store.semanticANNCacheMu.Unlock()

	hit, err := store.Lookup(context.Background(), LookupRequest{
		Question:               question,
		ExactQuestionHash:      ExactQuestionHash(question),
		NormalizedQuestionHash: QuestionHash(question),
		Provider:               "deepseek",
		Model:                  "deepseek-chat",
		PromptVersion:          "v1",
		Scope:                  scope,
		SemanticEnabled:        true,
		SemanticMode:           SemanticRecallModeANN,
		SemanticTopK:           2,
		SemanticThreshold:      0.80,
		SemanticCorpusLimit:    16,
	})
	if err != nil {
		t.Fatalf("ann semantic fallback lookup: %v", err)
	}
	if !hit.Hit || hit.HitType != HitTypeSemantic {
		t.Fatalf("expected semantic hit after brute-force fallback, got %+v", hit)
	}
	if hit.Answer != "fallback answer" {
		t.Fatalf("unexpected fallback answer: %+v", hit)
	}
	if hit.SemanticModeRequested != SemanticRecallModeANN || hit.SemanticModeUsed != SemanticRecallModeBruteForce {
		t.Fatalf("expected ann request with brute-force fallback, got %+v", hit)
	}
	if !hit.SemanticFallbackUsed || !strings.Contains(strings.ToLower(hit.SemanticFallbackReason), "ann index projections are missing") {
		t.Fatalf("expected fallback reason to mention missing projections, got %+v", hit)
	}
	if hit.SemanticSource != SemanticRecallSourceANNFallback {
		t.Fatalf("expected ANN fallback source, got %+v", hit)
	}
	if hit.Metadata["semantic_mode_requested"] != SemanticRecallModeANN || hit.Metadata["semantic_mode_used"] != SemanticRecallModeBruteForce {
		t.Fatalf("expected fallback metadata to record requested and used mode, got %+v", hit.Metadata)
	}
	if hit.Metadata["semantic_fallback_used"] != "true" {
		t.Fatalf("expected fallback metadata to record fallback usage, got %+v", hit.Metadata)
	}
	if !strings.Contains(strings.ToLower(hit.Metadata["semantic_fallback_reason"]), "ann index projections are missing") {
		t.Fatalf("expected fallback metadata to record reason, got %+v", hit.Metadata)
	}
}

func TestKVLiteStoreSemanticANNDirectGateAllowsSeparatedTopK(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)
	top1Vector := semanticANNUnitVector(1.0)
	top2Vector := semanticANNUnitVector(0.88)

	top1Record := newCandidateQARecord("direct ann top1 question", "direct ann answer", "deepseek", "deepseek-chat", scope)
	top2Record := newCandidateQARecord("direct ann top2 question", "direct ann distractor", "deepseek", "deepseek-chat", scope)
	top1ID := seedSemanticRecordWithVector(t, store, backend, top1Record, top1Vector, "manual-embedding")
	top2ID := seedSemanticRecordWithVector(t, store, backend, top2Record, top2Vector, "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, scope, []string{top1ID, top2ID}, time.Minute)
	installSemanticANNIndex(t, store, scope, 16, manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{
		top1ID: top1Record,
		top2ID: top2Record,
	}, map[string][]float32{
		top1ID: top1Vector,
		top2ID: top2Vector,
	}))

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "direct ann gate query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticTopK:        2,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("direct ann lookup: %v", err)
	}
	if !hit || !result.Hit {
		t.Fatalf("expected direct ann hit, got %+v", result)
	}
	if result.Answer != "direct ann answer" {
		t.Fatalf("unexpected direct ann answer: %+v", result)
	}
	if result.SemanticModeRequested != SemanticRecallModeANN || result.SemanticModeUsed != SemanticRecallModeANN {
		t.Fatalf("expected ANN mode to be preserved, got %+v", result)
	}
	if result.SemanticFallbackUsed {
		t.Fatalf("did not expect fallback for a separated top-K hit, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionHit {
		t.Fatalf("expected ANN hit decision, got %+v", result)
	}
	if result.SemanticANNTopK != 2 || result.SemanticANNTopScore <= 0 || result.SemanticANNTop2Score <= 0 || result.SemanticANNScoreGap <= 0 {
		t.Fatalf("unexpected ANN diagnostics on hit: %+v", result)
	}
	if result.SemanticSource != SemanticRecallSourceANNBucketedLSH {
		t.Fatalf("unexpected semantic source: %+v", result)
	}
	if result.SemanticCandidateCount != 2 || result.SemanticTopK != 2 {
		t.Fatalf("unexpected semantic candidate stats: %+v", result)
	}
	if result.Metadata["semantic_top_k"] != "2" || result.Metadata["semantic_ann_top_k"] != "2" || result.Metadata["semantic_ann_decision"] != semanticANNDecisionHit || result.Metadata["semantic_ann_score_gap"] == "" {
		t.Fatalf("expected top-K metadata to be populated, got %+v", result.Metadata)
	}
}

func TestKVLiteStoreSemanticANNDirectGateFallsBackOnSmallScoreGap(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)
	top1Vector := semanticANNUnitVector(1.0)
	top2Vector := semanticANNUnitVector(0.98)

	top1Record := newCandidateQARecord("gap top1 question", "gap top1 answer", "deepseek", "deepseek-chat", scope)
	top2Record := newCandidateQARecord("gap top2 question", "gap top2 distractor", "deepseek", "deepseek-chat", scope)
	top1ID := seedSemanticRecordWithVector(t, store, backend, top1Record, top1Vector, "manual-embedding")
	top2ID := seedSemanticRecordWithVector(t, store, backend, top2Record, top2Vector, "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, scope, []string{top1ID, top2ID}, time.Minute)
	installSemanticANNIndex(t, store, scope, 16, manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{
		top1ID: top1Record,
		top2ID: top2Record,
	}, map[string][]float32{
		top1ID: top1Vector,
		top2ID: top2Vector,
	}))

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "gap fallback query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticTopK:        2,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("gap fallback lookup: %v", err)
	}
	if !hit || !result.Hit {
		t.Fatalf("expected fallback lookup to hit, got %+v", result)
	}
	if result.Answer != "gap top1 answer" {
		t.Fatalf("unexpected fallback answer: %+v", result)
	}
	if result.SemanticModeRequested != SemanticRecallModeANN || result.SemanticModeUsed != SemanticRecallModeBruteForce {
		t.Fatalf("expected brute-force fallback, got %+v", result)
	}
	if !result.SemanticFallbackUsed {
		t.Fatalf("expected fallback to be recorded, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionFallback {
		t.Fatalf("expected ANN fallback decision, got %+v", result)
	}
	if result.SemanticANNTopK != 2 || result.SemanticANNTopScore <= 0 || result.SemanticANNTop2Score <= 0 || result.SemanticANNScoreGap <= 0 {
		t.Fatalf("unexpected ANN diagnostics on fallback: %+v", result)
	}
	if result.SemanticSource != SemanticRecallSourceANNFallback {
		t.Fatalf("unexpected semantic source: %+v", result)
	}
	if !strings.Contains(result.SemanticFallbackReason, semanticANNReasonDirectRejectedFallbackBrute) || !strings.Contains(result.SemanticFallbackReason, semanticANNReasonScoreGapTooSmall) {
		t.Fatalf("expected gap fallback reason, got %+v", result.SemanticFallbackReason)
	}
	if result.Metadata["semantic_ann_decision"] != semanticANNDecisionFallback {
		t.Fatalf("expected fallback ANN decision metadata, got %+v", result.Metadata)
	}
	if !strings.Contains(result.Metadata["semantic_fallback_reason"], semanticANNReasonScoreGapTooSmall) {
		t.Fatalf("expected fallback metadata to include gap reason, got %+v", result.Metadata)
	}
}

func TestKVLiteStoreSemanticANNDirectGateFallsBackWhenTopScoreBelowThreshold(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)
	top1Vector := semanticANNUnitVector(0.78)
	top2Vector := semanticANNUnitVector(0.20)

	top1Record := newCandidateQARecord("threshold top1 question", "threshold top1 answer", "deepseek", "deepseek-chat", scope)
	top2Record := newCandidateQARecord("threshold top2 question", "threshold top2 distractor", "deepseek", "deepseek-chat", scope)
	top1ID := seedSemanticRecordWithVector(t, store, backend, top1Record, top1Vector, "manual-embedding")
	top2ID := seedSemanticRecordWithVector(t, store, backend, top2Record, top2Vector, "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, scope, []string{top1ID, top2ID}, time.Minute)
	installSemanticANNIndex(t, store, scope, 16, manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{
		top1ID: top1Record,
		top2ID: top2Record,
	}, map[string][]float32{
		top1ID: top1Vector,
		top2ID: top2Vector,
	}))

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "threshold fallback query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticTopK:        2,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("threshold fallback lookup: %v", err)
	}
	if hit || result.Hit {
		t.Fatalf("expected threshold fallback to miss, got %+v", result)
	}
	if result.SemanticModeRequested != SemanticRecallModeANN || result.SemanticModeUsed != SemanticRecallModeBruteForce {
		t.Fatalf("expected brute-force fallback, got %+v", result)
	}
	if !result.SemanticFallbackUsed {
		t.Fatalf("expected fallback to be recorded, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionFallback {
		t.Fatalf("expected threshold ANN fallback decision, got %+v", result)
	}
	if result.SemanticANNTopK != 2 || result.SemanticANNTopScore <= 0 {
		t.Fatalf("unexpected ANN diagnostics on threshold fallback: %+v", result)
	}
	if !strings.Contains(result.SemanticFallbackReason, semanticANNReasonDirectRejectedFallbackBrute) || !strings.Contains(result.SemanticFallbackReason, semanticANNReasonScoreBelowThreshold) {
		t.Fatalf("expected threshold fallback reason, got %+v", result.SemanticFallbackReason)
	}
}

func TestKVLiteStoreSemanticANNDirectGateFallsBackWhenTopKCrowded(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)
	top1Vector := semanticANNUnitVector(0.99)
	top2Vector := semanticANNUnitVector(0.97)
	top3Vector := semanticANNUnitVector(0.96)

	top1Record := newCandidateQARecord("crowded top1 question", "crowded top1 answer", "deepseek", "deepseek-chat", scope)
	top2Record := newCandidateQARecord("crowded top2 question", "crowded top2 distractor", "deepseek", "deepseek-chat", scope)
	top3Record := newCandidateQARecord("crowded top3 question", "crowded top3 distractor", "deepseek", "deepseek-chat", scope)
	top1ID := seedSemanticRecordWithVector(t, store, backend, top1Record, top1Vector, "manual-embedding")
	top2ID := seedSemanticRecordWithVector(t, store, backend, top2Record, top2Vector, "manual-embedding")
	top3ID := seedSemanticRecordWithVector(t, store, backend, top3Record, top3Vector, "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, scope, []string{top1ID, top2ID, top3ID}, time.Minute)
	installSemanticANNIndex(t, store, scope, 16, manualShadowIndexForVector(scope, 16, queryVector, map[string]*llm.QARecord{
		top1ID: top1Record,
		top2ID: top2Record,
		top3ID: top3Record,
	}, map[string][]float32{
		top1ID: top1Vector,
		top2ID: top2Vector,
		top3ID: top3Vector,
	}))

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "crowded fallback query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticTopK:        3,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("crowded fallback lookup: %v", err)
	}
	if !hit || !result.Hit {
		t.Fatalf("expected crowded fallback lookup to hit, got %+v", result)
	}
	if result.Answer != "crowded top1 answer" {
		t.Fatalf("unexpected fallback answer: %+v", result)
	}
	if result.SemanticModeRequested != SemanticRecallModeANN || result.SemanticModeUsed != SemanticRecallModeBruteForce {
		t.Fatalf("expected brute-force fallback, got %+v", result)
	}
	if !result.SemanticFallbackUsed {
		t.Fatalf("expected fallback to be recorded, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionFallback {
		t.Fatalf("expected crowded ANN fallback decision, got %+v", result)
	}
	if !strings.Contains(result.SemanticFallbackReason, semanticANNReasonDirectRejectedFallbackBrute) || !strings.Contains(result.SemanticFallbackReason, semanticANNReasonAmbiguousTopK) {
		t.Fatalf("expected crowded top-K fallback reason, got %+v", result.SemanticFallbackReason)
	}
}

func TestKVLiteStoreSemanticANNDirectGateFallsBackWhenNoCandidates(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	store := newSemanticTestStore(t, backend, true, "manual-embedding", 4, 0.80)
	scope := BuildScope("deepseek", "deepseek-chat", "v1")
	queryVector := semanticANNUnitVector(1.0)

	result, hit, err := store.lookupBySemanticANNWithVector(context.Background(), LookupRequest{
		Question:            "empty ann query",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticTopK:        2,
		SemanticThreshold:   0.80,
		SemanticCorpusLimit: 16,
	}, queryVector)
	if err != nil {
		t.Fatalf("empty ann lookup: %v", err)
	}
	if hit || result.Hit {
		t.Fatalf("expected no hit for empty ANN corpus, got %+v", result)
	}
	if result.SemanticANNDecision != semanticANNDecisionMiss {
		t.Fatalf("expected ANN miss decision for empty corpus, got %+v", result)
	}
	if !result.SemanticFallbackUsed {
		t.Fatalf("expected fallback to be recorded, got %+v", result)
	}
	if !strings.Contains(result.SemanticFallbackReason, semanticANNReasonDirectRejectedFallbackBrute) || !strings.Contains(result.SemanticFallbackReason, semanticANNReasonNoCandidates) {
		t.Fatalf("expected no-candidates fallback reason, got %+v", result.SemanticFallbackReason)
	}
	if result.Metadata["semantic_ann_decision"] != semanticANNDecisionMiss || !strings.Contains(result.Metadata["semantic_fallback_reason"], semanticANNReasonNoCandidates) {
		t.Fatalf("expected fallback metadata to record no candidates, got %+v", result.Metadata)
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
