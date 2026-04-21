package server

import (
	"strings"
	"time"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/qa"
	"ai-chat-service/pkg/config"
)

func semanticModeFromConfig(cnf *config.Config) string {
	if cnf == nil {
		return qa.SemanticRecallModeBruteForce
	}
	switch strings.ToLower(strings.TrimSpace(cnf.Retrieval.Semantic.Mode)) {
	case qa.SemanticRecallModeANN:
		return qa.SemanticRecallModeANN
	default:
		return qa.SemanticRecallModeBruteForce
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func semanticShadowConfigFromConfig(cnf *config.Config) qa.SemanticShadowConfig {
	shadow := qa.SemanticShadowConfig{
		TopK:           4,
		ScoreThreshold: 0.8,
		LogDiffOnly:    true,
		SampleRate:     1.0,
	}
	if cnf == nil {
		return shadow
	}
	semantic := cnf.Retrieval.Semantic.ShadowANN
	shadow.Enabled = semantic.Enabled
	if semantic.TopK > 0 {
		shadow.TopK = semantic.TopK
	}
	if semantic.ScoreThreshold > 0 {
		shadow.ScoreThreshold = semantic.ScoreThreshold
	}
	shadow.LogDiffOnly = semantic.LogDiffOnly
	if semantic.SampleRate > 0 {
		shadow.SampleRate = semantic.SampleRate
	}
	return shadow
}

func normalizeSemanticShadowAnswer(answer string) string {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return ""
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

func semanticShadowAnswerEquals(left, right string) bool {
	return normalizeSemanticShadowAnswer(left) == normalizeSemanticShadowAnswer(right)
}

func (a *app) finalizeSemanticLookupTrace(trace *llm.AnswerTrace, result qa.LookupResult) {
	if a == nil || trace == nil {
		return
	}
	trace.SemanticModeRequested = result.SemanticModeRequested
	trace.SemanticModeUsed = result.SemanticModeUsed
	trace.SemanticFallbackUsed = result.SemanticFallbackUsed
	trace.SemanticFallbackReason = result.SemanticFallbackReason
	trace.SemanticLatencyMS = result.SemanticLatencyMS
	trace.SemanticANNLatencyMS = result.SemanticANNLatencyMS
	trace.SemanticBruteForceLatencyMS = result.SemanticBruteForceLatencyMS
	trace.SemanticANNTopK = result.SemanticANNTopK
	trace.SemanticANNTopScore = result.SemanticANNTopScore
	trace.SemanticANNTop2Score = result.SemanticANNTop2Score
	trace.SemanticANNScoreGap = result.SemanticANNScoreGap
	trace.SemanticANNMinScoreGap = result.SemanticANNMinScoreGap
	trace.SemanticANNDecision = result.SemanticANNDecision
}

func (a *app) finalizeSemanticShadowTrace(trace *llm.AnswerTrace, result qa.LookupResult, finalAnswer string) {
	if a == nil || trace == nil {
		return
	}
	shadow := result.SemanticShadowTrace
	if !shadow.ShadowSemanticEnabled {
		return
	}
	shadow.ShadowSemanticSameAsFinal = semanticShadowAnswerEquals(finalAnswer, shadow.ShadowSemanticAnswer)
	trace.SemanticShadowTrace = shadow
	a.recordSemanticShadowObservation(result, shadow)
}

func (a *app) recordSemanticShadowObservation(result qa.LookupResult, shadow llm.SemanticShadowTrace) {
	if a == nil || !shadow.ShadowSemanticEnabled {
		return
	}
	if a.busMetrics != nil {
		a.busMetrics.IncAnnShadowQueriesCount()
		if shadow.ShadowSemanticHit {
			a.busMetrics.IncAnnShadowHitsCount()
		}
		if shadow.ShadowSemanticSameTop1 {
			a.busMetrics.IncAnnShadowSameAsBruteforceCount()
		}
		if shadow.ShadowSemanticError != "" {
			a.busMetrics.IncAnnShadowErrorsCount()
		}
		if shadow.ShadowSemanticFallbackUsed {
			a.busMetrics.IncAnnShadowFallbackCount()
		}
		if (result.Hit && !shadow.ShadowSemanticHit) || (!result.Hit && shadow.ShadowSemanticHit) || (result.Hit && shadow.ShadowSemanticHit && !shadow.ShadowSemanticSameTop1) || shadow.ShadowSemanticError != "" || shadow.ShadowSemanticFallbackUsed {
			a.busMetrics.IncAnnShadowDiffCount()
		}
		if result.Hit && !shadow.ShadowSemanticHit {
			a.busMetrics.IncAnnShadowMissVsBruteforceHitCount()
		}
		if !result.Hit && shadow.ShadowSemanticHit {
			a.busMetrics.IncAnnShadowHitVsBruteforceMissCount()
		}
		a.busMetrics.ObserveAnnShadowLatency(time.Duration(shadow.ShadowSemanticLatencyMS * float64(time.Millisecond)))
	}

	logDiffOnly := true
	if a.semanticShadowConfig.Enabled {
		logDiffOnly = a.semanticShadowConfig.LogDiffOnly
	}
	shouldLog := shadow.ShadowSemanticError != "" || shadow.ShadowSemanticFallbackUsed || shadow.ShadowSemanticDiffCategory == "bf_hit_ann_hit_diff_top1" || shadow.ShadowSemanticDiffCategory == "bf_hit_ann_miss" || shadow.ShadowSemanticDiffCategory == "bf_miss_ann_hit" || !shadow.ShadowSemanticSameAsFinal
	if logDiffOnly && !shouldLog {
		return
	}
	if a.log == nil {
		return
	}
	fields := map[string]interface{}{
		"request_id":                      a.semanticShadowRequestID,
		"session_id":                      a.sessionID,
		"query":                           a.semanticShadowQuery,
		"main_hit":                        result.Hit,
		"main_hit_type":                   result.HitType,
		"main_answer_id":                  firstNonEmptyString(result.CanonicalQuestionID, result.QuestionID),
		"main_answer":                     strings.TrimSpace(result.Answer),
		"main_semantic_top_score":         result.SemanticTopScore,
		"main_semantic_ann_top_k":         result.SemanticANNTopK,
		"main_semantic_ann_top_score":     result.SemanticANNTopScore,
		"main_semantic_ann_top2_score":    result.SemanticANNTop2Score,
		"main_semantic_ann_score_gap":     result.SemanticANNScoreGap,
		"main_semantic_ann_min_score_gap": result.SemanticANNMinScoreGap,
		"main_semantic_ann_decision":      result.SemanticANNDecision,
		"main_semantic_fallback_reason":   result.SemanticFallbackReason,
		"main_semantic_source":            result.SemanticSource,
		"ann_shadow_enabled":              shadow.ShadowSemanticEnabled,
		"ann_shadow_hit":                  shadow.ShadowSemanticHit,
		"ann_shadow_layer":                shadow.ShadowSemanticLayer,
		"ann_shadow_answer_id":            shadow.ShadowSemanticAnswerID,
		"ann_shadow_answer":               shadow.ShadowSemanticAnswer,
		"ann_shadow_top_score":            shadow.ShadowSemanticTopScore,
		"ann_shadow_top_k":                shadow.ShadowSemanticTopK,
		"ann_shadow_latency_ms":           shadow.ShadowSemanticLatencyMS,
		"ann_shadow_index_source":         shadow.ShadowSemanticIndexSource,
		"ann_shadow_index_version":        shadow.ShadowSemanticIndexVersion,
		"ann_vs_bruteforce_same_top1":     shadow.ShadowSemanticSameTop1,
		"ann_vs_final_answer_same":        shadow.ShadowSemanticSameAsFinal,
		"ann_shadow_error":                shadow.ShadowSemanticError,
		"ann_shadow_fallback_used":        shadow.ShadowSemanticFallbackUsed,
		"ann_shadow_diff_category":        shadow.ShadowSemanticDiffCategory,
		"ann_shadow_top_hits":             shadow.ShadowSemanticTopHits,
	}
	a.log.WithFields(fields).Info("ann shadow compare")
}
