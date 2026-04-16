package config

import (
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
		RouteMode         string  `mapstructure:"route_mode"`
	}
	Providers []provider.ProviderProfile `mapstructure:"providers"`
	Retrieval struct {
		Semantic struct {
			Enabled        bool    `mapstructure:"enabled"`
			TopK           int     `mapstructure:"top_k"`
			Threshold      float64 `mapstructure:"threshold"`
			CorpusLimit    int     `mapstructure:"corpus_limit"`
			EmbeddingModel string  `mapstructure:"embedding_model"`
			EmbeddingDim   int     `mapstructure:"embedding_dim"`
			AsyncBackfill  bool    `mapstructure:"async_backfill"`
		} `mapstructure:"semantic"`
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
	VectorDB struct {
		Url                string
		Username           string
		Pwd                string
		Database           string
		Timeout            int
		MaxIdleConnPerHost int
		ReadConsistency    string
		IdleConnTimeout    int
	}
}

var conf *Config

func InitConfig(filePath string, typ ...string) {
	v := viper.New()
	v.SetDefault("retrieval.semantic.async_backfill", true)
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
