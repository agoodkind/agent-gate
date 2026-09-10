package installer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrServiceAbsent is the typed result of a missing service or daemon query.
var ErrServiceAbsent = errors.New("service is absent")

// ServiceState describes the exact managed installation.
type ServiceState struct {
	Absent     bool
	Running    bool
	BinaryPath string
	PID        int
}

// ServiceStatusRunner keeps service inspection and control behind one OS boundary.
type ServiceStatusRunner interface {
	OutputContext(context.Context, string, ...string) ([]byte, error)
}

// ServiceStatusOptions selects the expected installation and service manager.
type ServiceStatusOptions struct {
	Platform   string
	BinaryPath string
	UserID     int
	Runner     ServiceStatusRunner
}

func normalizeServiceStatus(options ServiceStatusOptions) ServiceStatusOptions {
	if options.Platform == "" {
		options.Platform = runtime.GOOS
	}
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if options.UserID == 0 {
		options.UserID = os.Getuid()
	}
	return options
}

func launchdTarget(options ServiceStatusOptions) string {
	return "gui/" + strconv.Itoa(options.UserID) + "/" + launchdLabel
}

// InspectService refuses malformed output or a service for another executable.
func InspectService(ctx context.Context, options ServiceStatusOptions) (ServiceState, error) {
	options = normalizeServiceStatus(options)
	slog.DebugContext(ctx, "inspect reset service")
	state, arguments, err := readResetService(ctx, options)
	if err != nil || state.Absent {
		return state, err
	}
	if state.BinaryPath == "" || len(arguments) != 2 || arguments[1] != "daemon" {
		return state, errors.New("managed service does not run the expected daemon command")
	}
	for _, path := range []string{state.BinaryPath, arguments[0]} {
		canonical, err := CanonicalExecutablePath(path)
		if err != nil {
			return state, err
		}
		if canonical != options.BinaryPath {
			return state, fmt.Errorf("managed service executable %q differs from %q", canonical, options.BinaryPath)
		}
	}
	if state.Running && state.PID <= 1 {
		return state, errors.New("running service has no valid daemon PID")
	}
	return state, nil
}

func readResetService(ctx context.Context, options ServiceStatusOptions) (ServiceState, []string, error) {
	var state ServiceState
	var arguments []string
	switch servicePlatform(options.Platform) {
	case servicePlatformDarwin:
		output, err := options.Runner.OutputContext(ctx, "launchctl", "print", launchdTarget(options))
		if errors.Is(err, ErrServiceAbsent) || launchdAbsent(output, err) {
			state.Absent = true
			return state, nil, nil
		}
		if err != nil {
			return state, nil, resetFailure("inspect launchd job", err)
		}
		fields := serviceFields(string(output), " = ")
		state.BinaryPath = fields["program"]
		state.Running = fields["state"] == "running"
		state.PID, err = parseServicePID(fields["pid"])
		if err != nil {
			return state, nil, err
		}
		arguments = parseLaunchdArguments(string(output))
	case servicePlatformLinux:
		output, err := options.Runner.OutputContext(ctx, "systemctl", "--user", "show", systemdServiceName, "--property=LoadState", "--property=ActiveState", "--property=ExecStart", "--property=MainPID")
		if errors.Is(err, ErrServiceAbsent) {
			state.Absent = true
			return state, nil, nil
		}
		if err != nil {
			return state, nil, resetFailure("inspect systemd service", err)
		}
		fields := serviceFields(string(output), "=")
		if fields["LoadState"] == "not-found" {
			state.Absent = true
			return state, nil, nil
		}
		if fields["LoadState"] != "loaded" {
			return state, nil, errors.New("systemd service load state is not loaded")
		}
		state.Running = fields["ActiveState"] == "active" || fields["ActiveState"] == "activating" || fields["ActiveState"] == "deactivating"
		state.PID, err = parseServicePID(fields["MainPID"])
		if err != nil {
			return state, nil, err
		}
		matches := systemdResetProgram.FindStringSubmatch(fields["ExecStart"])
		if len(matches) == 2 {
			state.BinaryPath = matches[1]
		}
		_, value, found := strings.Cut(fields["ExecStart"], "argv[]=")
		if found {
			value, _, _ = strings.Cut(value, " ;")
			if value == state.BinaryPath+" daemon" {
				arguments = []string{state.BinaryPath, "daemon"}
			}
		}
	default:
		return state, nil, fmt.Errorf("unsupported service platform %q", options.Platform)
	}
	return state, arguments, nil
}

var systemdResetProgram = regexp.MustCompile(`(?:^|[ ;])path=([^;]+?) ;`)

func launchdAbsent(output []byte, err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() == 113 && strings.Contains(string(output), "Could not find service")
}

func serviceFields(output string, separator string) map[string]string {
	fields := make(map[string]string)
	for line := range strings.SplitSeq(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), separator)
		if ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	return fields
}

func parseServicePID(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid < 0 || pid == 1 {
		return 0, fmt.Errorf("invalid service PID %q", value)
	}
	return pid, nil
}

func parseLaunchdArguments(output string) []string {
	lines := strings.Split(output, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "arguments = {" {
			continue
		}
		var arguments []string
		for _, next := range lines[index+1:] {
			argument := strings.TrimSpace(next)
			if argument == "}" {
				return arguments
			}
			if argument != "" {
				arguments = append(arguments, argument)
			}
		}
	}
	return nil
}

// StopService unloads only the selected user job, then verifies its stopped state.
func StopService(ctx context.Context, options ServiceStatusOptions) error {
	slog.DebugContext(ctx, "stop reset service")
	options = normalizeServiceStatus(options)
	state, err := InspectService(ctx, options)
	if err != nil {
		return err
	}
	if state.Absent {
		return nil
	}
	switch servicePlatform(options.Platform) {
	case servicePlatformDarwin:
		_, err = options.Runner.OutputContext(ctx, "launchctl", "bootout", launchdTarget(options))
	case servicePlatformLinux:
		_, err = options.Runner.OutputContext(ctx, "systemctl", "--user", "stop", systemdServiceName)
		if err == nil {
			_, err = options.Runner.OutputContext(ctx, "systemctl", "--user", "disable", systemdServiceName)
		}
	}
	if err != nil {
		return resetFailure("stop managed service", err)
	}
	if servicePlatform(options.Platform) == servicePlatformDarwin {
		state, err = waitForResetServiceAbsence(ctx, options)
	} else {
		state, err = InspectService(ctx, options)
	}
	if err != nil {
		return err
	}
	if options.Platform == "darwin" && !state.Absent {
		return errors.New("launchd job remains loaded after bootout")
	}
	if state.Running || state.PID != 0 {
		return errors.New("managed service remains running after stop")
	}
	return nil
}

func waitForResetServiceAbsence(
	ctx context.Context,
	options ServiceStatusOptions,
) (ServiceState, error) {
	var state ServiceState
	for range serviceWaitAttempts {
		var err error
		state, err = InspectService(ctx, options)
		if err != nil || state.Absent {
			return state, err
		}
		timer := time.NewTimer(serviceWaitSleep)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return state, resetFailure("wait for launchd exit", ctx.Err())
		}
	}
	return state, nil
}

func daemonPIDs(ctx context.Context, options ServiceStatusOptions) ([]int, error) {
	options = normalizeServiceStatus(options)
	// The executable file identifies symlink-launched processes independently of argv.
	// Exclude this reset process, which runs the same installed executable.
	output, err := options.Runner.OutputContext(ctx, "lsof", "-t", "-a", "-u", strconv.Itoa(options.UserID), "-p", "^"+strconv.Itoa(os.Getpid()), "-d", "txt", "--", options.BinaryPath)
	var exitError *exec.ExitError
	if errors.Is(err, ErrServiceAbsent) || (errors.As(err, &exitError) && exitError.ExitCode() == 1 && len(output) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, resetFailure("enumerate matching daemons", err)
	}
	var pids []int
	for value := range strings.FieldsSeq(string(output)) {
		pid, err := parseServicePID(value)
		if err != nil || pid <= 1 {
			return nil, errors.New("daemon enumeration returned an invalid PID")
		}
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	var matched []int
	for _, pid := range slices.Compact(pids) {
		identity, err := (NativeProcessControl{}).Inspect(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, resetFailure("inspect executable process candidate", err)
		}
		if identity.PID == pid && identity.Start != "" && identity.Executable == options.BinaryPath {
			matched = append(matched, pid)
		}
	}
	return matched, nil
}
