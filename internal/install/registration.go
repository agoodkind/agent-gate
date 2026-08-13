package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var errManagedLifecycleEventMissing = errors.New("managed lifecycle event missing")

// ManagedHookCommand is one installed observe-only lifecycle command.
type ManagedHookCommand struct {
	Provider   Provider
	EventName  string
	Executable string
	Arguments  []string
}

type lifecycleCommandGroup struct {
	Matcher string                 `json:"matcher" toml:"matcher"`
	Command string                 `json:"command" toml:"command"`
	Hooks   []lifecycleCommandHook `json:"hooks" toml:"hooks"`
}

type lifecycleCommandHook struct {
	Type    string `json:"type" toml:"type"`
	Command string `json:"command" toml:"command"`
}

type jsonHookConfiguration struct {
	Hooks map[string]json.RawMessage `json:"hooks"`
}

type codexHookConfiguration struct {
	Hooks struct {
		SessionStart []lifecycleCommandGroup `toml:"SessionStart"`
	} `toml:"hooks"`
}

// ReadManagedLifecycleCommand reads and validates one installed lifecycle command.
func ReadManagedLifecycleCommand(options HooksOptions, provider Provider) (ManagedHookCommand, error) {
	return ReadManagedLifecycleCommandContext(context.Background(), options, provider)
}

// HasManagedLifecycleRegistration reports whether the provider has one complete managed lifecycle registration.
func HasManagedLifecycleRegistration(options HooksOptions, provider Provider) (bool, error) {
	if !isKnownProvider(provider) {
		return false, fmt.Errorf("unknown provider %q", provider)
	}
	homeDir, err := resolvedHomeDir(options.HomeDir)
	if err != nil {
		return false, err
	}
	eventName, matchers, arguments := lifecycleRegistration(provider)
	groups, err := readLifecycleGroups(context.Background(), homeDir, provider, eventName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errManagedLifecycleEventMissing) {
			return false, nil
		}
		return false, err
	}
	if len(groups) == 0 {
		return false, nil
	}
	managed, err := containsManagedLifecycleCommand(groups, provider, arguments)
	if err != nil {
		return false, err
	}
	if !managed {
		return false, nil
	}
	if _, _, err := validateLifecycleGroups(provider, groups, matchers, "", arguments); err != nil {
		return false, err
	}
	return true, nil
}

func containsManagedLifecycleCommand(
	groups []lifecycleCommandGroup,
	provider Provider,
	expectedArguments []string,
) (bool, error) {
	managedMarker := " managed-hook " + string(provider)
	for _, group := range groups {
		for _, hook := range groupCommands(group) {
			_, managed, err := parseManagedLifecycleCommand(hook.Command, managedMarker, expectedArguments)
			if err != nil {
				wrappedErr := fmt.Errorf("%s: %w", provider, err)
				slog.Warn("managed lifecycle presence validation failed", "provider", provider, "err", wrappedErr)
				return false, wrappedErr
			}
			if managed {
				return true, nil
			}
		}
	}
	return false, nil
}

// ReadManagedLifecycleCommandContext reads and validates one installed lifecycle command.
func ReadManagedLifecycleCommandContext(
	ctx context.Context,
	options HooksOptions,
	provider Provider,
) (ManagedHookCommand, error) {
	if !isKnownProvider(provider) {
		return ManagedHookCommand{}, fmt.Errorf("unknown provider %q", provider)
	}
	executable := ""
	var err error
	if options.BinPath != "" {
		executable, err = CanonicalExecutablePath(options.BinPath)
		if err != nil {
			return ManagedHookCommand{}, err
		}
	}
	homeDir, err := resolvedHomeDir(options.HomeDir)
	if err != nil {
		return ManagedHookCommand{}, err
	}
	eventName, matchers, arguments := lifecycleRegistration(provider)
	groups, err := readLifecycleGroups(ctx, homeDir, provider, eventName)
	if err != nil {
		return ManagedHookCommand{}, err
	}
	installedExecutable, command, err := validateLifecycleGroups(
		provider,
		groups,
		matchers,
		executable,
		arguments,
	)
	if err != nil {
		return ManagedHookCommand{}, err
	}
	if err := validateInstalledExecutable(ctx, installedExecutable); err != nil {
		return ManagedHookCommand{}, err
	}
	return ManagedHookCommand{
		Provider:   provider,
		EventName:  eventName,
		Executable: installedExecutable,
		Arguments:  command,
	}, nil
}

func lifecycleRegistration(provider Provider) (string, []string, []string) {
	switch provider {
	case ProviderClaude:
		return "SessionStart", []string{""}, []string{"managed-hook", "claude"}
	case ProviderCodex:
		return "SessionStart", []string{"startup|resume|clear"}, []string{"managed-hook", "codex"}
	case ProviderCursor:
		return "sessionStart", []string{""}, []string{"managed-hook", "cursor"}
	case ProviderGemini:
		return "SessionStart", []string{"startup", "resume", "clear"}, []string{"managed-hook", "gemini"}
	case ProviderCopilot:
		return "sessionStart", []string{""}, []string{"managed-hook", "copilot", "sessionStart"}
	default:
		return "", nil, nil
	}
}

func readLifecycleGroups(
	ctx context.Context,
	homeDir string,
	provider Provider,
	eventName string,
) ([]lifecycleCommandGroup, error) {
	path := lifecycleConfigurationPath(homeDir, provider)
	content, err := os.ReadFile(path)
	if err != nil {
		slog.WarnContext(ctx, "read lifecycle registration failed", "provider", provider, "path", path, "err", err)
		return nil, fmt.Errorf("read %s lifecycle registration: %w", provider, err)
	}
	if provider == ProviderCodex {
		var configuration codexHookConfiguration
		if err := toml.Unmarshal(content, &configuration); err != nil {
			slog.WarnContext(ctx, "parse lifecycle registration failed", "provider", provider, "path", path, "err", err)
			return nil, fmt.Errorf("parse %s lifecycle registration: %w", provider, err)
		}
		return configuration.Hooks.SessionStart, nil
	}
	var configuration jsonHookConfiguration
	if err := json.Unmarshal(content, &configuration); err != nil {
		slog.WarnContext(ctx, "parse lifecycle registration failed", "provider", provider, "path", path, "err", err)
		return nil, fmt.Errorf("parse %s lifecycle registration: %w", provider, err)
	}
	event, ok := configuration.Hooks[eventName]
	if !ok {
		return nil, fmt.Errorf("%w: %s: missing lifecycle event %s", errManagedLifecycleEventMissing, provider, eventName)
	}
	var groups []lifecycleCommandGroup
	if err := json.Unmarshal(event, &groups); err != nil {
		slog.WarnContext(ctx, "parse lifecycle event failed", "provider", provider, "event", eventName, "err", err)
		return nil, fmt.Errorf("parse %s lifecycle event %s: %w", provider, eventName, err)
	}
	return groups, nil
}

func validateInstalledExecutable(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		slog.WarnContext(ctx, "installed lifecycle binary stat failed", "path", path, "err", err)
		return fmt.Errorf("agent-gate binary not found at %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("agent-gate binary path is a directory: %s", path)
	}
	if info.Mode().Perm()&executableModeMask == 0 {
		return fmt.Errorf("agent-gate binary is not executable: %s", path)
	}
	return nil
}

func lifecycleConfigurationPath(homeDir string, provider Provider) string {
	switch provider {
	case ProviderClaude:
		return filepath.Join(homeDir, ".claude", "settings.json")
	case ProviderCodex:
		return filepath.Join(homeDir, ".codex", "config.toml")
	case ProviderCursor:
		return filepath.Join(homeDir, ".cursor", "hooks.json")
	case ProviderGemini:
		return filepath.Join(homeDir, ".gemini", "settings.json")
	case ProviderCopilot:
		return filepath.Join(homeDir, ".copilot", "hooks", "agent-gate.json")
	default:
		return ""
	}
}

func validateLifecycleGroups(
	provider Provider,
	groups []lifecycleCommandGroup,
	expectedMatchers []string,
	executable string,
	expectedArguments []string,
) (string, []string, error) {
	expected := make(map[string]bool, len(expectedMatchers))
	for _, matcher := range expectedMatchers {
		expected[matcher] = true
	}
	found := make(map[string]string, len(expectedMatchers))
	managedMarker := " managed-hook " + string(provider)
	installedExecutable := ""
	for _, group := range groups {
		var err error
		installedExecutable, err = validateLifecycleGroup(
			provider,
			group,
			expected,
			managedMarker,
			expectedArguments,
			executable,
			installedExecutable,
			found,
		)
		if err != nil {
			return "", nil, err
		}
	}
	for _, matcher := range expectedMatchers {
		if _, ok := found[matcher]; !ok {
			return "", nil, fmt.Errorf("%s: missing lifecycle matcher %q", provider, matcher)
		}
	}
	return installedExecutable, append([]string(nil), expectedArguments...), nil
}

func validateLifecycleGroup(
	provider Provider,
	group lifecycleCommandGroup,
	expected map[string]bool,
	managedMarker string,
	expectedArguments []string,
	executable string,
	installedExecutable string,
	found map[string]string,
) (string, error) {
	for _, hook := range groupCommands(group) {
		commandExecutable, managed, err := parseManagedLifecycleCommand(
			hook.Command,
			managedMarker,
			expectedArguments,
		)
		if err != nil {
			slog.Warn("validate lifecycle command failed", "provider", provider, "err", err)
			return "", fmt.Errorf("%s: %w", provider, err)
		}
		if !managed {
			continue
		}
		if hook.Type != "command" {
			return "", fmt.Errorf(
				"%s: lifecycle hook type %q is not command",
				provider,
				hook.Type,
			)
		}
		if !expected[group.Matcher] {
			return "", fmt.Errorf("%s: unrecognized lifecycle matcher %q", provider, group.Matcher)
		}
		if executable != "" && commandExecutable != executable {
			return "", fmt.Errorf("%s: conflicting lifecycle commands", provider)
		}
		if installedExecutable != "" && commandExecutable != installedExecutable {
			return "", fmt.Errorf("%s: conflicting lifecycle commands", provider)
		}
		if existing, ok := found[group.Matcher]; ok {
			if existing != hook.Command {
				return "", fmt.Errorf("%s: conflicting lifecycle commands for matcher %q", provider, group.Matcher)
			}
			return "", fmt.Errorf("%s: duplicate lifecycle matcher %q", provider, group.Matcher)
		}
		installedExecutable = commandExecutable
		found[group.Matcher] = hook.Command
	}
	return installedExecutable, nil
}

func parseManagedLifecycleCommand(
	command string,
	managedMarker string,
	expectedArguments []string,
) (string, bool, error) {
	markerIndex := strings.LastIndex(command, managedMarker)
	if markerIndex <= 0 {
		return "", false, nil
	}
	commandArguments := strings.TrimPrefix(command[markerIndex:], " ")
	if commandArguments != strings.Join(expectedArguments, " ") {
		return "", true, errors.New("conflicting lifecycle commands")
	}
	executable, err := parseShellCommandArgument(strings.TrimSpace(command[:markerIndex]))
	if err != nil {
		wrappedErr := fmt.Errorf("parse lifecycle executable: %w", err)
		slog.Warn("lifecycle executable parse failed", "err", wrappedErr)
		return "", true, wrappedErr
	}
	return executable, true, nil
}

func parseShellCommandArgument(value string) (string, error) {
	if value == "" || value[0] != '\'' {
		return value, nil
	}
	var output strings.Builder
	index := 1
	for {
		closingOffset := strings.IndexByte(value[index:], '\'')
		if closingOffset < 0 {
			return "", errors.New("unterminated quoted executable")
		}
		closingIndex := index + closingOffset
		output.WriteString(value[index:closingIndex])
		index = closingIndex + 1
		if index == len(value) {
			return output.String(), nil
		}
		if !strings.HasPrefix(value[index:], "\"'\"'") {
			return "", errors.New("invalid quoted executable")
		}
		output.WriteByte('\'')
		index += len("\"'\"'")
	}
}

func groupCommands(group lifecycleCommandGroup) []lifecycleCommandHook {
	commands := make([]lifecycleCommandHook, 0, len(group.Hooks)+1)
	if group.Command != "" {
		commands = append(commands, lifecycleCommandHook{Type: "command", Command: group.Command})
	}
	for _, hook := range group.Hooks {
		if hook.Command != "" {
			commands = append(commands, hook)
		}
	}
	return commands
}
