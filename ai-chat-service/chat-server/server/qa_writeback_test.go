package server

import (
	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/chat-server/qa"
	"ai-chat-service/proto"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
)

type countingQAProvider struct {
	profile      provider.ProviderProfile
	chatAnswer   string
	streamAnswer string
	chatCalls    int
	streamCalls  int
	mu           sync.Mutex
}

func (p *countingQAProvider) Name() string { return p.profile.Name }

func (p *countingQAProvider) Profile() provider.ProviderProfile { return p.profile }

func (p *countingQAProvider) Capabilities() llm.CapabilitySet {
	return llm.CapabilitySet{
		SupportsChat:           true,
		SupportsStream:         true,
		SupportsNonStream:      true,
		SupportsEmbeddings:     false,
		SupportsToolCalls:      true,
		SupportsSystemPrompt:   true,
		SupportsUsageReporting: true,
	}
}

func (p *countingQAProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResult, error) {
	p.mu.Lock()
	p.chatCalls++
	p.mu.Unlock()
	answer := p.chatAnswer
	if answer == "" {
		answer = "qa chat answer"
	}
	model := req.Model
	if model == "" {
		model = p.profile.DefaultChatModel()
	}
	return &llm.ChatResult{
		ID:       "qa-chat-1",
		Provider: p.Name(),
		Model:    model,
		Message: openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: answer,
		},
		Usage: llm.UsageInfo{
			PromptTokens:     8,
			CompletionTokens: 8,
			TotalTokens:      16,
			UsageSource:      llm.UsageSourceProvider,
		},
	}, nil
}

func (p *countingQAProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	p.mu.Lock()
	p.streamCalls++
	p.mu.Unlock()
	answer := p.streamAnswer
	if answer == "" {
		answer = "qa stream answer"
	}
	model := req.Model
	if model == "" {
		model = p.profile.DefaultChatModel()
	}
	chunks := []*openai.ChatCompletionStreamResponse{{
		ID:    "qa-stream-1",
		Model: model,
		Choices: []openai.ChatCompletionStreamChoice{{
			Index: 0,
			Delta: openai.ChatCompletionStreamChoiceDelta{Role: openai.ChatMessageRoleAssistant, Content: answer},
		}},
	}}
	return &fakeQAProviderStream{chunks: chunks}, nil
}

type fakeQAProviderStream struct {
	chunks []*openai.ChatCompletionStreamResponse
	index  int
}

func (f *fakeQAProviderStream) Recv() (*openai.ChatCompletionStreamResponse, error) {
	if f.index >= len(f.chunks) {
		return nil, io.EOF
	}
	chunk := f.chunks[f.index]
	f.index++
	return chunk, nil
}

func (f *fakeQAProviderStream) Close() error { return nil }

type failingQAStore struct {
	lookupErr error
	upsertErr error
}

func (f *failingQAStore) Lookup(ctx context.Context, req qa.LookupRequest) (qa.LookupResult, error) {
	if f.lookupErr != nil {
		return qa.LookupResult{}, f.lookupErr
	}
	return qa.LookupResult{Hit: false}, nil
}

func (f *failingQAStore) Upsert(ctx context.Context, record *llm.QARecord, ttl time.Duration) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	return nil
}

func (f *failingQAStore) Close() error { return nil }

func (f *failingQAStore) Name() string { return "failing-qa-store" }

func TestQAWritethroughUnaryExactHitAfterFirstGeneration(t *testing.T) {
	store := qa.NewMemoryRetriever()
	svc := newQAPreflightTestService(t, store, &fakeChatRecordsData{}, nil)
	svc.keywordsFn = func(context.Context, *proto.ChatCompletionRequest) []string { return nil }
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
		chatAnswer: "exact answer",
	}
	registry := provider.NewRegistry(nil)
	registry.Register(providerImpl)
	svc.providerRegistry = registry

	request := &proto.ChatCompletionRequest{Id: "req-1", Message: "How do I cache answers?"}
	response, err := svc.ChatCompletion(context.Background(), request)
	if err != nil {
		t.Fatalf("first chat completion: %v", err)
	}
	if response == nil || len(response.Choices) != 1 || response.Choices[0].Message.Content != "exact answer" {
		t.Fatalf("unexpected first response: %+v", response)
	}
	waitForQALookupHit(t, store, request.Message, "deepseek", "deepseek-chat", "")

	response, err = svc.ChatCompletion(context.Background(), request)
	if err != nil {
		t.Fatalf("second chat completion: %v", err)
	}
	if response == nil || response.Choices[0].Message.Content != "exact answer" {
		t.Fatalf("unexpected cached response: %+v", response)
	}
	if providerImpl.chatCalls != 1 {
		t.Fatalf("expected provider to be bypassed on second call, got %d calls", providerImpl.chatCalls)
	}
}

func TestQAWritethroughUnaryNormalizedHitAfterFirstGeneration(t *testing.T) {
	store := qa.NewMemoryRetriever()
	svc := newQAPreflightTestService(t, store, &fakeChatRecordsData{}, nil)
	svc.keywordsFn = func(context.Context, *proto.ChatCompletionRequest) []string { return nil }
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
		chatAnswer: "normalized answer",
	}
	registry := provider.NewRegistry(nil)
	registry.Register(providerImpl)
	svc.providerRegistry = registry

	request := &proto.ChatCompletionRequest{Id: "req-1", Message: "How do I cache answers?"}
	if _, err := svc.ChatCompletion(context.Background(), request); err != nil {
		t.Fatalf("first chat completion: %v", err)
	}
	waitForQALookupHit(t, store, request.Message, "deepseek", "deepseek-chat", "")

	normalizedRequest := &proto.ChatCompletionRequest{Id: "req-2", Message: "How do I cache answers!!!"}
	response, err := svc.ChatCompletion(context.Background(), normalizedRequest)
	if err != nil {
		t.Fatalf("normalized chat completion: %v", err)
	}
	if response == nil || response.Choices[0].Message.Content != "normalized answer" {
		t.Fatalf("unexpected normalized response: %+v", response)
	}
	if providerImpl.chatCalls != 1 {
		t.Fatalf("expected provider to be bypassed on normalized hit, got %d calls", providerImpl.chatCalls)
	}
}

func TestQAWritethroughStreamExactHitAfterFirstGeneration(t *testing.T) {
	store := qa.NewMemoryRetriever()
	svc := newQAPreflightTestService(t, store, &fakeChatRecordsData{}, nil)
	svc.keywordsFn = func(context.Context, *proto.ChatCompletionRequest) []string { return nil }
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
		streamAnswer: "stream exact answer",
	}
	registry := provider.NewRegistry(nil)
	registry.Register(providerImpl)
	svc.providerRegistry = registry

	request := &proto.ChatCompletionRequest{Id: "req-1", Message: "How do I stream cache answers?"}
	firstStream := &fakeChatCompletionStreamServer{ctx: context.Background()}
	if err := svc.ChatCompletionStream(request, firstStream); err != nil {
		t.Fatalf("first stream completion: %v", err)
	}
	waitForQALookupHit(t, store, request.Message, "deepseek", "deepseek-chat", "")

	secondStream := &fakeChatCompletionStreamServer{ctx: context.Background()}
	if err := svc.ChatCompletionStream(request, secondStream); err != nil {
		t.Fatalf("second stream completion: %v", err)
	}
	if providerImpl.streamCalls != 1 {
		t.Fatalf("expected stream provider to be bypassed on second call, got %d calls", providerImpl.streamCalls)
	}
	var streamed strings.Builder
	for _, chunk := range secondStream.sent {
		if len(chunk.Choices) > 0 {
			streamed.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if streamed.String() != "stream exact answer" {
		t.Fatalf("unexpected streamed cache response: %q", streamed.String())
	}
}

func TestQAWritethroughFailureDoesNotBlockResponse(t *testing.T) {
	store := &failingQAStore{upsertErr: errors.New("write-back failed")}
	svc := newQAPreflightTestService(t, store, &fakeChatRecordsData{}, nil)
	svc.keywordsFn = func(context.Context, *proto.ChatCompletionRequest) []string { return nil }
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
		chatAnswer: "write-back error answer",
	}
	registry := provider.NewRegistry(nil)
	registry.Register(providerImpl)
	svc.providerRegistry = registry

	request := &proto.ChatCompletionRequest{Id: "req-1", Message: "Writeback should not block"}
	response, err := svc.ChatCompletion(context.Background(), request)
	if err != nil {
		t.Fatalf("chat completion with write-back failure: %v", err)
	}
	if response == nil || response.Choices[0].Message.Content != "write-back error answer" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if providerImpl.chatCalls != 1 {
		t.Fatalf("expected provider to be called once, got %d", providerImpl.chatCalls)
	}
	response, err = svc.ChatCompletion(context.Background(), request)
	if err != nil {
		t.Fatalf("second chat completion after failed write-back: %v", err)
	}
	if providerImpl.chatCalls != 2 {
		t.Fatalf("expected provider to be called again after failed write-back, got %d", providerImpl.chatCalls)
	}
	if response == nil || response.Choices[0].Message.Content != "write-back error answer" {
		t.Fatalf("unexpected response after failed write-back: %+v", response)
	}
}

func waitForQALookupHit(t *testing.T, store qa.Retriever, question, providerName, modelName, promptVersion string) {
	t.Helper()
	req := qa.LookupRequest{
		Question:               question,
		ExactQuestionHash:      qa.ExactQuestionHash(question),
		NormalizedQuestionHash: qa.QuestionHash(question),
		Provider:               providerName,
		Model:                  modelName,
		PromptVersion:          promptVersion,
		Scope:                  qa.BuildScope(providerName, modelName, promptVersion),
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		hit, err := store.Lookup(context.Background(), req)
		if err != nil {
			t.Fatalf("wait for qa lookup hit: %v", err)
		}
		if hit.Hit {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for qa store hit for %q", question)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
