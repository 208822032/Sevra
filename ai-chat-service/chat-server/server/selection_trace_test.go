package server

import (
	"ai-chat-service/chat-server/data"
	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/chat-server/qa"
	vector_data "ai-chat-service/chat-server/vector-data"
	serviceconfig "ai-chat-service/pkg/config"
	bklog "ai-chat-service/pkg/log"
	"ai-chat-service/proto"
	"context"
	"io"
	"testing"
)

type fakeChatRecordsData struct {
	record *data.ChatRecord
}

func (f *fakeChatRecordsData) Add(record *data.ChatRecord) error {
	return nil
}

func (f *fakeChatRecordsData) GetById(id int64) (*data.ChatRecord, error) {
	if f.record != nil {
		copyRecord := *f.record
		return &copyRecord, nil
	}
	return &data.ChatRecord{
		ID:      id,
		UserMsg: "cached question",
		AIMsg:   "cached answer",
	}, nil
}

type fakeVectorRecordsData struct {
	expectedProvider string
	expectedModel    string
	hitID            string
	hitScore         float32
	lastQuery        map[string][]string
}

func (f *fakeVectorRecordsData) UpsertData(ctx context.Context, list []*vector_data.ChatRecord) error {
	return nil
}

func (f *fakeVectorRecordsData) QueryData(ctx context.Context, text map[string][]string) (string, float32, error) {
	f.lastQuery = cloneQuery(text)
	if firstValue(text["provider"]) == f.expectedProvider && firstValue(text["model"]) == f.expectedModel {
		return f.hitID, f.hitScore, nil
	}
	return "", 0, nil
}

func cloneQuery(src map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(src))
	for key, values := range src {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func TestBuildAnswerTraceIncludesSelectedAndActualModels(t *testing.T) {
	app := &app{
		providerProfile:  provider.ProviderProfile{Name: "deepseek"},
		openaiConf:       &openaiConf{Model: "deepseek-chat"},
		selectedProvider: "deepseek",
		selectedModel:    "deepseek-reasoner",
		fallbackUsed:     true,
		fallbackReason:   "invalid_model_for_provider",
		qaLookupResult: qa.LookupResult{
			SemanticModeRequested:       qa.SemanticRecallModeANN,
			SemanticModeUsed:            qa.SemanticRecallModeBruteForce,
			SemanticFallbackUsed:        true,
			SemanticFallbackReason:      "ann_query_error",
			SemanticLatencyMS:           12.5,
			SemanticANNLatencyMS:        4.2,
			SemanticBruteForceLatencyMS: 8.3,
			SemanticSource:              qa.SemanticRecallSourceANNFallback,
		},
	}
	decision := llm.RouteDecision{
		Mode: llm.AnswerModeCacheHit,
		RetrievalHits: []llm.RetrievalHit{{
			RecordID: "42",
			Provider: "deepseek",
			Model:    "deepseek-chat",
		}},
	}

	trace := app.buildAnswerTrace(decision, llm.FinalAnswerSourceLocal, true, true, "cached local answer")
	if trace.SelectedProvider != "deepseek" || trace.SelectedModel != "deepseek-reasoner" {
		t.Fatalf("unexpected selected trace fields: %+v", trace)
	}
	if trace.Provider != "deepseek" || trace.Model != "deepseek-chat" {
		t.Fatalf("unexpected actual trace fields: %+v", trace)
	}
	if !trace.FallbackUsed || trace.FallbackReason != "invalid_model_for_provider" {
		t.Fatalf("unexpected fallback trace fields: %+v", trace)
	}
	if trace.FinalAnswerSource != llm.FinalAnswerSourceLocal {
		t.Fatalf("unexpected final answer source: %+v", trace)
	}
	if trace.AnswerSourceKind != llm.AnswerSourceKindCache {
		t.Fatalf("unexpected answer source kind: %+v", trace)
	}
	if trace.SemanticModeRequested != qa.SemanticRecallModeANN || trace.SemanticModeUsed != qa.SemanticRecallModeBruteForce {
		t.Fatalf("unexpected semantic trace fields: %+v", trace)
	}
	if !trace.SemanticFallbackUsed || trace.SemanticFallbackReason != "ann_query_error" {
		t.Fatalf("unexpected semantic fallback trace fields: %+v", trace)
	}
	if trace.SemanticSource != qa.SemanticRecallSourceANNFallback {
		t.Fatalf("unexpected semantic source trace field: %+v", trace)
	}
	if trace.SemanticLatencyMS != 12.5 || trace.SemanticANNLatencyMS != 4.2 || trace.SemanticBruteForceLatencyMS != 8.3 {
		t.Fatalf("unexpected semantic latency trace fields: %+v", trace)
	}
	if !trace.CacheHit || !trace.UsedLocalRetrieval {
		t.Fatalf("unexpected cache or retrieval flags: %+v", trace)
	}
	if trace.GenerationProvider != "deepseek" || trace.GenerationModel != "deepseek-chat" {
		t.Fatalf("unexpected generation fields: %+v", trace)
	}
	if len(trace.RetrievedHits) != 1 {
		t.Fatalf("unexpected retrieved hits: %+v", trace)
	}
}

func TestResolveRouteDecisionScopesExactHitsByProviderAndModel(t *testing.T) {
	logger := bklog.NewLogger()
	logger.SetOutput(io.Discard)

	t.Run("selected provider hits only its own scope", func(t *testing.T) {
		vectorStore := &fakeVectorRecordsData{
			expectedProvider: "deepseek",
			expectedModel:    "deepseek-chat",
			hitID:            "88",
			hitScore:         0.995,
		}
		app := &app{
			config:          &serviceconfig.Config{},
			openaiConf:      &openaiConf{Model: "deepseek-chat", MaxTokens: 4096, MinResponseTokens: 256},
			log:             logger,
			data:            &fakeChatRecordsData{record: &data.ChatRecord{ID: 88, UserMsg: "cached question", AIMsg: "cached answer"}},
			vectorData:      vectorStore,
			providerProfile: provider.ProviderProfile{Name: "deepseek"},
			keywordsFn: func(context.Context, *proto.ChatCompletionRequest) []string {
				return []string{"explicit model scope"}
			},
		}

		decision, err := app.resolveRouteDecision(context.Background(), &proto.ChatCompletionRequest{Message: "explicit model scope"})
		if err != nil {
			t.Fatalf("resolve route decision: %v", err)
		}
		if decision.Mode != llm.AnswerModeCacheHit {
			t.Fatalf("expected cache hit, got %+v", decision)
		}
		if firstValue(vectorStore.lastQuery["provider"]) != "deepseek" {
			t.Fatalf("expected provider scope in exact hit query, got %+v", vectorStore.lastQuery)
		}
		if firstValue(vectorStore.lastQuery["model"]) != "deepseek-chat" {
			t.Fatalf("expected model scope in exact hit query, got %+v", vectorStore.lastQuery)
		}
	})

	t.Run("different provider does not cross hit", func(t *testing.T) {
		vectorStore := &fakeVectorRecordsData{
			expectedProvider: "deepseek",
			expectedModel:    "deepseek-chat",
			hitID:            "88",
			hitScore:         0.995,
		}
		app := &app{
			config:          &serviceconfig.Config{},
			openaiConf:      &openaiConf{Model: "gpt-3.5-turbo", MaxTokens: 4096, MinResponseTokens: 256},
			log:             logger,
			data:            &fakeChatRecordsData{record: &data.ChatRecord{ID: 88, UserMsg: "cached question", AIMsg: "cached answer"}},
			vectorData:      vectorStore,
			providerProfile: provider.ProviderProfile{Name: "openai"},
			keywordsFn: func(context.Context, *proto.ChatCompletionRequest) []string {
				return []string{"explicit model scope"}
			},
		}

		decision, err := app.resolveRouteDecision(context.Background(), &proto.ChatCompletionRequest{Message: "explicit model scope"})
		if err != nil {
			t.Fatalf("resolve route decision: %v", err)
		}
		if decision.Mode == llm.AnswerModeCacheHit {
			t.Fatalf("did not expect cross-provider exact hit: %+v", decision)
		}
		if firstValue(vectorStore.lastQuery["provider"]) != "openai" {
			t.Fatalf("expected provider scope to follow the actual provider, got %+v", vectorStore.lastQuery)
		}
		if firstValue(vectorStore.lastQuery["model"]) != "gpt-3.5-turbo" {
			t.Fatalf("expected model scope to follow the actual model, got %+v", vectorStore.lastQuery)
		}
	})
}
