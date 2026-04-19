package server

import (
	chat_context "ai-chat-service/chat-server/chat-context"
	"ai-chat-service/chat-server/data"
	"ai-chat-service/chat-server/llm"
	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/chat-server/qa"
	vector_data "ai-chat-service/chat-server/vector-data"
	"ai-chat-service/pkg/config"
	"ai-chat-service/pkg/log"
	"ai-chat-service/pkg/zerror"
	"ai-chat-service/proto"
	"ai-chat-service/services"
	keywords_filter "ai-chat-service/services/keywords-filter"
	keywords_proto "ai-chat-service/services/keywords-filter/proto"
	"ai-chat-service/services/tokenizer"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

const ChatPrimedTokens = 2

type openaiConf struct {
	ApiKey            string
	BaseUrl           string
	Model             string
	MaxTokens         int
	Temperature       float32
	TopP              float32
	PresencePenalty   float32
	FrequencyPenalty  float32
	BotDesc           string
	ContextTTL        int
	ContextLen        int
	MinResponseTokens int
	TimeoutMS         int
}
type app struct {
	config                  *config.Config
	openaiConf              *openaiConf
	log                     log.ILogger
	contextStore            chat_context.SessionStore
	data                    data.IChatRecordsData
	vectorData              vector_data.IChatRecordsData
	providerRegistry        *provider.Registry
	provider                provider.Provider
	providerProfile         provider.ProviderProfile
	qaRetriever             qa.Retriever
	qaStore                 qa.Store
	semanticMode            string
	semanticShadowConfig    qa.SemanticShadowConfig
	qaLookupResult          qa.LookupResult
	selectedProvider        string
	selectedModel           string
	fallbackUsed            bool
	fallbackReason          string
	routeDecision           llm.RouteDecision
	retrievalHits           []llm.RetrievalHit
	promptTokensSource      string
	keywordsFn              func(context.Context, *proto.ChatCompletionRequest) []string
	sensitiveFn             func(context.Context, *proto.ChatCompletionRequest) (bool, string, error)
	fastPathCache           *sessionFastPathCache
	busMetrics              *metrics_bus.BusMetrics
	semanticShadowQuery     string
	semanticShadowRequestID string
	sessionID               string
	pendingTurn             *chat_context.TurnRecord
	pendingMeta             *chat_context.SessionMeta
}

func (s *chatService) newApp(in *proto.ChatCompletionRequest, contextStore chat_context.SessionStore, stream bool) *app {
	conf := &openaiConf{
		ApiKey:            s.config.Chat.ApiKey,
		BaseUrl:           s.config.Chat.BaseUrl,
		Model:             s.config.Chat.Model,
		MaxTokens:         s.config.Chat.MaxTokens,
		Temperature:       s.config.Chat.Temperature,
		TopP:              s.config.Chat.TopP,
		PresencePenalty:   s.config.Chat.PresencePenalty,
		FrequencyPenalty:  s.config.Chat.FrequencyPenalty,
		BotDesc:           s.config.Chat.BotDesc,
		ContextTTL:        s.config.Chat.ContextTTL,
		ContextLen:        s.config.Chat.ContextLen,
		MinResponseTokens: s.config.Chat.MinResponseTokens,
		TimeoutMS:         60000,
	}
	registry := s.providerRegistry
	if registry == nil {
		registry = provider.NewRegistry(s.config.ProviderProfiles())
		s.providerRegistry = registry
	}
	if in.ChatParam != nil {
		if in.ChatParam.TopP != 0 {
			conf.TopP = in.ChatParam.TopP
		}
		if in.ChatParam.FrequencyPenalty != 0 {
			conf.FrequencyPenalty = in.ChatParam.FrequencyPenalty
		}
		if in.ChatParam.PresencePenalty != 0 {
			conf.PresencePenalty = in.ChatParam.PresencePenalty
		}
		if in.ChatParam.Temperature != 0 {
			conf.Temperature = in.ChatParam.Temperature
		}
		if in.ChatParam.BotDesc != "" {
			conf.BotDesc = in.ChatParam.BotDesc
		}
		if in.ChatParam.MaxTokens != 0 {
			conf.MaxTokens = int(in.ChatParam.MaxTokens)
		}
		if in.ChatParam.ContextTTL != 0 {
			conf.ContextTTL = int(in.ChatParam.ContextTTL)
		}
		if in.ChatParam.ContextLen != 0 {
			conf.ContextLen = int(in.ChatParam.ContextLen)
		}
		if in.ChatParam.MinResponseTokens != 0 {
			conf.MinResponseTokens = int(in.ChatParam.MinResponseTokens)
		}
	}
	selectedProvider := ""
	selectedModel := ""
	if in.ChatParam != nil {
		selectedProvider = strings.TrimSpace(in.ChatParam.GetProvider())
		selectedModel = strings.TrimSpace(in.ChatParam.GetModel())
	}
	selection, err := registry.ResolveSelection(selectedProvider, selectedModel, stream)
	if err != nil {
		s.log.Error(err)
		if providerRef, profileRef, resolveErr := registry.Default(); resolveErr == nil {
			selection = provider.SelectionResult{
				RequestedProvider: selectedProvider,
				RequestedModel:    selectedModel,
				ActualProvider:    profileRef.Name,
				ActualModel:       profileRef.DefaultChatModel(),
				Provider:          providerRef,
				Profile:           profileRef,
				FallbackUsed:      true,
				FallbackReason:    "registry_default",
			}
		} else {
			selection = provider.SelectionResult{}
		}
	}
	actualProfile := selection.Profile
	actualProvider := selection.Provider
	actualModel := selection.ActualModel
	if actualModel == "" {
		actualModel = conf.Model
	}
	if actualProfile.ContextWindow > 0 && conf.MaxTokens <= 0 {
		conf.MaxTokens = actualProfile.ContextWindow
	}
	if actualProfile.DefaultReservedCompletionTokens > 0 && conf.MinResponseTokens <= 0 {
		conf.MinResponseTokens = actualProfile.DefaultReservedCompletionTokens
	}
	if actualProfile.TimeoutMS > 0 {
		conf.TimeoutMS = actualProfile.TimeoutMS
	}
	conf.Model = actualModel
	return &app{
		config:               s.config,
		openaiConf:           conf,
		log:                  s.log,
		contextStore:         contextStore,
		data:                 s.data,
		vectorData:           s.vectorData,
		providerRegistry:     registry,
		provider:             actualProvider,
		providerProfile:      actualProfile,
		qaRetriever:          s.qaRetriever,
		qaStore:              s.qaStore,
		semanticMode:         semanticModeFromConfig(s.config),
		semanticShadowConfig: semanticShadowConfigFromConfig(s.config),
		selectedProvider:     selectedProvider,
		selectedModel:        selectedModel,
		fallbackUsed:         selection.FallbackUsed,
		fallbackReason:       selection.FallbackReason,
		keywordsFn:           s.keywordsFn,
		sensitiveFn:          s.sensitiveFn,
		fastPathCache:        s.ensureFastPathCache(),
		busMetrics:           s.busMetrics,
	}
}
func (a *app) getOpenaiClient() *openai.Client {
	accessToken := a.openaiConf.ApiKey
	config := openai.DefaultConfig(accessToken)
	config.BaseURL = a.openaiConf.BaseUrl
	client := openai.NewClientWithConfig(config)
	return client
}

func (a *app) resolveSession(ctx context.Context, in *proto.ChatCompletionRequest) error {
	if a == nil || a.contextStore == nil || in == nil {
		return nil
	}
	if a.sessionID != "" {
		return nil
	}
	sessionID, err := a.contextStore.ResolveSession(ctx, in.GetSessionId(), in.GetId(), in.GetPid())
	if err != nil {
		return err
	}
	a.sessionID = sessionID
	return nil
}

func (a *app) loadSessionMetaFast(ctx context.Context) (*chat_context.SessionMeta, error) {
	if a == nil || a.sessionID == "" || a.contextStore == nil {
		return nil, nil
	}
	if a.fastPathCache != nil {
		if meta, ok := a.fastPathCache.Meta(a.sessionID); ok {
			if a.busMetrics != nil {
				a.busMetrics.IncMetaCacheHitCount()
			}
			return meta, nil
		}
		if a.busMetrics != nil {
			a.busMetrics.IncMetaCacheMissCount()
		}
	}
	meta, err := a.contextStore.LoadSessionMeta(ctx, a.sessionID)
	if err != nil {
		return nil, err
	}
	if a.fastPathCache != nil {
		a.fastPathCache.PutMeta(meta)
	}
	return meta, nil
}

func (a *app) loadSessionSummaryFast(ctx context.Context) (*chat_context.SummaryRecord, error) {
	if a == nil || a.sessionID == "" || a.contextStore == nil {
		return nil, nil
	}
	return a.contextStore.LoadSummary(ctx, a.sessionID)
}

// 构建请求参数
func (a *app) buildChatCompletionRequest(ctx context.Context, in *proto.ChatCompletionRequest, stream bool) (req openai.ChatCompletionRequest, tokens, currTokens int, currMessage openai.ChatCompletionMessage, err error) {
	//当前消息
	currMessage = openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: in.Message,
	}
	req = openai.ChatCompletionRequest{
		Model: a.openaiConf.Model,
		Messages: []openai.ChatCompletionMessage{
			currMessage,
		},
		MaxTokens:        a.openaiConf.MinResponseTokens, //默认最少回复长度，后续会根据上下文长度调整
		Temperature:      a.openaiConf.Temperature,
		TopP:             a.openaiConf.TopP,
		PresencePenalty:  a.openaiConf.PresencePenalty,
		FrequencyPenalty: a.openaiConf.FrequencyPenalty,
		Stream:           stream,
	}
	a.promptTokensSource = llm.UsageSourceProvider
	turns := make([]*chat_context.TurnRecord, 0)
	baselineTurns := make([]*chat_context.TurnRecord, 0)
	var summary *chat_context.SummaryRecord
	if a.contextStore != nil {
		err = a.resolveSession(ctx, in)
		if err != nil {
			a.log.Error(err)
			return
		}
		useContext := in.GetEnableContext() || in.GetPid() != "" || in.GetSessionId() != ""
		if useContext {
			keepRecent := a.openaiConf.ContextLen
			if keepRecent <= 0 {
				keepRecent = 1
			}
			baselineTurns, err = a.contextStore.LoadRecentTurns(ctx, a.sessionID, keepRecent)
			if err != nil {
				a.log.Error(err)
				return
			}
			turns = baselineTurns
			summary, err = a.loadSessionSummaryFast(ctx)
			if err != nil {
				a.log.Error(err)
				return
			}
		}
	}
	//重构req.Messages
	tokens, currTokens, req.Messages, err = a.rebuildMessages(summary, turns, currMessage)
	if err != nil {
		a.log.Error(err)
		return
	}
	if a.busMetrics != nil && len(baselineTurns) > 0 {
		savedPromptTokensSource := a.promptTokensSource
		beforeTokens, _, _, beforeErr := a.rebuildMessages(nil, baselineTurns, currMessage)
		if beforeErr == nil {
			_ = beforeTokens
		}
		a.promptTokensSource = savedPromptTokensSource
	}
	if a.contextStore != nil {
		seq, allocErr := a.contextStore.AllocateTurnSeq(ctx, a.sessionID)
		if allocErr != nil {
			a.log.Error(allocErr)
			err = allocErr
			return
		}
		now := time.Now().UnixNano() / int64(time.Millisecond)
		a.pendingTurn = &chat_context.TurnRecord{
			SessionID:        a.sessionID,
			Seq:              seq,
			TurnStatus:       chat_context.TurnStatusOpen,
			RequestMessageID: in.Id,
			ParentMessageID:  in.Pid,
			UserMessage:      currMessage,
			UserTokens:       currTokens,
			TotalTokens:      currTokens,
			CreatedAtMS:      now,
			UpdatedAtMS:      now,
		}
		openTTL := time.Duration(a.openaiConf.ContextTTL) * time.Second
		if writer, ok := a.contextStore.(interface {
			WriteOpenTurnWithMeta(context.Context, *chat_context.TurnRecord, time.Duration) (*chat_context.SessionMeta, error)
		}); ok {
			pendingMeta, writeErr := writer.WriteOpenTurnWithMeta(ctx, a.pendingTurn, openTTL)
			if writeErr != nil {
				a.log.Error(writeErr)
				err = writeErr
				return
			}
			a.pendingMeta = pendingMeta
			if a.fastPathCache != nil && pendingMeta != nil {
				a.fastPathCache.PutMeta(pendingMeta)
			}
		} else if writeErr := a.contextStore.WriteOpenTurn(ctx, a.pendingTurn, openTTL); writeErr != nil {
			a.log.Error(writeErr)
			err = writeErr
			return
		} else {
			a.pendingMeta = nil
		}
		if a.fastPathCache != nil {
			a.fastPathCache.UpdateAfterOpenTurn(a.pendingTurn)
		}
	}
	remainingTokens := a.openaiConf.MaxTokens - tokens
	if remainingTokens < a.openaiConf.MinResponseTokens {
		remainingTokens = a.openaiConf.MinResponseTokens
	}
	req.MaxTokens = remainingTokens
	return
}
func (a *app) rebuildMessages(summary *chat_context.SummaryRecord, turns []*chat_context.TurnRecord, currMessage openai.ChatCompletionMessage) (tokens, currTokens int, messages []openai.ChatCompletionMessage, err error) {
	//系统消息和当前消息的tokens数
	var sysMessage openai.ChatCompletionMessage
	botTokens := 0
	var botTokenSource string
	if a.openaiConf.BotDesc != "" {
		sysMessage = openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: a.openaiConf.BotDesc,
		}
		botTokens, botTokenSource, err = tokenizer.GetTokensWithSource(&sysMessage, a.openaiConf.Model)
		if err != nil {
			a.log.Error(err)
			return
		}
		a.promptTokensSource = mergeTokenSource(a.promptTokensSource, botTokenSource)
	}
	summaryTokens := 0
	var summaryMessage openai.ChatCompletionMessage
	if summary != nil && strings.TrimSpace(summary.SummaryText) != "" {
		summaryMessage = summaryPromptMessage(summary)
		var summaryTokenSource string
		summaryTokens, summaryTokenSource, err = tokenizer.GetTokensWithSource(&summaryMessage, a.openaiConf.Model)
		if err != nil {
			a.log.Error(err)
			return
		}
		a.promptTokensSource = mergeTokenSource(a.promptTokensSource, summaryTokenSource)
	}
	retrievalTokens := 0
	var retrievalMessage openai.ChatCompletionMessage
	if a != nil && a.routeDecision.UseRetrievalAsContext && len(a.retrievalHits) > 0 {
		retrievalMessage = a.retrievalPromptMessage(a.retrievalHits, a.openaiConf.Model)
		var retrievalTokenSource string
		retrievalTokens, retrievalTokenSource, err = tokenizer.GetTokensWithSource(&retrievalMessage, a.openaiConf.Model)
		if err != nil {
			a.log.Error(err)
			return
		}
		a.promptTokensSource = mergeTokenSource(a.promptTokensSource, retrievalTokenSource)
	}
	//获取系统消息后重新构建消息列表，历史 turn 在前，当前消息在最后
	messages = make([]openai.ChatCompletionMessage, 0)
	var currTokenSource string
	currTokens, currTokenSource, err = tokenizer.GetTokensWithSource(&currMessage, a.openaiConf.Model)
	if err != nil {
		a.log.Error(err)
		return
	}
	a.promptTokensSource = mergeTokenSource(a.promptTokensSource, currTokenSource)
	//如果当前消息tokens数超过限制，直接返回错误
	maxPromptTokens := a.openaiConf.MaxTokens - a.openaiConf.MinResponseTokens - botTokens - summaryTokens - retrievalTokens - ChatPrimedTokens
	if currTokens > maxPromptTokens {
		err = zerror.NewByMsg("请求消息超限")
		a.log.Error(err)
		return
	}
	tokens = currTokens + botTokens + summaryTokens + retrievalTokens + ChatPrimedTokens
	if summaryTokens > 0 {
		messages = append(messages, summaryMessage)
	}
	if retrievalTokens > 0 {
		messages = append(messages, retrievalMessage)
	}
	//按 turn 顺序回放最近 N 轮上下文
	for _, turn := range turns {
		turnCost := turn.TotalTokens + 2*ChatPrimedTokens
		if tokens+turnCost > a.openaiConf.MaxTokens-a.openaiConf.MinResponseTokens {
			break
		}
		if turn.UserMessage.Content != "" {
			messages = append(messages, turn.UserMessage)
		}
		if turn.AssistantMessage.Content != "" {
			messages = append(messages, turn.AssistantMessage)
		}
		tokens += turnCost
	}
	messages = append(messages, currMessage)
	if botTokens > 0 {
		messages = append([]openai.ChatCompletionMessage{sysMessage}, messages...)
	}
	return
}

func summaryPromptMessage(summary *chat_context.SummaryRecord) openai.ChatCompletionMessage {
	if summary == nil || strings.TrimSpace(summary.SummaryText) == "" {
		return openai.ChatCompletionMessage{}
	}
	begin := summary.SummarizedRangeBegin
	end := summary.SummarizedRangeEnd
	if end <= 0 {
		end = summary.SummarySeq
	}
	if begin <= 0 {
		begin = end
	}
	content := fmt.Sprintf("以下是该会话的压缩摘要，仅用于辅助最近上下文，不覆盖最近轮次原文。\n覆盖范围: seq %d-%d\n%s", begin, end, strings.TrimSpace(summary.SummaryText))
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: content}
}
func (a *app) buildChatCompletionResponse(msg string) *proto.ChatCompletionResponse {
	res := &proto.ChatCompletionResponse{
		Id:        uuid.New().String(),
		Object:    "chat.completion",
		Created:   time.Now().Unix(),
		Model:     a.openaiConf.Model,
		SessionId: a.sessionID,
		Choices: []*proto.ChatCompletionChoice{
			{
				Message: &proto.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: msg,
				},
				FinishReason: "stop",
			},
		},
		Usage: &proto.Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}
	return res
}

func (a *app) buildChatCompletionStreamResponse(id, delta, finishReason string) *proto.ChatCompletionStreamResponse {
	res := &proto.ChatCompletionStreamResponse{
		Id:        id,
		Object:    "chat.completion.chunk",
		Created:   time.Now().Unix(),
		Model:     a.openaiConf.Model,
		SessionId: a.sessionID,
		Choices: []*proto.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: &proto.ChatCompletionStreamChoiceDelta{
					Content: delta,
					Role:    openai.ChatMessageRoleAssistant,
				},
				FinishReason: finishReason,
			},
		},
	}
	return res
}

func (a *app) buildChatCompletionStreamResponseList(id, msg string) []*proto.ChatCompletionStreamResponse {
	list := make([]*proto.ChatCompletionStreamResponse, 0)
	for _, delta := range msg {
		list = append(list, a.buildChatCompletionStreamResponse(id, string(delta), ""))
	}
	return list
}

// 保存上下文
func (a *app) saveContext(messageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int) error {
	if a.contextStore == nil || a.pendingTurn == nil {
		return nil
	}
	ctx := context.Background()
	completeTTL := time.Duration(a.openaiConf.ContextTTL) * time.Second
	var err error
	if fastComplete, ok := a.contextStore.(interface {
		CompleteTurnWithTurn(context.Context, *chat_context.TurnRecord, *chat_context.SessionMeta, string, openai.ChatCompletionMessage, int, time.Duration) error
	}); ok {
		turnCopy := *a.pendingTurn
		turnCopy.TurnStatus = chat_context.TurnStatusCompleted
		turnCopy.AssistantMessageID = messageID
		turnCopy.AssistantMessage = assistantMessage
		turnCopy.AssistantTokens = assistantTokens
		turnCopy.TotalTokens = turnCopy.UserTokens + assistantTokens
		turnCopy.UpdatedAtMS = time.Now().UnixNano() / int64(time.Millisecond)
		err = fastComplete.CompleteTurnWithTurn(ctx, &turnCopy, a.pendingMeta, messageID, assistantMessage, assistantTokens, completeTTL)
	} else {
		err = a.contextStore.CompleteTurn(ctx, a.sessionID, a.pendingTurn.Seq, messageID, assistantMessage, assistantTokens, completeTTL)
	}
	if err != nil {
		a.log.Error(err)
		return err
	}
	if a.fastPathCache != nil {
		completedTurn := *a.pendingTurn
		completedTurn.TurnStatus = chat_context.TurnStatusCompleted
		completedTurn.AssistantMessageID = messageID
		completedTurn.AssistantMessage = assistantMessage
		completedTurn.AssistantTokens = assistantTokens
		completedTurn.TotalTokens = completedTurn.UserTokens + assistantTokens
		completedTurn.UpdatedAtMS = time.Now().UnixNano() / int64(time.Millisecond)
		a.fastPathCache.UpdateAfterCompleteTurn(&completedTurn, nil)
	}
	return nil
}
func (a *app) keywords(ctx context.Context, in *proto.ChatCompletionRequest) []string {
	if a != nil && a.keywordsFn != nil {
		return a.keywordsFn(ctx, in)
	}
	pool := keywords_filter.GetKeywordsClientPool()
	conn := pool.Get()
	defer pool.Put(conn)
	accessToken := config.GetConfig().DependOn.Keywords.AccessToken
	client := keywords_proto.NewFilterClient(conn)
	ctx = services.AppendBearerTokenToContext(ctx, accessToken)
	req := &keywords_proto.FilterReq{
		Text: in.Message,
	}
	res, err := client.FindAll(ctx, req)
	if err != nil {
		a.log.Error(err)
		return []string{}
	}
	return res.Keywords

}
func (a *app) sensitive(ctx context.Context, in *proto.ChatCompletionRequest) (ok bool, msg string, err error) {
	pool := keywords_filter.GetSensitiveClientPool()
	conn := pool.Get()
	defer pool.Put(conn)
	//鉴权
	accessToken := config.GetConfig().DependOn.Sensitive.AccessToken
	client := keywords_proto.NewFilterClient(conn)
	ctx = services.AppendBearerTokenToContext(ctx, accessToken) //上下文
	req := &keywords_proto.FilterReq{
		Text: in.Message,
	}
	res, err := client.Validate(ctx, req)
	if err != nil {
		a.log.Error(err)
		return false, "", err
	}
	ok = res.Ok
	if !ok {
		msg = "触发到了知识盲区，请换个问题再问"
	}
	return
}
