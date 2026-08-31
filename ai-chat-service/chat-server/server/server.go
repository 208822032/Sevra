package server

import (
	chat_context "ai-chat-service/chat-server/chat-context"
	"ai-chat-service/chat-server/data"
	"ai-chat-service/chat-server/llm"
	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/chat-server/retrieval/kvlite"
	"ai-chat-service/pkg/config"
	"ai-chat-service/pkg/log"
	"ai-chat-service/proto"
	"ai-chat-service/services/tokenizer"
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/golang/protobuf/jsonpb"
	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

type chatService struct {
	proto.UnimplementedChatServer
	config                  *config.Config
	log                     log.ILogger
	contextStore            chat_context.SessionStore
	data                    data.IChatRecordsData //聊天记录数据库
	vectorStore             kvlite.Store
	retrievalDisabledReason string
	providerRegistry        *provider.Registry
	busMetrics              *metrics_bus.BusMetrics
	keywordsFn              func(context.Context, *proto.ChatCompletionRequest) []string
	sensitiveFn             func(context.Context, *proto.ChatCompletionRequest) (bool, string, error)
	fastPathOnce            sync.Once
	fastPathCache           *sessionFastPathCache
}

func NewChatService(data data.IChatRecordsData, config *config.Config, log log.ILogger, busMetrics *metrics_bus.BusMetrics, contextStore chat_context.SessionStore, vectorStore kvlite.Store, retrievalDisabledReason string) proto.ChatServer {
	svc := &chatService{
		config:                  config,
		log:                     log,
		contextStore:            contextStore,
		data:                    data,
		vectorStore:             vectorStore,
		retrievalDisabledReason: retrievalDisabledReason,
		busMetrics:              busMetrics,
		providerRegistry:        provider.NewRegistry(config.ProviderProfiles()),
	}
	svc.ensureFastPathCache()
	return svc
}

func (s *chatService) ensureFastPathCache() *sessionFastPathCache {
	if s == nil {
		return nil
	}
	s.fastPathOnce.Do(func() {
		if s.fastPathCache == nil {
			s.fastPathCache = newSessionFastPathCache()
		}
	})
	return s.fastPathCache
}

func (s *chatService) ChatCompletion(ctx context.Context, in *proto.ChatCompletionRequest) (*proto.ChatCompletionResponse, error) {
	app := s.newApp(in, s.contextStore, false)
	if err := app.resolveSession(ctx, in); err != nil {
		s.log.Error(err)
	}
	//敏感词过滤
	ok, msg, err := app.checkSensitive(ctx, in)
	if err != nil {
		s.log.Error(err)
		return nil, err
	}
	if !ok {
		res := app.buildChatCompletionResponse(msg)
		return res, nil
	}
	keywords := app.keywords(ctx, in)
	decision, err := app.resolveRouteDecision(ctx, in)
	if err != nil {
		s.log.Error(err)
	}
	app.routeDecision = decision
	app.retrievalHits = decision.RetrievalHits
	req, tokens, currTokens, _, err := app.buildChatCompletionRequest(ctx, in, false)
	if err != nil {
		s.log.Error(err)
		return nil, err
	}
	if decision.DirectReturnLocal {
		return s.respondWithKnowledgeUnary(ctx, app, in, decision, keywords, tokens, currTokens)
	}
	providerReq := llm.ChatRequest{
		Model:            req.Model,
		Messages:         req.Messages,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		Stream:           false,
		Timeout:          app.providerProfile.Timeout(),
		RequestID:        in.Id,
		SessionID:        app.sessionID,
	}
	result, err := app.provider.Chat(ctx, providerReq)
	if err != nil {
		s.log.Error(err)
		return nil, err
	}
	res := &proto.ChatCompletionResponse{
		Id:        result.ID,
		Object:    "chat.completion",
		Created:   time.Now().Unix(),
		Model:     result.Model,
		SessionId: app.sessionID,
		Choices: []*proto.ChatCompletionChoice{
			{
				Message: &proto.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: result.Message.Content,
				},
				FinishReason: "stop",
			},
		},
		Usage: &proto.Usage{
			PromptTokens:     int32(result.Usage.PromptTokens),
			CompletionTokens: int32(result.Usage.CompletionTokens),
			TotalTokens:      int32(result.Usage.TotalTokens),
		},
	}
	//启用协程保存上下文和聊天记录，避免阻塞主流程
	go func() {
		err := app.saveContext(result.ID, result.Message, result.Usage.CompletionTokens)
		if err != nil {
			s.log.Error(err)
			return
		}
	}()
	if result.Usage.UsageSource == "" {
		result.Usage.UsageSource = llm.UsageSourceProvider
	}
	s.persistChatRecord(in, keywords, tokens, currTokens, result.Message.Content, result.Usage.CompletionTokens)
	return res, nil
}
func (s *chatService) ChatCompletionStream(in *proto.ChatCompletionRequest, stream proto.Chat_ChatCompletionStreamServer) error {
	app := s.newApp(in, s.contextStore, true)
	if err := app.resolveSession(stream.Context(), in); err != nil {
		s.log.Error(err)
	}
	//敏感词过滤
	ok, msg, err := app.checkSensitive(stream.Context(), in)
	if err != nil {
		if s.busMetrics != nil {
			s.busMetrics.ErrQuestionsTotalCounter.Inc()
		}
		s.log.Error(err)
		return err
	}
	if !ok {
		if s.busMetrics != nil {
			s.busMetrics.SensitiveQuestionsTotalCounter.Inc()
		}
		resId := uuid.New().String()
		startRes := app.buildChatCompletionStreamResponse(resId, "", "")
		endRes := app.buildChatCompletionStreamResponse(resId, "", "stop")
		err = stream.Send(startRes)
		if err != nil {
			s.log.Error(err)
			return err
		}
		resList := app.buildChatCompletionStreamResponseList(resId, msg)
		for _, res := range resList {
			err = stream.Send(res)
			if err != nil {
				s.log.Error(err)
				return err
			}
		}
		err = stream.Send(endRes)
		if err != nil {
			s.log.Error(err)
			return err
		}
		return nil
	}
	decision, err := app.resolveRouteDecision(stream.Context(), in)
	if err != nil {
		s.log.Error(err)
	}
	keywords := app.keywords(stream.Context(), in)
	app.routeDecision = decision
	app.retrievalHits = decision.RetrievalHits
	req, tokens, currTokens, _, err := app.buildChatCompletionRequest(stream.Context(), in, false)
	if err != nil {
		if s.busMetrics != nil {
			s.busMetrics.ErrQuestionsTotalCounter.Inc()
		}
		s.log.Error(err)
		return err
	}
	if decision.DirectReturnLocal {
		return s.respondWithKnowledgeStream(stream, app, in, decision, keywords, tokens, currTokens)
	}
	providerReq := llm.ChatRequest{
		Model:            req.Model,
		Messages:         req.Messages,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		Stream:           true,
		Timeout:          app.providerProfile.Timeout(),
		RequestID:        in.Id,
		SessionID:        app.sessionID,
	}
	chatStream, err := app.provider.ChatStream(stream.Context(), providerReq)
	if err != nil {
		if s.busMetrics != nil {
			s.busMetrics.ErrQuestionsTotalCounter.Inc()
		}
		s.log.Error(err)
		return err
	}
	defer chatStream.Close()
	completionContent := ""
	resultID := ""
	for {
		resp, err := chatStream.Recv()
		if err != nil && err != io.EOF {
			s.log.Error(err)
			return err
		}
		if err == io.EOF {
			break
		}
		if resultID == "" {
			resultID = resp.ID
		}
		completionContent += resp.Choices[0].Delta.Content
		res := &proto.ChatCompletionStreamResponse{}
		bytes, err := json.Marshal(resp)
		if err != nil {
			s.log.Error(err)
			return err
		}
		err = jsonpb.UnmarshalString(string(bytes), res)
		if err != nil {
			s.log.Error(err)
			return err
		}
		err = stream.Send(res)
		if err != nil {
			s.log.Error(err)
			return err
		}
	}
	if resultID == "" {
		resultID = uuid.New().String()
	}
	resultMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: completionContent,
	}
	resultTokens, resultTokenSource, err := tokenizer.GetTokensWithSource(&resultMessage, app.openaiConf.Model)
	if err != nil {
		if s.busMetrics != nil {
			s.busMetrics.ErrQuestionsTotalCounter.Inc()
		}
		s.log.Error(err)
		return err
	}
	usage := llm.UsageInfo{
		PromptTokens:     tokens,
		CompletionTokens: resultTokens,
		TotalTokens:      tokens + resultTokens,
		UsageSource:      resultTokenSource,
	}
	trace := app.buildAnswerTrace(decision, llm.FinalAnswerSourceLLM, false, false)
	meta := app.buildResponseMeta(trace, usage, tokens)
	app.setResponseMetaTrailer(stream.Context(), meta)
	go func() {
		err := app.saveContext(resultID, resultMessage, resultTokens)
		if err != nil {
			s.log.Error(err)
			return
		}
	}()
	if s.busMetrics != nil {
		s.busMetrics.QuestionsTotalCounter.Inc()
	}
	s.persistChatRecord(in, keywords, tokens, currTokens, completionContent, resultTokens)
	return nil
}

func (s *chatService) respondWithKnowledgeUnary(ctx context.Context, app *app, in *proto.ChatCompletionRequest, decision llm.RouteDecision, keywords []string, promptTokens, userTokens int) (*proto.ChatCompletionResponse, error) {
	assistantMessage := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: decision.LocalAnswer}
	completionTokens, tokenSource, err := tokenizer.GetTokensWithSource(&assistantMessage, app.openaiConf.Model)
	if err != nil {
		s.log.Error(err)
	}
	if completionTokens == 0 {
		completionTokens = len([]rune(assistantMessage.Content)) / 4
	}
	res := app.buildChatCompletionResponse(assistantMessage.Content)
	res.Usage = &proto.Usage{
		PromptTokens:     int32(promptTokens),
		CompletionTokens: int32(completionTokens),
		TotalTokens:      int32(promptTokens + completionTokens),
	}
	trace := app.buildAnswerTrace(decision, llm.FinalAnswerSourceKVLite, true, false)
	meta := app.buildResponseMeta(trace, llm.UsageInfo{
		PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens, UsageSource: tokenSource,
	}, promptTokens)
	app.setResponseMetaTrailer(ctx, meta)
	go func() {
		if err := app.saveContext(res.Id, assistantMessage, completionTokens); err != nil {
			s.log.Error(err)
		}
	}()
	s.persistChatRecord(in, keywords, promptTokens, userTokens, assistantMessage.Content, completionTokens)
	return res, nil
}

func (s *chatService) respondWithKnowledgeStream(stream proto.Chat_ChatCompletionStreamServer, app *app, in *proto.ChatCompletionRequest, decision llm.RouteDecision, keywords []string, promptTokens, userTokens int) error {
	assistantMessage := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: decision.LocalAnswer}
	completionTokens, tokenSource, err := tokenizer.GetTokensWithSource(&assistantMessage, app.openaiConf.Model)
	if err != nil {
		s.log.Error(err)
	}
	if completionTokens == 0 {
		completionTokens = len([]rune(assistantMessage.Content)) / 4
	}
	trace := app.buildAnswerTrace(decision, llm.FinalAnswerSourceKVLite, true, false)
	meta := app.buildResponseMeta(trace, llm.UsageInfo{
		PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens, UsageSource: tokenSource,
	}, promptTokens)
	app.setResponseMetaTrailer(stream.Context(), meta)
	resultID := uuid.New().String()
	if err := stream.Send(app.buildChatCompletionStreamResponse(resultID, "", "")); err != nil {
		return err
	}
	for _, res := range app.buildChatCompletionStreamResponseList(resultID, assistantMessage.Content) {
		if err := stream.Send(res); err != nil {
			return err
		}
	}
	if err := stream.Send(app.buildChatCompletionStreamResponse(resultID, "", "stop")); err != nil {
		return err
	}
	go func() {
		if err := app.saveContext(resultID, assistantMessage, completionTokens); err != nil {
			s.log.Error(err)
		}
	}()
	if s.busMetrics != nil {
		s.busMetrics.QuestionsTotalCounter.Inc()
	}
	s.persistChatRecord(in, keywords, promptTokens, userTokens, assistantMessage.Content, completionTokens)
	return nil
}

func (s *chatService) persistChatRecord(in *proto.ChatCompletionRequest, keywords []string, promptTokens, userTokens int, answer string, answerTokens int) {
	if s == nil || s.data == nil || in == nil {
		return
	}
	go func() {
		records := &data.ChatRecord{
			UserMsg:         in.Message,
			UserMsgTokens:   userTokens,
			UserMsgKeywords: keywords,
			AIMsg:           answer,
			AIMsgTokens:     answerTokens,
			ReqTokens:       promptTokens,
			CreateAt:        time.Now().Unix(),
		}
		if err := s.data.Add(records); err != nil {
			s.log.Error(err)
		}
	}()
}

func (a *app) buildAnswerTrace(decision llm.RouteDecision, finalAnswerSource string, usedLocalRetrieval bool, cacheHit bool) llm.AnswerTrace {
	providerName := ""
	modelName := ""
	selectedProvider := ""
	selectedModel := ""
	fallbackUsed := false
	fallbackReason := ""
	if a != nil {
		providerName = a.providerProfile.Name
		modelName = a.openaiConf.Model
		selectedProvider = a.selectedProvider
		selectedModel = a.selectedModel
		fallbackUsed = a.fallbackUsed
		fallbackReason = a.fallbackReason
	}
	trace := llm.AnswerTrace{
		AnswerMode:         decision.Mode,
		Provider:           providerName,
		Model:              modelName,
		SelectedProvider:   selectedProvider,
		SelectedModel:      selectedModel,
		ActualProvider:     providerName,
		ActualModel:        modelName,
		FallbackUsed:       fallbackUsed,
		FallbackReason:     fallbackReason,
		AnswerSourceKind:   answerSourceKindFromDecision(cacheHit),
		UsedLocalRetrieval: usedLocalRetrieval,
		CacheHit:           cacheHit,
		FinalAnswerSource:  finalAnswerSource,
		GenerationProvider: providerName,
		GenerationModel:    modelName,
		RetrievedHits:      decision.RetrievalHits,
	}
	if usedLocalRetrieval {
		trace.AnswerSourceKind = llm.AnswerSourceKindKnowledgeBase
	}
	return trace
}

func answerSourceKindFromDecision(_ bool) string {
	return llm.AnswerSourceKindAI
}
