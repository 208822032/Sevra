package qa

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-chat-service/chat-server/llm"
)

type semanticEvalCase struct {
	Name           string
	Query          string
	ExpectedAnswer string
}

func TestKVLiteStoreSemanticVsLexicalEvaluationReport(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	backend := newMemoryKVLiteBackend()
	lexicalStore := mustKVLiteStore(t, NewKVLiteStoreWithBackend(backend, "test:semantic-eval", time.Minute))
	semanticStore := mustKVLiteStore(t, NewKVLiteStoreWithBackend(backend, "test:semantic-eval", time.Minute,
		WithKVLiteSemanticConfig(SemanticConfig{
			Enabled:        true,
			TopK:           4,
			Threshold:      0.75,
			CorpusLimit:    32,
			EmbeddingModel: "placeholder-hash-embedding",
			EmbeddingDim:   DefaultSemanticEmbeddingDim,
		}),
		WithKVLiteSemanticProvider(NewHashEmbeddingProvider("placeholder-hash-embedding", DefaultSemanticEmbeddingDim)),
	))

	providerName := "deepseek"
	modelName := "deepseek-chat"
	promptVersion := "v1"
	scope := BuildScope(providerName, modelName, promptVersion)
	records := []*llm.QARecord{
		semanticEvalRecord("How do I reset the cache safely?", "reset-cache", providerName, modelName, promptVersion),
		semanticEvalRecord("How do I clear the Redis cache safely?", "clear-cache", providerName, modelName, promptVersion),
		semanticEvalRecord("How do I rotate API keys in production?", "rotate-keys", providerName, modelName, promptVersion),
		semanticEvalRecord("How do I inspect semantic trace fields?", "trace-fields", providerName, modelName, promptVersion),
		semanticEvalRecord("How do I enable async backfill?", "backfill-on", providerName, modelName, promptVersion),
	}
	backfiller, ok := interface{}(semanticStore).(SemanticBackfiller)
	if !ok {
		t.Fatalf("semantic store does not implement semantic backfiller")
	}
	for _, record := range records {
		if err := lexicalStore.Upsert(ctx, record, time.Minute); err != nil {
			t.Fatalf("upsert lexical record: %v", err)
		}
		if err := backfiller.BackfillSemanticEmbedding(ctx, record); err != nil {
			t.Fatalf("backfill semantic embedding: %v", err)
		}
	}

	cases := []semanticEvalCase{
		{Name: "exact", Query: "How do I reset the cache safely?", ExpectedAnswer: "reset-cache"},
		{Name: "normalized", Query: "  HOW DO I RESET THE CACHE SAFELY!!!  ", ExpectedAnswer: "reset-cache"},
		{Name: "candidate", Query: "How can I safely clear Redis cache?", ExpectedAnswer: "clear-cache"},
		{Name: "trace", Query: "How do I inspect semantic trace fields?", ExpectedAnswer: "trace-fields"},
		{Name: "backfill", Query: "How do I enable async backfill?", ExpectedAnswer: "backfill-on"},
		{Name: "probe", Query: "What is the safest way to refresh stale cache entries?", ExpectedAnswer: "reset-cache"},
	}

	var report strings.Builder
	lexicalCorrect := 0
	semanticCorrect := 0
	combinedCorrect := 0
	semanticOnlyWins := 0
	lexicalOnlyWins := 0
	semanticScoreSum := 0.0
	semanticHitCount := 0

	for _, tc := range cases {
		req := LookupRequest{
			Question:               tc.Query,
			ExactQuestionHash:      ExactQuestionHash(tc.Query),
			NormalizedQuestion:     NormalizeQuestion(tc.Query),
			NormalizedQuestionHash: QuestionHash(tc.Query),
			Provider:               providerName,
			Model:                  modelName,
			PromptVersion:          promptVersion,
			Scope:                  scope,
			CandidateTopK:          DefaultCandidateTopK,
			CandidateMinScore:      DefaultCandidateMinScore,
			SemanticEnabled:        false,
			SemanticTopK:           4,
			SemanticThreshold:      0.75,
			SemanticCorpusLimit:    32,
		}
		lexicalResult, err := lexicalStore.Lookup(ctx, req)
		if err != nil {
			t.Fatalf("lexical lookup for %q: %v", tc.Name, err)
		}

		semanticReq := req
		semanticReq.SemanticEnabled = true
		semanticResult, semanticHit, err := semanticStore.lookupBySemantic(ctx, semanticReq)
		if err != nil {
			t.Fatalf("semantic lookup for %q: %v", tc.Name, err)
		}

		combinedResult, err := semanticStore.Lookup(ctx, semanticReq)
		if err != nil {
			t.Fatalf("combined lookup for %q: %v", tc.Name, err)
		}

		lexicalMatched := lexicalResult.Hit && strings.TrimSpace(lexicalResult.Answer) == tc.ExpectedAnswer
		semanticMatched := semanticHit && semanticResult.Hit && strings.TrimSpace(semanticResult.Answer) == tc.ExpectedAnswer
		combinedMatched := combinedResult.Hit && strings.TrimSpace(combinedResult.Answer) == tc.ExpectedAnswer

		if lexicalMatched {
			lexicalCorrect++
		}
		if semanticMatched {
			semanticCorrect++
			semanticHitCount++
			semanticScoreSum += semanticResult.SemanticTopScore
		}
		if combinedMatched {
			combinedCorrect++
		}
		if semanticMatched && !lexicalMatched {
			semanticOnlyWins++
		}
		if lexicalMatched && !semanticMatched {
			lexicalOnlyWins++
		}

		verdict := "miss"
		switch {
		case lexicalMatched && semanticMatched:
			verdict = "both"
		case lexicalMatched:
			verdict = "lexical-only"
		case semanticMatched:
			verdict = "semantic-only"
		}

		fmt.Fprintf(&report, "[semantic-eval] case=%s query=%q expected=%q lexical=%s semantic=%s combined=%s verdict=%s\n",
			tc.Name,
			tc.Query,
			tc.ExpectedAnswer,
			describeSemanticEvalLookup(lexicalResult),
			describeSemanticEvalLookup(semanticResult),
			describeSemanticEvalLookup(combinedResult),
			verdict,
		)
	}

	semanticAverage := 0.0
	if semanticHitCount > 0 {
		semanticAverage = semanticScoreSum / float64(semanticHitCount)
	}
	fmt.Fprintf(&report, "[semantic-eval] summary cases=%d lexical_correct=%d semantic_correct=%d combined_correct=%d semantic_only_wins=%d lexical_only_wins=%d semantic_avg_score=%.4f\n",
		len(cases), lexicalCorrect, semanticCorrect, combinedCorrect, semanticOnlyWins, lexicalOnlyWins, semanticAverage)

	t.Log(report.String())
}

func mustKVLiteStore(tb testing.TB, store Store) *KVLiteStore {
	tb.Helper()
	kvStore, ok := store.(*KVLiteStore)
	if !ok {
		tb.Fatalf("expected *KVLiteStore, got %T", store)
	}
	return kvStore
}

func semanticEvalRecord(question, answer, provider, model, promptVersion string) *llm.QARecord {
	scope := BuildScope(provider, model, promptVersion)
	record := newCandidateQARecord(question, answer, provider, model, scope)
	record.PromptVersion = promptVersion
	if record.Metadata == nil {
		record.Metadata = map[string]string{}
	}
	record.Metadata["prompt_version"] = promptVersion
	return record
}

func describeSemanticEvalLookup(result LookupResult) string {
	if !result.Hit {
		return "miss"
	}
	parts := []string{fmt.Sprintf("hit=%s", result.HitType)}
	if answer := strings.TrimSpace(result.Answer); answer != "" {
		parts = append(parts, fmt.Sprintf("answer=%q", answer))
	}
	switch result.HitType {
	case HitTypeCandidate:
		if result.CandidateTopScore > 0 {
			parts = append(parts, fmt.Sprintf("score=%.4f", result.CandidateTopScore))
		}
	case HitTypeSemantic:
		if result.SemanticTopScore > 0 {
			parts = append(parts, fmt.Sprintf("score=%.4f", result.SemanticTopScore))
		}
	}
	return strings.Join(parts, ",")
}
