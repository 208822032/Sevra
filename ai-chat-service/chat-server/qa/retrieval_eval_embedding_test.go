package qa

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/pkg/config"
)

type countingEmbeddingProvider struct {
	name  string
	mu    sync.Mutex
	calls int
	texts []string
}

func (p *countingEmbeddingProvider) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (p *countingEmbeddingProvider) Capabilities() llm.CapabilitySet {
	return llm.CapabilitySet{SupportsEmbeddings: true}
}

func (p *countingEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p == nil {
		return nil, errors.New("provider is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.texts = append(p.texts, texts...)
	vectors := make([][]float32, 0, len(texts))
	for _, text := range texts {
		seed := float32(len(strings.TrimSpace(text)))
		vectors = append(vectors, []float32{seed, 1, 0, 0})
	}
	return vectors, nil
}

func TestResolveRetrievalEvalEmbeddingSource(t *testing.T) {
	t.Run("placeholder default", func(t *testing.T) {
		source, err := ResolveRetrievalEvalEmbeddingSource(nil, "", "")
		if err != nil {
			t.Fatalf("resolve placeholder embedding source: %v", err)
		}
		if source.Mode != RetrievalEvalEmbeddingModePlaceholder {
			t.Fatalf("unexpected embedding mode: %s", source.Mode)
		}
		if source.Provider == nil {
			t.Fatalf("expected placeholder provider")
		}
		if source.ProfileName != "placeholder-hash-embedding" {
			t.Fatalf("unexpected placeholder profile: %s", source.ProfileName)
		}
		if source.ProviderName != "placeholder-hash-embedding" {
			t.Fatalf("unexpected placeholder provider name: %s", source.ProviderName)
		}
	})

	t.Run("provider profile", func(t *testing.T) {
		cnf := &config.Config{
			Providers: []provider.ProviderProfile{
				{
					Name:               "mock-embeddings",
					DisplayName:        "Mock Embeddings",
					Kind:               "openai-compatible",
					BaseURL:            "https://example.invalid/v1",
					APIKey:             "test-key",
					DefaultModel:       "text-embedding-ada-002",
					EmbeddingModel:     "text-embedding-ada-002",
					SupportsEmbeddings: true,
					Enabled:            true,
					Priority:           100,
				},
			},
		}
		source, err := ResolveRetrievalEvalEmbeddingSource(cnf, string(RetrievalEvalEmbeddingModeProvider), "mock-embeddings")
		if err != nil {
			t.Fatalf("resolve provider embedding source: %v", err)
		}
		if source.Mode != RetrievalEvalEmbeddingModeProvider {
			t.Fatalf("unexpected embedding mode: %s", source.Mode)
		}
		if source.ProfileName != "mock-embeddings" {
			t.Fatalf("unexpected profile name: %s", source.ProfileName)
		}
		if source.ProviderName != "mock-embeddings" {
			t.Fatalf("unexpected provider name: %s", source.ProviderName)
		}
		if source.Model != "text-embedding-ada-002" {
			t.Fatalf("unexpected embedding model: %s", source.Model)
		}
		if source.Provider == nil {
			t.Fatalf("expected provider-backed embedding source")
		}
	})

	t.Run("missing profile errors", func(t *testing.T) {
		cnf := &config.Config{
			Providers: []provider.ProviderProfile{
				{
					Name:         "mock-chat",
					DisplayName:  "Mock Chat",
					Kind:         "openai-compatible",
					BaseURL:      "https://example.invalid/v1",
					APIKey:       "test-key",
					DefaultModel: "gpt-4o-mini",
					Enabled:      true,
					Priority:     100,
				},
			},
		}
		_, err := ResolveRetrievalEvalEmbeddingSource(cnf, string(RetrievalEvalEmbeddingModeProvider), "missing-profile")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected missing profile error, got %v", err)
		}
	})

	t.Run("profile without embeddings errors", func(t *testing.T) {
		cnf := &config.Config{
			Providers: []provider.ProviderProfile{
				{
					Name:               "mock-chat",
					DisplayName:        "Mock Chat",
					Kind:               "openai-compatible",
					BaseURL:            "https://example.invalid/v1",
					APIKey:             "test-key",
					DefaultModel:       "gpt-4o-mini",
					SupportsEmbeddings: false,
					Enabled:            true,
					Priority:           100,
				},
			},
		}
		_, err := ResolveRetrievalEvalEmbeddingSource(cnf, string(RetrievalEvalEmbeddingModeProvider), "mock-chat")
		if err == nil || !strings.Contains(err.Error(), "does not support embeddings") {
			t.Fatalf("expected unsupported embeddings error, got %v", err)
		}
	})
}

func TestEvaluateRetrievalSuiteWithProviderEmbeddingSourceUsesCachedProvider(t *testing.T) {
	countingProvider := &countingEmbeddingProvider{name: "mock-embeddings"}
	source := RetrievalEvalEmbeddingSource{
		Mode:         RetrievalEvalEmbeddingModeProvider,
		ProfileName:  "mock-embeddings",
		ProviderName: "mock-embeddings",
		Model:        "mock-embedding-model",
		Provider:     newCachedEmbeddingProvider(countingProvider),
	}
	suite := &RetrievalEvalSuite{
		Name:    "provider-cache-probe",
		Version: "v1",
		DefaultScope: RetrievalEvalScope{
			Provider:      "deepseek",
			Model:         "deepseek-chat",
			PromptVersion: "v1",
		},
		Seeds: []RetrievalEvalSeed{
			{
				ID:       "seed-1",
				Question: "How do I reset the cache safely?",
				Answer:   "reset-cache",
			},
		},
		Cases: []RetrievalEvalCase{
			{
				ID:               "exact-reset-cache",
				Query:            "How do I reset the cache safely?",
				ExpectedBehavior: "hit",
				ExpectedLayer:    "exact",
				ExpectedAnswerID: "seed-1",
			},
		},
		ParameterSets: []RetrievalEvalParameterSet{
			{
				Name:                    "baseline",
				CandidateTopK:           6,
				CandidateScoreThreshold: 0.62,
				SemanticTopK:            4,
				SemanticScoreThreshold:  0.75,
				SemanticCorpusLimit:     32,
			},
			{
				Name:                    "wide",
				CandidateTopK:           8,
				CandidateScoreThreshold: 0.58,
				SemanticTopK:            6,
				SemanticScoreThreshold:  0.70,
				SemanticCorpusLimit:     48,
			},
		},
	}
	report, err := EvaluateRetrievalSuiteWithOptions(context.Background(), suite, "inline", RetrievalEvalOptions{EmbeddingSource: source})
	if err != nil {
		t.Fatalf("evaluate provider-backed suite: %v", err)
	}
	if report.EmbeddingMode != string(RetrievalEvalEmbeddingModeProvider) {
		t.Fatalf("unexpected report embedding mode: %s", report.EmbeddingMode)
	}
	if report.EmbeddingProfile != "mock-embeddings" {
		t.Fatalf("unexpected report embedding profile: %s", report.EmbeddingProfile)
	}
	if report.EmbeddingProvider != "mock-embeddings" {
		t.Fatalf("unexpected report embedding provider: %s", report.EmbeddingProvider)
	}
	if report.EmbeddingModel != "mock-embedding-model" {
		t.Fatalf("unexpected report embedding model: %s", report.EmbeddingModel)
	}
	if !strings.Contains(report.EmbeddingSource, "mode=provider") {
		t.Fatalf("expected provider embedding source description, got %s", report.EmbeddingSource)
	}
	if len(report.Results) != 2 {
		t.Fatalf("expected two parameter reports, got %d", len(report.Results))
	}
	if report.ANNRecommendation == "" {
		t.Fatalf("expected top-level recommendation")
	}
	if report.SemanticMode != string(SemanticRecallModeBruteForce) {
		t.Fatalf("unexpected default semantic mode: %s", report.SemanticMode)
	}
	if report.ComparisonSemanticMode != string(SemanticRecallModeANN) {
		t.Fatalf("unexpected comparison semantic mode: %s", report.ComparisonSemanticMode)
	}
	if report.SemanticComparison.SelectedMode != string(SemanticRecallModeBruteForce) || report.SemanticComparison.ComparisonMode != string(SemanticRecallModeANN) {
		t.Fatalf("unexpected top-level semantic comparison: %+v", report.SemanticComparison)
	}
	if report.Results[0].EmbeddingMode != string(RetrievalEvalEmbeddingModeProvider) {
		t.Fatalf("unexpected parameter embedding mode: %s", report.Results[0].EmbeddingMode)
	}
	if report.Results[0].SemanticMode != string(SemanticRecallModeBruteForce) || report.Results[0].ComparisonSemanticMode != string(SemanticRecallModeANN) {
		t.Fatalf("unexpected parameter semantic modes: %+v", report.Results[0])
	}
	if report.Results[0].SemanticComparison.SelectedMode != string(SemanticRecallModeBruteForce) || report.Results[0].SemanticComparison.ComparisonMode != string(SemanticRecallModeANN) {
		t.Fatalf("unexpected parameter semantic comparison: %+v", report.Results[0].SemanticComparison)
	}
	if report.Results[0].ANNRecommendation == "" {
		t.Fatalf("expected parameter recommendation")
	}
	annReport, err := EvaluateRetrievalSuiteWithOptions(context.Background(), suite, "inline", RetrievalEvalOptions{EmbeddingSource: source, SemanticMode: SemanticRecallModeANN})
	if err != nil {
		t.Fatalf("evaluate provider-backed suite in ann mode: %v", err)
	}
	if annReport.SemanticMode != string(SemanticRecallModeANN) || annReport.ComparisonSemanticMode != string(SemanticRecallModeBruteForce) {
		t.Fatalf("unexpected ann report semantic modes: %+v", annReport)
	}
	if countingProvider.calls != 1 {
		t.Fatalf("expected cached provider to be called once, got %d calls with texts %v", countingProvider.calls, countingProvider.texts)
	}
	if len(countingProvider.texts) != 1 || countingProvider.texts[0] != "How do I reset the cache safely?" {
		t.Fatalf("unexpected provider texts: %v", countingProvider.texts)
	}
}

func TestResolveRetrievalEvalEmbeddingSourceProviderModeFailsWithoutProfile(t *testing.T) {
	cnf := &config.Config{
		Providers: []provider.ProviderProfile{
			{
				Name:               "mock-embeddings",
				DisplayName:        "Mock Embeddings",
				Kind:               "openai-compatible",
				BaseURL:            "https://example.invalid/v1",
				APIKey:             "test-key",
				DefaultModel:       "text-embedding-ada-002",
				EmbeddingModel:     "text-embedding-ada-002",
				SupportsEmbeddings: true,
				Enabled:            true,
				Priority:           100,
			},
		},
	}
	_, err := ResolveRetrievalEvalEmbeddingSource(cnf, string(RetrievalEvalEmbeddingModeProvider), "")
	if err == nil || !strings.Contains(err.Error(), "embedding provider mode requires") {
		t.Fatalf("expected missing profile error, got %v", err)
	}
}

func TestOpenAICompatibleEmbeddingsProfileNormalization(t *testing.T) {
	profile := provider.ProviderProfile{
		Name:               "mock-embeddings",
		DisplayName:        "Mock Embeddings",
		Kind:               "openai-compatible",
		BaseURL:            "https://example.invalid/v1",
		APIKey:             "test-key",
		DefaultModel:       "text-embedding-ada-002",
		EmbeddingModel:     "text-embedding-ada-002",
		SupportsEmbeddings: true,
		Enabled:            true,
		Priority:           100,
	}
	normalized := profile.Normalize()
	if normalized.EmbeddingModel != "text-embedding-ada-002" {
		t.Fatalf("unexpected embedding model after normalize: %s", normalized.EmbeddingModel)
	}
	if normalized.DefaultEmbeddingModel != "text-embedding-ada-002" {
		t.Fatalf("unexpected default embedding model after normalize: %s", normalized.DefaultEmbeddingModel)
	}
	if normalized.DefaultModel != "text-embedding-ada-002" {
		t.Fatalf("unexpected default model after normalize: %s", normalized.DefaultModel)
	}
	if normalized.DisplayNameOrDefault() != "Mock Embeddings" {
		t.Fatalf("unexpected display name: %s", normalized.DisplayNameOrDefault())
	}
}

func TestBenchmarkRecommendationOrdering(t *testing.T) {
	stay, stayReason := decideRetrievalEvalRecommendation(RetrievalEvalEmbeddingModeProvider, RetrievalEvalParameterReport{TotalCases: 8, SemanticOnlyWins: 0})
	if stay != "stay_with_lexical" || !strings.Contains(stayReason, "semantic_only_wins=0") {
		t.Fatalf("unexpected stay recommendation: %s %s", stay, stayReason)
	}
	ann, annReason := decideRetrievalEvalRecommendation(RetrievalEvalEmbeddingModeProvider, RetrievalEvalParameterReport{TotalCases: 8, SemanticOnlyWins: 3, CandidateAvgLatencyMs: 1, SemanticAvgLatencyMs: 1.4, WrongHitRatio: 0.02})
	if ann != "ann_prototype_candidate" || !strings.Contains(annReason, "semantic_only_wins=3") {
		t.Fatalf("unexpected ann recommendation: %s %s", ann, annReason)
	}
}

func TestCombineRetrievalEvalRecommendations(t *testing.T) {
	recommendation, reason := combineRetrievalEvalRecommendations([]RetrievalEvalParameterReport{
		{ParameterSet: RetrievalEvalParameterSet{Name: "baseline"}, ANNRecommendation: "stay_with_lexical", ANNRecommendationReason: "no semantic wins"},
		{ParameterSet: RetrievalEvalParameterSet{Name: "wide"}, ANNRecommendation: "ann_prototype_candidate", ANNRecommendationReason: "semantic-only wins"},
	})
	if recommendation != "ann_prototype_candidate" {
		t.Fatalf("unexpected recommendation: %s", recommendation)
	}
	if !strings.Contains(reason, "parameter_set=wide") {
		t.Fatalf("unexpected recommendation reason: %s", reason)
	}
}
