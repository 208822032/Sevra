package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/sashabaranov/go-openai"
)

const (
	AnswerModeAPI      = "api"
	AnswerModeLocal    = "local"
	AnswerModeHybrid   = "hybrid"
	AnswerModeCacheHit = "cache_hit"

	FinalAnswerSourceLLM       = "llm"
	FinalAnswerSourceLocal     = "local_cache"
	FinalAnswerSourceHybridLLM = "hybrid_llm"

	UsageSourceProvider  = "provider"
	UsageSourceEstimated = "estimated"

	BudgetSourceExact     = "exact"
	BudgetSourceEstimated = "estimated"
	BudgetSourceFallback  = "fallback"

	SourceTypeAPI       = "api"
	SourceTypeLocal     = "local"
	SourceTypeHybrid    = "hybrid"
	SourceTypeCacheHit  = "cache_hit"
	SourceTypeRetrieval = "retrieval"
)

type ErrorClass string

const (
	ErrorClassCanceled    ErrorClass = "canceled"
	ErrorClassTimeout     ErrorClass = "timeout"
	ErrorClassAuth        ErrorClass = "auth"
	ErrorClassRateLimit   ErrorClass = "rate_limit"
	ErrorClassInvalid     ErrorClass = "invalid_request"
	ErrorClassUpstream    ErrorClass = "upstream_error"
	ErrorClassConfig      ErrorClass = "config_error"
	ErrorClassUnavailable ErrorClass = "unavailable"
	ErrorClassUnknown     ErrorClass = "unknown"
)

type ProviderError struct {
	Class   ErrorClass
	Source  string
	Model   string
	Message string
	Err     error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("provider error: class=%s source=%s model=%s", e.Class, e.Source, e.Model)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type CapabilitySet struct {
	SupportsChat           bool `json:"supports_chat"`
	SupportsStream         bool `json:"supports_stream"`
	SupportsNonStream      bool `json:"supports_non_stream"`
	SupportsEmbeddings     bool `json:"supports_embeddings"`
	SupportsToolCalls      bool `json:"supports_tool_calls"`
	SupportsSystemPrompt   bool `json:"supports_system_prompt"`
	SupportsUsageReporting bool `json:"supports_usage_reporting"`
}

type RetrievalHit struct {
	RecordID        string  `json:"record_id"`
	Score           float64 `json:"score"`
	HitType         string  `json:"hit_type"`
	QuestionPreview string  `json:"question_preview"`
	AnswerPreview   string  `json:"answer_preview"`
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	SessionID       string  `json:"session_id,omitempty"`
	TurnSeq         int64   `json:"turn_seq,omitempty"`
	SourceType      string  `json:"source_type,omitempty"`
}

type QARecord struct {
	RecordID           string            `json:"record_id"`
	SessionID          string            `json:"session_id"`
	TurnSeq            int64             `json:"turn_seq"`
	RequestMessageID   string            `json:"request_message_id,omitempty"`
	ParentMessageID    string            `json:"parent_message_id,omitempty"`
	Question           string            `json:"question"`
	NormalizedQuestion string            `json:"normalized_question"`
	QuestionHash       string            `json:"question_hash"`
	Answer             string            `json:"answer"`
	AnswerSummary      string            `json:"answer_summary,omitempty"`
	SourceType         string            `json:"source_type"`
	Provider           string            `json:"provider"`
	Model              string            `json:"model"`
	EmbeddingModel     string            `json:"embedding_model,omitempty"`
	EmbeddingStatus    string            `json:"embedding_status"`
	SimilarityScore    float64           `json:"similarity_score,omitempty"`
	OriginRecordID     string            `json:"origin_record_id,omitempty"`
	RetrievedHits      []RetrievalHit    `json:"retrieved_hits,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	CreatedAt          int64             `json:"created_at"`
	UpdatedAt          int64             `json:"updated_at"`
	ExpiresAt          int64             `json:"expires_at,omitempty"`
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

type AnswerTrace struct {
	AnswerMode         string         `json:"answer_mode"`
	Provider           string         `json:"provider"`
	Model              string         `json:"model"`
	SelectedProvider   string         `json:"selected_provider,omitempty"`
	SelectedModel      string         `json:"selected_model,omitempty"`
	ActualProvider     string         `json:"actual_provider,omitempty"`
	ActualModel        string         `json:"actual_model,omitempty"`
	FallbackUsed       bool           `json:"fallback_used,omitempty"`
	FallbackReason     string         `json:"fallback_reason,omitempty"`
	UsedLocalRetrieval bool           `json:"used_local_retrieval"`
	CacheHit           bool           `json:"cache_hit"`
	FinalAnswerSource  string         `json:"final_answer_source"`
	GenerationProvider string         `json:"generation_provider,omitempty"`
	GenerationModel    string         `json:"generation_model,omitempty"`
	RetrievedHits      []RetrievalHit `json:"retrieved_hits,omitempty"`
}

type ResponseMeta struct {
	Trace  AnswerTrace `json:"trace"`
	Usage  UsageInfo   `json:"usage"`
	Budget BudgetInfo  `json:"budget"`
}

type QueryPolicy struct {
	Mode                  string  `json:"mode"`
	ShouldCallLLM         bool    `json:"should_call_llm"`
	ShouldQueryLocal      bool    `json:"should_query_local"`
	UseRetrievalAsContext bool    `json:"use_retrieval_as_context"`
	DirectReturnLocal     bool    `json:"direct_return_local"`
	Threshold             float64 `json:"threshold"`
	Scope                 string  `json:"scope,omitempty"`
	Reason                string  `json:"reason,omitempty"`
}

type RouteDecision struct {
	Policy                QueryPolicy    `json:"policy"`
	Mode                  string         `json:"mode"`
	ShouldCallLLM         bool           `json:"should_call_llm"`
	ShouldQueryLocal      bool           `json:"should_query_local"`
	UseRetrievalAsContext bool           `json:"use_retrieval_as_context"`
	DirectReturnLocal     bool           `json:"direct_return_local"`
	Threshold             float64        `json:"threshold"`
	RetrievalScore        float64        `json:"retrieval_score"`
	RetrievalHits         []RetrievalHit `json:"retrieval_hits,omitempty"`
	LocalAnswer           string         `json:"local_answer,omitempty"`
	Provider              string         `json:"provider,omitempty"`
	Model                 string         `json:"model,omitempty"`
	Reason                string         `json:"reason,omitempty"`
}

type ChatRequest struct {
	Model            string
	Messages         []openai.ChatCompletionMessage
	MaxTokens        int
	Temperature      float32
	TopP             float32
	PresencePenalty  float32
	FrequencyPenalty float32
	Stream           bool
	Timeout          time.Duration
	RequestID        string
	SessionID        string
	Metadata         map[string]string
}

func (r ChatRequest) ToOpenAIRequest(model string) openai.ChatCompletionRequest {
	if model == "" {
		model = r.Model
	}
	return openai.ChatCompletionRequest{
		Model:            model,
		Messages:         append([]openai.ChatCompletionMessage(nil), r.Messages...),
		MaxTokens:        r.MaxTokens,
		Temperature:      r.Temperature,
		TopP:             r.TopP,
		PresencePenalty:  r.PresencePenalty,
		FrequencyPenalty: r.FrequencyPenalty,
		Stream:           r.Stream,
	}
}

type ChatResult struct {
	ID         string
	Provider   string
	Model      string
	Message    openai.ChatCompletionMessage
	Usage      UsageInfo
	RawUsage   *openai.Usage
	Trace      AnswerTrace
	ResponseID string
}

type ChatStream interface {
	Recv() (*openai.ChatCompletionStreamResponse, error)
	Close() error
}

type EmbeddingProvider interface {
	Name() string
	Capabilities() CapabilitySet
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

var invalidChars = regexp.MustCompile(`[^\pL\pN]+`)

func NormalizeQuestion(question string) string {
	trimmed := strings.ToLower(strings.TrimSpace(question))
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.In(r, unicode.Han) {
			builder.WriteRune(r)
		}
	}
	normalized := builder.String()
	if normalized != "" {
		return normalized
	}
	return invalidChars.ReplaceAllString(trimmed, "")
}

func QuestionHash(question string) string {
	normalized := NormalizeQuestion(question)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func DetectProviderFromModel(model string) string {
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

func PreviewText(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "..."
}

func ClampBudget(maxContextTokens, promptTokens, reservedCompletionTokens int) int {
	remaining := maxContextTokens - promptTokens - reservedCompletionTokens
	if remaining < 0 {
		return 0
	}
	return remaining
}
