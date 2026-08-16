package config

import (
	"fmt"
	"slices"
)

var validDisableProviders = []string{"claude", "cursor", "codex", "copilot", "gemini"}

func validateDisableProviders(ruleName string, providers []string) error {
	for _, provider := range providers {
		if !slices.Contains(validDisableProviders, provider) {
			return fmt.Errorf(
				"rule %q: unknown disable_providers entry %q (expected one of %q, %q, %q, %q, or %q)",
				ruleName,
				provider,
				validDisableProviders[0],
				validDisableProviders[1],
				validDisableProviders[2],
				validDisableProviders[3],
				validDisableProviders[4],
			)
		}
	}
	return nil
}

// ProviderDisabled reports whether system is listed in disable_providers.
func (r *Rule) ProviderDisabled(system string) bool {
	return slices.Contains(r.DisableProviders, system)
}
