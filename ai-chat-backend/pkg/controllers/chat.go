package controllers

import (
	"ai-chat-backend/pkg/config"
	"ai-chat-backend/pkg/log"
	"ai-chat-backend/services"
	ai_chat_service "ai-chat-backend/services/ai-chat-service"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	ai_chat_service_proto "ai-chat-backend/services/ai-chat-service/proto"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openai "github.com/sashabaranov/go-openai"
	"google.golang.org/grpc/metadata"
	"k8s.io/klog/v2"
)

type ChatService struct {
	config *config.Config
	log    log.ILogger
}

type ChatCompletionParams struct {
	Model                 string        `json:"model"`
	MaxTokens             int           `json:"max_tokens,omitempty"`
	Temperature           float32       `json:"temperature,omitempty"`
	PresencePenalty       float32       `json:"presence_penalty,omitempty"`
	FrequencyPenalty      float32       `json:"frequency_penalty,omitempty"`
	ChatSessionTTL        time.Duration `json:"chat_session_ttl"`
	ChatMinResponseTokens int           `json:"chat_min_response_tokens"`
}

type ChatMessageRequest struct {
	Prompt        string                    `json:"prompt"`
	Provider      string                    `json:"provider,omitempty"`
	Model         string                    `json:"model,omitempty"`
	SystemMessage string                    `json:"systemMessage,omitempty"`
	Options       ChatMessageRequestOptions `json:"options"`
}

type ChatMessageRequestOptions struct {
	Name            string `json:"name"`
	SessionId       string `json:"sessionId"`
	ParentMessageId string `json:"parentMessageId"`
}

type ChatMessage struct {
	ID              string                                              `json:"id"`
	SessionID       string                                              `json:"sessionId"`
	ConversationID  string                                              `json:"conversationId,omitempty"`
	Text            string                                              `json:"text"`
	Role            string                                              `json:"role"`
	Name            string                                              `json:"name"`
	Delta           string                                              `json:"delta"`
	Detail          *ai_chat_service_proto.ChatCompletionStreamResponse `json:"detail"`
	TokenCount      int                                                 `json:"tokenCount"`
	ParentMessageId string                                              `json:"parentMessageId"`
	Trace           *AnswerTrace                                        `json:"trace,omitempty"`
	Usage           *UsageInfo                                          `json:"usage,omitempty"`
	Budget          *BudgetInfo                                         `json:"budget,omitempty"`
}

type AnswerTrace struct {
	AnswerMode                  string         `json:"answer_mode"`
	Provider                    string         `json:"provider"`
	Model                       string         `json:"model"`
	SelectedProvider            string         `json:"selected_provider,omitempty"`
	SelectedModel               string         `json:"selected_model,omitempty"`
	ActualProvider              string         `json:"actual_provider,omitempty"`
	ActualModel                 string         `json:"actual_model,omitempty"`
	FallbackUsed                bool           `json:"fallback_used,omitempty"`
	FallbackReason              string         `json:"fallback_reason,omitempty"`
	AnswerSourceKind            string         `json:"answer_source_kind,omitempty"`
	UsedLocalRetrieval          bool           `json:"used_local_retrieval"`
	CacheHit                    bool           `json:"cache_hit"`
	QAHitType                   string         `json:"qa_hit_type,omitempty"`
	CandidateCount              int            `json:"candidate_count,omitempty"`
	CandidateTopScore           float64        `json:"candidate_top_score,omitempty"`
	CandidateSource             string         `json:"candidate_source,omitempty"`
	SemanticCandidateCount      int            `json:"semantic_candidate_count,omitempty"`
	SemanticTopK                int            `json:"semantic_top_k,omitempty"`
	SemanticTopScore            float64        `json:"semantic_top_score,omitempty"`
	SemanticANNTopK             int            `json:"semantic_ann_top_k,omitempty"`
	SemanticANNTopScore         float64        `json:"semantic_ann_top_score,omitempty"`
	SemanticANNTop2Score        float64        `json:"semantic_ann_top2_score,omitempty"`
	SemanticANNScoreGap         float64        `json:"semantic_ann_score_gap,omitempty"`
	SemanticANNMinScoreGap      float64        `json:"semantic_ann_min_score_gap,omitempty"`
	SemanticANNDecision         string         `json:"semantic_ann_decision,omitempty"`
	SemanticModeRequested       string         `json:"semantic_mode_requested,omitempty"`
	SemanticModeUsed            string         `json:"semantic_mode_used,omitempty"`
	SemanticFallbackUsed        bool           `json:"semantic_fallback_used,omitempty"`
	SemanticFallbackReason      string         `json:"semantic_fallback_reason,omitempty"`
	SemanticLatencyMS           float64        `json:"semantic_latency_ms,omitempty"`
	SemanticANNLatencyMS        float64        `json:"semantic_ann_latency_ms,omitempty"`
	SemanticBruteForceLatencyMS float64        `json:"semantic_brute_force_latency_ms,omitempty"`
	SemanticModel               string         `json:"semantic_model,omitempty"`
	SemanticSource              string         `json:"semantic_source,omitempty"`
	FinalAnswerSource           string         `json:"final_answer_source"`
	GenerationProvider          string         `json:"generation_provider,omitempty"`
	GenerationModel             string         `json:"generation_model,omitempty"`
	RetrievedHits               []RetrievalHit `json:"retrieved_hits,omitempty"`
}

type RetrievalHit struct {
	RecordID        string  `json:"record_id"`
	Score           float64 `json:"score"`
	HitType         string  `json:"hit_type"`
	QuestionPreview string  `json:"question_preview"`
	AnswerPreview   string  `json:"answer_preview"`
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
}

type UsageInfo struct {
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	UsageSource      string `json:"usage_source"`
}

type BudgetInfo struct {
	MaxContextTokens                int    `json:"max_context_tokens"`
	ReservedCompletionTokens        int    `json:"reserved_completion_tokens"`
	EstimatedRemainingContextTokens int    `json:"estimated_remaining_context_tokens"`
	BudgetSource                    string `json:"budget_source"`
}

type ResponseMeta struct {
	Trace  AnswerTrace `json:"trace"`
	Usage  UsageInfo   `json:"usage"`
	Budget BudgetInfo  `json:"budget"`
}

func NewChatService(config *config.Config, log log.ILogger) (*ChatService, error) {
	return &ChatService{
		config: config,
		log:    log,
	}, nil
}

func (chat *ChatService) Config(ctx *gin.Context) {
	data := chat.config.ProviderCatalog()
	ctx.JSON(200, gin.H{
		"status":  "Success",
		"message": "",
		"data":    data,
	})
}

// HTTP入口到gRPC的桥梁 将HTTP请求转换为gRPC请求，并将gRPC响应转换为HTTP响应
func (chat *ChatService) ChatProcess(ctx *gin.Context) {
	requestCtx := ctx.Request.Context()
	payload := ChatMessageRequest{}
	if err := ctx.BindJSON(&payload); err != nil {
		klog.Error(err)
		ctx.JSON(200, gin.H{
			"status":  "Fail",
			"message": fmt.Sprintf("%v", err),
			"data":    nil,
		})
		return
	}

	messageID := uuid.New().String()
	defaultProvider, defaultModel := chat.defaultProviderSelection()
	selectedProvider := strings.TrimSpace(payload.Provider)
	selectedModel := strings.TrimSpace(payload.Model)
	systemMessage := chat.config.Chat.BotDesc
	if strings.TrimSpace(payload.SystemMessage) != "" {
		systemMessage = payload.SystemMessage
	}

	result := ChatMessage{
		ID:              uuid.New().String(),
		Role:            openai.ChatMessageRoleAssistant,
		Text:            "",
		ParentMessageId: messageID,
		SessionID:       payload.Options.SessionId,
		ConversationID:  payload.Options.SessionId,
		Trace: &AnswerTrace{
			AnswerMode:         "api",
			Provider:           defaultProvider,
			Model:              defaultModel,
			SelectedProvider:   selectedProvider,
			SelectedModel:      selectedModel,
			ActualProvider:     defaultProvider,
			ActualModel:        defaultModel,
			AnswerSourceKind:   "ai",
			UsedLocalRetrieval: false,
			CacheHit:           false,
			FinalAnswerSource:  "llm",
			GenerationProvider: defaultProvider,
			GenerationModel:    defaultModel,
		},
		Usage:  &UsageInfo{UsageSource: "estimated"},
		Budget: &BudgetInfo{BudgetSource: "estimated"},
	}

	// 从连接池获取AI聊天服务的客户端连接，并在函数结束时将连接放回池中
	aiChatServicePool := ai_chat_service.GetAiChatServiceClientPool()
	conn := aiChatServicePool.Get()
	defer aiChatServicePool.Put(conn)
	// 将访问令牌添加到上下文中，以便在调用AI聊天服务时进行身份验证
	ctx1 := services.AppendBearerTokenToContext(requestCtx, chat.config.DependOn.AiChatService.AccessToken)
	// 构造AI聊天服务的请求参数
	in := &ai_chat_service_proto.ChatCompletionRequest{
		Id:            messageID,
		Message:       payload.Prompt,
		Pid:           payload.Options.ParentMessageId,
		SessionId:     payload.Options.SessionId,
		EnableContext: payload.Options.SessionId != "" || payload.Options.ParentMessageId != "",
		ChatParam: &ai_chat_service_proto.ChatParam{
			Provider:          selectedProvider,
			Model:             selectedModel,
			MaxTokens:         int32(chat.config.Chat.MaxTokens),
			Temperature:       chat.config.Chat.Temperature,
			TopP:              chat.config.Chat.TopP,
			PresencePenalty:   chat.config.Chat.PresencePenalty,
			FrequencyPenalty:  chat.config.Chat.FrequencyPenalty,
			BotDesc:           systemMessage,
			ContextTTL:        int32(chat.config.Chat.ContextTTL),
			ContextLen:        int32(chat.config.Chat.ContextLen),
			MinResponseTokens: int32(chat.config.Chat.MinResponseTokens),
		},
	}
	// 调用AI聊天服务的ChatCompletionStream方法，获取一个流式响应，以便逐步接收AI生成的回复内容
	aiChatServiceClient := ai_chat_service_proto.NewChatClient(conn)
	stream, err := aiChatServiceClient.ChatCompletionStream(ctx1, in)
	if err != nil {
		chat.log.Error(err)
		ctx.JSON(200, gin.H{
			"status":  "Fail",
			"message": fmt.Sprintf("%v", err),
			"data":    nil,
		})
		return
	}
	defer stream.CloseSend()

	// 以Chunk方式将响应发送给客户端，以便客户端能够实时显示AI生成的回复内容
	firstChunk := true
	ctx.Header("Content-type", "application/octet-stream")
	for {
		rsp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			trailers := stream.Trailer()
			if meta := parseResponseMeta(trailers); meta != nil {
				mergeResponseMeta(&result, meta)
				if !firstChunk {
					ctx.Writer.Write([]byte("\n"))
				}
				bts, marshalErr := json.Marshal(result)
				if marshalErr == nil {
					_, _ = ctx.Writer.Write(bts)
					ctx.Writer.Flush()
				}
			}
			return
		}

		if err != nil {
			if requestCtx.Err() != nil {
				return
			}
			if ctx.Writer.Written() {
				return
			}
			klog.Error(err)
			ctx.JSON(200, gin.H{
				"status":  "Fail",
				"message": fmt.Sprintf("OpenAI Event Error %v", err),
				"data":    nil,
			})
			return
		}

		if rsp.Id != "" {
			result.ID = rsp.Id
		}
		if rsp.SessionId != "" {
			result.SessionID = rsp.SessionId
			result.ConversationID = rsp.SessionId
		}
		if rsp.Model != "" {
			providerName := detectProviderFromModel(rsp.Model)
			if result.Trace == nil {
				result.Trace = &AnswerTrace{}
			}
			result.Trace.Provider = providerName
			result.Trace.GenerationProvider = providerName
			result.Trace.Model = rsp.Model
			result.Trace.GenerationModel = rsp.Model
			result.Trace.ActualProvider = providerName
			result.Trace.ActualModel = rsp.Model
		}

		if len(rsp.Choices) > 0 {
			content := rsp.Choices[0].Delta.Content
			result.Delta = content
			if len(content) > 0 {
				result.Text += content
			}
			result.Detail = rsp
		}

		bts, err := json.Marshal(result)
		if err != nil {
			klog.Error(err)
			ctx.JSON(200, gin.H{
				"status":  "Fail",
				"message": fmt.Sprintf("OpenAI Event Marshal Error %v", err),
				"data":    nil,
			})
			return
		}

		if !firstChunk {
			ctx.Writer.Write([]byte("\n"))
		} else {
			firstChunk = false
		}

		if _, err := ctx.Writer.Write(bts); err != nil {
			klog.Error(err)
			return
		}

		ctx.Writer.Flush()
	}
}

func (chat *ChatService) defaultProviderSelection() (string, string) {
	if chat == nil || chat.config == nil {
		return "", ""
	}
	if profile, ok := chat.config.DefaultProviderProfile(); ok {
		return profile.Name, profile.DefaultModel
	}
	return detectProviderFromModel(chat.config.Chat.Model), chat.config.Chat.Model
}

func detectProviderFromModel(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "qwen"):
		return "qwen"
	case strings.Contains(lower, "deepseek"):
		return "deepseek"
	case strings.Contains(lower, "local"):
		return "local"
	default:
		return "openai"
	}
}

func parseResponseMeta(md metadata.MD) *ResponseMeta {
	if len(md) == 0 {
		return nil
	}
	meta := &ResponseMeta{}
	loaded := false
	if values := md.Get("x-ai-trace"); len(values) > 0 {
		_ = json.Unmarshal([]byte(values[len(values)-1]), &meta.Trace)
		loaded = true
	}
	if values := md.Get("x-ai-usage"); len(values) > 0 {
		_ = json.Unmarshal([]byte(values[len(values)-1]), &meta.Usage)
		loaded = true
	}
	if values := md.Get("x-ai-budget"); len(values) > 0 {
		_ = json.Unmarshal([]byte(values[len(values)-1]), &meta.Budget)
		loaded = true
	}
	if !loaded {
		return nil
	}
	if meta.Trace.AnswerMode == "" {
		meta.Trace.AnswerMode = "api"
	}
	if meta.Trace.FinalAnswerSource == "" {
		meta.Trace.FinalAnswerSource = "llm"
	}
	if meta.Trace.AnswerSourceKind == "" {
		if meta.Trace.CacheHit || strings.HasPrefix(meta.Trace.FinalAnswerSource, "qa_") || meta.Trace.FinalAnswerSource == "local_cache" {
			meta.Trace.AnswerSourceKind = "cache"
		} else {
			meta.Trace.AnswerSourceKind = "ai"
		}
	}
	if meta.Usage.UsageSource == "" {
		meta.Usage.UsageSource = "estimated"
	}
	if meta.Budget.BudgetSource == "" {
		meta.Budget.BudgetSource = "estimated"
	}
	return meta
}

func mergeResponseMeta(result *ChatMessage, meta *ResponseMeta) {
	if result == nil || meta == nil {
		return
	}
	if result.Trace == nil {
		result.Trace = &AnswerTrace{}
	}
	if meta.Trace.AnswerMode != "" {
		result.Trace.AnswerMode = meta.Trace.AnswerMode
	}
	if meta.Trace.Provider != "" {
		result.Trace.Provider = meta.Trace.Provider
	}
	if meta.Trace.Model != "" {
		result.Trace.Model = meta.Trace.Model
	}
	if meta.Trace.SelectedProvider != "" {
		result.Trace.SelectedProvider = meta.Trace.SelectedProvider
	}
	if meta.Trace.SelectedModel != "" {
		result.Trace.SelectedModel = meta.Trace.SelectedModel
	}
	if meta.Trace.ActualProvider != "" {
		result.Trace.ActualProvider = meta.Trace.ActualProvider
	}
	if meta.Trace.ActualModel != "" {
		result.Trace.ActualModel = meta.Trace.ActualModel
	}
	result.Trace.FallbackUsed = result.Trace.FallbackUsed || meta.Trace.FallbackUsed
	if meta.Trace.FallbackReason != "" {
		result.Trace.FallbackReason = meta.Trace.FallbackReason
	}
	if meta.Trace.AnswerSourceKind != "" {
		result.Trace.AnswerSourceKind = meta.Trace.AnswerSourceKind
	}
	result.Trace.UsedLocalRetrieval = meta.Trace.UsedLocalRetrieval
	result.Trace.CacheHit = meta.Trace.CacheHit
	if meta.Trace.QAHitType != "" {
		result.Trace.QAHitType = meta.Trace.QAHitType
	}
	result.Trace.CandidateCount = meta.Trace.CandidateCount
	result.Trace.CandidateTopScore = meta.Trace.CandidateTopScore
	if meta.Trace.CandidateSource != "" {
		result.Trace.CandidateSource = meta.Trace.CandidateSource
	}
	result.Trace.SemanticCandidateCount = meta.Trace.SemanticCandidateCount
	result.Trace.SemanticTopK = meta.Trace.SemanticTopK
	result.Trace.SemanticTopScore = meta.Trace.SemanticTopScore
	result.Trace.SemanticANNTopK = meta.Trace.SemanticANNTopK
	result.Trace.SemanticANNTopScore = meta.Trace.SemanticANNTopScore
	result.Trace.SemanticANNTop2Score = meta.Trace.SemanticANNTop2Score
	result.Trace.SemanticANNScoreGap = meta.Trace.SemanticANNScoreGap
	result.Trace.SemanticANNMinScoreGap = meta.Trace.SemanticANNMinScoreGap
	if meta.Trace.SemanticANNDecision != "" {
		result.Trace.SemanticANNDecision = meta.Trace.SemanticANNDecision
	}
	if meta.Trace.SemanticModeRequested != "" {
		result.Trace.SemanticModeRequested = meta.Trace.SemanticModeRequested
	}
	if meta.Trace.SemanticModeUsed != "" {
		result.Trace.SemanticModeUsed = meta.Trace.SemanticModeUsed
	}
	result.Trace.SemanticFallbackUsed = meta.Trace.SemanticFallbackUsed
	if meta.Trace.SemanticFallbackReason != "" {
		result.Trace.SemanticFallbackReason = meta.Trace.SemanticFallbackReason
	}
	if meta.Trace.SemanticLatencyMS != 0 {
		result.Trace.SemanticLatencyMS = meta.Trace.SemanticLatencyMS
	}
	if meta.Trace.SemanticANNLatencyMS != 0 {
		result.Trace.SemanticANNLatencyMS = meta.Trace.SemanticANNLatencyMS
	}
	if meta.Trace.SemanticBruteForceLatencyMS != 0 {
		result.Trace.SemanticBruteForceLatencyMS = meta.Trace.SemanticBruteForceLatencyMS
	}
	if meta.Trace.SemanticModel != "" {
		result.Trace.SemanticModel = meta.Trace.SemanticModel
	}
	if meta.Trace.SemanticSource != "" {
		result.Trace.SemanticSource = meta.Trace.SemanticSource
	}
	if meta.Trace.FinalAnswerSource != "" {
		result.Trace.FinalAnswerSource = meta.Trace.FinalAnswerSource
	}
	if meta.Trace.GenerationProvider != "" {
		result.Trace.GenerationProvider = meta.Trace.GenerationProvider
	}
	if meta.Trace.GenerationModel != "" {
		result.Trace.GenerationModel = meta.Trace.GenerationModel
	}
	if len(meta.Trace.RetrievedHits) > 0 {
		result.Trace.RetrievedHits = meta.Trace.RetrievedHits
	}
	if meta.Usage.TotalTokens > 0 || meta.Budget.MaxContextTokens > 0 {
		result.Usage = &meta.Usage
		result.Budget = &meta.Budget
	}
	if meta.Usage.TotalTokens > 0 {
		result.TokenCount = meta.Usage.TotalTokens
	}
	if result.Usage == nil {
		result.Usage = &UsageInfo{}
	}
	if result.Budget == nil {
		result.Budget = &BudgetInfo{}
	}
}
