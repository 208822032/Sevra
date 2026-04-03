package config

import (
	"log"

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
