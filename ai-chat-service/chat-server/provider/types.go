package provider

import (
	"context"
	"sort"
	"strings"
	"time"

	"ai-chat-service/chat-server/llm"
)

type ProviderProfile struct {
	Name                            string            `mapstructure:"name" json:"name"`
	DisplayName                     string            `mapstructure:"display_name" json:"display_name,omitempty"`
	Kind                            string            `mapstructure:"kind" json:"kind"`
	BaseURL                         string            `mapstructure:"base_url" json:"base_url"`
	APIKey                          string            `mapstructure:"api_key" json:"api_key"`
	DefaultModel                    string            `mapstructure:"default_model" json:"default_model"`
	ChatModel                       string            `mapstructure:"chat_model" json:"chat_model,omitempty"`
	DefaultEmbeddingModel           string            `mapstructure:"default_embedding_model" json:"default_embedding_model,omitempty"`
	EmbeddingModel                  string            `mapstructure:"embedding_model" json:"embedding_model,omitempty"`
	Models                          []string          `mapstructure:"models" json:"models,omitempty"`
	SupportsStream                  bool              `mapstructure:"supports_stream" json:"supports_stream"`
	SupportsNonStream               bool              `mapstructure:"supports_non_stream" json:"supports_non_stream"`
	SupportsEmbeddings              bool              `mapstructure:"supports_embeddings" json:"supports_embeddings"`
	ContextWindow                   int               `mapstructure:"context_window" json:"context_window"`
	DefaultReservedCompletionTokens int               `mapstructure:"default_reserved_completion_tokens" json:"default_reserved_completion_tokens"`
	Enabled                         bool              `mapstructure:"enabled" json:"enabled"`
	Priority                        int               `mapstructure:"priority" json:"priority"`
	TimeoutMS                       int               `mapstructure:"timeout_ms" json:"timeout_ms"`
	MaxRetries                      int               `mapstructure:"max_retries" json:"max_retries"`
	ModelAliases                    map[string]string `mapstructure:"model_aliases" json:"model_aliases,omitempty"`
}

func (p ProviderProfile) Normalize() ProviderProfile {
	if p.Name == "" {
		p.Name = llm.DetectProviderFromModel(p.DefaultModel)
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		p.DisplayName = p.Name
	}
	if p.Kind == "" {
		p.Kind = "openai-compatible"
	}
	if strings.TrimSpace(p.ChatModel) == "" {
		p.ChatModel = p.DefaultModel
	}
	if p.DefaultModel == "" {
		p.DefaultModel = p.ChatModel
	}
	if p.ChatModel == "" {
		p.ChatModel = p.DefaultModel
	}
	if p.DefaultModel == "" {
		p.DefaultModel = "gpt-3.5-turbo"
	}
	if p.ChatModel == "" {
		p.ChatModel = p.DefaultModel
	}
	if strings.TrimSpace(p.EmbeddingModel) == "" {
		p.EmbeddingModel = p.DefaultEmbeddingModel
	}
	if p.DefaultEmbeddingModel == "" {
		p.DefaultEmbeddingModel = p.EmbeddingModel
	}
	if p.DefaultEmbeddingModel == "" {
		p.DefaultEmbeddingModel = ""
	}
	if p.DefaultReservedCompletionTokens <= 0 {
		p.DefaultReservedCompletionTokens = 256
	}
	if p.ContextWindow <= 0 {
		p.ContextWindow = 4096
	}
	if p.TimeoutMS <= 0 {
		p.TimeoutMS = 60000
	}
	if p.Priority == 0 {
		p.Priority = 100
	}
	if p.ModelAliases == nil {
		p.ModelAliases = map[string]string{}
	}
	return p
}

func (p ProviderProfile) Timeout() time.Duration {
	if p.TimeoutMS <= 0 {
		return 60 * time.Second
	}
	return time.Duration(p.TimeoutMS) * time.Millisecond
}

func (p ProviderProfile) IsEnabled() bool {
	return p.Enabled
}

func (p ProviderProfile) EffectiveModel(model string) string {
	if model == "" {
		return p.DefaultModel
	}
	trimmed := strings.TrimSpace(model)
	for alias, target := range p.ModelAliases {
		if strings.EqualFold(alias, trimmed) && strings.TrimSpace(target) != "" {
			return target
		}
	}
	return trimmed
}

type Provider interface {
	Name() string
	Profile() ProviderProfile
	Capabilities() llm.CapabilitySet
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResult, error)
	ChatStream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error)
}

type EmbeddingProvider = llm.EmbeddingProvider

type Registry struct {
	providers map[string]Provider
	profiles  map[string]ProviderProfile
	models    map[string]string
	ordered   []string
}

func NewRegistry(profiles []ProviderProfile) *Registry {
	r := &Registry{
		providers: map[string]Provider{},
		profiles:  map[string]ProviderProfile{},
		models:    map[string]string{},
		ordered:   []string{},
	}
	normalized := make([]ProviderProfile, 0, len(profiles))
	for _, profile := range profiles {
		if !profile.Enabled {
			continue
		}
		normalized = append(normalized, profile.Normalize())
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Priority == normalized[j].Priority {
			return normalized[i].Name < normalized[j].Name
		}
		return normalized[i].Priority > normalized[j].Priority
	})
	for _, profile := range normalized {
		provider := NewOpenAICompatibleProvider(profile)
		r.Register(provider)
	}
	return r
}

func (r *Registry) Register(provider Provider) {
	if r == nil || provider == nil {
		return
	}
	profile := provider.Profile().Normalize()
	r.providers[profile.Name] = provider
	r.profiles[profile.Name] = profile
	if _, exists := r.models[strings.ToLower(strings.TrimSpace(profile.DefaultModel))]; !exists {
		r.models[strings.ToLower(strings.TrimSpace(profile.DefaultModel))] = profile.Name
	}
	for _, model := range profile.AvailableModels() {
		key := strings.ToLower(strings.TrimSpace(model))
		if key == "" {
			continue
		}
		if _, exists := r.models[key]; !exists {
			r.models[key] = profile.Name
		}
	}
	r.ordered = append(r.ordered, profile.Name)
}

func (r *Registry) Resolve(model string) (Provider, ProviderProfile, error) {
	if r == nil || len(r.providers) == 0 {
		return nil, ProviderProfile{}, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Message: "provider registry is empty"}
	}
	if model != "" {
		if provider, profile, ok := r.ProfileByModel(model); ok {
			return provider, profile, nil
		}
		for name, profile := range r.profiles {
			if strings.EqualFold(profile.Name, model) || strings.EqualFold(profile.DefaultModel, model) {
				if provider, ok := r.providers[name]; ok {
					return provider, profile, nil
				}
			}
		}
	}
	return r.Default()
}

func (r *Registry) Default() (Provider, ProviderProfile, error) {
	if r == nil || len(r.ordered) == 0 {
		return nil, ProviderProfile{}, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Message: "no enabled provider profile"}
	}
	name := r.ordered[0]
	provider, ok := r.providers[name]
	if !ok {
		return nil, ProviderProfile{}, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Message: "default provider not found"}
	}
	return provider, r.profiles[name], nil
}

func (r *Registry) ProfileNames() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.ordered...)
}

func (r *Registry) Profiles() []ProviderProfile {
	if r == nil {
		return nil
	}
	profiles := make([]ProviderProfile, 0, len(r.ordered))
	for _, name := range r.ordered {
		profiles = append(profiles, r.profiles[name])
	}
	return profiles
}
