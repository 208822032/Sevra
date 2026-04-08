package config

import (
	"sort"
	"strings"
)

type ModelCatalogEntry struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	DisplayName      string `json:"display_name"`
	Enabled          bool   `json:"enabled"`
	IsDefault        bool   `json:"is_default"`
	SupportsStream   bool   `json:"supports_stream"`
	SupportsChat     bool   `json:"supports_chat"`
	SupportsEmbedding bool  `json:"supports_embedding,omitempty"`
	ContextWindow    int    `json:"context_window,omitempty"`
}

type ProviderCatalogEntry struct {
	Provider          string              `json:"provider"`
	DisplayName       string              `json:"display_name"`
	Enabled           bool                `json:"enabled"`
	IsDefault         bool                `json:"is_default"`
	SupportsStream    bool                `json:"supports_stream"`
	SupportsChat      bool                `json:"supports_chat"`
	SupportsEmbedding bool                `json:"supports_embedding,omitempty"`
	ContextWindow     int                 `json:"context_window,omitempty"`
	DefaultModel      string              `json:"default_model"`
	Models            []ModelCatalogEntry `json:"models"`
}

type ChatConfigResponse struct {
	ApiModel         string               `json:"apiModel"`
	SelectedProvider string               `json:"selectedProvider"`
	SelectedModel    string               `json:"selectedModel"`
	DefaultProvider  string               `json:"defaultProvider,omitempty"`
	DefaultModel     string               `json:"defaultModel,omitempty"`
	Providers        []ProviderCatalogEntry `json:"providers"`
	TimeoutMs        int                  `json:"timeoutMs,omitempty"`
	SocksProxy       string               `json:"socksProxy,omitempty"`
	HttpsProxy       string               `json:"httpsProxy,omitempty"`
	ReverseProxy     string               `json:"reverseProxy,omitempty"`
	Balance          string               `json:"balance,omitempty"`
}

func (c *Config) DefaultProviderProfile() (ProviderProfile, bool) {
	if c == nil {
		return ProviderProfile{}, false
	}
	profiles := sortProviderProfiles(c.ProviderProfiles())
	if len(profiles) == 0 {
		return ProviderProfile{}, false
	}
	for _, profile := range profiles {
		if profile.Enabled {
			return profile, true
		}
	}
	return profiles[0], true
}

func (c *Config) ProviderCatalog() ChatConfigResponse {
	defaultProfile, ok := c.DefaultProviderProfile()
	profiles := sortProviderProfiles(c.ProviderProfiles())
	response := ChatConfigResponse{
		ApiModel: "ChatGPTAPI",
	}
	if ok {
		response.SelectedProvider = defaultProfile.Name
		response.SelectedModel = defaultProfile.DefaultModel
		response.DefaultProvider = defaultProfile.Name
		response.DefaultModel = defaultProfile.DefaultModel
		response.TimeoutMs = defaultProfile.TimeoutMS
	} else if c != nil {
		response.SelectedModel = c.Chat.Model
		response.DefaultModel = c.Chat.Model
		response.TimeoutMs = 60000
	}
	if len(profiles) == 0 && c != nil {
		profiles = []ProviderProfile{{
			Name:          providerNameFromModel(c.Chat.Model),
			DisplayName:    providerNameFromModel(c.Chat.Model),
			Kind:          "openai-compatible",
			DefaultModel:   c.Chat.Model,
			ChatModel:      c.Chat.Model,
			Models:         []string{c.Chat.Model},
			SupportsStream: true,
			SupportsNonStream: true,
			ContextWindow:  c.Chat.MaxTokens,
			Enabled:       true,
			Priority:      100,
			TimeoutMS:     60000,
		}}
	}
	response.Providers = make([]ProviderCatalogEntry, 0, len(profiles))
	for _, profile := range profiles {
		response.Providers = append(response.Providers, profile.toProviderCatalogEntry(ok && profile.Name == defaultProfile.Name))
	}
	return response
}

func sortProviderProfiles(profiles []ProviderProfile) []ProviderProfile {
	if len(profiles) == 0 {
		return nil
	}
	sorted := append([]ProviderProfile(nil), profiles...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority == sorted[j].Priority {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Priority > sorted[j].Priority
	})
	return sorted
}

func (p ProviderProfile) DisplayNameOrDefault() string {
	if strings.TrimSpace(p.DisplayName) != "" {
		return p.DisplayName
	}
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return providerNameFromModel(p.DefaultModel)
}

func (p ProviderProfile) AvailableModels() []string {
	candidates := make([]string, 0, len(p.Models)+4)
	appendModel := func(model string) {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" {
			return
		}
		for _, existing := range candidates {
			if strings.EqualFold(existing, trimmed) {
				return
			}
		}
		candidates = append(candidates, trimmed)
	}
	appendModel(p.DefaultModel)
	appendModel(p.ChatModel)
	for _, model := range p.Models {
		appendModel(model)
	}
	for alias, target := range p.ModelAliases {
		appendModel(alias)
		appendModel(target)
	}
	return candidates
}

func (p ProviderProfile) SupportsModel(model string) bool {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return false
	}
	for _, candidate := range p.AvailableModels() {
		if strings.EqualFold(candidate, trimmed) {
			return true
		}
	}
	return false
}

func (p ProviderProfile) EffectiveModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		if strings.TrimSpace(p.DefaultModel) != "" {
			return p.DefaultModel
		}
		return p.ChatModel
	}
	for alias, target := range p.ModelAliases {
		if strings.EqualFold(alias, trimmed) && strings.TrimSpace(target) != "" {
			return target
		}
	}
	return trimmed
}

func (p ProviderProfile) toProviderCatalogEntry(isDefault bool) ProviderCatalogEntry {
	models := p.AvailableModels()
	modelEntries := make([]ModelCatalogEntry, 0, len(models))
	for _, model := range models {
		modelEntries = append(modelEntries, ModelCatalogEntry{
			Provider:          p.Name,
			Model:             model,
			DisplayName:       model,
			Enabled:           p.Enabled,
			IsDefault:         strings.EqualFold(model, p.DefaultModel),
			SupportsStream:    p.SupportsStream,
			SupportsChat:      true,
			SupportsEmbedding: p.SupportsEmbeddings,
			ContextWindow:     p.ContextWindow,
		})
	}
	return ProviderCatalogEntry{
		Provider:          p.Name,
		DisplayName:       p.DisplayNameOrDefault(),
		Enabled:           p.Enabled,
		IsDefault:         isDefault,
		SupportsStream:    p.SupportsStream,
		SupportsChat:      true,
		SupportsEmbedding: p.SupportsEmbeddings,
		ContextWindow:     p.ContextWindow,
		DefaultModel:      p.DefaultModel,
		Models:            modelEntries,
	}
}