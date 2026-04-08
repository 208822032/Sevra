package config

import (
	"log"
	"strings"

	"github.com/spf13/viper"
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

type Config struct {
	Http struct {
		IP   string
		Port int
	}
	Log struct {
		Level   string
		LogPath string `mapstructure:"logPath"`
	} `mapstructure:"log"`
	Chat struct {
		ApiKey            string  `mapstructure:"api_key"`
		BaseUrl           string  `mapstructure:"base_url"`
		Model             string  `mapstructure:"model"`
		MaxTokens         int     `mapstructure:"max_tokens"`
		Temperature       float32 `mapstructure:"temperature"`
		TopP              float32 `mapstructure:"top_p"`
		PresencePenalty   float32 `mapstructure:"presence_penalty"`
		FrequencyPenalty  float32 `mapstructure:"frequency_penalty"`
		BotDesc           string  `mapstructure:"bot_desc"`
		MinResponseTokens int     `mapstructure:"min_response_tokens"`
		ContextTTL        int     `mapstructure:"context_ttl"`
		ContextLen        int     `mapstructure:"context_len"`
		RouteMode         string  `mapstructure:"route_mode"`
	}
	Providers []ProviderProfile `mapstructure:"providers"`
	DependOn  struct {
		AiChatService struct {
			Address     string
			AccessToken string
		} `mapstructure:"ai-chat-service"`
	}
}

var conf *Config

func InitConfig(filePath string, typ ...string) {
	v := viper.New()
	v.SetConfigFile(filePath)
	if len(typ) > 0 {
		v.SetConfigType(typ[0])
	}
	err := v.ReadInConfig()
	if err != nil {
		log.Fatal(err)
	}
	conf = &Config{}
	err = v.Unmarshal(conf)
	if err != nil {
		log.Fatal(err)
	}

}

func GetConfig() *Config {
	return conf
}

func (c *Config) ProviderProfiles() []ProviderProfile {
	if c == nil {
		return nil
	}
	if len(c.Providers) > 0 {
		profiles := make([]ProviderProfile, 0, len(c.Providers))
		for _, profile := range c.Providers {
			profiles = append(profiles, profile.normalize())
		}
		return profiles
	}
	defaultProfile := ProviderProfile{
		Name:                            providerNameFromModel(c.Chat.Model),
		DisplayName:                     providerNameFromModel(c.Chat.Model),
		Kind:                            "openai-compatible",
		BaseURL:                         c.Chat.BaseUrl,
		APIKey:                          firstNonEmpty(c.Chat.ApiKey),
		DefaultModel:                    c.Chat.Model,
		ChatModel:                       c.Chat.Model,
		Models:                          []string{c.Chat.Model},
		SupportsStream:                  true,
		SupportsNonStream:               true,
		SupportsEmbeddings:              false,
		ContextWindow:                   c.Chat.MaxTokens,
		DefaultReservedCompletionTokens: c.Chat.MinResponseTokens,
		Enabled:                         true,
		Priority:                        100,
		TimeoutMS:                       60000,
	}
	return []ProviderProfile{defaultProfile.normalize()}
}

func (p ProviderProfile) normalize() ProviderProfile {
	if p.Name == "" {
		p.Name = providerNameFromModel(p.DefaultModel)
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

func providerNameFromModel(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "qwen"):
		return "qwen"
	case strings.Contains(lower, "deepseek"):
		return "deepseek"
	default:
		return "openai"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
