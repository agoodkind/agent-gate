// Package installer owns agent-gate hook and user service installation.
package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentgate "goodkind.io/agent-gate"
)

const (
	agentGateBinaryName     = "agent-gate"
	agentGatePlaceholder    = "__AGENT_GATE_BIN__"
	codexManagedBlockStart  = "# BEGIN agent-gate managed hooks"
	codexManagedBlockEnd    = "# END agent-gate managed hooks"
	launchdLabel            = "io.goodkind.agent-gate"
	launchdTemplateName     = "io.goodkind.agent-gate.plist.in"
	systemdServiceName      = "agent-gate.service"
	systemdServiceTemplate  = "agent-gate.service.in"
	serviceWaitAttempts     = 50
	serviceWaitSleep        = 200 * time.Millisecond
	privateFileMode         = 0o600
	privateDirMode          = 0o700
	userConfigDirMode       = 0o755
	executableModeMask      = 0o111
	defaultCursorConfig     = `{"version":1}`
	defaultGenericJSONHooks = `{}`
)

type servicePlatform string

const (
	servicePlatformDarwin servicePlatform = "darwin"
	servicePlatformLinux  servicePlatform = "linux"
)

// CommandRunner runs external commands for service management.
type CommandRunner interface {
	Run(name string, args ...string) error
	Output(name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands through os/exec.
type ExecRunner struct{}

// Run executes a command and returns an error containing combined output.
func (ExecRunner) Run(name string, args ...string) error {
	command := exec.CommandContext(context.Background(), name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		trimmedOutput := strings.TrimSpace(string(output))
		if trimmedOutput == "" {
			wrappedErr := fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
			slog.Warn("install command failed", "command", name, "args", args, "err", wrappedErr)
			return wrappedErr
		}
		wrappedErr := fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmedOutput)
		slog.Warn("install command failed", "command", name, "args", args, "output", trimmedOutput, "err", wrappedErr)
		return wrappedErr
	}
	return nil
}

// Output executes a command and returns stdout plus stderr.
func (ExecRunner) Output(name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(context.Background(), name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		wrappedErr := fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		slog.Debug("install command output failed", "command", name, "args", args, "err", wrappedErr)
		return output, wrappedErr
	}
	return output, nil
}

// HooksOptions configures hook file installation.
type HooksOptions struct {
	BinPath        string
	TemplatesDir   string
	HomeDir        string
	Stdout         io.Writer
	InstallClaude  bool
	InstallCodex   bool
	InstallCursor  bool
	InstallGemini  bool
	InstallCopilot bool
}

// HookInstallationPlan contains fully prepared hook file updates.
type HookInstallationPlan struct {
	writes []hookInstallationWrite
	writer io.Writer
}

type hookInstallationWrite struct {
	targetPath string
	provider   string
	content    []byte
}

// DefaultHooksOptions returns hook options matching install.sh defaults.
func DefaultHooksOptions(binPath string) HooksOptions {
	return HooksOptions{
		BinPath:        binPath,
		TemplatesDir:   "",
		HomeDir:        "",
		Stdout:         nil,
		InstallClaude:  true,
		InstallCodex:   true,
		InstallCursor:  true,
		InstallGemini:  true,
		InstallCopilot: true,
	}
}

// ServiceOptions configures daemon service installation.
type ServiceOptions struct {
	BinPath             string
	ServiceTemplatesDir string
	HomeDir             string
	ConfigHome          string
	StateHome           string
	Stdout              io.Writer
	Runner              CommandRunner
	Ready               func() error
}

// ValidateServiceOptions verifies service inputs without writing service files.
func ValidateServiceOptions(options ServiceOptions) error {
	canonicalBinPath, err := CanonicalExecutablePath(options.BinPath)
	if err != nil {
		return err
	}
	options.BinPath = canonicalBinPath
	if err := ValidateExecutable(options.BinPath); err != nil {
		return err
	}
	homeDir, err := resolvedHomeDir(options.HomeDir)
	if err != nil {
		return err
	}
	switch servicePlatform(runtime.GOOS) {
	case servicePlatformDarwin:
		stateDir := defaultStateDir(homeDir, options.StateHome)
		_, err = renderServiceTemplate(
			options.ServiceTemplatesDir,
			"macos",
			launchdTemplateName,
			map[string]string{
				"@@BIN_PATH@@": options.BinPath,
				"@@HOME@@":     homeDir,
				"@@LOG_PATH@@": filepath.Join(stateDir, agentGateBinaryName+".log"),
			},
		)
	case servicePlatformLinux:
		_, err = renderServiceTemplate(
			options.ServiceTemplatesDir,
			"systemd",
			systemdServiceTemplate,
			map[string]string{"@@BIN_PATH@@": options.BinPath},
		)
	default:
		return fmt.Errorf("unsupported OS for service install: %s", runtime.GOOS)
	}
	return err
}

// PrepareHookInstallation reads and validates every selected hook update without writing files.
func PrepareHookInstallation(options HooksOptions) (*HookInstallationPlan, error) {
	canonicalBinPath, err := CanonicalExecutablePath(options.BinPath)
	if err != nil {
		return nil, err
	}
	if err := ValidateExecutable(canonicalBinPath); err != nil {
		return nil, err
	}
	homeDir, err := resolvedHomeDir(options.HomeDir)
	if err != nil {
		return nil, err
	}
	writer := options.Stdout
	if writer == nil {
		writer = io.Discard
	}
	plan := &HookInstallationPlan{writes: nil, writer: writer}
	if options.InstallClaude {
		targetPath := filepath.Join(homeDir, ".claude", "settings.json")
		content, prepareErr := prepareJSONHooks(options.TemplatesDir, "claude", canonicalBinPath, targetPath, false)
		if prepareErr != nil {
			return nil, prepareErr
		}
		plan.addWrite(targetPath, "claude", content)
	}
	if options.InstallCodex {
		targetPath := filepath.Join(homeDir, ".codex", "config.toml")
		content, prepareErr := prepareCodexHooks(options.TemplatesDir, canonicalBinPath, targetPath)
		if prepareErr != nil {
			return nil, prepareErr
		}
		plan.addWrite(targetPath, "codex", content)
	}
	if options.InstallCursor {
		targetPath := filepath.Join(homeDir, ".cursor", "hooks.json")
		content, prepareErr := prepareJSONHooks(options.TemplatesDir, "cursor", canonicalBinPath, targetPath, true)
		if prepareErr != nil {
			return nil, prepareErr
		}
		plan.addWrite(targetPath, "cursor", content)
	}
	if options.InstallGemini {
		targetPath := filepath.Join(homeDir, ".gemini", "settings.json")
		content, prepareErr := prepareJSONHooks(options.TemplatesDir, "gemini", canonicalBinPath, targetPath, false)
		if prepareErr != nil {
			return nil, prepareErr
		}
		plan.addWrite(targetPath, "gemini", content)
	}
	if options.InstallCopilot {
		targetPath := filepath.Join(homeDir, ".copilot", "hooks", "agent-gate.json")
		content, prepareErr := prepareReplacementJSONHooks(options.TemplatesDir, "copilot", canonicalBinPath, targetPath)
		if prepareErr != nil {
			return nil, prepareErr
		}
		plan.addWrite(targetPath, "copilot", content)
	}
	return plan, nil
}

// ApplyHookInstallation writes bytes retained by a prepared hook installation plan.
func ApplyHookInstallation(plan *HookInstallationPlan) error {
	if plan == nil {
		return errors.New("hook installation plan is required")
	}
	for _, write := range plan.writes {
		if err := writeFileAtomic(write.targetPath, write.content); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(
			plan.writer,
			"agent-gate install: updated %s (%s hooks)\n",
			write.targetPath,
			write.provider,
		)
	}
	return nil
}

func (plan *HookInstallationPlan) addWrite(targetPath string, provider string, content []byte) {
	plan.writes = append(plan.writes, hookInstallationWrite{
		targetPath: targetPath,
		provider:   provider,
		content:    content,
	})
}

// InstallService writes and starts the per-user daemon service.
func InstallService(options ServiceOptions) error {
	canonicalBinPath, err := CanonicalExecutablePath(options.BinPath)
	if err != nil {
		return err
	}
	options.BinPath = canonicalBinPath
	if err := ValidateServiceOptions(options); err != nil {
		return err
	}
	runner := options.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	writer := options.Stdout
	if writer == nil {
		writer = io.Discard
	}
	homeDir, err := resolvedHomeDir(options.HomeDir)
	if err != nil {
		return err
	}
	currentPlatform := servicePlatform(runtime.GOOS)
	switch currentPlatform {
	case servicePlatformDarwin:
		return installLaunchdService(options, homeDir, writer, runner)
	case servicePlatformLinux:
		return installSystemdService(options, homeDir, writer, runner)
	default:
		return fmt.Errorf("unsupported OS for service install: %s", runtime.GOOS)
	}
}

func installLaunchdService(options ServiceOptions, homeDir string, writer io.Writer, runner CommandRunner) error {
	stateDir := defaultStateDir(homeDir, options.StateHome)
	targetPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
	logPath := filepath.Join(stateDir, agentGateBinaryName+".log")
	replacements := map[string]string{
		"@@BIN_PATH@@": options.BinPath,
		"@@HOME@@":     homeDir,
		"@@LOG_PATH@@": logPath,
	}
	renderedTemplate, err := renderServiceTemplate(options.ServiceTemplatesDir, "macos", launchdTemplateName, replacements)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), userConfigDirMode); err != nil {
		return logInstallError("create launchd dir failed", fmt.Errorf("create launchd dir: %w", err), slog.String("path", filepath.Dir(targetPath)))
	}
	if err := os.MkdirAll(stateDir, userConfigDirMode); err != nil {
		return logInstallError("create state dir failed", fmt.Errorf("create state dir: %w", err), slog.String("path", stateDir))
	}
	if err := writeFileAtomic(targetPath, []byte(renderedTemplate)); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	serviceTarget := domain + "/" + launchdLabel
	_, _ = runner.Output("launchctl", "bootout", serviceTarget)
	waitForLaunchdExit(runner, serviceTarget)
	stopUnmanagedDaemons(runner, options.BinPath)
	_, _ = runner.Output("launchctl", "enable", serviceTarget)
	if err := runner.Run("launchctl", "bootstrap", domain, targetPath); err != nil {
		return logInstallError("launchctl bootstrap failed", fmt.Errorf("launchctl bootstrap failed: %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	_, _ = fmt.Fprintf(writer, "agent-gate install: installed launchd service %s\n", targetPath)
	return waitForServiceReadiness(options.Ready)
}

func installSystemdService(options ServiceOptions, homeDir string, writer io.Writer, runner CommandRunner) error {
	targetPath := filepath.Join(defaultConfigHome(homeDir, options.ConfigHome), "systemd", "user", systemdServiceName)
	replacements := map[string]string{
		"@@BIN_PATH@@": options.BinPath,
	}
	renderedTemplate, err := renderServiceTemplate(options.ServiceTemplatesDir, "systemd", systemdServiceTemplate, replacements)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), userConfigDirMode); err != nil {
		return logInstallError("create systemd dir failed", fmt.Errorf("create systemd dir: %w", err), slog.String("path", filepath.Dir(targetPath)))
	}
	if err := writeFileAtomic(targetPath, []byte(renderedTemplate)); err != nil {
		return err
	}
	if err := runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return logInstallError("systemctl daemon-reload failed", fmt.Errorf("systemctl --user daemon-reload failed: %w", err))
	}
	_ = runner.Run("systemctl", "--user", "stop", systemdServiceName)
	stopUnmanagedDaemons(runner, options.BinPath)
	if err := runner.Run("systemctl", "--user", "enable", "--now", systemdServiceName); err != nil {
		return logInstallError("systemctl enable failed", fmt.Errorf("systemctl --user enable --now failed: %w", err))
	}
	_, _ = fmt.Fprintf(writer, "agent-gate install: installed systemd user service %s\n", targetPath)
	return waitForServiceReadiness(options.Ready)
}

func waitForServiceReadiness(ready func() error) error {
	if ready == nil {
		return nil
	}
	if err := ready(); err != nil {
		return logInstallError("service readiness failed", fmt.Errorf("readiness: %w", err))
	}
	return nil
}

// ValidateExecutable verifies that binPath identifies an executable file.
func ValidateExecutable(binPath string) error {
	if binPath == "" {
		return errors.New("--bin-path is required")
	}
	info, err := os.Stat(binPath)
	if err != nil {
		return logInstallError("agent-gate binary stat failed", fmt.Errorf("agent-gate binary not found at %s: %w", binPath, err), slog.String("path", binPath))
	}
	if info.IsDir() {
		return fmt.Errorf("agent-gate binary path is a directory: %s", binPath)
	}
	if info.Mode().Perm()&executableModeMask == 0 {
		return fmt.Errorf("agent-gate binary is not executable: %s", binPath)
	}
	return nil
}

func prepareJSONHooks(
	templatesDir string,
	tool string,
	binPath string,
	targetPath string,
	cursor bool,
) ([]byte, error) {
	renderedHooks, err := renderJSONHooks(templatesDir, tool, binPath)
	if err != nil {
		return nil, err
	}
	configJSON := []byte(defaultGenericJSONHooks)
	if cursor {
		configJSON = []byte(defaultCursorConfig)
	}
	if existingJSON, readErr := os.ReadFile(targetPath); readErr == nil {
		configJSON = existingJSON
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, logInstallError("read JSON hook config failed", fmt.Errorf("read %s: %w", targetPath, readErr), slog.String("path", targetPath))
	}
	var target map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &target); err != nil {
		return nil, logInstallError("parse JSON hook config failed", fmt.Errorf("parse %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	if target == nil {
		target = make(map[string]json.RawMessage)
	}
	if cursor {
		if _, ok := target["version"]; !ok {
			target["version"] = json.RawMessage("1")
		}
	}
	mergedHooks, err := mergeJSONHooks(target["hooks"], renderedHooks)
	if err != nil {
		return nil, logInstallError("merge JSON hooks failed", fmt.Errorf("merge %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	target["hooks"] = mergedHooks
	return marshalJSONHookConfig(targetPath, target)
}

func prepareReplacementJSONHooks(
	templatesDir string,
	tool string,
	binPath string,
	targetPath string,
) ([]byte, error) {
	renderedHooks, err := renderJSONHooks(templatesDir, tool, binPath)
	if err != nil {
		return nil, err
	}
	if _, readErr := os.ReadFile(targetPath); readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, logInstallError("read JSON hook config failed", fmt.Errorf("read %s: %w", targetPath, readErr), slog.String("path", targetPath))
	}
	target := map[string]json.RawMessage{"hooks": renderedHooks}
	return marshalJSONHookConfig(targetPath, target)
}

func marshalJSONHookConfig(targetPath string, target map[string]json.RawMessage) ([]byte, error) {
	output, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return nil, logInstallError("render JSON hook config failed", fmt.Errorf("render %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	output = append(output, '\n')
	return output, nil
}

func mergeJSONHooks(existingJSON json.RawMessage, managedJSON json.RawMessage) (json.RawMessage, error) {
	existingHooks := make(map[string][]json.RawMessage)
	if len(existingJSON) > 0 && string(existingJSON) != "null" {
		if err := json.Unmarshal(existingJSON, &existingHooks); err != nil {
			return nil, logInstallError("parse existing JSON hooks failed", fmt.Errorf("parse existing hooks: %w", err))
		}
	}
	managedHooks := make(map[string][]json.RawMessage)
	if err := json.Unmarshal(managedJSON, &managedHooks); err != nil {
		return nil, logInstallError("parse managed JSON hooks failed", fmt.Errorf("parse managed hooks: %w", err))
	}

	for eventName, groups := range existingHooks {
		if len(groups) == 0 {
			continue
		}
		preservedGroups := make([]json.RawMessage, 0, len(groups))
		for _, group := range groups {
			preservedGroup, preserve, err := removeAgentGateCommands(group)
			if err != nil {
				return nil, logInstallError("clean JSON hook event failed", fmt.Errorf("clean event %s: %w", eventName, err), slog.String("event", eventName))
			}
			if preserve {
				preservedGroups = append(preservedGroups, preservedGroup)
			}
		}
		if len(preservedGroups) == 0 {
			delete(existingHooks, eventName)
			continue
		}
		existingHooks[eventName] = preservedGroups
	}

	for eventName, managedGroups := range managedHooks {
		existingHooks[eventName] = append(existingHooks[eventName], managedGroups...)
	}
	output, err := json.Marshal(existingHooks)
	if err != nil {
		return nil, logInstallError("marshal merged JSON hooks failed", fmt.Errorf("marshal merged hooks: %w", err))
	}
	return json.RawMessage(output), nil
}

func removeAgentGateCommands(group json.RawMessage) (json.RawMessage, bool, error) {
	var groupObject map[string]json.RawMessage
	if err := json.Unmarshal(group, &groupObject); err != nil {
		return nil, false, logInstallError("parse JSON hook group failed", fmt.Errorf("parse hook group: %w", err))
	}
	if isAgentGateCommandObject(groupObject) {
		return nil, false, nil
	}
	nestedHooksJSON, hasNestedHooks := groupObject["hooks"]
	if !hasNestedHooks {
		return group, true, nil
	}
	var nestedHooks []json.RawMessage
	if err := json.Unmarshal(nestedHooksJSON, &nestedHooks); err != nil {
		return nil, false, logInstallError("parse JSON command group failed", fmt.Errorf("parse command group: %w", err))
	}
	if len(nestedHooks) == 0 {
		return group, true, nil
	}
	preservedHooks := make([]json.RawMessage, 0, len(nestedHooks))
	for _, hook := range nestedHooks {
		var hookObject map[string]json.RawMessage
		if err := json.Unmarshal(hook, &hookObject); err != nil {
			preservedHooks = append(preservedHooks, hook)
			continue
		}
		if !isAgentGateCommandObject(hookObject) {
			preservedHooks = append(preservedHooks, hook)
		}
	}
	if len(preservedHooks) == 0 {
		return nil, false, nil
	}
	preservedHooksJSON, err := json.Marshal(preservedHooks)
	if err != nil {
		return nil, false, logInstallError("marshal JSON command group failed", fmt.Errorf("marshal command group: %w", err))
	}
	groupObject["hooks"] = preservedHooksJSON
	preservedGroup, err := json.Marshal(groupObject)
	if err != nil {
		return nil, false, logInstallError("marshal JSON hook group failed", fmt.Errorf("marshal hook group: %w", err))
	}
	return json.RawMessage(preservedGroup), true, nil
}

func isAgentGateCommandObject(commandObject map[string]json.RawMessage) bool {
	commandJSON, ok := commandObject["command"]
	if !ok {
		return false
	}
	var command string
	if err := json.Unmarshal(commandJSON, &command); err != nil {
		return false
	}
	trimmedCommand := strings.TrimSpace(command)
	if trimmedCommand == agentGateBinaryName {
		return true
	}
	executable := firstCommandToken(trimmedCommand)
	return filepath.Base(executable) == agentGateBinaryName
}

func firstCommandToken(command string) string {
	if command == "" {
		return ""
	}
	if command[0] != '\'' && command[0] != '"' {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return ""
		}
		return fields[0]
	}
	quote := command[0]
	for i := 1; i < len(command); i++ {
		if command[i] == quote {
			return command[1:i]
		}
	}
	return command
}

func renderJSONHooks(templatesDir string, tool string, binPath string) (json.RawMessage, error) {
	content, err := readHookTemplate(templatesDir, tool, "json")
	if err != nil {
		return nil, err
	}
	if !json.Valid(content) {
		return nil, fmt.Errorf("parse %s hook template: invalid JSON", tool)
	}
	renderedHooks, err := marshalJSONCommandPlaceholders(json.RawMessage(content), binPath)
	if err != nil {
		return nil, logInstallError("parse JSON hook template failed", fmt.Errorf("parse %s hook template: %w", tool, err), slog.String("tool", tool))
	}
	output, err := json.MarshalIndent(renderedHooks, "", "  ")
	if err != nil {
		return nil, logInstallError("render JSON hook template failed", fmt.Errorf("render %s hook template: %w", tool, err), slog.String("tool", tool))
	}
	return json.RawMessage(output), nil
}

func marshalJSONCommandPlaceholders(value json.RawMessage, binPath string) (json.RawMessage, error) {
	trimmedValue := strings.TrimSpace(string(value))
	if trimmedValue == "" {
		return value, nil
	}
	switch trimmedValue[0] {
	case '{':
		objectValue := make(map[string]json.RawMessage)
		if err := json.Unmarshal(value, &objectValue); err != nil {
			return nil, logInstallError("unmarshal JSON hook object failed", fmt.Errorf("unmarshal object: %w", err))
		}
		for key, childValue := range objectValue {
			if key == "command" {
				replacedValue, replaced := replaceJSONCommand(childValue, binPath)
				if replaced {
					objectValue[key] = replacedValue
					continue
				}
			}
			replacedChild, err := marshalJSONCommandPlaceholders(childValue, binPath)
			if err != nil {
				return nil, err
			}
			objectValue[key] = replacedChild
		}
		output, err := json.Marshal(objectValue)
		if err != nil {
			return nil, logInstallError("marshal JSON hook object failed", fmt.Errorf("marshal object: %w", err))
		}
		return json.RawMessage(output), nil
	case '[':
		var arrayValue []json.RawMessage
		if err := json.Unmarshal(value, &arrayValue); err != nil {
			return nil, logInstallError("unmarshal JSON hook array failed", fmt.Errorf("unmarshal array: %w", err))
		}
		for i, childValue := range arrayValue {
			replacedChild, err := marshalJSONCommandPlaceholders(childValue, binPath)
			if err != nil {
				return nil, err
			}
			arrayValue[i] = replacedChild
		}
		output, err := json.Marshal(arrayValue)
		if err != nil {
			return nil, logInstallError("marshal JSON hook array failed", fmt.Errorf("marshal array: %w", err))
		}
		return json.RawMessage(output), nil
	default:
		return value, nil
	}
}

func replaceJSONCommand(value json.RawMessage, binPath string) (json.RawMessage, bool) {
	var command string
	if err := json.Unmarshal(value, &command); err != nil {
		return value, false
	}
	replacedCommand := strings.ReplaceAll(command, agentGatePlaceholder, binPath)
	output, err := json.Marshal(replacedCommand)
	if err != nil {
		return value, false
	}
	return json.RawMessage(output), true
}

func prepareCodexHooks(templatesDir string, binPath string, targetPath string) ([]byte, error) {
	templateContent, err := readHookTemplate(templatesDir, "codex", "toml")
	if err != nil {
		return nil, err
	}
	renderedTemplate := strings.ReplaceAll(string(templateContent), agentGatePlaceholder, binPath)
	existingContent := ""
	if content, readErr := os.ReadFile(targetPath); readErr == nil {
		existingContent = string(content)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, logInstallError("read Codex config failed", fmt.Errorf("read %s: %w", targetPath, readErr), slog.String("path", targetPath))
	}
	if err := validateCodexManagedBlock(existingContent); err != nil {
		return nil, logInstallError("validate Codex managed hooks failed", fmt.Errorf("validate %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	contentWithoutBlock := removeCodexManagedBlock(existingContent)
	contentWithFeature := ensureCodexHooksFeature(contentWithoutBlock)
	output := strings.TrimRight(contentWithFeature, "\n")
	if output != "" {
		output += "\n"
	}
	output += "\n" + codexManagedBlockStart + "\n"
	output += strings.TrimRight(renderedTemplate, "\n") + "\n"
	output += codexManagedBlockEnd + "\n"
	return []byte(output), nil
}

func validateCodexManagedBlock(content string) error {
	inManagedBlock := false
	sawManagedBlock := false
	for _, line := range splitLines(content) {
		switch line {
		case codexManagedBlockStart:
			if inManagedBlock || sawManagedBlock {
				return errors.New("codex managed hooks contain multiple start markers")
			}
			inManagedBlock = true
			sawManagedBlock = true
		case codexManagedBlockEnd:
			if !inManagedBlock {
				return errors.New("codex managed hooks contain an unmatched end marker")
			}
			inManagedBlock = false
		}
	}
	if inManagedBlock {
		return errors.New("codex managed hooks contain an unmatched start marker")
	}
	return nil
}

func removeCodexManagedBlock(content string) string {
	lines := splitLines(content)
	var output []string
	skipping := false
	for _, line := range lines {
		switch line {
		case codexManagedBlockStart:
			skipping = true
			continue
		case codexManagedBlockEnd:
			skipping = false
			continue
		}
		if !skipping {
			output = append(output, line)
		}
	}
	return joinLines(output)
}

func ensureCodexHooksFeature(content string) string {
	lines := splitLines(content)
	var output []string
	inFeatures := false
	sawFeatures := false
	sawHooks := false
	emitMissingHooks := func() {
		if inFeatures && !sawHooks {
			output = append(output, "hooks = true")
			sawHooks = true
		}
	}
	for _, line := range lines {
		if isTOMLHeader(line) {
			emitMissingHooks()
			inFeatures = isFeaturesHeader(line)
			if inFeatures {
				sawFeatures = true
				sawHooks = false
			}
			output = append(output, line)
			continue
		}
		if inFeatures && isHooksAssignment(line) {
			output = append(output, "hooks = true")
			sawHooks = true
			continue
		}
		output = append(output, line)
	}
	emitMissingHooks()
	if !sawFeatures {
		if len(output) > 0 && output[len(output)-1] != "" {
			output = append(output, "")
		}
		output = append(output, "[features]", "hooks = true")
	}
	return joinLines(output)
}

func isTOMLHeader(line string) bool {
	trimmedLine := strings.TrimSpace(stripTOMLComment(line))
	return strings.HasPrefix(trimmedLine, "[") && strings.HasSuffix(trimmedLine, "]")
}

func isFeaturesHeader(line string) bool {
	trimmedLine := strings.TrimSpace(stripTOMLComment(line))
	return trimmedLine == "[features]"
}

func isHooksAssignment(line string) bool {
	trimmedLine := strings.TrimSpace(stripTOMLComment(line))
	keyName, _, found := strings.Cut(trimmedLine, "=")
	if !found {
		return false
	}
	return strings.TrimSpace(keyName) == "hooks"
}

func stripTOMLComment(line string) string {
	beforeComment, _, found := strings.Cut(line, "#")
	if found {
		return beforeComment
	}
	return line
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	trimmedContent := strings.TrimRight(content, "\n")
	if trimmedContent == "" {
		return []string{""}
	}
	return strings.Split(trimmedContent, "\n")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func readHookTemplate(templatesDir string, tool string, extension string) ([]byte, error) {
	name := tool + "." + extension
	if templatesDir != "" {
		content, err := os.ReadFile(filepath.Join(templatesDir, name))
		if err == nil {
			return content, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, logInstallError("read hook template failed", fmt.Errorf("read hook template %s: %w", name, err), slog.String("name", name))
		}
	}
	assetPath := filepath.Join("hooks", name)
	content, err := fs.ReadFile(agentgate.InstallAssets, filepath.ToSlash(assetPath))
	if err != nil {
		return nil, logInstallError("read embedded hook template failed", fmt.Errorf("read embedded hook template %s: %w", name, err), slog.String("name", name))
	}
	return content, nil
}

func renderServiceTemplate(templatesDir string, platformDir string, name string, replacements map[string]string) (string, error) {
	content, err := readServiceTemplate(templatesDir, platformDir, name)
	if err != nil {
		return "", err
	}
	renderedTemplate := string(content)
	for placeholder, value := range replacements {
		renderedTemplate = strings.ReplaceAll(renderedTemplate, placeholder, value)
	}
	return renderedTemplate, nil
}

func readServiceTemplate(templatesDir string, platformDir string, name string) ([]byte, error) {
	if templatesDir != "" {
		content, err := os.ReadFile(filepath.Join(templatesDir, platformDir, name))
		if err == nil {
			return content, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, logInstallError("read service template failed", fmt.Errorf("read service template %s/%s: %w", platformDir, name, err), slog.String("platform", platformDir), slog.String("name", name))
		}
	}
	assetPath := filepath.Join("packaging", platformDir, name)
	content, err := fs.ReadFile(agentgate.InstallAssets, filepath.ToSlash(assetPath))
	if err != nil {
		return nil, logInstallError("read embedded service template failed", fmt.Errorf("read embedded service template %s/%s: %w", platformDir, name, err), slog.String("platform", platformDir), slog.String("name", name))
	}
	return content, nil
}

func writeFileAtomic(targetPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), privateDirMode); err != nil {
		return logInstallError("create install target parent failed", fmt.Errorf("create parent dir for %s: %w", targetPath, err), slog.String("path", filepath.Dir(targetPath)))
	}
	tempFile, err := os.CreateTemp(filepath.Dir(targetPath), "."+filepath.Base(targetPath)+".*.tmp")
	if err != nil {
		return logInstallError("create install temp file failed", fmt.Errorf("create temp file for %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	tempPath := tempFile.Name()
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := tempFile.Chmod(privateFileMode); err != nil {
		_ = tempFile.Close()
		return logInstallError("chmod install temp file failed", fmt.Errorf("chmod temp file for %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	if _, err := tempFile.Write(content); err != nil {
		_ = tempFile.Close()
		return logInstallError("write install temp file failed", fmt.Errorf("write temp file for %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	if err := tempFile.Close(); err != nil {
		return logInstallError("close install temp file failed", fmt.Errorf("close temp file for %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return logInstallError("replace install target failed", fmt.Errorf("replace %s: %w", targetPath, err), slog.String("path", targetPath))
	}
	cleanupTemp = false
	slog.Debug("install wrote file", "path", targetPath)
	return nil
}

func defaultConfigHome(homeDir string, override string) string {
	if override != "" {
		return override
	}
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return configHome
	}
	return filepath.Join(homeDir, ".config")
}

func defaultStateDir(homeDir string, override string) string {
	stateHome := override
	if stateHome == "" {
		stateHome = os.Getenv("XDG_STATE_HOME")
	}
	if stateHome == "" {
		stateHome = filepath.Join(homeDir, ".local", "state")
	}
	return filepath.Join(stateHome, agentGateBinaryName)
}

func resolvedHomeDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		return homeDir, nil
	}
	if homeDir = os.Getenv("HOME"); homeDir != "" {
		return homeDir, nil
	}
	return "", errors.New("could not resolve home directory")
}

func waitForLaunchdExit(runner CommandRunner, serviceTarget string) {
	for range serviceWaitAttempts {
		if _, err := runner.Output("launchctl", "print", serviceTarget); err != nil {
			return
		}
		timer := time.NewTimer(serviceWaitSleep)
		select {
		case <-timer.C:
		case <-context.Background().Done():
		}
	}
}

func stopUnmanagedDaemons(runner CommandRunner, binPath string) {
	pattern := "^" + regexp.QuoteMeta(binPath) + " daemon$"
	output, err := runner.Output("pgrep", "-f", pattern)
	if err != nil {
		return
	}
	for pidText := range strings.FieldsSeq(string(output)) {
		pid, parseErr := strconv.Atoi(pidText)
		if parseErr != nil {
			continue
		}
		process, findErr := os.FindProcess(pid)
		if findErr != nil {
			continue
		}
		_ = process.Signal(syscall.SIGTERM)
	}
}

func logInstallError(message string, err error, attrs ...slog.Attr) error {
	attrs = append(attrs, slog.Any("err", err))
	slog.LogAttrs(context.Background(), slog.LevelWarn, message, attrs...)
	return err
}
