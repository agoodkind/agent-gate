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

const codexHookTrustGuidance = `agent-gate install: Codex hooks require review before they can run.
Codex Desktop:
  1. Open Settings > Hooks.
  2. Click Reload hooks.
  3. Under From Config, open User config.
  4. Click Trust for each agent-gate hook marked New hook or Hook changed since last trusted.
Codex CLI:
  1. Restart Codex CLI.
  2. Run /hooks.
  3. Select each event containing an agent-gate hook and press Enter.
  4. Select each agent-gate hook and press t to trust it.
OpenAI docs: https://developers.openai.com/codex/hooks/
`

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
		slog.Warn(
			"install command failed",
			"command",
			name,
			"args",
			args,
			"output",
			trimmedOutput,
			"err",
			wrappedErr,
		)

		return wrappedErr
	}

	return nil
}

// Output executes a command and returns stdout plus stderr.
func (ExecRunner) Output(name string, args ...string) ([]byte, error) {
	return ExecRunner{}.OutputContext(context.Background(), name, args...)
}

// OutputContext executes a command until it completes or its context ends.
func (ExecRunner) OutputContext(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		wrappedErr := fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		slog.DebugContext(
			ctx,
			"install command output failed",
			"command",
			name,
			"args",
			args,
			"err",
			wrappedErr,
		)

		return output, wrappedErr
	}

	return output, nil
}

// HooksOptions configures hook file installation.
type HooksOptions struct {
	BinPath      string
	TemplatesDir string
	HomeDir      string
	Stdout       io.Writer
	Providers    []Provider
}

// HookInstallationPlan contains fully prepared hook file updates.
type HookInstallationPlan struct {
	writes []hookInstallationWrite
	writer io.Writer
}

type hookInstallationWrite struct {
	targetPath string
	provider   Provider
	content    []byte
}

// DefaultHooksOptions returns hook options matching install.sh defaults.
func DefaultHooksOptions(binPath string) HooksOptions {
	return HooksOptions{
		BinPath:      binPath,
		TemplatesDir: "",
		HomeDir:      "",
		Stdout:       nil,
		Providers:    nil,
	}
}

// PrepareHookInstallation reads and validates every selected hook update without writing files.
func PrepareHookInstallation(options HooksOptions) (*HookInstallationPlan, error) {
	providers, err := selectedProviders(options.Providers)
	if err != nil {
		return nil, err
	}

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

	for _, provider := range providers {
		if err := prepareProviderHooks(
			plan,
			options,
			provider,
			homeDir,
			canonicalBinPath,
		); err != nil {
			return nil, err
		}
	}

	return plan, nil
}

func prepareProviderHooks(
	plan *HookInstallationPlan,
	options HooksOptions,
	provider Provider,
	homeDir string,
	canonicalBinPath string,
) error {
	switch provider {
	case ProviderClaude:
		targetPath := filepath.Join(homeDir, ".claude", "settings.json")
		content, err := prepareJSONHooks(
			options.TemplatesDir,
			string(provider),
			canonicalBinPath,
			targetPath,
			false,
		)
		if err != nil {
			return err
		}

		plan.addWrite(targetPath, provider, content)
	case ProviderCodex:
		targetPath := filepath.Join(homeDir, ".codex", "config.toml")
		content, err := prepareCodexHooks(options.TemplatesDir, canonicalBinPath, targetPath)
		if err != nil {
			return err
		}

		plan.addWrite(targetPath, provider, content)
	case ProviderCursor:
		targetPath := filepath.Join(homeDir, ".cursor", "hooks.json")
		content, err := prepareJSONHooks(
			options.TemplatesDir,
			string(provider),
			canonicalBinPath,
			targetPath,
			true,
		)
		if err != nil {
			return err
		}

		plan.addWrite(targetPath, provider, content)
	case ProviderGemini:
		targetPath := filepath.Join(homeDir, ".gemini", "settings.json")
		content, err := prepareJSONHooks(
			options.TemplatesDir,
			string(provider),
			canonicalBinPath,
			targetPath,
			false,
		)
		if err != nil {
			return err
		}

		plan.addWrite(targetPath, provider, content)
	case ProviderCopilot:
		targetPath := filepath.Join(homeDir, ".copilot", "hooks", "agent-gate.json")
		content, err := prepareReplacementJSONHooks(
			options.TemplatesDir,
			string(provider),
			canonicalBinPath,
			targetPath,
		)
		if err != nil {
			return err
		}

		plan.addWrite(targetPath, provider, content)
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}

	return nil
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
		if write.provider == "codex" {
			_, _ = fmt.Fprint(plan.writer, codexHookTrustGuidance)
		}
	}

	return nil
}

// PrepareHookRemoval removes only Agent Gate registrations from selected providers.
func PrepareHookRemoval(options HooksOptions) (*HookInstallationPlan, error) {
	providers, err := selectedProviders(options.Providers)
	if err != nil {
		return nil, err
	}

	homeDir, err := resolvedHomeDir(options.HomeDir)
	if err != nil {
		return nil, err
	}

	plan := &HookInstallationPlan{writes: nil, writer: options.Stdout}
	if plan.writer == nil {
		plan.writer = io.Discard
	}

	for _, provider := range providers {
		path := lifecycleConfigurationPath(homeDir, provider)
		content, found, err := prepareHookRemoval(provider, path)
		if err != nil {
			return nil, err
		}

		if found {
			plan.addWrite(path, provider, content)
		}
	}

	return plan, nil
}

func prepareHookRemoval(provider Provider, targetPath string) ([]byte, bool, error) {
	content, err := os.ReadFile(targetPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		slog.Warn("read hook removal target failed", "path", targetPath, "err", err)
		return nil, false, fmt.Errorf("read %s: %w", targetPath, err)
	}

	if provider == ProviderCodex {
		if err := validateCodexManagedBlock(string(content)); err != nil {
			return nil, false, err
		}

		return []byte(removeCodexManagedBlock(string(content))), true, nil
	}

	var target map[string]json.RawMessage
	if err := json.Unmarshal(content, &target); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", targetPath, err)
	}

	if target == nil {
		return content, true, nil
	}

	cleaned, err := mergeJSONHooks(target["hooks"], json.RawMessage(`{}`))
	if err != nil {
		return nil, false, err
	}

	target["hooks"] = cleaned
	output, err := marshalJSONHookConfig(targetPath, target)

	return output, err == nil, err
}

func (plan *HookInstallationPlan) addWrite(targetPath string, provider Provider, content []byte) {
	plan.writes = append(
		plan.writes,
		hookInstallationWrite{
			targetPath: targetPath,
			provider:   provider,
			content:    content,
		},
	)
}

// ValidateExecutable verifies that binPath identifies an executable file.
func ValidateExecutable(binPath string) error {
	if binPath == "" {
		return errors.New("--bin-path is required")
	}

	info, err := os.Stat(binPath)
	if err != nil {
		return logInstallError(
			"agent-gate binary stat failed",
			fmt.Errorf("agent-gate binary not found at %s: %w", binPath, err),
			slog.String("path", binPath),
		)
	}

	if info.IsDir() {
		return fmt.Errorf("agent-gate binary path is a directory: %s", binPath)
	}

	if info.Mode().Perm()&executableModeMask == 0 {
		return fmt.Errorf("agent-gate binary is not executable: %s", binPath)
	}

	return nil
}

func prepareCodexHooks(templatesDir string, binPath string, targetPath string) ([]byte, error) {
	templateContent, err := readHookTemplate(templatesDir, "codex", "toml")
	if err != nil {
		return nil, err
	}

	quotedBinPath := shellCommandArgument(binPath)
	encodedBinPath := strconv.Quote(quotedBinPath)
	escapedBinPath := encodedBinPath[1 : len(encodedBinPath)-1]
	renderedTemplate := strings.ReplaceAll(
		string(templateContent),
		agentGatePlaceholder,
		escapedBinPath,
	)
	existingContent := ""
	if content, readErr := os.ReadFile(targetPath); readErr == nil {
		existingContent = string(content)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, logInstallError(
			"read Codex config failed",
			fmt.Errorf("read %s: %w", targetPath, readErr),
			slog.String("path", targetPath),
		)
	}

	if err := validateCodexManagedBlock(existingContent); err != nil {
		return nil, logInstallError(
			"validate Codex managed hooks failed",
			fmt.Errorf("validate %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
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
			return nil, logInstallError(
				"read hook template failed",
				fmt.Errorf("read hook template %s: %w", name, err),
				slog.String("name", name),
			)
		}
	}

	assetPath := filepath.Join("hooks", name)
	content, err := fs.ReadFile(agentgate.InstallAssets, filepath.ToSlash(assetPath))
	if err != nil {
		return nil, logInstallError(
			"read embedded hook template failed",
			fmt.Errorf("read embedded hook template %s: %w", name, err),
			slog.String("name", name),
		)
	}

	return content, nil
}

func renderServiceTemplate(
	templatesDir string,
	platformDir string,
	name string,
	replacements map[string]string,
) (string, error) {
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
			return nil, logInstallError(
				"read service template failed",
				fmt.Errorf("read service template %s/%s: %w", platformDir, name, err),
				slog.String("platform", platformDir),
				slog.String("name", name),
			)
		}
	}

	assetPath := filepath.Join("packaging", platformDir, name)
	content, err := fs.ReadFile(agentgate.InstallAssets, filepath.ToSlash(assetPath))
	if err != nil {
		return nil, logInstallError(
			"read embedded service template failed",
			fmt.Errorf("read embedded service template %s/%s: %w", platformDir, name, err),
			slog.String("platform", platformDir),
			slog.String("name", name),
		)
	}

	return content, nil
}

func writeFileAtomic(targetPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), privateDirMode); err != nil {
		return logInstallError(
			"create install target parent failed",
			fmt.Errorf("create parent dir for %s: %w", targetPath, err),
			slog.String("path", filepath.Dir(targetPath)),
		)
	}

	tempFile, err := os.CreateTemp(filepath.Dir(targetPath), "."+filepath.Base(targetPath)+".*.tmp")
	if err != nil {
		return logInstallError(
			"create install temp file failed",
			fmt.Errorf("create temp file for %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
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

		return logInstallError(
			"chmod install temp file failed",
			fmt.Errorf("chmod temp file for %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
	}

	if _, err := tempFile.Write(content); err != nil {
		_ = tempFile.Close()

		return logInstallError(
			"write install temp file failed",
			fmt.Errorf("write temp file for %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
	}

	if err := tempFile.Close(); err != nil {
		return logInstallError(
			"close install temp file failed",
			fmt.Errorf("close temp file for %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		return logInstallError(
			"replace install target failed",
			fmt.Errorf("replace %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
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
