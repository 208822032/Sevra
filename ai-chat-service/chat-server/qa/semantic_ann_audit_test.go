package qa

import (
	"ai-chat-service/chat-server/llm"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSemanticANNSampleTestStore(tb testing.TB, backend *memoryKVLiteBackend, samplePath string, exportFallbackOnly bool) *KVLiteStore {
	tb.Helper()
	storeIface := NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute,
		WithKVLiteSemanticConfig(SemanticConfig{
			Enabled:          true,
			TopK:             3,
			Threshold:        0.80,
			CorpusLimit:      16,
			ANN:              SemanticANNConfig{TopK: 2, MinScoreGap: 0.20, SampleEnabled: true, SampleRate: 1, ExportFallbackOnly: exportFallbackOnly, ExportPath: samplePath},
			EmbeddingProfile: "profile-a",
			EmbeddingModel:   "manual-embedding",
			EmbeddingDim:     4,
		}),
		WithKVLiteSemanticProvider(NewHashEmbeddingProvider("manual-embedding", 4)),
	)
	store, ok := storeIface.(*KVLiteStore)
	if !ok {
		tb.Fatalf("expected *KVLiteStore, got %T", storeIface)
	}
	return store
}

func TestSemanticANNAuditTrailAccumulatesStats(t *testing.T) {
	audit := newSemanticANNAuditTrail(SemanticANNAuditConfig{})
	audit.Observe(SemanticANNSampleRecord{
		DirectDecision: semanticANNDecisionHit,
		FinalSource:    SemanticRecallSourceANNBucketedLSH,
		FinalHit:       true,
		CandidateCount: 2,
		Top1Score:      0.93,
		Top2Score:      0.87,
		ScoreGap:       0.06,
	})
	audit.Observe(SemanticANNSampleRecord{
		DirectDecision: semanticANNDecisionFallback,
		FallbackReason: semanticANNDirectFallbackReason(semanticANNReasonScoreGapTooSmall),
		FinalSource:    SemanticRecallSourceANNFallback,
		FinalHit:       true,
		CandidateCount: 3,
		Top1Score:      0.91,
		Top2Score:      0.90,
		ScoreGap:       0.01,
	})
	audit.Observe(SemanticANNSampleRecord{
		DirectDecision: semanticANNDecisionMiss,
		FallbackReason: semanticANNDirectFallbackReason(semanticANNReasonNoCandidates),
		FinalSource:    SemanticRecallSourceANNFallback,
		FinalHit:       false,
		CandidateCount: 0,
		Top1Score:      0,
	})

	snapshot := audit.Snapshot()
	if snapshot.Total != 3 {
		t.Fatalf("unexpected total count: %+v", snapshot)
	}
	if snapshot.DirectDecisionCounts[semanticANNDecisionHit] != 1 || snapshot.DirectDecisionCounts[semanticANNDecisionFallback] != 1 || snapshot.DirectDecisionCounts[semanticANNDecisionMiss] != 1 {
		t.Fatalf("unexpected decision counts: %+v", snapshot.DirectDecisionCounts)
	}
	if snapshot.FallbackReasonCounts[semanticANNReasonDirectRejectedFallbackBrute] != 2 {
		t.Fatalf("expected fallback wrapper count 2, got %+v", snapshot.FallbackReasonCounts)
	}
	if snapshot.FallbackReasonCounts[semanticANNReasonScoreGapTooSmall] != 1 || snapshot.FallbackReasonCounts[semanticANNReasonNoCandidates] != 1 {
		t.Fatalf("unexpected fallback reason counts: %+v", snapshot.FallbackReasonCounts)
	}
	if snapshot.PathCounts["ann_direct_hit"] != 1 || snapshot.PathCounts["brute_force_hit"] != 1 || snapshot.PathCounts["final_miss"] != 1 {
		t.Fatalf("unexpected path counts: %+v", snapshot.PathCounts)
	}
	if snapshot.CandidateCount.Count != 3 || snapshot.Top1Score.Count != 3 || snapshot.Top2Score.Count != 2 || snapshot.ScoreGap.Count != 2 {
		t.Fatalf("unexpected numeric summary counts: %+v %+v %+v %+v", snapshot.CandidateCount, snapshot.Top1Score, snapshot.Top2Score, snapshot.ScoreGap)
	}
}

func TestSemanticANNSampleExportFallbackOnly(t *testing.T) {
	backend := newMemoryKVLiteBackend()
	samplePath := filepath.Join(t.TempDir(), "semantic-ann-samples.jsonl")
	store := newSemanticANNSampleTestStore(t, backend, samplePath, true)
	defer store.Close()

	queryVector := semanticANNUnitVector(1.0)
	request := LookupRequest{
		Question:            "sample export query",
		NormalizedQuestion:  NormalizeQuestion("sample export query"),
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               BuildScope("deepseek", "deepseek-chat", "v1-direct"),
		SemanticEnabled:     true,
		SemanticMode:        SemanticRecallModeANN,
		SemanticTopK:        2,
		SemanticThreshold:   0.80,
		SemanticANNTopK:     2,
		SemanticCorpusLimit: 16,
		Metadata: map[string]string{
			"request_id": "req-sample",
			"session_id": "sess-sample",
		},
	}

	// Direct hit: should not be exported when fallback-only sampling is enabled.
	directScope := request.Scope
	directTop1 := newCandidateQARecord("sample export direct top1", "direct answer", "deepseek", "deepseek-chat", directScope)
	directTop2 := newCandidateQARecord("sample export direct top2", "direct distractor", "deepseek", "deepseek-chat", directScope)
	directTop1ID := seedSemanticRecordWithVector(t, store, backend, directTop1, semanticANNUnitVector(1.0), "manual-embedding")
	directTop2ID := seedSemanticRecordWithVector(t, store, backend, directTop2, semanticANNUnitVector(0.75), "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, directScope, []string{directTop1ID, directTop2ID}, time.Minute)
	installSemanticANNIndex(t, store, directScope, 16, manualShadowIndexForVector(directScope, 16, queryVector, map[string]*llm.QARecord{
		directTop1ID: directTop1,
		directTop2ID: directTop2,
	}, map[string][]float32{
		directTop1ID: semanticANNUnitVector(1.0),
		directTop2ID: semanticANNUnitVector(0.75),
	}))
	directResult, hit, err := store.lookupBySemanticANNWithVector(context.Background(), request, queryVector)
	if err != nil {
		t.Fatalf("direct ann lookup: %v", err)
	}
	if !hit || !directResult.Hit {
		t.Fatalf("expected direct ANN hit, got %+v", directResult)
	}
	if _, err := os.Stat(samplePath); !os.IsNotExist(err) {
		t.Fatalf("expected no sample export for direct hit, got err=%v", err)
	}

	// Fallback hit: should be exported.
	fallbackScope := BuildScope("deepseek", "deepseek-chat", "v1-fallback")
	fallbackRequest := request
	fallbackRequest.Scope = fallbackScope
	fallbackTop1 := newCandidateQARecord("sample export fallback top1", "fallback answer", "deepseek", "deepseek-chat", fallbackScope)
	fallbackTop2 := newCandidateQARecord("sample export fallback top2", "fallback distractor", "deepseek", "deepseek-chat", fallbackScope)
	fallbackTop1ID := seedSemanticRecordWithVector(t, store, backend, fallbackTop1, semanticANNUnitVector(1.0), "manual-embedding")
	fallbackTop2ID := seedSemanticRecordWithVector(t, store, backend, fallbackTop2, semanticANNUnitVector(0.98), "manual-embedding")
	writeSemanticScopeIndex(t, backend, store, fallbackScope, []string{fallbackTop1ID, fallbackTop2ID}, time.Minute)
	installSemanticANNIndex(t, store, fallbackScope, 16, manualShadowIndexForVector(fallbackScope, 16, queryVector, map[string]*llm.QARecord{
		fallbackTop1ID: fallbackTop1,
		fallbackTop2ID: fallbackTop2,
	}, map[string][]float32{
		fallbackTop1ID: semanticANNUnitVector(1.0),
		fallbackTop2ID: semanticANNUnitVector(0.98),
	}))
	fallbackResult, fallbackHit, err := store.lookupBySemanticANNWithVector(context.Background(), fallbackRequest, queryVector)
	if err != nil {
		t.Fatalf("fallback ann lookup: %v", err)
	}
	if !fallbackHit || !fallbackResult.Hit {
		t.Fatalf("expected fallback ANN hit, got %+v", fallbackResult)
	}

	// Final miss: should also be exported in fallback-only mode.
	missRequest := request
	missRequest.Scope = BuildScope("deepseek", "deepseek-chat", "v1-miss")
	missResult, missHit, err := store.lookupBySemanticANNWithVector(context.Background(), missRequest, queryVector)
	if err != nil {
		t.Fatalf("miss ann lookup: %v", err)
	}
	if missHit || missResult.Hit {
		t.Fatalf("expected final miss, got %+v", missResult)
	}

	content, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("read sample file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 exported samples, got %d: %s", len(lines), string(content))
	}

	var exported []map[string]json.RawMessage
	for _, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode sample record: %v", err)
		}
		exported = append(exported, record)
	}

	requiredFields := []string{
		"query",
		"normalized_query",
		"semantic_mode",
		"semantic_mode_used",
		"top_k",
		"threshold",
		"min_score_gap",
		"candidate_count",
		"top1_score",
		"top2_score",
		"score_gap",
		"direct_decision",
		"fallback_reason",
		"final_source",
		"final_hit",
		"selected_embedding_provider",
		"selected_embedding_model",
		"actual_embedding_provider",
		"actual_embedding_model",
		"scope",
		"corpus_limit",
	}
	for _, field := range requiredFields {
		if _, ok := exported[0][field]; !ok {
			t.Fatalf("expected sample field %q to be exported, record=%v", field, exported[0])
		}
	}

	var fallbackSample SemanticANNSampleRecord
	if err := json.Unmarshal([]byte(lines[0]), &fallbackSample); err != nil {
		t.Fatalf("decode fallback sample: %v", err)
	}
	if fallbackSample.DirectDecision != semanticANNDecisionFallback || !fallbackSample.FinalHit || fallbackSample.FinalSource != SemanticRecallSourceANNFallback {
		t.Fatalf("unexpected fallback sample: %+v", fallbackSample)
	}
	if fallbackSample.SelectedEmbeddingProvider != "profile-a" || fallbackSample.SelectedEmbeddingModel != "manual-embedding" {
		t.Fatalf("unexpected selected embedding fields: %+v", fallbackSample)
	}
	if fallbackSample.ActualEmbeddingProvider != "manual-embedding" || fallbackSample.ActualEmbeddingModel != "manual-embedding" {
		t.Fatalf("unexpected actual embedding fields: %+v", fallbackSample)
	}
	if fallbackSample.CandidateCount != 2 || fallbackSample.Top1Score <= 0 || fallbackSample.Top2Score <= 0 || fallbackSample.ScoreGap <= 0 {
		t.Fatalf("unexpected fallback sample scores: %+v", fallbackSample)
	}
	if fallbackSample.Scope != fallbackScope || fallbackSample.CorpusLimit != 16 {
		t.Fatalf("unexpected fallback scope/corpus: %+v", fallbackSample)
	}

	var missSample SemanticANNSampleRecord
	if err := json.Unmarshal([]byte(lines[1]), &missSample); err != nil {
		t.Fatalf("decode miss sample: %v", err)
	}
	if missSample.FinalHit {
		t.Fatalf("expected miss sample final_hit=false, got %+v", missSample)
	}
	if missSample.DirectDecision != semanticANNDecisionMiss {
		t.Fatalf("unexpected miss sample decision: %+v", missSample)
	}
}
