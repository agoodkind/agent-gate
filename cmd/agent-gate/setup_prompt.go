package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/setup"
)

// PlanSummary describes the prepared setup plan presented for confirmation.
type PlanSummary struct {
	Providers       []installer.Provider
	EffectivePolicy config.AuditStoragePolicy
	Maintenance     *auditmaintenance.Plan
}

// Prompter selects interactive setup choices and confirms the prepared plan.
type Prompter interface {
	SelectProviders([]setup.ProviderState) ([]installer.Provider, error)
	SelectAuditProfile(config.AuditStoragePolicy) (config.AuditStorageProfile, error)
	Confirm(PlanSummary) (bool, error)
}

type setupPrompter struct {
	reader *bufio.Reader
	writer io.Writer
}

type confirmationChoice string

const (
	confirmationChoiceYes      confirmationChoice = "yes"
	confirmationChoiceY        confirmationChoice = "y"
	confirmationChoiceNo       confirmationChoice = "no"
	confirmationChoiceN        confirmationChoice = "n"
	confirmationChoiceDefaults confirmationChoice = ""
)

// NewSetupPrompter returns a line-oriented interactive setup prompter.
func NewSetupPrompter(reader io.Reader, writer io.Writer) Prompter {
	return &setupPrompter{reader: bufio.NewReader(reader), writer: writer}
}

func (prompter *setupPrompter) SelectProviders(states []setup.ProviderState) ([]installer.Provider, error) {
	defaults := make([]installer.Provider, 0, len(states))
	for _, state := range states {
		status := "not detected"
		if state.ClientPath != "" && state.ManagedRegistration {
			status = "client and managed registration detected"
		} else if state.ClientPath != "" {
			status = "client detected"
		} else if state.ManagedRegistration {
			status = "managed registration detected"
		}
		if _, err := fmt.Fprintf(prompter.writer, "%s: %s\n", state.Provider, status); err != nil {
			wrappedErr := fmt.Errorf("write provider detection: %w", err)
			slog.Warn("setup provider detection output failed", "err", wrappedErr)
			return nil, wrappedErr
		}
		if state.ClientPath != "" || state.ManagedRegistration {
			defaults = append(defaults, state.Provider)
		}
	}
	defaultValue := joinProviders(defaults)
	for {
		value, err := prompter.readChoice("Select providers", defaultValue)
		if err != nil {
			wrappedErr := fmt.Errorf("read provider selection: %w", err)
			slog.Warn("setup provider selection input failed", "err", wrappedErr)
			return nil, wrappedErr
		}
		providers, err := installer.ParseProviders(value)
		if err == nil {
			return providers, nil
		}
		if _, writeErr := fmt.Fprintf(prompter.writer, "%v\n", err); writeErr != nil {
			wrappedErr := fmt.Errorf("write provider selection error: %w", writeErr)
			slog.Warn("setup provider selection output failed", "err", wrappedErr)
			return nil, wrappedErr
		}
	}
}

func (prompter *setupPrompter) SelectAuditProfile(
	policy config.AuditStoragePolicy,
) (config.AuditStorageProfile, error) {
	defaultProfile := policy.Profile
	switch defaultProfile {
	case config.AuditStorageProfileBalanced,
		config.AuditStorageProfileFull,
		config.AuditStorageProfileMinimal:
	default:
		defaultProfile = config.AuditStorageProfileBalanced
	}
	for {
		value, err := prompter.readChoice("Select audit profile", string(defaultProfile))
		if err != nil {
			wrappedErr := fmt.Errorf("read audit profile: %w", err)
			slog.Warn("setup audit profile input failed", "err", wrappedErr)
			return "", wrappedErr
		}
		profile := config.AuditStorageProfile(value)
		switch profile {
		case config.AuditStorageProfileBalanced,
			config.AuditStorageProfileFull,
			config.AuditStorageProfileMinimal:
			return profile, nil
		default:
			if _, writeErr := fmt.Fprintln(prompter.writer, "select balanced, full, or minimal"); writeErr != nil {
				wrappedErr := fmt.Errorf("write audit profile error: %w", writeErr)
				slog.Warn("setup audit profile output failed", "err", wrappedErr)
				return "", wrappedErr
			}
		}
	}
}

func (prompter *setupPrompter) Confirm(summary PlanSummary) (bool, error) {
	providerNames := joinProviders(summary.Providers)
	for {
		value, err := prompter.readChoice("Apply setup for "+providerNames+"? Enter yes or no", "no")
		if err != nil {
			wrappedErr := fmt.Errorf("read confirmation: %w", err)
			slog.Warn("setup confirmation input failed", "err", wrappedErr)
			return false, wrappedErr
		}
		switch confirmationChoice(strings.ToLower(value)) {
		case confirmationChoiceYes, confirmationChoiceY:
			return true, nil
		case confirmationChoiceNo, confirmationChoiceN:
			return false, nil
		case confirmationChoiceDefaults:
			return false, nil
		default:
			if _, writeErr := fmt.Fprintln(prompter.writer, "enter yes or no"); writeErr != nil {
				wrappedErr := fmt.Errorf("write confirmation error: %w", writeErr)
				slog.Warn("setup confirmation output failed", "err", wrappedErr)
				return false, wrappedErr
			}
		}
	}
}

func (prompter *setupPrompter) readChoice(label string, defaultValue string) (string, error) {
	if _, err := fmt.Fprintf(prompter.writer, "%s [%s]: ", label, defaultValue); err != nil {
		wrappedErr := fmt.Errorf("write prompt: %w", err)
		slog.Warn("setup prompt output failed", "err", wrappedErr)
		return "", wrappedErr
	}
	line, err := prompter.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

func joinProviders(providers []installer.Provider) string {
	values := make([]string, len(providers))
	for index, provider := range providers {
		values[index] = string(provider)
	}
	return strings.Join(values, ",")
}
