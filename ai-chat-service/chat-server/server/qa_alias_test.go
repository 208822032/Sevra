package server

import (
	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/chat-server/qa"
	"ai-chat-service/proto"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type aliasKVLiteBackend struct {
	mu       sync.RWMutex
	values   map[string]string
	expires  map[string]time.Time
	getErrFn func(string) error
	name     string
}

func newAliasKVLiteBackend() *aliasKVLiteBackend {
	return &aliasKVLiteBackend{
		values:  make(map[string]string),
		expires: make(map[string]time.Time),
		name:    "alias-kvlite",
	}
}

func (b *aliasKVLiteBackend) Name() string { return b.name }

func (b *aliasKVLiteBackend) Close() error { return nil }

func (b *aliasKVLiteBackend) RGet(ctx context.Context, key string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.getErrFn != nil {
		if err := b.getErrFn(key); err != nil {
			return "", err
		}
	}
	if expiry, ok := b.expires[key]; ok && !expiry.IsZero() && time.Now().After(expiry) {
		delete(b.values, key)
		delete(b.expires, key)
		return "", nil
	}
	value, ok := b.values[key]
	if !ok {
		return "", nil
	}
	return value, nil
}

func (b *aliasKVLiteBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[key] = value
	if ttl > 0 {
		b.expires[key] = time.Now().Add(ttl)
	} else {
		delete(b.expires, key)
	}
	return nil
}

func (b *aliasKVLiteBackend) Del(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.values, key)
	delete(b.expires, key)
	return nil
}

func seedAliasCanonicalStore(t *testing.T, getErrFn func(string) error) (qa.Store, *aliasKVLiteBackend, string, string, string) {
	t.Helper()
	backend := newAliasKVLiteBackend()
	backend.getErrFn = getErrFn
	store := qa.NewKVLiteStoreWithBackend(backend, "test:qa", time.Minute)
	scope := qa.BuildScope("deepseek", "deepseek-chat", "")
	canonicalQuestion := "How do I keep the canonical answer?"
	canonicalID := qa.BuildQuestionID(scope, qa.ExactQuestionHash(canonicalQuestion))
	record := &llm.QARecord{
		QuestionID:          canonicalID,
		RecordID:            canonicalID,
		CanonicalQuestionID: canonicalID,
		Question:            canonicalQuestion,
		RawQuestion:         canonicalQuestion,
		CanonicalQuestion:   canonicalQuestion,
		QuestionHash:        qa.QuestionHash(canonicalQuestion),
		RawQuestionHash:     qa.ExactQuestionHash(canonicalQuestion),
		NormalizedQuestion:  qa.NormalizeQuestion(canonicalQuestion),
		Answer:              "canonical answer",
		Provider:            "deepseek",
		Model:               "deepseek-chat",
		Scope:               scope,
		SourceType:          llm.SourceTypeAPI,
		Metadata:            map[string]string{"answer_source": llm.FinalAnswerSourceLLM},
	}
	if err := store.Upsert(context.Background(), record, time.Minute); err != nil {
		t.Fatalf("upsert canonical record: %v", err)
	}
	return store, backend, scope, canonicalID, canonicalQuestion
}

func newAliasTestService(t *testing.T, retriever qa.Retriever) *chatService {
	t.Helper()
	svc := newQAPreflightTestService(t, retriever, &fakeChatRecordsData{}, nil)
	svc.keywordsFn = func(context.Context, *proto.ChatCompletionRequest) []string { return nil }
	return svc
}

func newAliasProviderRegistry(answer string) (*provider.Registry, *countingQAProvider) {
	providerImpl := &countingQAProvider{
		profile: provider.ProviderProfile{
			Name:              "deepseek",
			DefaultModel:      "deepseek-chat",
			ChatModel:         "deepseek-chat",
			SupportsStream:    true,
			SupportsNonStream: true,
			Enabled:           true,
			Priority:          100,
		},
		chatAnswer:   answer,
		streamAnswer: answer,
	}
	registry := provider.NewRegistry(nil)
	registry.Register(providerImpl)
	return registry, providerImpl
}

func TestQAAliasUnaryHitShortCircuitsToCanonicalAnswer(t *testing.T) {
	store, _, scope, canonicalID, _ := seedAliasCanonicalStore(t, nil)
	aliasQuestion := "How do I keep the answer canonical?"
	if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scope, time.Minute); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	svc := newAliasTestService(t, store)
	registry, providerImpl := newAliasProviderRegistry("unexpected provider answer")
	svc.providerRegistry = registry

	request := &proto.ChatCompletionRequest{Id: "req-alias-1", Message: aliasQuestion}
	app := svc.newApp(request, nil, false)
	hit, result, err := app.tryQAPreflight(context.Background(), request)
	if err != nil {
		t.Fatalf("alias preflight lookup: %v", err)
	}
	if !hit || result.HitType != qa.HitTypeAlias {
		t.Fatalf("expected alias hit, got %+v", result)
	}
	if result.QuestionID != canonicalID || result.CanonicalQuestionID != canonicalID {
		t.Fatalf("expected canonical question id, got %+v", result)
	}
	if result.Answer != "canonical answer" {
		t.Fatalf("unexpected alias answer: %+v", result)
	}
	response, meta, err := app.buildQAPreflightUnaryResponse(context.Background(), request, result)
	if err != nil {
		t.Fatalf("build alias unary response: %v", err)
	}
	if meta.Trace.QAHitType != qa.HitTypeAlias {
		t.Fatalf("expected alias trace hit type, got %+v", meta.Trace)
	}
	if meta.Trace.FinalAnswerSource != llm.FinalAnswerSourceQAStore {
		t.Fatalf("expected qa store trace source, got %+v", meta.Trace)
	}
	if response == nil || len(response.Choices) != 1 || response.Choices[0].Message.Content != "canonical answer" {
		t.Fatalf("unexpected unary alias response: %+v", response)
	}

	flowResponse, err := svc.ChatCompletion(context.Background(), request)
	if err != nil {
		t.Fatalf("chat completion alias hit: %v", err)
	}
	if flowResponse == nil || flowResponse.Choices[0].Message.Content != "canonical answer" {
		t.Fatalf("unexpected alias flow response: %+v", flowResponse)
	}
	if providerImpl.chatCalls != 0 {
		t.Fatalf("expected provider to be bypassed, got %d calls", providerImpl.chatCalls)
	}
}

func TestQAAliasStreamHitShortCircuitsToCanonicalAnswer(t *testing.T) {
	store, _, scope, canonicalID, _ := seedAliasCanonicalStore(t, nil)
	aliasQuestion := "How do I keep the stream canonical?"
	if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scope, time.Minute); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	svc := newAliasTestService(t, store)
	registry, providerImpl := newAliasProviderRegistry("unexpected stream answer")
	svc.providerRegistry = registry

	stream := &fakeChatCompletionStreamServer{ctx: context.Background()}
	if err := svc.ChatCompletionStream(&proto.ChatCompletionRequest{Id: "req-alias-stream", Message: aliasQuestion}, stream); err != nil {
		t.Fatalf("chat completion stream alias hit: %v", err)
	}
	if providerImpl.streamCalls != 0 {
		t.Fatalf("expected stream provider to be bypassed, got %d calls", providerImpl.streamCalls)
	}
	if len(stream.sent) < 3 {
		t.Fatalf("expected alias stream chunks, got %+v", stream.sent)
	}
	var content strings.Builder
	for i, chunk := range stream.sent {
		if chunk == nil || len(chunk.Choices) == 0 {
			t.Fatalf("unexpected stream chunk at %d: %+v", i, chunk)
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
	}
	if content.String() != "canonical answer" {
		t.Fatalf("unexpected alias stream content: %q", content.String())
	}
}

func TestQAAliasLookupErrorFallsBackToProvider(t *testing.T) {
	store, _, scope, canonicalID, _ := seedAliasCanonicalStore(t, func(key string) error {
		if strings.Contains(key, "qalias:") {
			return errors.New("alias backend failure")
		}
		return nil
	})
	aliasQuestion := "How do alias failures behave?"
	if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scope, time.Minute); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	svc := newAliasTestService(t, store)
	registry, providerImpl := newAliasProviderRegistry("fallback answer")
	svc.providerRegistry = registry
	response, err := svc.ChatCompletion(context.Background(), &proto.ChatCompletionRequest{Id: "req-alias-error", Message: aliasQuestion})
	if err != nil {
		t.Fatalf("alias lookup fallback: %v", err)
	}
	if response == nil || response.Choices[0].Message.Content != "fallback answer" {
		t.Fatalf("unexpected fallback response: %+v", response)
	}
	if providerImpl.chatCalls != 1 {
		t.Fatalf("expected provider to be called once after alias error, got %d", providerImpl.chatCalls)
	}
}

func TestQAPreflightExactAndNormalizedStillWinBeforeAlias(t *testing.T) {
	store, _, scope, canonicalID, canonicalQuestion := seedAliasCanonicalStore(t, func(key string) error {
		if strings.Contains(key, "qalias:") {
			return errors.New("alias backend failure")
		}
		return nil
	})
	aliasQuestion := "How do I keep the answer canonical?"
	if err := store.AddAlias(context.Background(), canonicalID, aliasQuestion, scope, time.Minute); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	svc := newAliasTestService(t, store)
	registry, providerImpl := newAliasProviderRegistry("should not be used")
	svc.providerRegistry = registry

	exactResponse, err := svc.ChatCompletion(context.Background(), &proto.ChatCompletionRequest{Id: "req-exact", Message: canonicalQuestion})
	if err != nil {
		t.Fatalf("exact hit chat completion: %v", err)
	}
	if exactResponse == nil || exactResponse.Choices[0].Message.Content != "canonical answer" {
		t.Fatalf("unexpected exact hit response: %+v", exactResponse)
	}
	normalizedResponse, err := svc.ChatCompletion(context.Background(), &proto.ChatCompletionRequest{Id: "req-normalized", Message: canonicalQuestion + "!!!"})
	if err != nil {
		t.Fatalf("normalized hit chat completion: %v", err)
	}
	if normalizedResponse == nil || normalizedResponse.Choices[0].Message.Content != "canonical answer" {
		t.Fatalf("unexpected normalized hit response: %+v", normalizedResponse)
	}
	if providerImpl.chatCalls != 0 {
		t.Fatalf("expected provider to remain bypassed, got %d calls", providerImpl.chatCalls)
	}
}
