// Package update implements release discovery, verification, and installation.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"goodkind.io/agent-gate/internal/clock"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/version"
)

const (
	defaultHTTPTimeout      = 30 * time.Second
	maxExtractedBinaryBytes = 128 * 1024 * 1024
)

var (
	updateWithLock                 = WithLock
	updateFetchLatestRelease       = fetchLatestRelease
	updateDownloadFile             = downloadFile
	updateVerifyChecksum           = verifyChecksum
	updateVerifyGitHubAttestations = verifyGitHubAttestations
	updateExtractCandidate         = extractCandidate
	updateValidateCandidate        = validateCandidate
	updateReplaceBinary            = replaceBinary
)

// Options configures one update check or apply operation.
type Options struct {
	Config      *config.Config
	Client      *http.Client
	InstallPath string
	CacheDir    string
	StatePath   string
	DryRun      bool
	Log         *slog.Logger
}

// CheckResult describes the current and latest release view.
type CheckResult struct {
	CurrentVersion   string
	CurrentCommit    string
	CurrentBuildHash string
	LatestTag        string
	LatestURL        string
	AssetName        string
	UpdateAvailable  bool
}

// ApplyResult describes one attempted apply operation.
type ApplyResult struct {
	CheckResult
	Applied bool
	DryRun  bool
}

type release struct {
	HTMLURL    string         `json:"html_url"`
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

// Check records the latest allowed release and whether an update is available.
func Check(ctx context.Context, options Options) (CheckResult, error) {
	resolvedOptions := resolveOptions(options)
	cfg := resolvedOptions.Config
	latest, err := updateFetchLatestRelease(ctx, resolvedOptions)
	if err != nil {
		recordCheckError(resolvedOptions, err)
		return CheckResult{}, err
	}
	asset, err := selectArchiveAsset(latest.Assets)
	if err != nil {
		recordCheckError(resolvedOptions, err)
		return CheckResult{}, err
	}
	result := CheckResult{
		CurrentVersion:   version.Version,
		CurrentCommit:    version.Commit,
		CurrentBuildHash: version.BuildHash(),
		LatestTag:        latest.TagName,
		LatestURL:        latest.HTMLURL,
		AssetName:        asset.Name,
		UpdateAvailable:  releaseIsNewer(version.Version, latest.TagName),
	}
	var state State
	state.LastCheckAt = clock.Now()
	state.LatestTag = result.LatestTag
	state.InstalledVersion = result.CurrentVersion
	state.InstalledCommit = result.CurrentCommit
	state.InstalledBuildHash = result.CurrentBuildHash
	state.LastResult = "check"
	if cfg != nil {
		state.NextCheckAt = state.LastCheckAt.Add(cfg.UpdateInterval())
	}
	if err := SaveState(resolvedOptions.StatePath, state); err != nil {
		return result, err
	}
	return result, nil
}

// Apply stages, verifies, and installs the latest allowed release.
func Apply(ctx context.Context, options Options) (ApplyResult, error) {
	resolvedOptions := resolveOptions(options)
	var result ApplyResult
	err := updateWithLock(ctx, func() error {
		check, checkErr := Check(ctx, resolvedOptions)
		if checkErr != nil {
			return checkErr
		}
		result.CheckResult = check
		result.DryRun = resolvedOptions.DryRun
		if !check.UpdateAvailable {
			return saveApplyState(resolvedOptions, result, "current", "")
		}
		return applyLatest(ctx, resolvedOptions, &result)
	})
	if err != nil {
		recordCheckError(resolvedOptions, err)
		return result, err
	}
	return result, nil
}

func applyLatest(ctx context.Context, options Options, result *ApplyResult) error {
	latest, err := updateFetchLatestRelease(ctx, options)
	if err != nil {
		options.Log.WarnContext(ctx, "update apply latest release lookup failed", "err", err)
		return err
	}
	asset, err := selectArchiveAsset(latest.Assets)
	if err != nil {
		options.Log.WarnContext(ctx, "update apply asset selection failed", "tag", latest.TagName, "err", err)
		return err
	}
	cacheDir := options.CacheDir
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		options.Log.WarnContext(ctx, "update apply cache dir create failed", "path", cacheDir, "err", err)
		return fmt.Errorf("create update cache dir: %w", err)
	}
	archivePath := filepath.Join(cacheDir, asset.Name)
	if err := updateDownloadFile(ctx, options.Client, asset.BrowserDownloadURL, archivePath); err != nil {
		return err
	}
	if err := updateVerifyChecksum(ctx, options, latest, asset, archivePath); err != nil {
		return err
	}
	if err := updateVerifyGitHubAttestations(ctx, options, latest, asset, archivePath); err != nil {
		return err
	}
	candidatePath, cleanup, err := updateExtractCandidate(archivePath)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := updateValidateCandidate(ctx, candidatePath); err != nil {
		return err
	}
	if result.DryRun {
		return saveApplyState(options, *result, "dry_run", "")
	}
	if err := updateReplaceBinary(candidatePath, options.InstallPath); err != nil {
		return err
	}
	result.Applied = true
	return saveApplyState(options, *result, "applied", "")
}

func resolveOptions(options Options) Options {
	if options.Config == nil {
		var cfg config.Config
		options.Config = &cfg
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if options.CacheDir == "" {
		options.CacheDir = filepath.Join(config.DefaultCacheDir(), "update")
	}
	if options.InstallPath == "" {
		if exe, err := os.Executable(); err == nil {
			options.InstallPath = exe
		}
	}
	if options.Log == nil {
		options.Log = slog.Default()
	}
	return options
}

func fetchLatestRelease(ctx context.Context, options Options) (release, error) {
	log := options.Log
	repo := options.Config.UpdateRepo()
	if options.Config.Update.AllowPrerelease {
		releases, err := fetchReleaseList(ctx, options.Client, repo)
		if err != nil {
			log.WarnContext(ctx, "update release list query failed", "repo", repo, "err", err)
			return release{}, err
		}
		for _, candidate := range releases {
			if !candidate.Draft {
				return candidate, nil
			}
		}
		noReleaseErr := fmt.Errorf("no non-draft releases found for %s", repo)
		log.WarnContext(ctx, "update release list had no eligible release", "repo", repo, "err", noReleaseErr)
		return release{}, noReleaseErr
	}
	url := releaseAPIBaseURL() + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.WarnContext(ctx, "update latest release request build failed", "repo", repo, "err", err)
		return release{}, fmt.Errorf("build latest release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := options.Client.Do(req)
	if err != nil {
		log.WarnContext(ctx, "update latest release request failed", "repo", repo, "err", err)
		return release{}, fmt.Errorf("query latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("query latest release: HTTP %d", resp.StatusCode)
		log.WarnContext(ctx, "update latest release status failed", "repo", repo, "status_code", resp.StatusCode, "err", err)
		return release{}, err
	}
	var latest release
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
		log.WarnContext(ctx, "update latest release decode failed", "repo", repo, "err", err)
		return release{}, fmt.Errorf("decode latest release: %w", err)
	}
	if latest.Draft || latest.Prerelease {
		err := fmt.Errorf("latest release %q is not an allowed stable release", latest.TagName)
		log.WarnContext(ctx, "update latest release rejected", "repo", repo, "tag", latest.TagName, "err", err)
		return release{}, err
	}
	return latest, nil
}

func fetchReleaseList(ctx context.Context, client *http.Client, repo string) ([]release, error) {
	log := slog.Default()
	url := releaseAPIBaseURL() + "/repos/" + repo + "/releases"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.WarnContext(ctx, "update release list request build failed", "repo", repo, "err", err)
		return nil, fmt.Errorf("build release list request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		log.WarnContext(ctx, "update release list request failed", "repo", repo, "err", err)
		return nil, fmt.Errorf("query releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("query releases: HTTP %d", resp.StatusCode)
		log.WarnContext(ctx, "update release list status failed", "repo", repo, "status_code", resp.StatusCode, "err", err)
		return nil, err
	}
	var releases []release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		log.WarnContext(ctx, "update release list decode failed", "repo", repo, "err", err)
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	return releases, nil
}

func selectArchiveAsset(assets []releaseAsset) (releaseAsset, error) {
	name := fmt.Sprintf("agent-gate_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for _, asset := range assets {
		if asset.Name == name && asset.BrowserDownloadURL != "" {
			return asset, nil
		}
	}
	return releaseAsset{}, fmt.Errorf("release asset %q not found", name)
}

func findAsset(assets []releaseAsset, name string) (releaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name && asset.BrowserDownloadURL != "" {
			return asset, true
		}
	}
	return releaseAsset{Name: "", BrowserDownloadURL: "", Digest: ""}, false
}

func downloadFile(ctx context.Context, client *http.Client, url string, path string) error {
	slog.InfoContext(ctx, "update download file", "url", url, "path", path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.WarnContext(ctx, "update download request build failed", "url", url, "err", err)
		return fmt.Errorf("build download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.WarnContext(ctx, "update download request failed", "url", url, "err", err)
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
		slog.WarnContext(ctx, "update download status failed", "url", url, "status_code", resp.StatusCode, "err", err)
		return err
	}
	tmpPath := path + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		slog.WarnContext(ctx, "update download temp open failed", "path", tmpPath, "err", err)
		return fmt.Errorf("open download temp: %w", err)
	}
	_, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		slog.WarnContext(ctx, "update download copy failed", "path", path, "err", copyErr)
		return fmt.Errorf("write download temp: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		slog.WarnContext(ctx, "update download close failed", "path", path, "err", closeErr)
		return fmt.Errorf("close download temp: %w", closeErr)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		slog.WarnContext(ctx, "update download replace failed", "path", path, "err", err)
		return fmt.Errorf("replace download: %w", err)
	}
	return nil
}

func verifyChecksum(ctx context.Context, options Options, latest release, asset releaseAsset, archivePath string) error {
	want := checksumFromAsset(asset)
	if want == "" {
		checksums, ok := findAsset(latest.Assets, "checksums.txt")
		if !ok {
			return fmt.Errorf("checksum unavailable for %s", asset.Name)
		}
		checksumsPath := filepath.Join(options.CacheDir, "checksums.txt")
		if err := downloadFile(ctx, options.Client, checksums.BrowserDownloadURL, checksumsPath); err != nil {
			return err
		}
		resolved, err := checksumFromFile(checksumsPath, asset.Name)
		if err != nil {
			return err
		}
		want = resolved
	}
	got, err := sha256File(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want, got) {
		return fmt.Errorf("checksum mismatch for %s", asset.Name)
	}
	return nil
}

func checksumFromAsset(asset releaseAsset) string {
	if digest, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok {
		return digest
	}
	return ""
}

func checksumFromFile(path string, name string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("update checksums read failed", "path", path, "err", err)
		return "", fmt.Errorf("read checksums: %w", err)
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == name {
			return fields[0], nil
		}
	}
	err = fmt.Errorf("checksum entry not found for %s", name)
	slog.Warn("update checksums entry missing", "path", path, "name", name, "err", err)
	return "", err
}

func extractCandidate(archivePath string) (string, func(), error) {
	slog.Info("update extract candidate", "archive", archivePath)
	tmpDir, err := os.MkdirTemp("", "agent-gate-update-*")
	if err != nil {
		slog.Warn("update extract dir create failed", "archive", archivePath, "err", err)
		return "", func() {}, fmt.Errorf("create extract dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	file, err := os.Open(archivePath)
	if err != nil {
		cleanup()
		slog.Warn("update archive open failed", "archive", archivePath, "err", err)
		return "", cleanup, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		cleanup()
		slog.Warn("update gzip reader open failed", "archive", archivePath, "err", err)
		return "", cleanup, fmt.Errorf("open gzip archive: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	candidatePath := filepath.Join(tmpDir, "agent-gate")
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			slog.Warn("update archive read failed", "archive", archivePath, "err", err)
			return "", cleanup, fmt.Errorf("read archive: %w", err)
		}
		if header.Name != "agent-gate" {
			continue
		}
		if header.Size <= 0 || header.Size > maxExtractedBinaryBytes {
			cleanup()
			sizeErr := fmt.Errorf("candidate size %d outside allowed range", header.Size)
			slog.Warn("update candidate size rejected", "archive", archivePath, "size", header.Size, "err", sizeErr)
			return "", cleanup, sizeErr
		}
		out, err := os.OpenFile(candidatePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			cleanup()
			slog.Warn("update candidate create failed", "path", candidatePath, "err", err)
			return "", cleanup, fmt.Errorf("create candidate: %w", err)
		}
		_, copyErr := io.CopyN(out, tarReader, header.Size)
		closeErr := out.Close()
		if copyErr != nil {
			cleanup()
			slog.Warn("update candidate write failed", "path", candidatePath, "err", copyErr)
			return "", cleanup, fmt.Errorf("write candidate: %w", copyErr)
		}
		if closeErr != nil {
			cleanup()
			slog.Warn("update candidate close failed", "path", candidatePath, "err", closeErr)
			return "", cleanup, fmt.Errorf("close candidate: %w", closeErr)
		}
		return candidatePath, cleanup, nil
	}
	cleanup()
	err = fmt.Errorf("archive did not contain agent-gate")
	slog.Warn("update candidate missing", "archive", archivePath, "err", err)
	return "", cleanup, err
}

func validateCandidate(ctx context.Context, candidatePath string) error {
	slog.InfoContext(ctx, "update validate candidate", "path", candidatePath)
	cmd := exec.CommandContext(ctx, candidatePath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.WarnContext(ctx, "update candidate version failed", "path", candidatePath, "err", err)
		return fmt.Errorf("candidate version failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), "version:") {
		err := fmt.Errorf("candidate version output did not include version")
		slog.WarnContext(ctx, "update candidate version output invalid", "path", candidatePath, "err", err)
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := verifyDarwinCodeSignature(ctx, candidatePath); err != nil {
			return err
		}
	}
	return nil
}

func verifyDarwinCodeSignature(ctx context.Context, candidatePath string) error {
	cmd := exec.CommandContext(ctx, "codesign", "--verify", "--strict", "--verbose=2", candidatePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.WarnContext(ctx, "update candidate codesign verify failed", "path", candidatePath, "err", err)
		return fmt.Errorf("candidate codesign verify failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func replaceBinary(candidatePath string, installPath string) error {
	slog.Info("update replace binary", "candidate", candidatePath, "install_path", installPath)
	if installPath == "" {
		err := fmt.Errorf("install path is empty")
		slog.Warn("update replace binary missing install path", "err", err)
		return err
	}
	targetDir := filepath.Dir(installPath)
	tmpPath := filepath.Join(targetDir, ".agent-gate-update-"+strconv.FormatInt(clock.Now().UnixNano(), 10))
	in, err := os.Open(candidatePath)
	if err != nil {
		slog.Warn("update candidate open failed", "path", candidatePath, "err", err)
		return fmt.Errorf("open candidate: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		slog.Warn("update install temp create failed", "path", tmpPath, "err", err)
		return fmt.Errorf("create install temp: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		slog.Warn("update install temp write failed", "path", tmpPath, "err", copyErr)
		return fmt.Errorf("write install temp: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		slog.Warn("update install temp close failed", "path", tmpPath, "err", closeErr)
		return fmt.Errorf("close install temp: %w", closeErr)
	}
	if err := os.Rename(tmpPath, installPath); err != nil {
		_ = os.Remove(tmpPath)
		slog.Warn("update install replace failed", "path", installPath, "err", err)
		return fmt.Errorf("replace installed binary: %w", err)
	}
	return nil
}

func sha256File(path string) (string, error) {
	slog.Info("update hash file", "path", path)
	file, err := os.Open(path)
	if err != nil {
		slog.Warn("update checksum input open failed", "path", path, "err", err)
		return "", fmt.Errorf("open checksum input: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		slog.Warn("update checksum input hash failed", "path", path, "err", err)
		return "", fmt.Errorf("hash checksum input: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func saveApplyState(options Options, result ApplyResult, status string, errorMessage string) error {
	var state State
	state.LastCheckAt = clock.Now()
	state.LatestTag = result.LatestTag
	state.AppliedTag = ""
	state.InstalledVersion = result.CurrentVersion
	state.InstalledCommit = result.CurrentCommit
	state.InstalledBuildHash = result.CurrentBuildHash
	state.LastResult = status
	state.LastError = errorMessage
	if options.Config != nil {
		state.NextCheckAt = state.LastCheckAt.Add(options.Config.UpdateInterval())
	}
	if result.Applied {
		state.AppliedTag = result.LatestTag
	}
	return SaveState(options.StatePath, state)
}

func recordCheckError(options Options, err error) {
	if err == nil {
		return
	}
	state, loadErr := LoadState(options.StatePath)
	if loadErr != nil {
		var emptyState State
		state = emptyState
	}
	state.LastCheckAt = clock.Now()
	state.LastResult = "error"
	state.LastError = err.Error()
	if options.Config != nil {
		state.NextCheckAt = state.LastCheckAt.Add(options.Config.UpdateInterval())
	}
	if saveErr := SaveState(options.StatePath, state); saveErr != nil && options.Log != nil {
		options.Log.Warn("save update error state failed", "err", saveErr)
	}
}

func releaseAPIBaseURL() string {
	override := strings.TrimSpace(os.Getenv("AGENT_GATE_UPDATE_API_BASE_URL"))
	if override == "" {
		return "https://api.github.com"
	}
	return strings.TrimRight(override, "/")
}

func releaseIsNewer(currentVersion string, latestTag string) bool {
	if latestTag == "" || latestTag == currentVersion {
		return false
	}
	if semver.IsValid(currentVersion) && semver.IsValid(latestTag) {
		return semver.Compare(latestTag, currentVersion) > 0
	}
	currentTimestamp := versionTimestampPrefix(currentVersion)
	latestTimestamp := versionTimestampPrefix(latestTag)
	if currentTimestamp != "" && latestTimestamp != "" {
		return latestTimestamp > currentTimestamp
	}
	if latestTimestamp != "" && (currentVersion == "dev" || currentVersion == "unknown") {
		return true
	}
	return true
}

func versionTimestampPrefix(value string) string {
	if len(value) < 12 {
		return ""
	}
	prefix := value[:12]
	for _, char := range prefix {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return prefix
}
