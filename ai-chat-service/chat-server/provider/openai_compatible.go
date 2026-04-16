package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-chat-service/chat-server/llm"

	openai "github.com/sashabaranov/go-openai"
)

type OpenAICompatibleProvider struct {
	profile ProviderProfile
	client  *openai.Client
}

type openAIChatStream struct {
	stream *openai.ChatCompletionStream
	cancel context.CancelFunc
}

func NewOpenAICompatibleProvider(profile ProviderProfile) *OpenAICompatibleProvider {
	normalized := profile.Normalize()
	config := openai.DefaultConfig(normalized.APIKey)
	config.BaseURL = strings.TrimSuffix(normalized.BaseURL, "/")
	return &OpenAICompatibleProvider{
		profile: normalized,
		client:  openai.NewClientWithConfig(config),
	}
}

func (p *OpenAICompatibleProvider) Name() string {
	if p == nil {
		return ""
	}
	return p.profile.Name
}

func (p *OpenAICompatibleProvider) Profile() ProviderProfile {
	if p == nil {
		return ProviderProfile{}
	}
	return p.profile
}

func (p *OpenAICompatibleProvider) Capabilities() llm.CapabilitySet {
	if p == nil {
		return llm.CapabilitySet{}
	}
	return llm.CapabilitySet{
		SupportsChat:           true,
		SupportsStream:         p.profile.SupportsStream,
		SupportsNonStream:      p.profile.SupportsNonStream || !p.profile.SupportsStream,
		SupportsEmbeddings:     p.profile.SupportsEmbeddings,
		SupportsToolCalls:      true,
		SupportsSystemPrompt:   true,
		SupportsUsageReporting: true,
	}
}

func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResult, error) {
	if p == nil || p.client == nil {
		return nil, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Source: p.Name(), Model: req.Model, Message: "provider client is not initialized"}
	}
	request := req.ToOpenAIRequest(p.profile.EffectiveModel(req.Model))
	request.Stream = false
	providerCtx, cancel := p.withTimeout(ctx, req.Timeout)
	defer cancel()
	resp, err := p.client.CreateChatCompletion(providerCtx, request)
	if err != nil {
		return nil, p.wrapError(err, req.Model)
	}
	result := &llm.ChatResult{
		ID:       resp.ID,
		Provider: p.Name(),
		Model:    resp.Model,
		Message:  resp.Choices[0].Message,
		RawUsage: &resp.Usage,
		Usage: llm.UsageInfo{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			UsageSource:      llm.UsageSourceProvider,
		},
		Trace: llm.AnswerTrace{
			AnswerMode:         llm.AnswerModeAPI,
			Provider:           p.Name(),
			Model:              resp.Model,
			ActualProvider:     p.Name(),
			ActualModel:        resp.Model,
			GenerationProvider: p.Name(),
			GenerationModel:    resp.Model,
			FinalAnswerSource:  llm.FinalAnswerSourceLLM,
		},
		ResponseID: resp.ID,
	}
	return result, nil
}

func (p *OpenAICompatibleProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	if p == nil || p.client == nil {
		return nil, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Source: p.Name(), Model: req.Model, Message: "provider client is not initialized"}
	}
	request := req.ToOpenAIRequest(p.profile.EffectiveModel(req.Model))
	request.Stream = true
	providerCtx, cancel := p.withTimeout(ctx, req.Timeout)
	stream, err := p.client.CreateChatCompletionStream(providerCtx, request)
	if err != nil {
		cancel()
		return nil, p.wrapError(err, req.Model)
	}
	return &openAIChatStream{stream: stream, cancel: cancel}, nil
}

func (p *OpenAICompatibleProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p == nil || p.client == nil {
		return nil, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Source: p.Name(), Message: "provider client is not initialized"}
	}
	if !p.profile.SupportsEmbeddings {
		return nil, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Source: p.Name(), Message: "provider does not support embeddings"}
	}
	if len(texts) == 0 {
		return nil, nil
	}
	embeddingModelName := strings.TrimSpace(p.profile.EmbeddingModel)
	if embeddingModelName == "" {
		embeddingModelName = strings.TrimSpace(p.profile.DefaultEmbeddingModel)
	}
	if embeddingModelName == "" {
		embeddingModelName = openai.AdaEmbeddingV2.String()
	}
	var embeddingModel openai.EmbeddingModel
	if err := embeddingModel.UnmarshalText([]byte(embeddingModelName)); err != nil || embeddingModel == openai.Unknown {
		return nil, &llm.ProviderError{Class: llm.ErrorClassInvalid, Source: p.Name(), Model: embeddingModelName, Message: fmt.Sprintf("unsupported embedding model %q", embeddingModelName), Err: err}
	}
	providerCtx, cancel := p.withTimeout(ctx, p.profile.Timeout())
	defer cancel()
	resp, err := p.client.CreateEmbeddings(providerCtx, openai.EmbeddingRequest{
		Input: texts,
		Model: embeddingModel,
	})
	if err != nil {
		return nil, p.wrapError(err, embeddingModelName)
	}
	vectors := make([][]float32, 0, len(resp.Data))
	for _, item := range resp.Data {
		vectors = append(vectors, append([]float32(nil), item.Embedding...))
	}
	return vectors, nil
}

func (p *OpenAICompatibleProvider) withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = p.profile.Timeout()
	}
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func (p *OpenAICompatibleProvider) wrapError(err error, model string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	class := llm.ErrorClassUpstream
	switch {
	case errors.Is(err, context.Canceled):
		class = llm.ErrorClassCanceled
	case errors.Is(err, context.DeadlineExceeded):
		class = llm.ErrorClassTimeout
	case strings.Contains(strings.ToLower(message), "unauthorized") || strings.Contains(strings.ToLower(message), "401"):
		class = llm.ErrorClassAuth
	case strings.Contains(strings.ToLower(message), "rate limit") || strings.Contains(strings.ToLower(message), "429"):
		class = llm.ErrorClassRateLimit
	case strings.Contains(strings.ToLower(message), "invalid"):
		class = llm.ErrorClassInvalid
	}
	return &llm.ProviderError{Class: class, Source: p.Name(), Model: model, Message: message, Err: err}
}

func (s *openAIChatStream) Recv() (*openai.ChatCompletionStreamResponse, error) {
	if s == nil || s.stream == nil {
		return nil, fmt.Errorf("stream is nil")
	}
	resp, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *openAIChatStream) Close() error {
	if s == nil || s.stream == nil {
		if s != nil && s.cancel != nil {
			s.cancel()
		}
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.stream.Close()
	return nil
}
