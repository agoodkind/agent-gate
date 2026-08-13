package installer

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

// Stage identifies one installation mutation boundary.
type Stage string

const (
	// StageConfig applies prepared configuration bytes.
	StageConfig Stage = "config"
	// StageService applies the service file and waits for readiness.
	StageService Stage = "service"
	// StageHooks applies prepared provider hook bytes.
	StageHooks Stage = "hooks"
)

const (
	configRepairCommand  = "agent-gate install all --no-service --no-claude --no-codex --no-cursor --no-gemini --no-copilot"
	serviceRepairCommand = "agent-gate install service"
	hooksRepairCommand   = "agent-gate install hooks"
)

// InstallationOptions selects complete installation plan components.
type InstallationOptions struct {
	Config  *config.EnsureDefaultsOptions
	Hooks   *HooksOptions
	Service *ServiceOptions
}

// InstallationPlan retains every selected validated replacement.
type InstallationPlan struct {
	Config         *config.DefaultsPlan
	Service        *ServiceInstallationPlan
	Hooks          *HookInstallationPlan
	repairCommands map[Stage]string
	prepared       bool
	config         *config.DefaultsPlan
	service        *ServiceInstallationPlan
	hooks          *HookInstallationPlan
}

// ApplyResult records stages that completed before return.
type ApplyResult struct {
	Completed []Stage
}

// ApplyError reports one failed stage and its supported repair command.
type ApplyError struct {
	Stage         Stage
	RepairCommand string
	Err           error
}

// Error formats the failed installation stage.
func (err *ApplyError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s: %v; repair with: %s", err.Stage, err.Err, err.RepairCommand)
}

// Unwrap returns the stage failure.
func (err *ApplyError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// PrepareInstallation validates every selected layer before any write.
func PrepareInstallation(options InstallationOptions) (_ *InstallationPlan, resultErr error) {
	slog.Info(
		"prepare installation",
		"config", options.Config != nil,
		"service", options.Service != nil,
		"hooks", options.Hooks != nil,
	)
	plan := &InstallationPlan{
		Config:         nil,
		Service:        nil,
		Hooks:          nil,
		repairCommands: installationRepairCommands(options),
		prepared:       true,
		config:         nil,
		service:        nil,
		hooks:          nil,
	}
	defer func() {
		if resultErr != nil && plan.config != nil {
			_ = plan.config.Close()
		}
	}()
	if options.Config != nil {
		configPlan, err := config.PrepareDefaults(*options.Config)
		if err != nil {
			return nil, reportPreparationError(StageConfig, err)
		}
		plan.config = configPlan
		if configPlan.Config == nil {
			return nil, reportPreparationError(
				StageConfig,
				errors.New("prepared configuration is missing validation"),
			)
		}
		if validationErrors := hook.ValidateConfig(configPlan.Config); len(validationErrors) > 0 {
			return nil, reportPreparationError(
				StageConfig,
				fmt.Errorf("hook schema: %w", validationErrors[0]),
			)
		}
		plan.Config = configPlan
	}
	if options.Service != nil {
		servicePlan, err := PrepareServiceInstallation(*options.Service)
		if err != nil {
			return nil, reportPreparationError(StageService, err)
		}
		plan.Service = servicePlan
		plan.service = servicePlan
	}
	if options.Hooks != nil {
		hookPlan, err := PrepareHookInstallation(*options.Hooks)
		if err != nil {
			return nil, reportPreparationError(StageHooks, err)
		}
		plan.Hooks = hookPlan
		plan.hooks = hookPlan
	}
	return plan, nil
}

// Close releases a prepared installation plan without applying it.
func (plan *InstallationPlan) Close() error {
	if plan == nil {
		return nil
	}
	configPlan, _, _ := planComponents(plan)
	if configPlan == nil {
		return nil
	}
	if err := configPlan.Close(); err != nil {
		wrappedErr := fmt.Errorf("close prepared configuration: %w", err)
		slog.Warn("close installation plan failed", "err", wrappedErr)
		return wrappedErr
	}
	return nil
}

// ApplyInstallation applies configuration, service, and hooks in stage order.
func ApplyInstallation(plan *InstallationPlan) (ApplyResult, error) {
	result := ApplyResult{Completed: nil}
	if plan == nil {
		return result, errors.New("installation plan is required")
	}
	configPlan, servicePlan, hookPlan := planComponents(plan)
	if configPlan != nil {
		if _, err := config.ApplyDefaults(configPlan); err != nil {
			return result, newApplyError(plan, StageConfig, err)
		}
		result.Completed = append(result.Completed, StageConfig)
	}
	if servicePlan != nil {
		if err := ApplyServiceInstallation(servicePlan); err != nil {
			return result, newApplyError(plan, StageService, err)
		}
		result.Completed = append(result.Completed, StageService)
	}
	if hookPlan != nil {
		if err := ApplyHookInstallation(hookPlan); err != nil {
			return result, newApplyError(plan, StageHooks, err)
		}
		result.Completed = append(result.Completed, StageHooks)
	}
	return result, nil
}

func planComponents(
	plan *InstallationPlan,
) (*config.DefaultsPlan, *ServiceInstallationPlan, *HookInstallationPlan) {
	if plan.prepared {
		return plan.config, plan.service, plan.hooks
	}
	return plan.Config, plan.Service, plan.Hooks
}

func reportPreparationError(stage Stage, err error) error {
	wrappedErr := fmt.Errorf("%s: %w", stage, err)
	slog.Warn("prepare installation failed", "stage", stage, "err", wrappedErr)
	return wrappedErr
}

func newApplyError(plan *InstallationPlan, stage Stage, err error) *ApplyError {
	repairCommand := hooksRepairCommand
	switch stage {
	case StageConfig:
		repairCommand = configRepairCommand
	case StageService:
		repairCommand = serviceRepairCommand
	case StageHooks:
		repairCommand = hooksRepairCommand
	}
	if plan != nil && plan.repairCommands[stage] != "" {
		repairCommand = plan.repairCommands[stage]
	}
	return &ApplyError{Stage: stage, RepairCommand: repairCommand, Err: err}
}

func installationRepairCommands(options InstallationOptions) map[Stage]string {
	commands := map[Stage]string{
		StageConfig:  configRepairCommand,
		StageService: serviceRepairCommand,
		StageHooks:   hooksRepairCommand,
	}
	if options.Config != nil {
		parts := []string{configRepairCommand}
		if options.Config.AutoUpdateMode != "" {
			parts = append(parts, "--auto-update", shellCommandArgument(options.Config.AutoUpdateMode))
		}
		if options.Config.AuditProfile != "" {
			parts = append(parts, "--audit-profile", shellCommandArgument(string(options.Config.AuditProfile)))
		}
		commands[StageConfig] = strings.Join(parts, " ")
	}
	if options.Service != nil {
		parts := []string{serviceRepairCommand}
		parts = appendCommandOption(parts, "--bin-path", options.Service.BinPath)
		parts = appendCommandOption(parts, "--service-templates", options.Service.ServiceTemplatesDir)
		commands[StageService] = strings.Join(parts, " ")
	}
	if options.Hooks != nil {
		parts := []string{hooksRepairCommand}
		parts = appendCommandOption(parts, "--bin-path", options.Hooks.BinPath)
		parts = appendCommandOption(parts, "--templates", options.Hooks.TemplatesDir)
		parts = appendHookSelection(parts, *options.Hooks)
		commands[StageHooks] = strings.Join(parts, " ")
	}
	return commands
}

func appendCommandOption(parts []string, name string, value string) []string {
	if value == "" {
		return parts
	}
	return append(parts, name, shellCommandArgument(value))
}

func appendHookSelection(parts []string, options HooksOptions) []string {
	selections := []struct {
		selected bool
		flag     string
	}{
		{selected: options.InstallClaude, flag: "--no-claude"},
		{selected: options.InstallCodex, flag: "--no-codex"},
		{selected: options.InstallCursor, flag: "--no-cursor"},
		{selected: options.InstallGemini, flag: "--no-gemini"},
		{selected: options.InstallCopilot, flag: "--no-copilot"},
	}
	for _, selection := range selections {
		if !selection.selected {
			parts = append(parts, selection.flag)
		}
	}
	return parts
}

func shellCommandArgument(value string) string {
	if value != "" && strings.IndexFunc(value, func(character rune) bool {
		return !strings.ContainsRune(
			"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:-",
			character,
		)
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
