package qa

import (
	"testing"

	"ai-chat-service/chat-server/provider"
	"ai-chat-service/pkg/config"
)

func TestSelectSemanticEmbeddingProviderHonorsEmbeddingProfile(t *testing.T) {
	cnf := &config.Config{}
	cnf.Retrieval.Semantic.EmbeddingProfile = "openai-embeddings"
	cnf.Providers = []provider.ProviderProfile{
		{
			Name:               "deepseek",
			DisplayName:        "DeepSeek",
			DefaultModel:       "deepseek-chat",
			ChatModel:          "deepseek-chat",
			SupportsStream:     true,
			SupportsNonStream:  true,
			SupportsEmbeddings: false,
			Enabled:            true,
			Priority:           100,
		},
		{
			Name:               "openai-embeddings",
			DisplayName:        "Mock OpenAI Embeddings",
			DefaultModel:       "text-embedding-ada-002",
			EmbeddingModel:     "text-embedding-ada-002",
			SupportsEmbeddings: true,
			Enabled:            true,
			Priority:           94,
		},
	}

	selection := selectSemanticEmbeddingProvider(cnf, SemanticConfig{
		Enabled:        true,
		EmbeddingModel: "custom-semantic-model",
		EmbeddingDim:   64,
	})
	if selection.provider == nil {
		t.Fatalf("expected a semantic embedding provider")
	}
	if selection.provider.Name() != "openai-embeddings" {
		t.Fatalf("expected openai-embeddings provider, got %q", selection.provider.Name())
	}
	if selection.model != "text-embedding-ada-002" {
		t.Fatalf("expected runtime model to prefer provider embedding model, got %q", selection.model)
	}
}
