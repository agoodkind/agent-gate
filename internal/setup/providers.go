package setup

import (
	"fmt"
	"log/slog"
	"os/exec"

	installer "goodkind.io/agent-gate/internal/install"
)

// ProviderState records the detected client and managed registration for one provider.
type ProviderState struct {
	Provider            installer.Provider
	ClientPath          string
	ManagedRegistration bool
}

// DetectOptions supplies provider detection boundaries.
type DetectOptions struct {
	HomeDir  string
	LookPath func(string) (string, error)
}

var providerExecutables = map[installer.Provider]string{
	installer.ProviderClaude:  "claude",
	installer.ProviderCodex:   "codex",
	installer.ProviderCursor:  "cursor",
	installer.ProviderGemini:  "gemini",
	installer.ProviderCopilot: "copilot",
}

// DetectProviders detects supported clients and managed registrations in canonical order.
func DetectProviders(options DetectOptions) ([]ProviderState, error) {
	lookPath := options.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	states := make([]ProviderState, 0, len(installer.AllProviders()))
	for _, provider := range installer.AllProviders() {
		state := ProviderState{Provider: provider, ClientPath: "", ManagedRegistration: false}
		if clientPath, err := lookPath(providerExecutables[provider]); err == nil {
			state.ClientPath = clientPath
		}
		hooks := installer.DefaultHooksOptions("")
		hooks.HomeDir = options.HomeDir
		managed, err := installer.HasManagedLifecycleRegistration(hooks, provider)
		if err != nil {
			wrappedErr := fmt.Errorf("detect %s managed registration: %w", provider, err)
			slog.Warn("provider registration detection failed", "provider", provider, "err", wrappedErr)
			return nil, wrappedErr
		}
		state.ManagedRegistration = managed
		states = append(states, state)
	}
	return states, nil
}
