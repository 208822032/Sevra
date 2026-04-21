package qa

import (
	"strings"
	"testing"
)

func TestAnalyzeSemanticANNSamplesConclusionAndTopQueries(t *testing.T) {
	t.Run("coverage issue", func(t *testing.T) {
		samples := []SemanticANNSampleRecord{
			{
				Query:           "how to clear cache",
				NormalizedQuery: "how to clear cache",
				DirectDecision:  semanticANNDecisionMiss,
				FallbackReason:  semanticANNDirectFallbackReason(semanticANNReasonNoCandidates),
				FinalSource:     SemanticRecallSourceANNFallback,
				FinalHit:        false,
			},
			{
				Query:           "how to clear cache",
				NormalizedQuery: "how to clear cache",
				DirectDecision:  semanticANNDecisionFallback,
				FallbackReason:  semanticANNDirectFallbackReason(semanticANNReasonNoCandidates),
				FinalSource:     SemanticRecallSourceANNFallback,
				FinalHit:        true,
				CandidateCount:  2,
				Top1Score:       0.82,
				Top2Score:       0.61,
				ScoreGap:        0.21,
			},
			{
				Query:           "why does login fail",
				NormalizedQuery: "why does login fail",
				DirectDecision:  semanticANNDecisionFallback,
				FallbackReason:  semanticANNDirectFallbackReason(semanticANNReasonScoreBelowThreshold),
				FinalSource:     SemanticRecallSourceANNFallback,
				FinalHit:        false,
				CandidateCount:  1,
				Top1Score:       0.77,
			},
		}

		report := AnalyzeSemanticANNSamples(samples, 3)
		if !strings.Contains(report.Conclusion, "覆盖问题") {
			t.Fatalf("expected coverage-oriented conclusion, got %q", report.Conclusion)
		}
		if report.FallbackReasonCounts[semanticANNReasonNoCandidates] != 2 {
			t.Fatalf("unexpected no-candidates count: %+v", report.FallbackReasonCounts)
		}
		if len(report.TopQueriesByReason[semanticANNReasonNoCandidates]) == 0 {
			t.Fatalf("expected top queries for no-candidates, got %+v", report.TopQueriesByReason)
		}
		text := report.RenderText()
		if !strings.Contains(text, semanticANNReasonScoreBelowThreshold) || !strings.Contains(text, "覆盖问题") {
			t.Fatalf("rendered report missing expected content: %s", text)
		}
	})

	t.Run("sorting issue", func(t *testing.T) {
		samples := []SemanticANNSampleRecord{
			{
				Query:           "how to reset token",
				NormalizedQuery: "how to reset token",
				DirectDecision:  semanticANNDecisionHit,
				FinalSource:     SemanticRecallSourceANNBucketedLSH,
				FinalHit:        true,
				CandidateCount:  3,
				Top1Score:       0.95,
				Top2Score:       0.92,
				ScoreGap:        0.03,
			},
			{
				Query:           "how to reset token",
				NormalizedQuery: "how to reset token",
				DirectDecision:  semanticANNDecisionFallback,
				FallbackReason:  semanticANNDirectFallbackReason(semanticANNReasonScoreGapTooSmall),
				FinalSource:     SemanticRecallSourceANNFallback,
				FinalHit:        true,
				CandidateCount:  3,
				Top1Score:       0.94,
				Top2Score:       0.91,
				ScoreGap:        0.03,
			},
			{
				Query:           "how to reset token",
				NormalizedQuery: "how to reset token",
				DirectDecision:  semanticANNDecisionFallback,
				FallbackReason:  semanticANNDirectFallbackReason(semanticANNReasonAmbiguousTopK),
				FinalSource:     SemanticRecallSourceANNFallback,
				FinalHit:        false,
				CandidateCount:  4,
				Top1Score:       0.93,
				Top2Score:       0.91,
				ScoreGap:        0.02,
			},
			{
				Query:           "how to reset token",
				NormalizedQuery: "how to reset token",
				DirectDecision:  semanticANNDecisionFallback,
				FallbackReason:  semanticANNDirectFallbackReason(semanticANNReasonScoreGapTooSmall),
				FinalSource:     SemanticRecallSourceANNFallback,
				FinalHit:        true,
				CandidateCount:  2,
				Top1Score:       0.91,
				Top2Score:       0.89,
				ScoreGap:        0.02,
			},
		}

		report := AnalyzeSemanticANNSamples(samples, 2)
		if !strings.Contains(report.Conclusion, "排序不稳问题") {
			t.Fatalf("expected sorting-oriented conclusion, got %q", report.Conclusion)
		}
		if report.FallbackReasonCounts[semanticANNReasonScoreGapTooSmall] != 2 || report.FallbackReasonCounts[semanticANNReasonAmbiguousTopK] != 1 {
			t.Fatalf("unexpected sorting fallback counts: %+v", report.FallbackReasonCounts)
		}
		queries := report.TopQueriesByReason[semanticANNReasonScoreGapTooSmall]
		if len(queries) == 0 || queries[0].Query != "how to reset token" {
			t.Fatalf("unexpected top queries for score-gap reason: %+v", queries)
		}
		if report.ScoreGap.Count != 4 || report.Top1Score.Count != 4 {
			t.Fatalf("unexpected distribution summary counts: %+v %+v", report.ScoreGap, report.Top1Score)
		}
		text := report.RenderText()
		if !strings.Contains(text, semanticANNReasonScoreGapTooSmall) || !strings.Contains(text, semanticANNReasonAmbiguousTopK) {
			t.Fatalf("rendered report missing expected sorting content: %s", text)
		}
	})
}
