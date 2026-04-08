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
