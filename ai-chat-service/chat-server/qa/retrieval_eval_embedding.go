package qa

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"ai-chat-service/chat-server/llm"
	"ai-chat-service/chat-server/provider"
	"ai-chat-service/pkg/config"

	openai "github.com/sashabaranov/go-openai"
)

type RetrievalEvalEmbeddingMode string

const (
	RetrievalEvalEmbeddingModePlaceholder RetrievalEvalEmbeddingMode = "placeholder"
	RetrievalEvalEmbeddingModeProvider    RetrievalEvalEmbeddingMode = "provider"
)

type RetrievalEvalEmbeddingSource struct {
	Mode         RetrievalEvalEmbeddingMode
	ProfileName  string
	ProviderName string
	Model        string
	Provider     llm.EmbeddingProvider
}

func newPlaceholderRetrievalEvalEmbeddingSource() RetrievalEvalEmbeddingSource {
	provider := NewHashEmbeddingProvider("placeholder-hash-embedding", DefaultSemanticEmbeddingDim)
	return RetrievalEvalEmbeddingSource{
		Mode:         RetrievalEvalEmbeddingModePlaceholder,
		ProfileName:  "placeholder-hash-embedding",
		ProviderName: provider.Name(),
		Model:        provider.Name(),
		Provider:     newCachedEmbeddingProvider(provider),
	}
}

func (s RetrievalEvalEmbeddingSource) Describe() string {
	mode := strings.TrimSpace(string(s.Mode))
	if mode == "" {
		mode = string(RetrievalEvalEmbeddingModePlaceholder)
	}
	parts := []string{"mode=" + mode}
	if trimmed := strings.TrimSpace(s.ProfileName); trimmed != "" {
		parts = append(parts, "profile="+trimmed)
	}
	if trimmed := strings.TrimSpace(s.ProviderName); trimmed != "" {
		parts = append(parts, "provider="+trimmed)
	}
	if trimmed := strings.TrimSpace(s.Model); trimmed != "" {
		parts = append(parts, "model="+trimmed)
	}
	return strings.Join(parts, "|")
}

func ResolveRetrievalEvalEmbeddingSource(cnf *config.Config, requestedMode, requestedProfile string) (RetrievalEvalEmbeddingSource, error) {
	mode := RetrievalEvalEmbeddingMode(strings.ToLower(strings.TrimSpace(requestedMode)))
	switch mode {
	case "", RetrievalEvalEmbeddingModePlaceholder:
		return newPlaceholderRetrievalEvalEmbeddingSource(), nil
	case RetrievalEvalEmbeddingModeProvider:
		if cnf == nil {
			return RetrievalEvalEmbeddingSource{}, errors.New("embedding provider mode requires a config file")
		}
		profileName := strings.TrimSpace(requestedProfile)
		if profileName == "" {
			profileName = strings.TrimSpace(cnf.Retrieval.Semantic.EmbeddingProfile)
		}
		if profileName == "" {
			return RetrievalEvalEmbeddingSource{}, fmt.Errorf("embedding provider mode requires --embedding-profile or retrieval.semantic.embedding_profile; available embeddings profiles: %s", strings.Join(listRetrievalEvalEmbeddingProfiles(cnf), ", "))
		}
		registry := provider.NewRegistry(cnf.ProviderProfiles())
		selectedProvider, profile, ok := registry.ProfileByName(profileName)
		if !ok {
			return RetrievalEvalEmbeddingSource{}, fmt.Errorf("embedding profile %q not found; available profiles: %s", profileName, strings.Join(registry.ProfileNames(), ", "))
		}
		if !profile.SupportsEmbeddings {
			return RetrievalEvalEmbeddingSource{}, fmt.Errorf("embedding profile %q does not support embeddings", profileName)
		}
		embeddingProvider, ok := selectedProvider.(llm.EmbeddingProvider)
		if !ok {
			return RetrievalEvalEmbeddingSource{}, fmt.Errorf("embedding profile %q does not implement embeddings", profileName)
		}
		model := resolveRetrievalEvalEmbeddingModel(profile)
		return RetrievalEvalEmbeddingSource{
			Mode:         RetrievalEvalEmbeddingModeProvider,
			ProfileName:  profile.Name,
			ProviderName: selectedProvider.Name(),
			Model:        model,
			Provider:     newCachedEmbeddingProvider(embeddingProvider),
		}, nil
	default:
		return RetrievalEvalEmbeddingSource{}, fmt.Errorf("unsupported embedding mode %q", requestedMode)
	}
}

func normalizeRetrievalEvalEmbeddingSource(source RetrievalEvalEmbeddingSource) (RetrievalEvalEmbeddingSource, error) {
	mode := RetrievalEvalEmbeddingMode(strings.ToLower(strings.TrimSpace(string(source.Mode))))
	switch mode {
	case "":
		if source.Provider != nil {
			mode = RetrievalEvalEmbeddingModeProvider
		} else {
			mode = RetrievalEvalEmbeddingModePlaceholder
		}
	}
	source.Mode = mode
	switch mode {
	case RetrievalEvalEmbeddingModePlaceholder:
		if source.Provider == nil {
			return newPlaceholderRetrievalEvalEmbeddingSource(), nil
		}
		if strings.TrimSpace(source.ProfileName) == "" {
			source.ProfileName = "placeholder-hash-embedding"
		}
		if strings.TrimSpace(source.ProviderName) == "" {
			source.ProviderName = source.ProfileName
		}
		if strings.TrimSpace(source.Model) == "" {
			source.Model = source.ProfileName
		}
		return source, nil
	case RetrievalEvalEmbeddingModeProvider:
		if source.Provider == nil {
			return RetrievalEvalEmbeddingSource{}, errors.New("provider embedding source is missing an embedding provider")
		}
		if strings.TrimSpace(source.ProfileName) == "" {
			return RetrievalEvalEmbeddingSource{}, errors.New("provider embedding source is missing a profile name")
		}
		if strings.TrimSpace(source.ProviderName) == "" {
			source.ProviderName = source.ProfileName
		}
		if strings.TrimSpace(source.Model) == "" {
			source.Model = source.ProviderName
		}
		return source, nil
	default:
		return RetrievalEvalEmbeddingSource{}, fmt.Errorf("unsupported embedding mode %q", source.Mode)
	}
}

func resolveRetrievalEvalEmbeddingModel(profile provider.ProviderProfile) string {
	if trimmed := strings.TrimSpace(profile.EmbeddingModel); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(profile.DefaultEmbeddingModel); trimmed != "" {
		return trimmed
	}
	return openai.AdaEmbeddingV2.String()
}

func listRetrievalEvalEmbeddingProfiles(cnf *config.Config) []string {
	if cnf == nil {
		return nil
	}
	profiles := cnf.ProviderProfiles()
	result := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if profile.SupportsEmbeddings {
			result = append(result, profile.Name)
		}
	}
	sort.Strings(result)
	return result
}

type cachedEmbeddingProvider struct {
	base  llm.EmbeddingProvider
	mu    sync.RWMutex
	cache map[string][]float32
}

func newCachedEmbeddingProvider(base llm.EmbeddingProvider) llm.EmbeddingProvider {
	if base == nil {
		return nil
	}
	return &cachedEmbeddingProvider{
		base:  base,
		cache: map[string][]float32{},
	}
}

func (p *cachedEmbeddingProvider) Name() string {
	if p == nil || p.base == nil {
		return ""
	}
	return p.base.Name()
}

func (p *cachedEmbeddingProvider) Capabilities() llm.CapabilitySet {
	if p == nil || p.base == nil {
		return llm.CapabilitySet{}
	}
	return p.base.Capabilities()
}

func (p *cachedEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p == nil || p.base == nil {
		return nil, errors.New("embedding provider is nil")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	results := make([][]float32, len(texts))
	pendingOrder := make([]string, 0, len(texts))
	pendingIndexes := make(map[string][]int, len(texts))
	p.mu.RLock()
	for index, text := range texts {
		if cached, ok := p.cache[text]; ok {
			results[index] = cloneFloat32Slice(cached)
			continue
		}
		if _, ok := pendingIndexes[text]; !ok {
			pendingOrder = append(pendingOrder, text)
		}
		pendingIndexes[text] = append(pendingIndexes[text], index)
	}
	p.mu.RUnlock()
	if len(pendingOrder) == 0 {
		return results, nil
	}
	vectors, err := p.base.Embed(ctx, pendingOrder)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(pendingOrder) {
		return nil, fmt.Errorf("embedding provider returned %d vectors for %d texts", len(vectors), len(pendingOrder))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for index, text := range pendingOrder {
		vector := cloneFloat32Slice(vectors[index])
		p.cache[text] = vector
		for _, resultIndex := range pendingIndexes[text] {
			results[resultIndex] = cloneFloat32Slice(vector)
		}
	}
	return results, nil
}

func cloneFloat32Slice(values []float32) []float32 {
	if len(values) == 0 {
		return nil
	}
	return append([]float32(nil), values...)
}
