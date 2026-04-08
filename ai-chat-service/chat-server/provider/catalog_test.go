package provider

import "testing"

func TestResolveSelectionHonorsProviderModelAndFallback(t *testing.T) {
	registry := NewRegistry([]ProviderProfile{
		{
			Name:              "deepseek",
			DisplayName:       "DeepSeek",
			Kind:              "openai-compatible",
			BaseURL:           "https://api.deepseek.com/v1",
			APIKey:            "test-key",
			DefaultModel:      "deepseek-chat",
			ChatModel:         "deepseek-chat",
			Models:            []string{"deepseek-chat", "deepseek-reasoner"},
			SupportsStream:    true,
			SupportsNonStream: true,
			Enabled:           true,
			Priority:          200,
		},
		{
			Name:              "openai",
			DisplayName:       "OpenAI",
			Kind:              "openai-compatible",
			BaseURL:           "https://api.openai.com/v1",
			APIKey:            "test-key",
			DefaultModel:      "gpt-3.5-turbo",
			ChatModel:         "gpt-3.5-turbo",
			Models:            []string{"gpt-3.5-turbo", "gpt-4o-mini"},
			SupportsStream:    true,
			SupportsNonStream: true,
			Enabled:           true,
			Priority:          100,
		},
	})

	selected, err := registry.ResolveSelection("deepseek", "deepseek-reasoner", true)
	if err != nil {
		t.Fatalf("resolve explicit model: %v", err)
	}
	if selected.FallbackUsed {
		t.Fatalf("did not expect fallback for a supported model")
	}
	if selected.ActualProvider != "deepseek" {
		t.Fatalf("expected deepseek provider, got %s", selected.ActualProvider)
	}
	if selected.ActualModel != "deepseek-reasoner" {
		t.Fatalf("expected deepseek-reasoner, got %s", selected.ActualModel)
	}

	fallback, err := registry.ResolveSelection("deepseek", "gpt-4o-mini", true)
	if err != nil {
		t.Fatalf("resolve fallback model: %v", err)
	}
	if !fallback.FallbackUsed {
		t.Fatalf("expected fallback to trigger for unsupported model")
	}
	if fallback.FallbackReason != "invalid_model_for_provider" {
		t.Fatalf("unexpected fallback reason: %s", fallback.FallbackReason)
	}
	if fallback.ActualProvider != "deepseek" {
		t.Fatalf("expected fallback to stay on deepseek provider, got %s", fallback.ActualProvider)
	}
	if fallback.ActualModel != "deepseek-chat" {
		t.Fatalf("expected fallback to deepseek default model, got %s", fallback.ActualModel)
	}
}
