package provider

import (
	"strings"

	"ai-chat-service/chat-server/llm"
)

type SelectionResult struct {
	RequestedProvider string
	RequestedModel    string
	ActualProvider    string
	ActualModel       string
	Provider          Provider
	Profile           ProviderProfile
	FallbackUsed      bool
	FallbackReason    string
}

func (p ProviderProfile) DisplayNameOrDefault() string {
	if strings.TrimSpace(p.DisplayName) != "" {
		return p.DisplayName
	}
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return llm.DetectProviderFromModel(p.DefaultModel)
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

func (p ProviderProfile) DefaultChatModel() string {
	if strings.TrimSpace(p.DefaultModel) != "" {
		return p.EffectiveModel(p.DefaultModel)
	}
	if strings.TrimSpace(p.ChatModel) != "" {
		return p.EffectiveModel(p.ChatModel)
	}
	models := p.AvailableModels()
	if len(models) > 0 {
		return p.EffectiveModel(models[0])
	}
	return "gpt-3.5-turbo"
}

func (p ProviderProfile) supportsRequestType(stream bool) bool {
	if stream {
		return p.SupportsStream
	}
	return p.SupportsNonStream || !p.SupportsStream
}

func (r *Registry) ProfileByName(name string) (Provider, ProviderProfile, bool) {
	if r == nil {
		return nil, ProviderProfile{}, false
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, ProviderProfile{}, false
	}
	for providerName, profile := range r.profiles {
		if strings.EqualFold(providerName, trimmed) || strings.EqualFold(profile.Name, trimmed) {
			if provider, ok := r.providers[providerName]; ok {
				return provider, profile, true
			}
		}
	}
	return nil, ProviderProfile{}, false
}

func (r *Registry) ProfileByModel(model string) (Provider, ProviderProfile, bool) {
	if r == nil {
		return nil, ProviderProfile{}, false
	}
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil, ProviderProfile{}, false
	}
	if providerName, ok := r.models[strings.ToLower(trimmed)]; ok {
		if provider, ok := r.providers[providerName]; ok {
			return provider, r.profiles[providerName], true
		}
	}
	for providerName, profile := range r.profiles {
		if profile.SupportsModel(trimmed) {
			if provider, ok := r.providers[providerName]; ok {
				return provider, profile, true
			}
		}
	}
	return nil, ProviderProfile{}, false
}

func (r *Registry) firstSupportedProfile(stream bool) (Provider, ProviderProfile, bool) {
	if r == nil {
		return nil, ProviderProfile{}, false
	}
	for _, name := range r.ordered {
		profile, ok := r.profiles[name]
		if !ok || !profile.supportsRequestType(stream) {
			continue
		}
		provider, ok := r.providers[name]
		if !ok {
			continue
		}
		return provider, profile, true
	}
	return nil, ProviderProfile{}, false
}

func (r *Registry) ResolveSelection(requestedProvider, requestedModel string, stream bool) (SelectionResult, error) {
	if r == nil || len(r.providers) == 0 {
		return SelectionResult{}, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Message: "provider registry is empty"}
	}
	requestedProvider = strings.TrimSpace(requestedProvider)
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedProvider != "" {
		provider, profile, ok := r.ProfileByName(requestedProvider)
		if !ok {
			return r.resolveDefaultSelection(requestedProvider, requestedModel, stream, "unknown_provider")
		}
		selectedModel := requestedModel
		if selectedModel == "" {
			selectedModel = profile.DefaultChatModel()
		}
		if !profile.supportsRequestType(stream) {
			return r.resolveDefaultSelection(requestedProvider, selectedModel, stream, "unsupported_request_type")
		}
		if !profile.SupportsModel(selectedModel) {
			actualModel := profile.DefaultChatModel()
			if actualModel == "" {
				actualModel = selectedModel
			}
			if !profile.SupportsModel(actualModel) {
				if fallbackProvider, fallbackProfile, ok := r.firstSupportedProfile(stream); ok {
					return SelectionResult{
						RequestedProvider: requestedProvider,
						RequestedModel:    requestedModel,
						ActualProvider:    fallbackProfile.Name,
						ActualModel:       fallbackProfile.DefaultChatModel(),
						Provider:          fallbackProvider,
						Profile:           fallbackProfile,
						FallbackUsed:      true,
						FallbackReason:    "unsupported_request_type",
					}, nil
				}
				return SelectionResult{}, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Message: "no provider supports the request type"}
			}
			return SelectionResult{
				RequestedProvider: requestedProvider,
				RequestedModel:    requestedModel,
				ActualProvider:    profile.Name,
				ActualModel:       profile.EffectiveModel(actualModel),
				Provider:          provider,
				Profile:           profile,
				FallbackUsed:      true,
				FallbackReason:    "invalid_model_for_provider",
			}, nil
		}
		return SelectionResult{
			RequestedProvider: requestedProvider,
			RequestedModel:    requestedModel,
			ActualProvider:    profile.Name,
			ActualModel:       profile.EffectiveModel(selectedModel),
			Provider:          provider,
			Profile:           profile,
		}, nil
	}
	if requestedModel != "" {
		provider, profile, ok := r.ProfileByModel(requestedModel)
		if !ok {
			return r.resolveDefaultSelection(requestedProvider, requestedModel, stream, "unknown_model")
		}
		if !profile.supportsRequestType(stream) {
			return r.resolveDefaultSelection(requestedProvider, requestedModel, stream, "unsupported_request_type")
		}
		return SelectionResult{
			RequestedProvider: requestedProvider,
			RequestedModel:    requestedModel,
			ActualProvider:    profile.Name,
			ActualModel:       profile.EffectiveModel(requestedModel),
			Provider:          provider,
			Profile:           profile,
		}, nil
	}
	provider, profile, ok := r.firstSupportedProfile(stream)
	if !ok {
		return SelectionResult{}, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Message: "no enabled provider profile"}
	}
	return SelectionResult{
		ActualProvider: profile.Name,
		ActualModel:    profile.DefaultChatModel(),
		Provider:       provider,
		Profile:        profile,
	}, nil
}

func (r *Registry) resolveDefaultSelection(requestedProvider, requestedModel string, stream bool, reason string) (SelectionResult, error) {
	provider, profile, ok := r.firstSupportedProfile(stream)
	if !ok {
		return SelectionResult{}, &llm.ProviderError{Class: llm.ErrorClassUnavailable, Message: "no provider supports the request type"}
	}
	return SelectionResult{
		RequestedProvider: requestedProvider,
		RequestedModel:    requestedModel,
		ActualProvider:    profile.Name,
		ActualModel:       profile.DefaultChatModel(),
		Provider:          provider,
		Profile:           profile,
		FallbackUsed:      true,
		FallbackReason:    reason,
	}, nil
}
