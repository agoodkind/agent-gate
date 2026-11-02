// Command prepare-analyzer installs and verifies the project analyzer.
package main

import (
	"context"
	"debug/buildinfo"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

type analyzer struct {
	root    string
	module  string
	version string
	sum     string
}

func main() {
	slog.Debug("prepare project analyzer")
	var tool analyzer
	flag.StringVar(&tool.root, "root", "", "project directory")
	flag.StringVar(&tool.module, "module", "", "expected module path")
	flag.StringVar(&tool.version, "version", "", "expected module version")
	flag.StringVar(&tool.sum, "sum", "", "expected module checksum")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := tool.prepare(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}

func (tool analyzer) prepare(ctx context.Context) (result error) {
	defer func() {
		if result != nil {
			slog.Error("prepare analyzer failed", slog.Any("err", result))
		}
	}()
	if tool.root == "" || tool.module == "" || tool.version == "" || tool.sum == "" {
		return errors.New("set -root, -module, -version, and -sum")
	}
	if filepath.Base(tool.version) != tool.version || tool.version == "." || tool.version == ".." {
		return fmt.Errorf("invalid module version %q", tool.version)
	}
	directory := filepath.Join(tool.root, ".make", "verified-tools", "staticcheck-extra", tool.version)
	destination := filepath.Join(directory, "staticcheck-extra")
	if err := tool.verify(destination); err == nil {
		return nil
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create analyzer directory: %w", err)
	}
	staging, err := os.MkdirTemp(directory, ".install-")
	if err != nil {
		return fmt.Errorf("create analyzer staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			result = errors.Join(result, fmt.Errorf("remove analyzer staging directory: %w", err))
		}
	}()
	absoluteStaging, err := filepath.Abs(staging)
	if err != nil {
		return fmt.Errorf("resolve analyzer staging directory: %w", err)
	}
	command := exec.CommandContext(ctx, "go", "install")
	command.Args = append(command.Args, tool.module+"/cmd/staticcheck-extra@"+tool.version)
	command.Env = installEnvironment(absoluteStaging)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("install analyzer: %w", err)
	}
	staged := filepath.Join(staging, "staticcheck-extra")
	if err := tool.verify(staged); err != nil {
		return fmt.Errorf("verify installed analyzer: %w", err)
	}
	if err := tool.verify(destination); err == nil {
		return nil
	}
	if err := os.Rename(staged, destination); err != nil {
		return fmt.Errorf("replace analyzer: %w", err)
	}
	return nil
}

func installEnvironment(directory string) []string {
	values := map[string]string{
		"GOBIN": directory, "GOWORK": "off", "GOOS": runtime.GOOS,
		"GOARCH": runtime.GOARCH, "CGO_ENABLED": "0",
	}
	environment := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			environment = append(environment, entry)
		}
	}
	for key, value := range values {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func (tool analyzer) verify(path string) (result error) {
	defer func() {
		if result != nil && !errors.Is(result, os.ErrNotExist) {
			slog.Warn("analyzer requires preparation", slog.String("path", path), slog.Any("err", result))
		}
	}()
	file, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("read analyzer executable: %w", err)
	}
	if !file.Mode().IsRegular() || file.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not a regular executable", path)
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read analyzer identity: %w", err)
	}
	module := info.Main
	if info.Path != tool.module+"/cmd/staticcheck-extra" || module.Path != tool.module ||
		module.Version != tool.version || module.Sum != tool.sum || module.Replace != nil {
		return fmt.Errorf("analyzer identity mismatch: got %s %s %s; require %s %s %s",
			module.Path, module.Version, module.Sum, tool.module, tool.version, tool.sum)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH {
		return fmt.Errorf("analyzer host mismatch: got %s/%s; require %s/%s",
			settings["GOOS"], settings["GOARCH"], runtime.GOOS, runtime.GOARCH)
	}
	return nil
}
