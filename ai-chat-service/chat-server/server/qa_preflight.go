package server

import (
	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/qa"
	"ai-chat-service/proto"
	"ai-chat-service/services/tokenizer"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

func (a *app) checkSensitive(ctx context.Context, in *proto.ChatCompletionRequest) (bool, string, error) {
	if a == nil {
		return true, "", nil
	}
	if a.sensitiveFn != nil {
		return a.sensitiveFn(ctx, in)
	}
	return a.sensitive(ctx, in)
}

func (a *app) tryQAPreflight(ctx context.Context, in *proto.ChatCompletionRequest) (bool, qa.LookupResult, error) {
	if a == nil || a.qaRetriever == nil || in == nil {
		return false, qa.LookupResult{}, nil
	}
	providerName := ""
	modelName := ""
	providerName = a.providerProfile.Name
	if a.openaiConf != nil {
		modelName = a.openaiConf.Model
	}
	question := strings.TrimSpace(in.GetMessage())
	if question == "" {
		return false, qa.LookupResult{}, nil
	}
	semanticEnabled := false
	semanticMode := qa.SemanticRecallModeBruteForce
	semanticTopK := qa.DefaultSemanticTopK
	semanticThreshold := qa.DefaultSemanticMinScore
	semanticANNTopK := qa.DefaultSemanticANNTopK
	semanticANNMinScoreGap := qa.DefaultSemanticANNMinScoreGap
	semanticModel := ""
	semanticEmbeddingDim := qa.DefaultSemanticEmbeddingDim
	semanticCorpusLimit := qa.DefaultSemanticCorpusLimit
	cfg := a.config
	if cfg != nil {
		semanticEnabled = cfg.Retrieval.Semantic.Enabled
		semanticMode = semanticModeFromConfig(cfg)
		if cfg.Retrieval.Semantic.TopK > 0 {
			semanticTopK = cfg.Retrieval.Semantic.TopK
		}
		if cfg.Retrieval.Semantic.Threshold > 0 {
			semanticThreshold = cfg.Retrieval.Semantic.Threshold
		}
		if cfg.Retrieval.Semantic.ANN.TopK > 0 {
			semanticANNTopK = cfg.Retrieval.Semantic.ANN.TopK
		}
		if cfg.Retrieval.Semantic.ANN.MinScoreGap > 0 {
			semanticANNMinScoreGap = cfg.Retrieval.Semantic.ANN.MinScoreGap
		}
		if strings.TrimSpace(cfg.Retrieval.Semantic.EmbeddingModel) != "" {
			semanticModel = strings.TrimSpace(cfg.Retrieval.Semantic.EmbeddingModel)
		}
		if cfg.Retrieval.Semantic.EmbeddingDim > 0 {
			semanticEmbeddingDim = cfg.Retrieval.Semantic.EmbeddingDim
		}
		if cfg.Retrieval.Semantic.CorpusLimit > 0 {
			semanticCorpusLimit = cfg.Retrieval.Semantic.CorpusLimit
		}
	}
	semanticShadow := a.semanticShadowConfig
	semanticShadow.Enabled = semanticEnabled && semanticShadow.Enabled
	normalizedQuestion := qa.NormalizeQuestion(question)
	request := qa.LookupRequest{
		Question:               question,
		ExactQuestionHash:      qa.ExactQuestionHash(question),
		NormalizedQuestion:     normalizedQuestion,
		NormalizedQuestionHash: qa.QuestionHash(normalizedQuestion),
		Provider:               providerName,
		Model:                  modelName,
		Scope:                  qa.BuildScope(providerName, modelName, ""),
		CandidateTopK:          qa.DefaultCandidateTopK,
		CandidateMinScore:      qa.DefaultCandidateMinScore,
		SemanticEnabled:        semanticEnabled,
		SemanticMode:           semanticMode,
		SemanticTopK:           semanticTopK,
		SemanticThreshold:      semanticThreshold,
		SemanticANNTopK:        semanticANNTopK,
		SemanticANNMinScoreGap: semanticANNMinScoreGap,
		SemanticModel:          semanticModel,
		SemanticEmbeddingDim:   semanticEmbeddingDim,
		SemanticCorpusLimit:    semanticCorpusLimit,
		SemanticShadow:         semanticShadow,
		Metadata: map[string]string{
			"request_id":          in.GetId(),
			"session_id":          in.GetSessionId(),
			"parent_id":           in.GetPid(),
			"enable_context":      fmt.Sprintf("%t", in.GetEnableContext()),
			"question":            question,
			"normalized_question": normalizedQuestion,
		},
	}
	result, err := a.qaRetriever.Lookup(ctx, request)
	if err != nil {
		return false, qa.LookupResult{}, err
	}
	a.qaLookupResult = result
	a.semanticShadowQuery = question
	a.semanticShadowRequestID = in.GetId()
	a.logSemanticLookupResult(in, result)
	if !result.Hit || strings.TrimSpace(result.Answer) == "" {
		return false, qa.LookupResult{}, nil
	}
	if strings.TrimSpace(result.HitType) == "" {
		result.HitType = qa.HitTypeExact
	}
	return true, result, nil
}

func (a *app) logSemanticLookupResult(in *proto.ChatCompletionRequest, result qa.LookupResult) {
	if a == nil || a.log == nil {
		return
	}
	if strings.TrimSpace(result.SemanticModeRequested) == "" && strings.TrimSpace(result.SemanticANNDecision) == "" && strings.TrimSpace(result.SemanticFallbackReason) == "" && result.SemanticTopScore == 0 && result.SemanticANNTopScore == 0 && result.SemanticCandidateCount == 0 {
		return
	}
	fields := map[string]interface{}{
		"request_id":                 in.GetId(),
		"session_id":                 in.GetSessionId(),
		"question":                   strings.TrimSpace(in.GetMessage()),
		"qa_hit_type":                result.HitType,
		"semantic_mode_requested":    result.SemanticModeRequested,
		"semantic_mode_used":         result.SemanticModeUsed,
		"semantic_candidate_count":   result.SemanticCandidateCount,
		"semantic_top_k":             result.SemanticTopK,
		"semantic_top_score":         result.SemanticTopScore,
		"semantic_ann_top_k":         result.SemanticANNTopK,
		"semantic_ann_top_score":     result.SemanticANNTopScore,
		"semantic_ann_min_score_gap": result.SemanticANNMinScoreGap,
		"semantic_ann_decision":      result.SemanticANNDecision,
		"semantic_fallback_used":     result.SemanticFallbackUsed,
		"semantic_fallback_reason":   result.SemanticFallbackReason,
		"semantic_source":            result.SemanticSource,
		"semantic_model":             result.SemanticModel,
		"semantic_hit":               result.Hit,
	}
	if result.SemanticANNTop2Score != 0 {
		fields["semantic_ann_top2_score"] = result.SemanticANNTop2Score
	}
	if result.SemanticANNScoreGap != 0 {
		fields["semantic_ann_score_gap"] = result.SemanticANNScoreGap
	}
	a.log.WithFields(fields).Info("qa semantic lookup")
}

func (a *app) buildQAPreflightUnaryResponse(ctx context.Context, in *proto.ChatCompletionRequest, result qa.LookupResult) (*proto.ChatCompletionResponse, llm.ResponseMeta, error) {
	answer := strings.TrimSpace(result.Answer)
	if answer == "" {
		return nil, llm.ResponseMeta{}, fmt.Errorf("empty qa answer")
	}
	modelName := ""
	sessionID := ""
	if a != nil {
		sessionID = a.sessionID
		if a.openaiConf != nil {
			modelName = a.openaiConf.Model
		}
	}
	promptTokens, promptSource := a.estimateTokensForPreflightMessage(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: in.GetMessage()})
	completionTokens, completionSource := a.estimateTokensForPreflightMessage(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: answer})
	usage, meta := a.buildQAPreflightUsageAndMeta(result, promptTokens, promptSource, completionTokens, completionSource)
	responseID := uuid.New().String()
	response := &proto.ChatCompletionResponse{
		Id:        responseID,
		Object:    "chat.completion",
		Created:   time.Now().Unix(),
		Model:     modelName,
		SessionId: sessionID,
		Choices: []*proto.ChatCompletionChoice{
			{
				Message: &proto.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: answer,
				},
				FinishReason: "stop",
			},
		},
		Usage: &proto.Usage{
			PromptTokens:     int32(usage.PromptTokens),
			CompletionTokens: int32(usage.CompletionTokens),
			TotalTokens:      int32(usage.TotalTokens),
		},
	}
	return response, meta, nil
}

func (a *app) buildQAPreflightStreamResponses(ctx context.Context, in *proto.ChatCompletionRequest, result qa.LookupResult) ([]*proto.ChatCompletionStreamResponse, llm.ResponseMeta, error) {
	answer := strings.TrimSpace(result.Answer)
	if answer == "" {
		return nil, llm.ResponseMeta{}, fmt.Errorf("empty qa answer")
	}
	modelName := ""
	sessionID := ""
	if a != nil {
		sessionID = a.sessionID
		if a.openaiConf != nil {
			modelName = a.openaiConf.Model
		}
	}
	promptTokens, promptSource := a.estimateTokensForPreflightMessage(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: in.GetMessage()})
	completionTokens, completionSource := a.estimateTokensForPreflightMessage(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: answer})
	_, meta := a.buildQAPreflightUsageAndMeta(result, promptTokens, promptSource, completionTokens, completionSource)
	responseID := uuid.New().String()
	chunks := make([]*proto.ChatCompletionStreamResponse, 0, len([]rune(answer))+2)
	chunks = append(chunks, a.buildChatCompletionStreamResponse(responseID, "", ""))
	chunks = append(chunks, a.buildChatCompletionStreamResponseList(responseID, answer)...)
	chunks = append(chunks, a.buildChatCompletionStreamResponse(responseID, "", "stop"))
	for _, chunk := range chunks {
		if chunk != nil {
			chunk.Model = modelName
			chunk.SessionId = sessionID
		}
	}
	return chunks, meta, nil
}

func (a *app) buildQAPreflightUsageAndMeta(result qa.LookupResult, promptTokens int, promptSource string, completionTokens int, completionSource string) (llm.UsageInfo, llm.ResponseMeta) {
	usage := llm.UsageInfo{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		UsageSource:      mergeTokenSource(promptSource, completionSource),
	}
	if a == nil {
		return usage, llm.ResponseMeta{Usage: usage}
	}
	previousPromptSource := a.promptTokensSource
	a.promptTokensSource = promptSource
	meta := a.buildResponseMeta(a.buildQAPreflightTrace(result), usage, promptTokens)
	a.promptTokensSource = previousPromptSource
	return usage, meta
}

func (a *app) buildQAPreflightTrace(result qa.LookupResult) llm.AnswerTrace {
	providerName := ""
	modelName := ""
	selectedProvider := ""
	selectedModel := ""
	fallbackUsed := false
	fallbackReason := ""
	if a != nil {
		if a.providerProfile.Name != "" {
			providerName = a.providerProfile.Name
		}
		if a.openaiConf != nil && a.openaiConf.Model != "" {
			modelName = a.openaiConf.Model
		}
		selectedProvider = a.selectedProvider
		selectedModel = a.selectedModel
		fallbackUsed = a.fallbackUsed
		fallbackReason = a.fallbackReason
	}
	hitType := strings.TrimSpace(result.HitType)
	if hitType == "" {
		hitType = qa.HitTypeExact
	}
	finalAnswerSource := llm.FinalAnswerSourceQAStore
	if hitType == qa.HitTypeCandidate {
		finalAnswerSource = llm.FinalAnswerSourceQACandidate
	} else if hitType == qa.HitTypeSemantic {
		finalAnswerSource = llm.FinalAnswerSourceQASemantic
	}
	candidateSource := strings.TrimSpace(result.CandidateSource)
	if candidateSource == "" && hitType == qa.HitTypeCandidate {
		candidateSource = qa.CandidateRecallSourceBigram
	}
	semanticSource := strings.TrimSpace(result.SemanticSource)
	if semanticSource == "" && hitType == qa.HitTypeSemantic {
		semanticSource = qa.SemanticRecallSourceBruteForce
	}
	trace := llm.AnswerTrace{
		AnswerMode:             llm.AnswerModeCacheHit,
		Provider:               providerName,
		Model:                  modelName,
		SelectedProvider:       selectedProvider,
		SelectedModel:          selectedModel,
		ActualProvider:         providerName,
		ActualModel:            modelName,
		FallbackUsed:           fallbackUsed,
		FallbackReason:         fallbackReason,
		AnswerSourceKind:       llm.AnswerSourceKindCache,
		UsedLocalRetrieval:     true,
		CacheHit:               true,
		QAHitType:              hitType,
		CandidateCount:         result.CandidateCount,
		CandidateTopScore:      result.CandidateTopScore,
		CandidateSource:        candidateSource,
		SemanticCandidateCount: result.SemanticCandidateCount,
		SemanticTopK:           result.SemanticTopK,
		SemanticTopScore:       result.SemanticTopScore,
		SemanticANNTopK:        result.SemanticANNTopK,
		SemanticANNTopScore:    result.SemanticANNTopScore,
		SemanticANNTop2Score:   result.SemanticANNTop2Score,
		SemanticANNScoreGap:    result.SemanticANNScoreGap,
		SemanticANNMinScoreGap: result.SemanticANNMinScoreGap,
		SemanticANNDecision:    result.SemanticANNDecision,
		SemanticModel:          result.SemanticModel,
		SemanticSource:         semanticSource,
		FinalAnswerSource:      finalAnswerSource,
		GenerationProvider:     providerName,
		GenerationModel:        modelName,
	}
	a.finalizeSemanticLookupTrace(&trace, result)
	trace.RetrievedHits = []llm.RetrievalHit{buildQAPreflightRetrievalHit(result, providerName, modelName, hitType)}
	a.finalizeSemanticShadowTrace(&trace, result, result.Answer)
	return trace
}

func (a *app) queueQABackfill(ctx context.Context, in *proto.ChatCompletionRequest, answer, providerName, modelName string) {
	if a == nil || a.qaStore == nil || in == nil || strings.TrimSpace(answer) == "" {
		return
	}
	record := a.buildQAWriteRecord(in, answer, providerName, modelName)
	ttl := time.Duration(0)
	if a.openaiConf != nil && a.openaiConf.ContextTTL > 0 {
		ttl = time.Duration(a.openaiConf.ContextTTL) * time.Second
	}
	go func() {
		if err := a.qaStore.Upsert(context.Background(), record, ttl); err != nil && a.log != nil {
			a.log.Error(err)
			return
		}
		semanticBackfillEnabled := true
		if a != nil && a.config != nil {
			semanticBackfillEnabled = a.config.Retrieval.Semantic.Enabled && a.config.Retrieval.Semantic.AsyncBackfill
		}
		if semanticBackfillEnabled {
			if semanticBackfiller, ok := a.qaStore.(qa.SemanticBackfiller); ok {
				if err := semanticBackfiller.BackfillSemanticEmbedding(context.Background(), record); err != nil && a.log != nil {
					a.log.Error(err)
				}
			}
		}
	}()
}

func (a *app) buildQAWriteRecord(in *proto.ChatCompletionRequest, answer, providerName, modelName string) *llm.QARecord {
	if strings.TrimSpace(providerName) == "" && a != nil && a.providerProfile.Name != "" {
		providerName = a.providerProfile.Name
	}
	if strings.TrimSpace(modelName) == "" && a != nil && a.openaiConf != nil && a.openaiConf.Model != "" {
		modelName = a.openaiConf.Model
	}
	question := strings.TrimSpace(in.GetMessage())
	normalizedQuestion := qa.NormalizeQuestion(question)
	normalizedHash := qa.QuestionHash(question)
	exactHash := qa.ExactQuestionHash(question)
	scope := qa.BuildScope(providerName, modelName, "")
	questionID := qa.BuildQuestionID(scope, exactHash)
	now := time.Now().Unix()
	record := &llm.QARecord{
		QuestionID:          questionID,
		RecordID:            questionID,
		CanonicalQuestionID: questionID,
		SessionID:           a.sessionID,
		RequestMessageID:    in.GetId(),
		ParentMessageID:     in.GetPid(),
		Question:            question,
		RawQuestion:         question,
		CanonicalQuestion:   question,
		QuestionHash:        normalizedHash,
		RawQuestionHash:     exactHash,
		NormalizedQuestion:  normalizedQuestion,
		Answer:              strings.TrimSpace(answer),
		Scope:               scope,
		SourceType:          llm.SourceTypeAPI,
		Provider:            providerName,
		Model:               modelName,
		CreatedAt:           now,
		UpdatedAt:           now,
		HitCount:            1,
		LastHitAt:           now,
		Metadata: map[string]string{
			"request_id":               in.GetId(),
			"session_id":               a.sessionID,
			"parent_id":                in.GetPid(),
			"question_id":              questionID,
			"question":                 question,
			"raw_question":             question,
			"canonical_question":       question,
			"canonical_question_id":    questionID,
			"normalized_question":      normalizedQuestion,
			"normalized_question_hash": normalizedHash,
			"raw_question_hash":        exactHash,
			"canonical_question_hash":  exactHash,
			"scope":                    scope,
			"provider":                 providerName,
			"model":                    modelName,
			"source_type":              llm.SourceTypeAPI,
			"answer_source":            llm.FinalAnswerSourceLLM,
		},
	}
	if a.selectedProvider != "" {
		record.Metadata["selected_provider"] = a.selectedProvider
	}
	if a.selectedModel != "" {
		record.Metadata["selected_model"] = a.selectedModel
	}
	return record
}

func buildQAPreflightRetrievalHit(result qa.LookupResult, defaultProvider, defaultModel, hitType string) llm.RetrievalHit {
	providerName := lookupMetadataValue(result.Metadata, "provider", defaultProvider)
	modelName := lookupMetadataValue(result.Metadata, "model", defaultModel)
	sourceType := lookupMetadataValue(result.Metadata, "source_type", llm.SourceTypeCacheHit)
	if strings.TrimSpace(hitType) == qa.HitTypeCandidate {
		sourceType = llm.SourceTypeRetrieval
	} else if strings.TrimSpace(hitType) == qa.HitTypeSemantic {
		sourceType = llm.SourceTypeRetrieval
	}
	questionPreview := lookupMetadataValue(result.Metadata, "alias_question", "")
	if questionPreview == "" {
		questionPreview = lookupMetadataValue(result.Metadata, "canonical_question", "")
	}
	if questionPreview == "" {
		questionPreview = lookupMetadataValue(result.Metadata, "question", "")
	}
	return llm.RetrievalHit{
		RecordID:        strings.TrimSpace(firstNonEmpty(result.CanonicalQuestionID, result.QuestionID, lookupMetadataValue(result.Metadata, "record_id", ""))),
		Score:           retrievalScore(result, hitType),
		HitType:         hitType,
		QuestionPreview: llm.PreviewText(questionPreview, 128),
		AnswerPreview:   llm.PreviewText(result.Answer, 256),
		Provider:        providerName,
		Model:           modelName,
		SessionID:       lookupMetadataValue(result.Metadata, "session_id", ""),
		SourceType:      sourceType,
	}
}

func retrievalScore(result qa.LookupResult, hitType string) float64 {
	if strings.TrimSpace(hitType) == qa.HitTypeCandidate && result.CandidateTopScore > 0 {
		return result.CandidateTopScore
	}
	if strings.TrimSpace(hitType) == qa.HitTypeSemantic && result.SemanticTopScore > 0 {
		return result.SemanticTopScore
	}
	return 1
}

func (a *app) estimateTokensForPreflightMessage(message openai.ChatCompletionMessage) (int, string) {
	if a == nil || a.openaiConf == nil {
		return estimateTokenCount(message.Content), llm.UsageSourceEstimated
	}
	tokens, source, err := tokenizer.GetTokensWithSource(&message, a.openaiConf.Model)
	if err != nil {
		return estimateTokenCount(message.Content), llm.UsageSourceEstimated
	}
	if tokens <= 0 {
		tokens = estimateTokenCount(message.Content)
		if source == "" {
			source = llm.UsageSourceEstimated
		}
	}
	if source == "" {
		source = llm.UsageSourceEstimated
	}
	return tokens, source
}

func estimateTokenCount(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runes := []rune(trimmed)
	count := len(runes) / 4
	if count <= 0 {
		count = 1
	}
	return count
}

func lookupMetadataValue(metadata map[string]string, key, fallback string) string {
	if metadata == nil {
		return fallback
	}
	if value := strings.TrimSpace(metadata[key]); value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
