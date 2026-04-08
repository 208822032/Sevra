package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/sashabaranov/go-openai"
)

const (
	defaultCacheHitThreshold = 0.99
	defaultHybridThreshold   = 0.80
)

func (a *app) resolveRouteDecision(ctx context.Context, in *proto.ChatCompletionRequest) (llm.RouteDecision, error) {
	decision := llm.RouteDecision{
		Policy: llm.QueryPolicy{
			Mode:                  strings.ToLower(strings.TrimSpace(a.config.Chat.RouteMode)),
			ShouldCallLLM:         true,
			ShouldQueryLocal:      true,
			UseRetrievalAsContext: false,
			DirectReturnLocal:     false,
			Threshold:             defaultHybridThreshold,
			Reason:                "auto",
		},
		Mode:      llm.AnswerModeAPI,
		Threshold: defaultHybridThreshold,
		Provider:  llm.DetectProviderFromModel(a.openaiConf.Model),
		Model:     a.openaiConf.Model,
	}
	if a.providerProfile.Name != "" {
		decision.Provider = a.providerProfile.Name
	}
	mode := decision.Policy.Mode
	if mode == "" {
		mode = "auto"
	}

	keywords := a.keywords(ctx, in)
	if len(keywords) == 0 {
		if mode == "local_only" {
			decision.Mode = llm.AnswerModeLocal
			decision.ShouldCallLLM = false
			decision.ShouldQueryLocal = true
			decision.DirectReturnLocal = true
			decision.Reason = "local_only_without_hit"
			decision.Policy.Reason = decision.Reason
			decision.LocalAnswer = "本地知识库未命中，且当前策略禁止回源生成"
			return decision, nil
		}
		decision.Policy.ShouldQueryLocal = false
		decision.ShouldQueryLocal = false
		decision.ShouldCallLLM = true
		decision.Policy.Reason = "no_local_evidence"
		decision.Reason = decision.Policy.Reason
		return decision, nil
	}

	idStr, score, err := a.vectorData.QueryData(ctx, a.exactScopeQuery(keywords))
	if err != nil {
		decision.ShouldQueryLocal = true
		decision.Policy.ShouldQueryLocal = true
		decision.Reason = "local_search_error"
		decision.Policy.Reason = decision.Reason
		return decision, nil
	}
	decision.RetrievalScore = float64(score)
	decision.Policy.ShouldQueryLocal = true

	if strings.TrimSpace(idStr) == "" {
		if mode == "local_only" {
			decision.Mode = llm.AnswerModeLocal
			decision.ShouldCallLLM = false
			decision.DirectReturnLocal = true
			decision.Reason = "local_only_without_record"
			decision.Policy.Reason = decision.Reason
			decision.LocalAnswer = "本地知识库未命中，且当前策略禁止回源生成"
			return decision, nil
		}
		decision.Reason = "no_local_record"
		decision.Policy.Reason = decision.Reason
		return decision, nil
	}

	id, parseErr := parseRecordID(idStr)
	if parseErr != nil {
		decision.Reason = "invalid_record_id"
		decision.Policy.Reason = decision.Reason
		return decision, nil
	}
	record, recordErr := a.data.GetById(id)
	if recordErr != nil || record == nil {
		decision.Reason = "record_lookup_failed"
		decision.Policy.Reason = decision.Reason
		return decision, nil
	}
	hit := llm.RetrievalHit{
		RecordID:        strings.TrimSpace(idStr),
		Score:           float64(score),
		HitType:         hitTypeFromScore(float64(score)),
		QuestionPreview: llm.PreviewText(record.UserMsg, 128),
		AnswerPreview:   llm.PreviewText(record.AIMsg, 256),
		Provider:        decision.Provider,
		Model:           decision.Model,
		SourceType:      llm.SourceTypeRetrieval,
	}
	if id > 0 {
		hit.TurnSeq = id
	}
	decision.RetrievalHits = []llm.RetrievalHit{hit}
	decision.LocalAnswer = record.AIMsg

	switch {
	case mode == "api_only":
		decision.Mode = llm.AnswerModeAPI
		decision.ShouldCallLLM = true
		decision.UseRetrievalAsContext = false
		decision.DirectReturnLocal = false
		decision.Reason = "forced_api_only"
	case mode == "local_only":
		decision.Mode = llm.AnswerModeLocal
		decision.ShouldCallLLM = false
		decision.UseRetrievalAsContext = false
		decision.DirectReturnLocal = true
		decision.Reason = "forced_local_only"
	case mode == "cache_hit_return" || score >= defaultCacheHitThreshold:
		decision.Mode = llm.AnswerModeCacheHit
		decision.ShouldCallLLM = false
		decision.UseRetrievalAsContext = false
		decision.DirectReturnLocal = true
		decision.Policy.Threshold = defaultCacheHitThreshold
		decision.Threshold = defaultCacheHitThreshold
		decision.Reason = "cache_hit_return"
	default:
		decision.Mode = llm.AnswerModeHybrid
		decision.ShouldCallLLM = true
		decision.UseRetrievalAsContext = score >= defaultHybridThreshold || mode == "hybrid_rag_style"
		decision.DirectReturnLocal = false
		decision.Policy.Threshold = defaultHybridThreshold
		decision.Threshold = defaultHybridThreshold
		if decision.UseRetrievalAsContext {
			decision.Reason = "hybrid_retrieval_context"
		} else {
			decision.Mode = llm.AnswerModeAPI
			decision.Reason = "fallback_api"
		}
	}
	decision.Policy.Mode = mode
	decision.Policy.DirectReturnLocal = decision.DirectReturnLocal
	decision.Policy.UseRetrievalAsContext = decision.UseRetrievalAsContext
	decision.Policy.ShouldCallLLM = decision.ShouldCallLLM
	decision.Policy.ShouldQueryLocal = true
	decision.Policy.Threshold = decision.Threshold
	decision.Policy.Reason = decision.Reason
	decision.Policy.Scope = "session+global"
	return decision, nil
}

func (a *app) exactScopeQuery(keywords []string) map[string][]string {
	query := map[string][]string{}
	if len(keywords) > 0 {
		query["keywords"] = []string{strings.Join(keywords, ",")}
	}
	if a != nil && strings.TrimSpace(a.providerProfile.Name) != "" {
		query["provider"] = []string{strings.TrimSpace(a.providerProfile.Name)}
	}
	if a != nil && strings.TrimSpace(a.openaiConf.Model) != "" {
		query["model"] = []string{strings.TrimSpace(a.openaiConf.Model)}
	}
	return query
}

func parseRecordID(value string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func hitTypeFromScore(score float64) string {
	switch {
	case score >= defaultCacheHitThreshold:
		return "exact_norm_hit"
	case score >= defaultHybridThreshold:
		return "semantic_hit"
	default:
		return "fallback_hit"
	}
}

func (a *app) retrievalPromptMessage(hits []llm.RetrievalHit, model string) openai.ChatCompletionMessage {
	if len(hits) == 0 {
		return openai.ChatCompletionMessage{}
	}
	parts := make([]string, 0, len(hits)+1)
	parts = append(parts, "以下是本地检索到的相关问答，仅作为参考上下文，不要直接当作当前问题的唯一答案。")
	for index, hit := range hits {
		parts = append(parts, fmt.Sprintf("[hit-%d score=%.4f type=%s provider=%s model=%s]\nQ: %s\nA: %s", index+1, hit.Score, hit.HitType, hit.Provider, hit.Model, hit.QuestionPreview, hit.AnswerPreview))
	}
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: strings.Join(parts, "\n\n")}
}

func (a *app) buildResponseMeta(trace llm.AnswerTrace, usage llm.UsageInfo, promptTokens int) llm.ResponseMeta {
	maxContextTokens := a.openaiConf.MaxTokens
	if a.providerProfile.ContextWindow > 0 {
		maxContextTokens = a.providerProfile.ContextWindow
	}
	reserved := a.openaiConf.MinResponseTokens
	if reserved <= 0 && a.providerProfile.DefaultReservedCompletionTokens > 0 {
		reserved = a.providerProfile.DefaultReservedCompletionTokens
	}
	budgetSource := llm.BudgetSourceEstimated
	if maxContextTokens > 0 && promptTokens >= 0 && a.promptTokensSource == llm.UsageSourceProvider && usage.UsageSource == llm.UsageSourceProvider {
		budgetSource = llm.BudgetSourceExact
	}
	if maxContextTokens <= 0 {
		budgetSource = llm.BudgetSourceFallback
		maxContextTokens = 4096
	}
	remaining := llm.ClampBudget(maxContextTokens, promptTokens, reserved)
	return llm.ResponseMeta{
		Trace: trace,
		Usage: usage,
		Budget: llm.BudgetInfo{
			MaxContextTokens:                maxContextTokens,
			ReservedCompletionTokens:        reserved,
			EstimatedRemainingContextTokens: remaining,
			BudgetSource:                    budgetSource,
		},
	}
}

func mergeTokenSource(current, next string) string {
	if strings.TrimSpace(current) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return current
	}
	if current == llm.UsageSourceEstimated || next == llm.UsageSourceEstimated {
		return llm.UsageSourceEstimated
	}
	return llm.UsageSourceProvider
}

func (a *app) setResponseMetaTrailer(ctx context.Context, meta llm.ResponseMeta) {
	if ctx == nil {
		return
	}
	traceBytes, err := json.Marshal(meta.Trace)
	if err == nil {
		_ = grpc.SetTrailer(ctx, metadata.Pairs("x-ai-trace", string(traceBytes)))
	}
	usageBytes, err := json.Marshal(meta.Usage)
	if err == nil {
		_ = grpc.SetTrailer(ctx, metadata.Pairs("x-ai-usage", string(usageBytes)))
	}
	budgetBytes, err := json.Marshal(meta.Budget)
	if err == nil {
		_ = grpc.SetTrailer(ctx, metadata.Pairs("x-ai-budget", string(budgetBytes)))
	}
}
