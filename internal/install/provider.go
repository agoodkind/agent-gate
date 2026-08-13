package installer

import (
	"fmt"
	"strings"
)

// Provider identifies one supported managed hook provider.
type Provider string

const (
	// ProviderClaude selects Claude Code hooks.
	ProviderClaude Provider = "claude"
	// ProviderCodex selects Codex hooks.
	ProviderCodex Provider = "codex"
	// ProviderCursor selects Cursor hooks.
	ProviderCursor Provider = "cursor"
	// ProviderGemini selects Gemini CLI hooks.
	ProviderGemini Provider = "gemini"
	// ProviderCopilot selects GitHub Copilot hooks.
	ProviderCopilot Provider = "copilot"
)

var canonicalProviders = [...]Provider{
	ProviderClaude,
	ProviderCodex,
	ProviderCursor,
	ProviderGemini,
	ProviderCopilot,
}

// AllProviders returns every supported provider in canonical installation order.
func AllProviders() []Provider {
	providers := make([]Provider, len(canonicalProviders))
	copy(providers, canonicalProviders[:])
	return providers
}

// ParseProviders parses a comma-separated selection into canonical order.
func ParseProviders(value string) ([]Provider, error) {
	if value == "" {
		return []Provider{}, nil
	}
	requested := make(map[Provider]bool, len(canonicalProviders))
	for name := range strings.SplitSeq(value, ",") {
		provider := Provider(strings.TrimSpace(name))
		if !isKnownProvider(provider) {
			return nil, fmt.Errorf("unknown provider %q", name)
		}
		if requested[provider] {
			return nil, fmt.Errorf("duplicate provider %q", provider)
		}
		requested[provider] = true
	}
	return providersFromSet(requested), nil
}

func selectedProviders(selection []Provider) ([]Provider, error) {
	if selection == nil {
		return AllProviders(), nil
	}
	requested := make(map[Provider]bool, len(selection))
	for _, provider := range selection {
		if !isKnownProvider(provider) {
			return nil, fmt.Errorf("unknown provider %q", provider)
		}
		if requested[provider] {
			return nil, fmt.Errorf("duplicate provider %q", provider)
		}
		requested[provider] = true
	}
	return providersFromSet(requested), nil
}

func providersFromSet(requested map[Provider]bool) []Provider {
	providers := make([]Provider, 0, len(requested))
	for _, provider := range canonicalProviders {
		if requested[provider] {
			providers = append(providers, provider)
		}
	}
	return providers
}

func isKnownProvider(provider Provider) bool {
	switch provider {
	case ProviderClaude, ProviderCodex, ProviderCursor, ProviderGemini, ProviderCopilot:
		return true
	default:
		return false
	}
}
