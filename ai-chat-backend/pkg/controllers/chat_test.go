package controllers

import (
	"ai-chat-backend/pkg/config"
	bklog "ai-chat-backend/pkg/log"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/metadata"
)

func TestConfigReturnsProviderCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cnf := &config.Config{}
	cnf.Chat.Model = "gpt-3.5-turbo"
	cnf.Chat.MaxTokens = 4096
	cnf.Chat.MinResponseTokens = 256
	cnf.Chat.ContextLen = 8
	cnf.Providers = []config.ProviderProfile{
		{
			Name:                            "openai",
			DisplayName:                     "OpenAI",
			Kind:                            "openai-compatible",
			BaseURL:                         "https://api.openai.com/v1",
			APIKey:                          "test-key",
			DefaultModel:                    "gpt-3.5-turbo",
			ChatModel:                       "gpt-3.5-turbo",
			Models:                          []string{"gpt-3.5-turbo", "gpt-4o-mini"},
			SupportsStream:                  true,
			SupportsNonStream:               true,
			SupportsEmbeddings:              false,
			ContextWindow:                   4096,
			DefaultReservedCompletionTokens: 256,
			Enabled:                         true,
			Priority:                        100,
			TimeoutMS:                       60000,
		},
		{
			Name:                            "deepseek",
			DisplayName:                     "DeepSeek",
			Kind:                            "openai-compatible",
			BaseURL:                         "https://api.deepseek.com/v1",
			APIKey:                          "test-key",
			DefaultModel:                    "deepseek-chat",
			ChatModel:                       "deepseek-chat",
			Models:                          []string{"deepseek-chat", "deepseek-reasoner"},
			SupportsStream:                  true,
			SupportsNonStream:               true,
			SupportsEmbeddings:              false,
			ContextWindow:                   8192,
			DefaultReservedCompletionTokens: 512,
			Enabled:                         true,
			Priority:                        200,
			TimeoutMS:                       45000,
		},
	}

	logger := bklog.NewLogger()
	logger.SetOutput(io.Discard)
	service, err := NewChatService(cnf, logger)
	if err != nil {
		t.Fatalf("new chat service: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	ctx.Request = req

	service.Config(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}

	var resp struct {
		Status  string                    `json:"status"`
		Message string                    `json:"message"`
		Data    config.ChatConfigResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "Success" {
		t.Fatalf("unexpected status: %s", resp.Status)
	}
	if resp.Data.SelectedProvider != "deepseek" {
		t.Fatalf("expected deepseek as selected provider, got %s", resp.Data.SelectedProvider)
	}
	if resp.Data.SelectedModel != "deepseek-chat" {
		t.Fatalf("expected deepseek-chat as selected model, got %s", resp.Data.SelectedModel)
	}
	if len(resp.Data.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(resp.Data.Providers))
	}
	if resp.Data.Providers[0].Provider != "deepseek" {
		t.Fatalf("expected deepseek to be first provider, got %s", resp.Data.Providers[0].Provider)
	}
	if len(resp.Data.Providers[0].Models) < 2 {
		t.Fatalf("expected deepseek models list, got %+v", resp.Data.Providers[0].Models)
	}
	if resp.Data.Providers[0].Models[1].Model != "deepseek-reasoner" {
		t.Fatalf("expected deepseek-reasoner in model list, got %+v", resp.Data.Providers[0].Models)
	}
}

func TestParseAndMergeResponseMetaPreserveCandidateTrace(t *testing.T) {
	traceJSON := `{"answer_mode":"cache_hit","provider":"deepseek","model":"deepseek-chat","answer_source_kind":"cache","qa_hit_type":"candidate","candidate_count":3,"candidate_top_score":0.91,"candidate_source":"char_bigram","final_answer_source":"qa_candidate","generation_provider":"deepseek","generation_model":"deepseek-chat","retrieved_hits":[{"record_id":"qa-1","score":0.91,"hit_type":"candidate","question_preview":"How do I flush the Redis cache quickly?","answer_preview":"candidate answer","provider":"deepseek","model":"deepseek-chat"}]}`
	meta := parseResponseMeta(metadata.Pairs("x-ai-trace", traceJSON))
	if meta == nil {
		t.Fatalf("expected response meta")
	}
	if meta.Trace.CandidateCount != 3 {
		t.Fatalf("unexpected candidate count: %+v", meta.Trace)
	}
	if meta.Trace.CandidateTopScore != 0.91 {
		t.Fatalf("unexpected candidate top score: %+v", meta.Trace)
	}
	if meta.Trace.CandidateSource != "char_bigram" {
		t.Fatalf("unexpected candidate source: %+v", meta.Trace)
	}
	if meta.Trace.FinalAnswerSource != "qa_candidate" {
		t.Fatalf("unexpected final answer source: %+v", meta.Trace)
	}
	if meta.Trace.AnswerSourceKind != "cache" {
		t.Fatalf("unexpected answer source kind: %+v", meta.Trace)
	}

	message := &ChatMessage{
		Trace: &AnswerTrace{
			AnswerMode:        "api",
			Provider:          "openai",
			Model:             "gpt-4o-mini",
			FinalAnswerSource: "llm",
			AnswerSourceKind:  "ai",
		},
	}
	mergeResponseMeta(message, meta)
	if message.Trace == nil {
		t.Fatalf("expected trace after merge")
	}
	if message.Trace.CandidateCount != 3 || message.Trace.CandidateTopScore != 0.91 {
		t.Fatalf("unexpected merged candidate trace: %+v", message.Trace)
	}
	if message.Trace.CandidateSource != "char_bigram" {
		t.Fatalf("unexpected merged candidate source: %+v", message.Trace)
	}
	if message.Trace.FinalAnswerSource != "qa_candidate" {
		t.Fatalf("unexpected merged final answer source: %+v", message.Trace)
	}
	if message.Trace.AnswerSourceKind != "cache" {
		t.Fatalf("unexpected merged answer source kind: %+v", message.Trace)
	}
	if len(message.Trace.RetrievedHits) != 1 || message.Trace.RetrievedHits[0].HitType != "candidate" {
		t.Fatalf("unexpected merged retrieved hits: %+v", message.Trace.RetrievedHits)
	}
}

func TestParseAndMergeResponseMetaPreserveSemanticTrace(t *testing.T) {
	traceJSON := `{"answer_mode":"cache_hit","provider":"deepseek","model":"deepseek-chat","answer_source_kind":"cache","qa_hit_type":"semantic","semantic_candidate_count":4,"semantic_top_k":3,"semantic_top_score":0.93,"semantic_mode_requested":"ann","semantic_mode_used":"brute_force","semantic_fallback_used":true,"semantic_fallback_reason":"ann_index_missing","semantic_latency_ms":12.5,"semantic_ann_latency_ms":4.2,"semantic_brute_force_latency_ms":8.3,"semantic_model":"text-embedding-ada-002","semantic_source":"bruteforce_cosine","final_answer_source":"qa_semantic","retrieved_hits":[{"record_id":"qa-7","score":0.93,"hit_type":"semantic","question_preview":"How do I reset the cache safely?","answer_preview":"semantic answer","provider":"deepseek","model":"deepseek-chat"}]}`
	meta := parseResponseMeta(metadata.Pairs("x-ai-trace", traceJSON))
	if meta == nil {
		t.Fatalf("expected response meta")
	}
	if meta.Trace.SemanticCandidateCount != 4 || meta.Trace.SemanticTopK != 3 {
		t.Fatalf("unexpected semantic counts: %+v", meta.Trace)
	}
	if meta.Trace.SemanticTopScore != 0.93 {
		t.Fatalf("unexpected semantic top score: %+v", meta.Trace)
	}
	if meta.Trace.SemanticModel != "text-embedding-ada-002" || meta.Trace.SemanticSource != "bruteforce_cosine" {
		t.Fatalf("unexpected semantic model/source: %+v", meta.Trace)
	}
	if meta.Trace.FinalAnswerSource != "qa_semantic" {
		t.Fatalf("unexpected semantic final answer source: %+v", meta.Trace)
	}
	if meta.Trace.AnswerSourceKind != "cache" {
		t.Fatalf("unexpected semantic answer source kind: %+v", meta.Trace)
	}
	if meta.Trace.SemanticModeRequested != "ann" || meta.Trace.SemanticModeUsed != "brute_force" {
		t.Fatalf("unexpected semantic mode fields: %+v", meta.Trace)
	}
	if !meta.Trace.SemanticFallbackUsed || meta.Trace.SemanticFallbackReason != "ann_index_missing" {
		t.Fatalf("unexpected semantic fallback fields: %+v", meta.Trace)
	}
	if meta.Trace.SemanticLatencyMS != 12.5 || meta.Trace.SemanticANNLatencyMS != 4.2 || meta.Trace.SemanticBruteForceLatencyMS != 8.3 {
		t.Fatalf("unexpected semantic latency fields: %+v", meta.Trace)
	}

	message := &ChatMessage{Trace: &AnswerTrace{AnswerMode: "api", Provider: "openai", Model: "gpt-4o-mini", FinalAnswerSource: "llm", AnswerSourceKind: "ai"}}
	mergeResponseMeta(message, meta)
	if message.Trace.SemanticCandidateCount != 4 || message.Trace.SemanticTopK != 3 {
		t.Fatalf("unexpected merged semantic counts: %+v", message.Trace)
	}
	if message.Trace.SemanticTopScore != 0.93 {
		t.Fatalf("unexpected merged semantic top score: %+v", message.Trace)
	}
	if message.Trace.SemanticModel != "text-embedding-ada-002" || message.Trace.SemanticSource != "bruteforce_cosine" {
		t.Fatalf("unexpected merged semantic model/source: %+v", message.Trace)
	}
	if message.Trace.FinalAnswerSource != "qa_semantic" {
		t.Fatalf("unexpected merged semantic final answer source: %+v", message.Trace)
	}
	if message.Trace.AnswerSourceKind != "cache" {
		t.Fatalf("unexpected merged semantic answer source kind: %+v", message.Trace)
	}
	if message.Trace.SemanticModeRequested != "ann" || message.Trace.SemanticModeUsed != "brute_force" {
		t.Fatalf("unexpected merged semantic mode fields: %+v", message.Trace)
	}
	if !message.Trace.SemanticFallbackUsed || message.Trace.SemanticFallbackReason != "ann_index_missing" {
		t.Fatalf("unexpected merged semantic fallback fields: %+v", message.Trace)
	}
	if message.Trace.SemanticLatencyMS != 12.5 || message.Trace.SemanticANNLatencyMS != 4.2 || message.Trace.SemanticBruteForceLatencyMS != 8.3 {
		t.Fatalf("unexpected merged semantic latency fields: %+v", message.Trace)
	}
}
