package installer

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

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

// ServiceInstallationPlan retains one validated service file replacement.
type ServiceInstallationPlan struct {
	Platform   string
	TargetPath string
	Content    []byte
	Options    ServiceOptions
	stateDir   string
	content    []byte
	targetPath string
	options    ServiceOptions
	platform   servicePlatform
}

// ValidateServiceOptions verifies service inputs without writing service files.
func ValidateServiceOptions(options ServiceOptions) error {
	_, err := PrepareServiceInstallation(options)
	return err
}

// PrepareServiceInstallation renders and validates a service replacement without writing it.
func PrepareServiceInstallation(options ServiceOptions) (*ServiceInstallationPlan, error) {
	canonicalBinPath, err := CanonicalExecutablePath(options.BinPath)
	if err != nil {
		return nil, err
	}
	options.BinPath = canonicalBinPath
	if err := ValidateExecutable(options.BinPath); err != nil {
		return nil, err
	}
	homeDir, err := resolvedHomeDir(options.HomeDir)
	if err != nil {
		return nil, err
	}
	return prepareServiceInstallationForPlatform(options, homeDir, servicePlatform(runtime.GOOS))
}

func prepareServiceInstallationForPlatform(
	options ServiceOptions,
	homeDir string,
	platform servicePlatform,
) (*ServiceInstallationPlan, error) {
	var targetPath string
	var renderedTemplate string
	var stateDir string
	var err error
	switch platform {
	case servicePlatformDarwin:
		stateDir = defaultStateDir(homeDir, options.StateHome)
		targetPath = filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
		renderedTemplate, err = renderServiceTemplate(
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
		targetPath = filepath.Join(
			defaultConfigHome(homeDir, options.ConfigHome),
			"systemd",
			"user",
			systemdServiceName,
		)
		renderedTemplate, err = renderServiceTemplate(
			options.ServiceTemplatesDir,
			"systemd",
			systemdServiceTemplate,
			map[string]string{"@@BIN_PATH@@": systemdExecArgument(options.BinPath)},
		)
	default:
		return nil, fmt.Errorf("unsupported OS for service install: %s", platform)
	}
	if err != nil {
		return nil, err
	}
	return &ServiceInstallationPlan{
		Platform:   string(platform),
		TargetPath: targetPath,
		Content:    []byte(renderedTemplate),
		Options:    options,
		stateDir:   stateDir,
		content:    []byte(renderedTemplate),
		targetPath: targetPath,
		options:    options,
		platform:   platform,
	}, nil
}

func systemdExecArgument(value string) string {
	escaped := strings.ReplaceAll(value, "%", "%%")
	return strconv.Quote(escaped)
}

// InstallService writes and starts the per-user daemon service.
func InstallService(options ServiceOptions) error {
	plan, err := PrepareServiceInstallation(options)
	if err != nil {
		return err
	}
	return ApplyServiceInstallation(plan)
}

// ApplyServiceInstallation writes retained service bytes and starts the service.
func ApplyServiceInstallation(plan *ServiceInstallationPlan) error {
	if plan == nil {
		return errors.New("service installation plan is required")
	}
	runner := plan.options.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	writer := plan.options.Stdout
	if writer == nil {
		writer = io.Discard
	}
	switch plan.platform {
	case servicePlatformDarwin:
		return applyLaunchdService(plan, writer, runner)
	case servicePlatformLinux:
		return applySystemdService(plan, writer, runner)
	default:
		return fmt.Errorf("unsupported OS for service install: %s", plan.platform)
	}
}

func applyLaunchdService(plan *ServiceInstallationPlan, writer io.Writer, runner CommandRunner) error {
	options := plan.options
	targetPath := plan.targetPath
	if err := os.MkdirAll(filepath.Dir(targetPath), userConfigDirMode); err != nil {
		return logInstallError("create launchd dir failed", fmt.Errorf("create launchd dir: %w", err), slog.String("path", filepath.Dir(targetPath)))
	}
	if err := os.MkdirAll(plan.stateDir, userConfigDirMode); err != nil {
		return logInstallError("create state dir failed", fmt.Errorf("create state dir: %w", err), slog.String("path", plan.stateDir))
	}
	if err := writeFileAtomic(targetPath, plan.content); err != nil {
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

func applySystemdService(plan *ServiceInstallationPlan, writer io.Writer, runner CommandRunner) error {
	options := plan.options
	targetPath := plan.targetPath
	if err := os.MkdirAll(filepath.Dir(targetPath), userConfigDirMode); err != nil {
		return logInstallError("create systemd dir failed", fmt.Errorf("create systemd dir: %w", err), slog.String("path", filepath.Dir(targetPath)))
	}
	if err := writeFileAtomic(targetPath, plan.content); err != nil {
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
