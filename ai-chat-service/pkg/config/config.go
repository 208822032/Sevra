package config

import (
	"fmt"
	"log"
	"strings"

	"ai-chat-service/chat-server/provider"

	"github.com/spf13/viper"
)

type Config struct {
	Server struct {
		IP          string
		Port        int
		AccessToken string
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
	}
	Providers []provider.ProviderProfile `mapstructure:"providers"`
	Retrieval struct {
		KVLite struct {
			Enabled       bool    `mapstructure:"enabled"`
			Address       string  `mapstructure:"address"`
			StartupPolicy string  `mapstructure:"startup_policy"`
			TopK          int     `mapstructure:"top_k"`
			Threshold     float64 `mapstructure:"threshold"`
			TimeoutMS     int     `mapstructure:"timeout_ms"`
			PoolSize      int     `mapstructure:"pool_size"`
			MaxIdle       int     `mapstructure:"max_idle"`
			Scope         struct {
				Tenant        string `mapstructure:"tenant"`
				KnowledgeBase string `mapstructure:"knowledge_base"`
				CorpusVersion string `mapstructure:"corpus_version"`
				Provider      string `mapstructure:"provider"`
				Model         string `mapstructure:"model"`
				PromptVersion string `mapstructure:"prompt_version"`
			} `mapstructure:"scope"`
		} `mapstructure:"kvlite"`
	} `mapstructure:"retrieval"`
	Context struct {
		Backend         string `mapstructure:"context_backend"`
		FallbackToRedis bool   `mapstructure:"context_fallback_to_redis"`
		Namespace       string `mapstructure:"context_namespace"`
		KVLite          struct {
			Addr             string `mapstructure:"addr"`
			Password         string `mapstructure:"password"`
			TimeoutMS        int    `mapstructure:"timeout_ms"`
			ConnectTimeoutMS int    `mapstructure:"connect_timeout_ms"`
			ReadTimeoutMS    int    `mapstructure:"read_timeout_ms"`
			WriteTimeoutMS   int    `mapstructure:"write_timeout_ms"`
			PoolSize         int    `mapstructure:"pool_size"`
			MaxIdle          int    `mapstructure:"max_idle"`
			MaxRetries       int    `mapstructure:"max_retries"`
			RetryBackoffMS   int    `mapstructure:"retry_backoff_ms"`
		} `mapstructure:"kvlite"`
	} `mapstructure:"context"`
	Mysql struct {
		DSN         string
		MaxLifeTime int
		MaxOpenConn int
		MaxIdleConn int
	}
	Redis struct {
		Host string
		Port int
		Pwd  string `mapstructure:"pwd"`
	}
	DependOn struct {
		Sensitive struct {
			Address     string
			AccessToken string
		}
		Keywords struct {
			Address     string
			AccessToken string
		}
		Tokenizer struct {
			Address string
		}
	}
}

var conf *Config

func LoadConfig(filePath string, typ ...string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(filePath)
	if len(typ) > 0 {
		v.SetConfigType(typ[0])
	}
	err := v.ReadInConfig()
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	err = v.Unmarshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := validateKVLiteRetrievalConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validateKVLiteRetrievalConfig(cfg *Config) error {
	if cfg == nil || !cfg.Retrieval.KVLite.Enabled {
		return nil
	}
	retrieval := cfg.Retrieval.KVLite
	policy := strings.ToLower(strings.TrimSpace(retrieval.StartupPolicy))
	if policy != "reject" && policy != "fallback_llm" {
		return fmt.Errorf("retrieval.kvlite.startup_policy must be reject or fallback_llm")
	}
	if strings.TrimSpace(retrieval.Address) == "" || strings.TrimSpace(retrieval.Scope.Tenant) == "" ||
		strings.TrimSpace(retrieval.Scope.KnowledgeBase) == "" || strings.TrimSpace(retrieval.Scope.CorpusVersion) == "" {
		return fmt.Errorf("retrieval.kvlite.address and scope tenant, knowledge_base, corpus_version are required when enabled")
	}
	if retrieval.TopK <= 0 || retrieval.TopK > 1024 || retrieval.Threshold < -1 || retrieval.Threshold > 1 || retrieval.TimeoutMS <= 0 || retrieval.PoolSize <= 0 || retrieval.MaxIdle < 0 || retrieval.MaxIdle > retrieval.PoolSize {
		return fmt.Errorf("invalid retrieval.kvlite top_k, threshold, timeout_ms, pool_size or max_idle")
	}
	return nil
}

func InitConfig(filePath string, typ ...string) {
	cfg, err := LoadConfig(filePath, typ...)
	if err != nil {
		log.Fatal(err)
	}
	conf = cfg
}

func GetConfig() *Config {
	return conf
}

func (c *Config) ProviderProfiles() []provider.ProviderProfile {
	if c == nil {
		return nil
	}
	if len(c.Providers) > 0 {
		profiles := make([]provider.ProviderProfile, 0, len(c.Providers))
		for _, profile := range c.Providers {
			profiles = append(profiles, profile.Normalize())
		}
		return profiles
	}
	name := providerNameFromModel(c.Chat.Model)
	defaultProfile := provider.ProviderProfile{
		Name:                            name,
		DisplayName:                     name,
		Kind:                            "openai-compatible",
		BaseURL:                         c.Chat.BaseUrl,
		APIKey:                          c.Chat.ApiKey,
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
	return []provider.ProviderProfile{defaultProfile.Normalize()}
}

func providerNameFromModel(model string) string {
	if strings.Contains(strings.ToLower(model), "qwen") {
		return "qwen"
	}
	if strings.Contains(strings.ToLower(model), "deepseek") {
		return "deepseek"
	}
	return "openai"
}
