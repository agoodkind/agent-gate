package installer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// ServiceState describes the configured per-user daemon without changing it.
type ServiceState struct {
	Platform   string `json:"platform"`
	Managed    bool   `json:"managed"`
	Running    bool   `json:"running"`
	BinaryPath string `json:"binary_path"`
}

// ServiceStatusOptions configures read-only managed-service inspection.
type ServiceStatusOptions struct {
	Platform   string
	BinaryPath string
	UserID     int
	Runner     ServiceStatusRunner
}

// ServiceStatusRunner runs read-only managed-service inspection commands.
type ServiceStatusRunner interface {
	OutputContext(ctx context.Context, name string, args ...string) ([]byte, error)
}

// InspectService confirms the managed daemon identity and current running state.
func InspectService(ctx context.Context, options ServiceStatusOptions) (ServiceState, error) {
	if err := ctx.Err(); err != nil {
		return ServiceState{}, logServiceStatusError(
			ctx,
			"inspect managed service failed",
			fmt.Errorf("inspect managed service: %w", err),
		)
	}
	if strings.TrimSpace(options.BinaryPath) == "" {
		return ServiceState{}, errors.New("managed service binary path is required")
	}
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if options.Platform == "" {
		options.Platform = runtime.GOOS
	}
	if options.UserID == 0 {
		options.UserID = os.Getuid()
	}
	expectedBinary := filepath.Clean(options.BinaryPath)
	var state ServiceState
	var err error
	switch servicePlatform(options.Platform) {
	case servicePlatformDarwin:
		state, err = inspectLaunchdService(ctx, options.Runner, options.UserID, expectedBinary)
	case servicePlatformLinux:
		state, err = inspectSystemdService(ctx, options.Runner, expectedBinary)
	default:
		return ServiceState{}, fmt.Errorf("unsupported OS for service status: %s", options.Platform)
	}
	if err != nil {
		return ServiceState{}, err
	}
	if err := ctx.Err(); err != nil {
		return ServiceState{}, logServiceStatusError(
			ctx,
			"inspect managed service failed",
			fmt.Errorf("inspect managed service: %w", err),
		)
	}
	return state, nil
}

func inspectLaunchdService(
	ctx context.Context,
	runner ServiceStatusRunner,
	userID int,
	expectedBinary string,
) (ServiceState, error) {
	target := "gui/" + strconv.Itoa(userID) + "/" + launchdLabel
	output, err := runner.OutputContext(ctx, "launchctl", "print", target)
	if err != nil {
		return ServiceState{}, logServiceStatusError(
			ctx,
			"inspect launchd service failed",
			fmt.Errorf("inspect launchd service %s: %w", target, err),
		)
	}
	program := parseLaunchdProgram(string(output))
	if program == "" {
		return ServiceState{}, fmt.Errorf("inspect launchd service %s: program is missing", target)
	}
	arguments := parseLaunchdArguments(string(output))
	if filepath.Clean(program) != expectedBinary || !validServiceArguments(arguments, expectedBinary) {
		return ServiceState{}, fmt.Errorf(
			"managed service binary is %q, want %q daemon",
			program,
			expectedBinary,
		)
	}
	return ServiceState{
		Platform: "launchd", Managed: true,
		Running:    strings.Contains(string(output), "state = running"),
		BinaryPath: program,
	}, nil
}

func inspectSystemdService(
	ctx context.Context,
	runner ServiceStatusRunner,
	expectedBinary string,
) (ServiceState, error) {
	output, err := runner.OutputContext(
		ctx,
		"systemctl", "--user", "show", systemdServiceName,
		"--property=LoadState", "--property=ActiveState", "--property=ExecStart",
	)
	if err != nil {
		return ServiceState{}, logServiceStatusError(
			ctx,
			"inspect systemd service failed",
			fmt.Errorf("inspect systemd service %s: %w", systemdServiceName, err),
		)
	}
	fields := parseSystemdProperties(string(output))
	if fields["LoadState"] != "loaded" {
		return ServiceState{}, fmt.Errorf("managed service %s is not loaded", systemdServiceName)
	}
	program := parseSystemdProgram(fields["ExecStart"])
	if program == "" {
		return ServiceState{}, fmt.Errorf("inspect systemd service %s: ExecStart path is missing", systemdServiceName)
	}
	arguments := parseSystemdArguments(fields["ExecStart"])
	if filepath.Clean(program) != expectedBinary || !validServiceArguments(arguments, expectedBinary) {
		return ServiceState{}, fmt.Errorf(
			"managed service binary is %q, want %q daemon",
			program,
			expectedBinary,
		)
	}
	return ServiceState{
		Platform: "systemd", Managed: true,
		Running:    fields["ActiveState"] == "active",
		BinaryPath: program,
	}, nil
}

func parseLaunchdProgram(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " = ")
		if found && key == "program" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseLaunchdArguments(output string) []string {
	lines := strings.Split(output, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "arguments = {" {
			continue
		}
		arguments := make([]string, 0, 2)
		for _, argumentLine := range lines[index+1:] {
			argument := strings.TrimSpace(argumentLine)
			if argument == "}" {
				return arguments
			}
			if argument != "" {
				arguments = append(arguments, argument)
			}
		}
		return nil
	}
	return nil
}

func parseSystemdProperties(output string) map[string]string {
	properties := make(map[string]string)
	for line := range strings.SplitSeq(output, "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			properties[key] = value
		}
	}
	return properties
}

var systemdProgramPattern = regexp.MustCompile(`(?:^|[ ;])path=([^ ;]+)`)

func parseSystemdProgram(execStart string) string {
	matches := systemdProgramPattern.FindStringSubmatch(execStart)
	if len(matches) != 2 {
		return ""
	}
	return matches[1]
}

func parseSystemdArguments(execStart string) []string {
	const prefix = "argv[]="
	_, value, found := strings.Cut(execStart, prefix)
	if !found {
		return nil
	}
	if arguments, _, hasTerminator := strings.Cut(value, " ;"); hasTerminator {
		value = arguments
	}
	return strings.Fields(value)
}

func validServiceArguments(arguments []string, expectedBinary string) bool {
	return len(arguments) == 2 &&
		filepath.Clean(arguments[0]) == expectedBinary &&
		arguments[1] == "daemon"
}

func logServiceStatusError(ctx context.Context, message string, err error) error {
	slog.WarnContext(ctx, message, "err", err)
	return err
}
