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
	"ai-chat-service/proto"
	"ai-chat-service/services/tokenizer"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/protobuf/jsonpb"
	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

type chatService struct {
	proto.UnimplementedChatServer
	config           *config.Config
	log              log.ILogger
	contextStore     chat_context.SessionStore
	qaRetriever      qa.Retriever
	qaStore          qa.Store
	data             data.IChatRecordsData        //聊天记录数据库
	vectorData       vector_data.IChatRecordsData //向量数据库
	providerRegistry *provider.Registry
	busMetrics       *metrics_bus.BusMetrics
	keywordsFn       func(context.Context, *proto.ChatCompletionRequest) []string
	sensitiveFn      func(context.Context, *proto.ChatCompletionRequest) (bool, string, error)
	fastPathOnce     sync.Once
	fastPathCache    *sessionFastPathCache
}

func NewChatService(data data.IChatRecordsData, vectorData vector_data.IChatRecordsData, config *config.Config, log log.ILogger, busMetrics *metrics_bus.BusMetrics, contextStore chat_context.SessionStore, qaRetriever ...qa.Retriever) proto.ChatServer {
	retriever := qa.NewNoopRetriever()
	if len(qaRetriever) > 0 && qaRetriever[0] != nil {
		retriever = qaRetriever[0]
	}
	var qaStore qa.Store
	if store, ok := retriever.(qa.Store); ok {
		qaStore = store
	}
	svc := &chatService{
		config:           config,
		log:              log,
		contextStore:     contextStore,
		qaRetriever:      retriever,
		qaStore:          qaStore,
		data:             data,
		vectorData:       vectorData,
		busMetrics:       busMetrics,
		providerRegistry: provider.NewRegistry(config.ProviderProfiles()),
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
	if hit, preflightResult, preflightErr := app.tryQAPreflight(ctx, in); preflightErr != nil {
		s.log.Error(preflightErr)
	} else if hit {
		res, meta, buildErr := app.buildQAPreflightUnaryResponse(ctx, in, preflightResult)
		if buildErr != nil {
			s.log.Error(buildErr)
		} else {
			app.setResponseMetaTrailer(ctx, meta)
			return res, nil
		}
	}

	//关键词提取
	//应用向量数据库时 考虑使用msg直接对比 而不是使用关键词对比 因为有些问题可能没有明显的关键词 但是向量数据库可以通过语义理解找到相似的问题
	keywords := app.keywords(ctx, in)
	if len(keywords) > 0 {
		idStr, score, err := s.vectorData.QueryData(context.Background(), map[string][]string{"keywords": {strings.Join(keywords, ",")}})
		if err != nil {
			s.log.Error(err)
		} else if score > 0.99 { //余弦相似度大于0.99认为是相似问题
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				s.log.Error(err)
			} else {
				record, err := s.data.GetById(id)
				if err != nil {
					s.log.Error(err)
				} else {
					res := app.buildChatCompletionResponse(record.AIMsg)
					return res, nil
				}
			}
		}
	}
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
		res := app.buildChatCompletionResponse(decision.LocalAnswer)
		return res, nil
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
	app.queueQABackfill(ctx, in, result.Message.Content, result.Provider, result.Model)
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
	//持久化
	go func() {
		records := &data.ChatRecord{
			UserMsg:         in.Message,
			UserMsgTokens:   currTokens,
			UserMsgKeywords: keywords,
			AIMsg:           result.Message.Content,
			AIMsgTokens:     result.Usage.CompletionTokens,
			ReqTokens:       tokens,
			CreateAt:        time.Now().Unix(),
		}
		err := s.data.Add(records)
		if err != nil {
			s.log.Error(err)
			return
		}
		//保存到向量数据库
		if len(keywords) > 0 {
			list := []*vector_data.ChatRecord{
				{
					ID: strconv.FormatInt(records.ID, 10),
					KVs: map[string]string{
						"keywords":          strings.Join(keywords, ","),
						"provider":          app.providerProfile.Name,
						"model":             app.openaiConf.Model,
						"selected_provider": app.selectedProvider,
						"selected_model":    app.selectedModel,
					},
				},
			}
			err = s.vectorData.UpsertData(context.Background(), list)
			if err != nil {
				s.log.Error(err)
				return
			}
		}
	}()
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
	if hit, preflightResult, preflightErr := app.tryQAPreflight(stream.Context(), in); preflightErr != nil {
		s.log.Error(preflightErr)
	} else if hit {
		chunks, meta, buildErr := app.buildQAPreflightStreamResponses(stream.Context(), in, preflightResult)
		if buildErr != nil {
			s.log.Error(buildErr)
		} else {
			app.setResponseMetaTrailer(stream.Context(), meta)
			for _, chunk := range chunks {
				if err := stream.Send(chunk); err != nil {
					s.log.Error(err)
					return err
				}
			}
			return nil
		}
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
		resultID := uuid.New().String()
		localMessage := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: decision.LocalAnswer}
		resultTokens, resultTokenSource, tokenErr := tokenizer.GetTokensWithSource(&localMessage, app.openaiConf.Model)
		if tokenErr != nil {
			s.log.Error(tokenErr)
		}
		if resultTokens == 0 {
			resultTokens = len([]rune(localMessage.Content)) / 4
		}
		usage := llm.UsageInfo{
			PromptTokens:     tokens,
			CompletionTokens: resultTokens,
			TotalTokens:      tokens + resultTokens,
			UsageSource:      resultTokenSource,
		}
		trace := app.buildAnswerTrace(decision, llm.FinalAnswerSourceLocal, true, decision.Mode == llm.AnswerModeCacheHit)
		meta := app.buildResponseMeta(trace, usage, tokens)
		app.setResponseMetaTrailer(stream.Context(), meta)
		startRes := app.buildChatCompletionStreamResponse(resultID, "", "")
		endRes := app.buildChatCompletionStreamResponse(resultID, "", "stop")
		if err := stream.Send(startRes); err != nil {
			s.log.Error(err)
			return err
		}
		resList := app.buildChatCompletionStreamResponseList(resultID, decision.LocalAnswer)
		for _, res := range resList {
			if err := stream.Send(res); err != nil {
				s.log.Error(err)
				return err
			}
		}
		if err := stream.Send(endRes); err != nil {
			s.log.Error(err)
			return err
		}
		go func() {
			if err := app.saveContext(resultID, localMessage, resultTokens); err != nil {
				s.log.Error(err)
			}
		}()
		return nil
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
	trace := app.buildAnswerTrace(decision, llm.FinalAnswerSourceLLM, decision.UseRetrievalAsContext, false)
	if decision.UseRetrievalAsContext {
		trace.FinalAnswerSource = llm.FinalAnswerSourceHybridLLM
	}
	meta := app.buildResponseMeta(trace, usage, tokens)
	app.setResponseMetaTrailer(stream.Context(), meta)
	app.queueQABackfill(stream.Context(), in, completionContent, app.providerProfile.Name, app.openaiConf.Model)

	go func() {
		err := app.saveContext(resultID, resultMessage, resultTokens)
		if err != nil {
			s.log.Error(err)
			return
		}
	}()
	go func() {
		if s.busMetrics != nil {
			s.busMetrics.QuestionsTotalCounter.Inc()
		}
		records := &data.ChatRecord{
			UserMsg:         in.Message,
			UserMsgTokens:   currTokens,
			UserMsgKeywords: keywords,
			AIMsg:           completionContent,
			AIMsgTokens:     resultTokens,
			ReqTokens:       tokens,
			CreateAt:        time.Now().Unix(),
		}
		err := s.data.Add(records)
		if err != nil {
			s.log.Error(err)
			return
		}
		//保存到向量数据库
		if len(keywords) > 0 {
			list := []*vector_data.ChatRecord{
				{
					ID: strconv.FormatInt(records.ID, 10),
					KVs: map[string]string{
						"keywords":          strings.Join(keywords, ","),
						"provider":          app.providerProfile.Name,
						"model":             app.openaiConf.Model,
						"selected_provider": app.selectedProvider,
						"selected_model":    app.selectedModel,
					},
				},
			}
			err = s.vectorData.UpsertData(context.Background(), list)
			if err != nil {
				s.log.Error(err)
				return
			}
		}
	}()
	return nil
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
		UsedLocalRetrieval: usedLocalRetrieval,
		CacheHit:           cacheHit,
		FinalAnswerSource:  finalAnswerSource,
		GenerationProvider: providerName,
		GenerationModel:    modelName,
		RetrievedHits:      decision.RetrievalHits,
	}
	return trace
}
