package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/retrieval/kvlite"
	"ai-chat-service/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/sashabaranov/go-openai"
)

func (a *app) resolveRouteDecision(ctx context.Context, in *proto.ChatCompletionRequest) (llm.RouteDecision, error) {
	decision := llm.RouteDecision{
		Policy: llm.QueryPolicy{
			Mode:                  "api_only",
			ShouldCallLLM:         true,
			ShouldQueryLocal:      false,
			UseRetrievalAsContext: false,
			DirectReturnLocal:     false,
			Reason:                "kvlite_retrieval_disabled",
		},
		Mode:             llm.AnswerModeAPI,
		ShouldCallLLM:    true,
		ShouldQueryLocal: false,
		Provider:         llm.DetectProviderFromModel(a.openaiConf.Model),
		Model:            a.openaiConf.Model,
		Reason:           "kvlite_retrieval_disabled",
	}
	if a.providerProfile.Name != "" {
		decision.Provider = a.providerProfile.Name
	}
	if a == nil || a.vectorStore == nil {
		if a != nil && strings.TrimSpace(a.retrievalDisabledReason) != "" {
			decision.Reason = a.retrievalDisabledReason
			decision.Policy.Reason = decision.Reason
		}
		return decision, nil
	}
	if in == nil || strings.TrimSpace(in.GetMessage()) == "" {
		decision.Reason = "empty_query"
		decision.Policy.Reason = decision.Reason
		return decision, nil
	}
	scope, topK, threshold, err := a.kvliteRetrievalConfig()
	if err != nil {
		decision.Reason = "kvlite_config_error"
		decision.Policy.Reason = decision.Reason
		return decision, nil
	}
	decision.Policy.Mode = "kvlite_exact_then_hnsw"
	decision.Policy.ShouldQueryLocal = true
	decision.Policy.Threshold = threshold
	decision.Policy.Scope, _ = kvlite.EncodeScope(scope)
	decision.ShouldQueryLocal = true
	decision.Threshold = threshold

	doc, err := a.vectorStore.Get(ctx, in.GetMessage(), scope)
	if err != nil {
		return decisionWithRetrievalFailure(decision, err), nil
	}
	if doc != nil {
		return decisionWithKVLiteHit(decision, kvlite.Hit{Document: *doc, Score: 1}, "kvlite_exact_hit")
	}
	hits, err := a.vectorStore.Search(ctx, in.GetMessage(), scope, topK, threshold)
	if err != nil {
		return decisionWithRetrievalFailure(decision, err), nil
	}
	if len(hits) == 0 {
		decision.Reason = "kvlite_no_hit"
		decision.Policy.Reason = decision.Reason
		return decision, nil
	}
	return decisionWithKVLiteHit(decision, hits[0], "kvlite_vector_hit")
}

func (a *app) kvliteRetrievalConfig() (kvlite.LogicalScope, int, float64, error) {
	if a == nil || a.config == nil {
		return kvlite.LogicalScope{}, 0, 0, fmt.Errorf("missing config")
	}
	retrieval := a.config.Retrieval.KVLite
	if !retrieval.Enabled {
		return kvlite.LogicalScope{}, 0, 0, fmt.Errorf("retrieval disabled")
	}
	return kvlite.LogicalScope{
		Tenant:        retrieval.Scope.Tenant,
		KnowledgeBase: retrieval.Scope.KnowledgeBase,
		CorpusVersion: retrieval.Scope.CorpusVersion,
		Provider:      retrieval.Scope.Provider,
		Model:         retrieval.Scope.Model,
		PromptVersion: retrieval.Scope.PromptVersion,
	}, retrieval.TopK, retrieval.Threshold, nil
}

func decisionWithRetrievalFailure(decision llm.RouteDecision, err error) llm.RouteDecision {
	decision.Reason = kvlite.FailureReason(err)
	decision.Policy.Reason = decision.Reason
	return decision
}

func decisionWithKVLiteHit(decision llm.RouteDecision, hit kvlite.Hit, reason string) (llm.RouteDecision, error) {
	var trace llm.AnswerTrace
	if err := kvlite.AppendHitsToTrace(&trace, []kvlite.Hit{hit}); err != nil {
		return decisionWithRetrievalFailure(decision, err), nil
	}
	decision.Mode = llm.AnswerModeKnowledgeBase
	decision.ShouldCallLLM = false
	decision.DirectReturnLocal = true
	decision.RetrievalScore = hit.Score
	decision.RetrievalHits = trace.RetrievedHits
	decision.LocalAnswer = hit.Answer.Answer
	decision.Reason = reason
	decision.Policy.ShouldCallLLM = false
	decision.Policy.DirectReturnLocal = true
	decision.Policy.Reason = reason
	return decision, nil
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
