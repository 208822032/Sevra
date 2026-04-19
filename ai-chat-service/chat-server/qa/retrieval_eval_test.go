package qa

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRetrievalEvalSuite(t *testing.T) {
	suite, err := LoadRetrievalEvalSuite(filepath.Join("testdata", "retrieval_eval_suite.v1.json"))
	if err != nil {
		t.Fatalf("load retrieval eval suite: %v", err)
	}
	if suite.Name != "ai-chat retrieval quality suite" {
		t.Fatalf("unexpected suite name: %s", suite.Name)
	}
	if suite.Version != "v1" {
		t.Fatalf("unexpected suite version: %s", suite.Version)
	}
	if len(suite.Seeds) < 6 {
		t.Fatalf("expected seed records, got %d", len(suite.Seeds))
	}
	if len(suite.Cases) < 8 {
		t.Fatalf("expected evaluation cases, got %d", len(suite.Cases))
	}
	if len(suite.ParameterSets) < 3 {
		t.Fatalf("expected parameter sets, got %d", len(suite.ParameterSets))
	}
}

func TestLoadRetrievalEvalSuiteRejectsInvalidInputs(t *testing.T) {
	t.Run("missing required fields", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invalid.json")
		content := `{"name":"invalid","version":"v1","default_scope":{"provider":"deepseek","model":"deepseek-chat","prompt_version":"v1"},"seeds":[{"id":"seed-1","question":"Q","answer":"A"}],"cases":[],"parameter_sets":[{"name":"baseline","candidate_top_k":6,"candidate_score_threshold":0.62,"semantic_top_k":4,"semantic_score_threshold":0.75,"semantic_corpus_limit":32}]}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write invalid suite: %v", err)
		}
		_, err := LoadRetrievalEvalSuite(path)
		if err == nil || !strings.Contains(err.Error(), "cases are empty") {
			t.Fatalf("expected missing cases error, got %v", err)
		}
	})

	t.Run("invalid expected layer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invalid-layer.json")
		content := `{"name":"invalid","version":"v1","default_scope":{"provider":"deepseek","model":"deepseek-chat","prompt_version":"v1"},"seeds":[{"id":"seed-1","question":"Q","answer":"A"}],"cases":[{"id":"case-1","query":"Q","expected_behavior":"hit","expected_layer":"mystery","expected_answer_id":"seed-1"}],"parameter_sets":[{"name":"baseline","candidate_top_k":6,"candidate_score_threshold":0.62,"semantic_top_k":4,"semantic_score_threshold":0.75,"semantic_corpus_limit":32}]}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write invalid suite: %v", err)
		}
		_, err := LoadRetrievalEvalSuite(path)
		if err == nil || !strings.Contains(err.Error(), "unsupported expected_layer") {
			t.Fatalf("expected invalid layer error, got %v", err)
		}
	})
}

func TestEvaluateRetrievalEvalSuiteProducesStructuredReport(t *testing.T) {
	suite, err := LoadRetrievalEvalSuite(filepath.Join("testdata", "retrieval_eval_suite.v1.json"))
	if err != nil {
		t.Fatalf("load retrieval eval suite: %v", err)
	}
	report, err := EvaluateRetrievalSuite(context.Background(), suite, filepath.Join("testdata", "retrieval_eval_suite.v1.json"))
	if err != nil {
		t.Fatalf("evaluate retrieval suite: %v", err)
	}
	if len(report.Results) != len(suite.ParameterSets) {
		t.Fatalf("expected one report per parameter set, got %d", len(report.Results))
	}
	if report.EmbeddingMode != string(RetrievalEvalEmbeddingModePlaceholder) {
		t.Fatalf("expected placeholder embedding mode, got %s", report.EmbeddingMode)
	}
	if report.ANNRecommendation == "" {
		t.Fatalf("expected top-level recommendation")
	}
	baseline := report.Results[0]
	if baseline.TotalCases != len(suite.Cases) {
		t.Fatalf("unexpected total cases: %d", baseline.TotalCases)
	}
	if baseline.EmbeddingMode != string(RetrievalEvalEmbeddingModePlaceholder) {
		t.Fatalf("expected placeholder embedding mode in parameter report, got %s", baseline.EmbeddingMode)
	}
	if baseline.AvgLatencyMs <= 0 {
		t.Fatalf("expected average latency to be recorded, got %.4f", baseline.AvgLatencyMs)
	}
	if baseline.CandidateAvgLatencyMs <= 0 {
		t.Fatalf("expected candidate latency to be recorded, got %.4f", baseline.CandidateAvgLatencyMs)
	}
	if baseline.SemanticAvgLatencyMs <= 0 {
		t.Fatalf("expected semantic latency to be recorded, got %.4f", baseline.SemanticAvgLatencyMs)
	}
	if baseline.FallbackToLLMRatio < 0 || baseline.FallbackToLLMRatio > 1 {
		t.Fatalf("unexpected fallback ratio: %.4f", baseline.FallbackToLLMRatio)
	}
	if baseline.WrongHitRatio < 0 || baseline.WrongHitRatio > 1 {
		t.Fatalf("unexpected wrong-hit ratio: %.4f", baseline.WrongHitRatio)
	}
	if strings.TrimSpace(report.RenderText()) == "" {
		t.Fatalf("expected text report")
	}
	if strings.TrimSpace(report.RenderMarkdown()) == "" {
		t.Fatalf("expected markdown report")
	}
	jsonPath := filepath.Join(t.TempDir(), "report.json")
	mdPath := filepath.Join(t.TempDir(), "report.md")
	if err := report.WriteJSON(jsonPath); err != nil {
		t.Fatalf("write json report: %v", err)
	}
	if err := report.WriteMarkdown(mdPath); err != nil {
		t.Fatalf("write markdown report: %v", err)
	}
	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json report: %v", err)
	}
	var parsed RetrievalEvalReport
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("unmarshal json report: %v", err)
	}
	if len(parsed.Results) != len(report.Results) {
		t.Fatalf("unexpected parsed result count: %d", len(parsed.Results))
	}
}

func TestEvaluateRetrievalEvalSuiteCapturesWrongHits(t *testing.T) {
	suite := &RetrievalEvalSuite{
		Name:    "wrong-hit-probe",
		Version: "v1",
		DefaultScope: RetrievalEvalScope{
			Provider:      "deepseek",
			Model:         "deepseek-chat",
			PromptVersion: "v1",
		},
		Seeds: []RetrievalEvalSeed{
			{
				ID:       "seed-canonical",
				Question: "How do I keep the canonical answer?",
				Answer:   "canonical answer",
			},
		},
		Cases: []RetrievalEvalCase{
			{
				ID:               "intentional-wrong-hit",
				Query:            "How do I keep the canonical answer?",
				ExpectedBehavior: "miss",
				ExpectedLayer:    "miss",
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
		},
	}
	report, err := EvaluateRetrievalSuite(context.Background(), suite, "inline")
	if err != nil {
		t.Fatalf("evaluate wrong-hit suite: %v", err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("expected one parameter report, got %d", len(report.Results))
	}
	result := report.Results[0]
	if result.WrongHitCount == 0 {
		t.Fatalf("expected a wrong hit to be captured")
	}
	if len(result.WrongHitCases) == 0 {
		t.Fatalf("expected wrong-hit cases to be populated")
	}
	if result.WrongHitCases[0].ActualLayer == "" {
		t.Fatalf("expected actual layer in wrong-hit case: %+v", result.WrongHitCases[0])
	}
	if strings.TrimSpace(result.WrongHitCases[0].MismatchReason) == "" {
		t.Fatalf("expected mismatch reason in wrong-hit case: %+v", result.WrongHitCases[0])
	}
}

func TestNormalizeSemanticMode(t *testing.T) {
	if normalizeSemanticMode("ANN") != SemanticRecallModeANN {
		t.Fatalf("expected ANN mode normalization")
	}
	if normalizeSemanticMode("brute_force") != SemanticRecallModeBruteForce {
		t.Fatalf("expected brute-force mode normalization")
	}
	if oppositeSemanticMode(SemanticRecallModeANN) != SemanticRecallModeBruteForce {
		t.Fatalf("expected opposite mode for ANN")
	}
	if oppositeSemanticMode(SemanticRecallModeBruteForce) != SemanticRecallModeANN {
		t.Fatalf("expected opposite mode for brute-force")
	}
}

func TestBuildRetrievalEvalSemanticComparison(t *testing.T) {
	results := []RetrievalEvalCaseResult{
		{
			CaseID:                      "ann-only",
			Query:                       "q1",
			ExpectedBehavior:            "hit",
			ExpectedAnswerID:            "seed-1",
			ExpectedAnswer:              "answer-1",
			ShadowSemanticHit:           true,
			ShadowSemanticLayer:         HitTypeSemantic,
			ShadowSemanticAnswerID:      "seed-1",
			ShadowSemanticAnswer:        "answer-1",
			ShadowSemanticTopScore:      0.91,
			SemanticLatencyMs:           1.200,
			ComparisonSemanticHit:       false,
			ComparisonSemanticLayer:     "miss",
			ComparisonSemanticAnswer:    "",
			ComparisonSemanticAnswerID:  "",
			ComparisonSemanticTopScore:  0.10,
			ComparisonSemanticLatencyMs: 2.100,
		},
		{
			CaseID:                      "bf-only",
			Query:                       "q2",
			ExpectedBehavior:            "hit",
			ExpectedAnswerID:            "seed-2",
			ExpectedAnswer:              "answer-2",
			ShadowSemanticHit:           false,
			ShadowSemanticLayer:         "miss",
			ShadowSemanticAnswerID:      "",
			ShadowSemanticAnswer:        "",
			ShadowSemanticTopScore:      0.11,
			SemanticLatencyMs:           1.000,
			ComparisonSemanticHit:       true,
			ComparisonSemanticLayer:     HitTypeSemantic,
			ComparisonSemanticAnswer:    "answer-2",
			ComparisonSemanticAnswerID:  "seed-2",
			ComparisonSemanticTopScore:  0.95,
			ComparisonSemanticLatencyMs: 1.900,
		},
		{
			CaseID:                      "overlap",
			Query:                       "q3",
			ExpectedBehavior:            "hit",
			ExpectedAnswerID:            "seed-3",
			ExpectedAnswer:              "answer-3",
			ShadowSemanticHit:           true,
			ShadowSemanticLayer:         HitTypeSemantic,
			ShadowSemanticAnswerID:      "seed-3",
			ShadowSemanticAnswer:        "answer-3",
			ShadowSemanticTopScore:      0.88,
			SemanticLatencyMs:           1.300,
			ComparisonSemanticHit:       true,
			ComparisonSemanticLayer:     HitTypeSemantic,
			ComparisonSemanticAnswer:    "answer-3",
			ComparisonSemanticAnswerID:  "seed-3",
			ComparisonSemanticTopScore:  0.87,
			ComparisonSemanticLatencyMs: 2.000,
		},
	}
	comparison := buildRetrievalEvalSemanticComparison(results, SemanticRecallModeANN, SemanticRecallModeBruteForce)
	if comparison.SelectedMode != SemanticRecallModeANN || comparison.ComparisonMode != SemanticRecallModeBruteForce {
		t.Fatalf("unexpected selected/comparison modes: %+v", comparison)
	}
	if comparison.ANNHitCount != 2 || comparison.BruteForceHitCount != 2 {
		t.Fatalf("unexpected hit counts: %+v", comparison)
	}
	if comparison.ANNOnlyWins != 1 || comparison.BruteForceOnlyWins != 1 || comparison.ANNVsBruteForceOverlap != 1 {
		t.Fatalf("unexpected win/overlap counts: %+v", comparison)
	}
	if len(comparison.DiffCases) != 2 {
		t.Fatalf("expected two diff cases, got %+v", comparison.DiffCases)
	}
	if comparison.ANNWrongHitRatio <= 0 || comparison.BruteForceWrongHitRatio <= 0 {
		t.Fatalf("expected wrong-hit ratios to be populated: %+v", comparison)
	}
	if comparison.ANNAvgLatencyMs <= 0 || comparison.BruteForceAvgLatencyMs <= 0 {
		t.Fatalf("expected latency summaries to be populated: %+v", comparison)
	}
}
