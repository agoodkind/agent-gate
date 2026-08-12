// Command ci-auto-update verifies agent-gate's published automatic update path.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"goodkind.io/go-makefile/selfupdate"
)

const (
	githubAPIBaseURL    = "https://api.github.com"
	maxReleaseAttempts  = 10
	maxUpdateAttempts   = 120
	pollInterval        = 5 * time.Second
	requestTimeout      = 30 * time.Second
	processStopTimeout  = 15 * time.Second
	maxGitHubBodyBytes  = 4 << 20
	maxBinaryBytes      = 256 << 20
	diagnosticLineCount = 200
	testRootParent      = "/tmp"
	testRootPattern     = "agent-gate-auto-update-"
)

const daemonConfig = `[audit]
enabled = false

[update]
enabled = true
mode = "apply"
interval = "24h"
repo = "agoodkind/agent-gate"
allow_prerelease = true
`

type environment struct {
	repository string
	commit     string
	refType    string
	refName    string
	token      string
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Draft   bool          `json:"draft"`
	Assets  []githubAsset `json:"assets"`
}

type releaseSelection struct {
	target   githubRelease
	previous githubRelease
}

type childProcess struct {
	process  *os.Process
	done     chan error
	finished bool
	result   error
}

type updateCheck struct {
	environment environment
	testRoot    string
	testHome    string
	installBin  string
	daemonLog   string
	stateRoot   string
	configRoot  string
	cacheRoot   string
	runtimeRoot string
	stdout      io.Writer
	stderr      io.Writer
	child       *childProcess
}

func main() {
	slog.Info("ci.auto_update.main")
	os.Exit(realMain())
}

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.InfoContext(ctx, "ci.auto_update.invoked")
	if err := run(ctx, os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "auto-update check failed: %v\n", err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	return 0
}

func run(
	ctx context.Context,
	getenv func(string) string,
	stdout io.Writer,
	stderr io.Writer,
) (runErr error) {
	environment, err := loadEnvironment(getenv)
	if err != nil {
		return err
	}
	testRoot, err := os.MkdirTemp(testRootParent, testRootPattern)
	if err != nil {
		slog.ErrorContext(ctx, "ci.auto_update.root_create_failed", "err", err)
		return fmt.Errorf("create test root: %w", err)
	}
	slog.InfoContext(ctx, "ci.auto_update.started", "repository", environment.repository)
	check := &updateCheck{
		environment: environment,
		testRoot:    testRoot,
		testHome:    filepath.Join(testRoot, "home"),
		installBin:  filepath.Join(testRoot, "bin", "agent-gate"),
		daemonLog:   filepath.Join(testRoot, "daemon.log"),
		stateRoot:   filepath.Join(testRoot, "state"),
		configRoot:  filepath.Join(testRoot, "config"),
		cacheRoot:   filepath.Join(testRoot, "cache"),
		runtimeRoot: filepath.Join(testRoot, "run"),
		stdout:      stdout,
		stderr:      stderr,
	}
	defer func() {
		if cleanupErr := check.cleanup(); cleanupErr != nil {
			if runErr == nil {
				runErr = cleanupErr
				return
			}
			fmt.Fprintf(stderr, "auto-update cleanup failed: %v\n", cleanupErr)
		}
	}()
	if err := check.prepareDirectories(); err != nil {
		return err
	}
	selection, err := check.fetchReleaseSelection(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		stdout,
		"testing automatic update from %s to %s\n",
		selection.previous.TagName,
		selection.target.TagName,
	)
	if err := check.installOldRelease(ctx, selection.previous); err != nil {
		return err
	}
	if err := check.startDaemon(ctx); err != nil {
		return err
	}
	if err := check.waitForUpdate(ctx, selection.target.TagName); err != nil {
		check.printDiagnostics()
		return err
	}
	fmt.Fprintf(stdout, "automatic update applied: %s\n", selection.target.TagName)
	return nil
}

func loadEnvironment(getenv func(string) string) (environment, error) {
	environment := environment{
		repository: strings.TrimSpace(getenv("GITHUB_REPOSITORY")),
		commit:     strings.TrimSpace(getenv("GITHUB_SHA")),
		refType:    strings.TrimSpace(getenv("GITHUB_REF_TYPE")),
		refName:    strings.TrimSpace(getenv("GITHUB_REF_NAME")),
		token:      strings.TrimSpace(getenv("GH_TOKEN")),
	}
	required := []struct {
		name  string
		value string
	}{
		{name: "GITHUB_REPOSITORY", value: environment.repository},
		{name: "GITHUB_SHA", value: environment.commit},
		{name: "GITHUB_REF_TYPE", value: environment.refType},
		{name: "GITHUB_REF_NAME", value: environment.refName},
		{name: "GH_TOKEN", value: environment.token},
	}
	for _, variable := range required {
		if variable.value == "" {
			return environment, fmt.Errorf("%s is required", variable.name)
		}
	}
	if len(environment.commit) < 8 {
		return environment, fmt.Errorf("GITHUB_SHA must contain at least eight characters")
	}
	return environment, nil
}

func selectReleases(releases []githubRelease, environment environment) (releaseSelection, error) {
	eligible := make([]githubRelease, 0, len(releases))
	for _, release := range releases {
		if !release.Draft {
			eligible = append(eligible, release)
		}
	}
	targetTag := environment.refName
	if environment.refType != "tag" {
		targetTag = ""
		for _, release := range eligible {
			if releaseMatchesCommit(release.TagName, environment.commit) {
				targetTag = release.TagName
				break
			}
		}
	}
	if targetTag == "" {
		return releaseSelection{}, fmt.Errorf(
			"published release for commit %s was not found",
			environment.commit,
		)
	}
	for i, release := range eligible {
		if release.TagName != targetTag {
			continue
		}
		if i+1 >= len(eligible) {
			return releaseSelection{}, fmt.Errorf("release %s has no preceding release", targetTag)
		}
		return releaseSelection{target: release, previous: eligible[i+1]}, nil
	}
	return releaseSelection{}, fmt.Errorf("target release %s was not found", targetTag)
}

func releaseMatchesCommit(tag string, commit string) bool {
	_, suffix, found := strings.Cut(strings.TrimSpace(tag), "-")
	for found {
		var next string
		suffix, next, found = strings.Cut(suffix, "-")
		if found {
			suffix = next
		}
	}
	return len(suffix) >= 7 && strings.HasPrefix(commit, suffix)
}

func (check *updateCheck) fetchReleaseSelection(ctx context.Context) (releaseSelection, error) {
	client := &http.Client{Timeout: requestTimeout}
	var lastErr error
	for attempt := 1; attempt <= maxReleaseAttempts; attempt++ {
		releases, err := fetchGitHubReleases(ctx, client, check.environment)
		if err == nil {
			selection, selectErr := selectReleases(releases, check.environment)
			if selectErr == nil {
				return selection, nil
			}
			lastErr = selectErr
		} else {
			lastErr = err
		}
		fmt.Fprintf(check.stderr, "release selection attempt %d failed: %v\n", attempt, lastErr)
		if err := wait(ctx, pollInterval); err != nil {
			return releaseSelection{}, err
		}
	}
	slog.WarnContext(ctx, "ci.auto_update.release_selection_failed", "err", lastErr)
	return releaseSelection{}, fmt.Errorf("resolve target and preceding releases: %w", lastErr)
}

func fetchGitHubReleases(
	ctx context.Context,
	client *http.Client,
	environment environment,
) ([]githubRelease, error) {
	requestURL := githubAPIBaseURL + "/repos/" + environment.repository + "/releases?per_page=100"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		slog.WarnContext(ctx, "ci.auto_update.release_request_failed", "err", err)
		return nil, fmt.Errorf("build GitHub releases request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+environment.token)
	request.Header.Set("X-Github-Api-Version", "2022-11-28")
	response, err := client.Do(request)
	if err != nil {
		slog.WarnContext(ctx, "ci.auto_update.release_fetch_failed", "err", err)
		return nil, fmt.Errorf("query GitHub releases: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		slog.WarnContext(ctx, "ci.auto_update.release_fetch_rejected", "status", response.StatusCode)
		return nil, fmt.Errorf("query GitHub releases: HTTP %d", response.StatusCode)
	}
	var releases []githubRelease
	reader := io.LimitReader(response.Body, maxGitHubBodyBytes)
	if err := json.NewDecoder(reader).Decode(&releases); err != nil {
		slog.WarnContext(ctx, "ci.auto_update.release_decode_failed", "err", err)
		return nil, fmt.Errorf("decode GitHub releases: %w", err)
	}
	return releases, nil
}

func releaseAsset(release githubRelease, goos string, goarch string) (githubAsset, error) {
	assetName := fmt.Sprintf("agent-gate_%s_%s.tar.gz", goos, goarch)
	for _, asset := range release.Assets {
		if asset.Name == assetName && asset.BrowserDownloadURL != "" {
			return asset, nil
		}
	}
	return githubAsset{}, fmt.Errorf("release %s has no asset %s", release.TagName, assetName)
}

func (check *updateCheck) prepareDirectories() error {
	slog.Debug("ci.auto_update.prepare_started", "root", check.testRoot)
	for _, directory := range []string{
		check.testHome,
		filepath.Dir(check.installBin),
		check.stateRoot,
		check.configRoot,
		check.cacheRoot,
		check.runtimeRoot,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			slog.Warn("ci.auto_update.directory_create_failed", "path", directory, "err", err)
			return fmt.Errorf("create test directory %s: %w", directory, err)
		}
	}
	configPath := filepath.Join(check.configRoot, "agent-gate", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		slog.Warn("ci.auto_update.config_directory_create_failed", "path", configPath, "err", err)
		return fmt.Errorf("create daemon config directory: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(daemonConfig), 0o600); err != nil {
		slog.Warn("ci.auto_update.config_write_failed", "path", configPath, "err", err)
		return fmt.Errorf("write daemon config: %w", err)
	}
	return nil
}

func (check *updateCheck) installOldRelease(ctx context.Context, release githubRelease) error {
	asset, err := releaseAsset(release, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		slog.WarnContext(ctx, "ci.auto_update.old_release_request_failed", "err", err)
		return fmt.Errorf("build old release request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		slog.WarnContext(ctx, "ci.auto_update.old_release_download_failed", "err", err)
		return fmt.Errorf("download old release: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		slog.WarnContext(ctx, "ci.auto_update.old_release_download_rejected", "status", response.StatusCode)
		return fmt.Errorf("download old release: HTTP %d", response.StatusCode)
	}
	if err := extractBinary(response.Body, check.installBin); err != nil {
		return err
	}
	version, err := binaryVersion(ctx, check.installBin)
	if err != nil {
		return err
	}
	if version != release.TagName {
		slog.WarnContext(ctx, "ci.auto_update.old_release_version_mismatch", "got", version, "want", release.TagName)
		return fmt.Errorf("installed version is %q, expected %q", version, release.TagName)
	}
	return nil
}

func extractBinary(archive io.Reader, destination string) error {
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		slog.Warn("ci.auto_update.archive_open_failed", "err", err)
		return fmt.Errorf("open old release archive: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			slog.Warn("ci.auto_update.archive_read_failed", "err", nextErr)
			return fmt.Errorf("read old release archive: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "agent-gate" {
			continue
		}
		if header.Size <= 0 || header.Size > maxBinaryBytes {
			slog.Warn("ci.auto_update.archive_binary_size_invalid", "size", header.Size)
			return fmt.Errorf("old release binary has invalid size %d", header.Size)
		}
		output, openErr := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if openErr != nil {
			slog.Warn("ci.auto_update.binary_create_failed", "path", destination, "err", openErr)
			return fmt.Errorf("create old release binary: %w", openErr)
		}
		_, copyErr := io.CopyN(output, tarReader, header.Size)
		closeErr := output.Close()
		if copyErr != nil {
			slog.Warn("ci.auto_update.binary_extract_failed", "path", destination, "err", copyErr)
			return fmt.Errorf("extract old release binary: %w", copyErr)
		}
		if closeErr != nil {
			slog.Warn("ci.auto_update.binary_close_failed", "path", destination, "err", closeErr)
			return fmt.Errorf("close old release binary: %w", closeErr)
		}
		return nil
	}
	err = fmt.Errorf("old release archive has no agent-gate binary")
	slog.Warn("ci.auto_update.archive_binary_missing", "err", err)
	return err
}

func (check *updateCheck) startDaemon(ctx context.Context) error {
	logFile, err := os.OpenFile(check.daemonLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		slog.WarnContext(ctx, "ci.auto_update.daemon_log_create_failed", "path", check.daemonLog, "err", err)
		return fmt.Errorf("create daemon log: %w", err)
	}
	// #nosec G204 -- installBin is created under the validated temporary root.
	command := exec.CommandContext(context.WithoutCancel(ctx), check.installBin, "daemon")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = replaceEnvironment(os.Environ(), map[string]string{
		"HOME":            check.testHome,
		"XDG_CACHE_HOME":  check.cacheRoot,
		"XDG_CONFIG_HOME": check.configRoot,
		"XDG_RUNTIME_DIR": check.runtimeRoot,
		"XDG_STATE_HOME":  check.stateRoot,
	})
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		slog.WarnContext(ctx, "ci.auto_update.daemon_start_failed", "err", err)
		return fmt.Errorf("start daemon: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		var waitErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "ci.auto_update.daemon_wait_panic", "err", fmt.Sprintf("panic: %v", recovered))
				waitErr = fmt.Errorf("wait for daemon panic: %v", recovered)
			}
			if closeErr := logFile.Close(); waitErr == nil {
				waitErr = closeErr
			}
			done <- waitErr
		}()
		waitErr = command.Wait()
	}()
	check.child = &childProcess{process: command.Process, done: done}
	return nil
}

func (process *childProcess) status() (error, bool) {
	if process == nil {
		return nil, true
	}
	if process.finished {
		return process.result, true
	}
	select {
	case process.result = <-process.done:
		process.finished = true
		return process.result, true
	default:
		return nil, false
	}
}

func (process *childProcess) stop() error {
	if process == nil || process.finished {
		return nil
	}
	processGroupID := -process.process.Pid
	if err := syscall.Kill(processGroupID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Warn("ci.auto_update.daemon_signal_failed", "pid", process.process.Pid, "err", err)
		return fmt.Errorf("signal daemon process group: %w", err)
	}
	timer := time.NewTimer(processStopTimeout)
	defer timer.Stop()
	select {
	case process.result = <-process.done:
		process.finished = true
		return nil
	case <-timer.C:
	}
	if err := syscall.Kill(processGroupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Warn("ci.auto_update.daemon_kill_failed", "pid", process.process.Pid, "err", err)
		return fmt.Errorf("kill daemon process group: %w", err)
	}
	process.result = <-process.done
	process.finished = true
	return nil
}

func (check *updateCheck) waitForUpdate(ctx context.Context, targetTag string) error {
	for attempt := 1; attempt <= maxUpdateAttempts; attempt++ {
		version, versionErr := binaryVersion(ctx, check.installBin)
		applied, stateErr := stateReportsApplied(check.updateStatePath())
		if versionErr == nil && stateErr == nil && version == targetTag && applied {
			return nil
		}
		if processErr, exited := check.child.status(); exited {
			if processErr == nil {
				slog.WarnContext(ctx, "ci.auto_update.daemon_exited_early")
				return fmt.Errorf("daemon exited before applying the update")
			}
			slog.WarnContext(ctx, "ci.auto_update.daemon_exited", "err", processErr)
			return fmt.Errorf("daemon exited before applying the update: %w", processErr)
		}
		if err := wait(ctx, pollInterval); err != nil {
			return err
		}
	}
	err := fmt.Errorf("automatic update did not apply within ten minutes")
	slog.WarnContext(ctx, "ci.auto_update.timed_out", "err", err)
	return err
}

func stateReportsApplied(path string) (bool, error) {
	state, err := selfupdate.LoadState(path)
	if err != nil {
		slog.Warn("ci.auto_update.state_load_failed", "path", path, "err", err)
		return false, fmt.Errorf("load update state: %w", err)
	}
	return state.LastResult == "applied", nil
}

func (check *updateCheck) updateStatePath() string {
	return filepath.Join(check.stateRoot, "agent-gate", "update.json")
}

func binaryVersion(ctx context.Context, binary string) (string, error) {
	// #nosec G204 -- binary is created under the validated temporary root.
	command := exec.CommandContext(ctx, binary, "--version")
	var stdout strings.Builder
	var stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		slog.WarnContext(ctx, "ci.auto_update.version_failed", "binary", binary, "err", err)
		return "", fmt.Errorf("run %s --version: %w: %s", binary, err, strings.TrimSpace(stderr.String()))
	}
	return parseVersion(stdout.String())
}

func parseVersion(output string) (string, error) {
	for line := range strings.SplitSeq(output, "\n") {
		if version, found := strings.CutPrefix(line, "version:"); found {
			version = strings.TrimSpace(version)
			if version != "" {
				return version, nil
			}
		}
	}
	return "", fmt.Errorf("version output has no version field")
}

func (check *updateCheck) printDiagnostics() {
	fmt.Fprintln(check.stderr, "daemon output:")
	lines, err := tailLines(check.daemonLog, diagnosticLineCount)
	if err != nil {
		fmt.Fprintf(check.stderr, "(unavailable: %v)\n", err)
	} else {
		for _, line := range lines {
			fmt.Fprintln(check.stderr, line)
		}
	}
	fmt.Fprintln(check.stderr, "update state:")
	content, err := os.ReadFile(check.updateStatePath())
	if err != nil {
		fmt.Fprintf(check.stderr, "(unavailable: %v)\n", err)
		return
	}
	_, _ = check.stderr.Write(content)
}

func tailLines(path string, count int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		slog.Warn("ci.auto_update.diagnostic_open_failed", "path", path, "err", err)
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	lines := make([]string, 0, count)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if len(lines) == count {
			copy(lines, lines[1:])
			lines[count-1] = scanner.Text()
			continue
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("ci.auto_update.diagnostic_scan_failed", "path", path, "err", err)
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return lines, nil
}

func (check *updateCheck) cleanup() error {
	var cleanupErrors []error
	if check.child != nil {
		if err := check.child.stop(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := removeTestRoot(check.testRoot); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	err := errors.Join(cleanupErrors...)
	if err != nil {
		slog.Warn("ci.auto_update.cleanup_failed", "err", err)
	}
	return err
}

func removeTestRoot(path string) error {
	cleaned := filepath.Clean(path)
	relative, err := filepath.Rel(testRootParent, cleaned)
	if err != nil {
		slog.Warn("ci.auto_update.root_resolve_failed", "path", path, "err", err)
		return fmt.Errorf("resolve test root %s: %w", path, err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("refuse to remove test root outside the temp directory: %s", path)
	}
	if !strings.HasPrefix(filepath.Base(cleaned), testRootPattern) {
		return fmt.Errorf("refuse to remove unexpected test root %s", path)
	}
	if err := os.RemoveAll(cleaned); err != nil {
		slog.Warn("ci.auto_update.root_remove_failed", "path", cleaned, "err", err)
		return fmt.Errorf("remove test root %s: %w", cleaned, err)
	}
	return nil
}

func replaceEnvironment(current []string, replacements map[string]string) []string {
	result := make([]string, 0, len(current)+len(replacements))
	for _, entry := range current {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := replacements[name]; replaced {
				continue
			}
		}
		result = append(result, entry)
	}
	for name, value := range replacements {
		result = append(result, name+"="+value)
	}
	return result
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		slog.WarnContext(ctx, "ci.auto_update.wait_interrupted", "err", ctx.Err())
		return fmt.Errorf("wait interrupted: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
