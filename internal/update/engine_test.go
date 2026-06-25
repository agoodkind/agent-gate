package update

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/version"
)

func TestSelectArchiveAssetMatchesRuntimePlatform(t *testing.T) {
	runtimeAssetName := "agent-gate_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	assets := []releaseAsset{
		{Name: runtimeAssetName, BrowserDownloadURL: "https://example.invalid/runtime"},
		{Name: "agent-gate_other_other.tar.gz", BrowserDownloadURL: "https://example.invalid/other"},
	}

	asset, err := selectArchiveAsset(assets)
	if err != nil {
		t.Fatalf("selectArchiveAsset() error: %v", err)
	}
	if asset.Name != runtimeAssetName {
		t.Fatalf("asset name = %q, want %q", asset.Name, runtimeAssetName)
	}
}

func TestChecksumFromAsset(t *testing.T) {
	asset := releaseAsset{Digest: "sha256:abc123"}
	if got := checksumFromAsset(asset); got != "abc123" {
		t.Fatalf("checksumFromAsset() = %q, want %q", got, "abc123")
	}
}

func TestChecksumFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checksums.txt")
	content := "abc123  agent-gate_darwin_arm64.tar.gz\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	got, err := checksumFromFile(path, "agent-gate_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatalf("checksumFromFile() error: %v", err)
	}
	if got != "abc123" {
		t.Fatalf("checksumFromFile() = %q, want %q", got, "abc123")
	}
}

func TestResolveOptionsDefaults(t *testing.T) {
	options := resolveOptions(Options{})
	if options.Config == nil {
		t.Fatal("Config = nil, want default config")
	}
	if options.Client == nil {
		t.Fatal("Client = nil, want default client")
	}
	if options.Client.Timeout != defaultHTTPTimeout {
		t.Fatalf("Client.Timeout = %s, want %s", options.Client.Timeout, defaultHTTPTimeout)
	}
	if !strings.Contains(options.CacheDir, "agent-gate") {
		t.Fatalf("CacheDir = %q, want agent-gate path", options.CacheDir)
	}
}

func TestReleaseIsNewer(t *testing.T) {
	testCases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "equal", current: "v1.2.3", latest: "v1.2.3", want: false},
		{name: "semver newer", current: "v1.2.3", latest: "v1.2.4", want: true},
		{name: "semver older", current: "v1.2.4", latest: "v1.2.3", want: false},
		{name: "timestamp newer", current: "202606210601-6a-a2d8820-2-g2c1e52b-dirty", latest: "202606220101-ab-1234567", want: true},
		{name: "timestamp older", current: "202606210601-6a-a2d8820-2-g2c1e52b-dirty", latest: "202606060459-4b-9822954", want: false},
		{name: "dev current", current: "dev", latest: "202606060459-4b-9822954", want: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := releaseIsNewer(testCase.current, testCase.latest)
			if got != testCase.want {
				t.Fatalf("releaseIsNewer(%q, %q) = %t, want %t", testCase.current, testCase.latest, got, testCase.want)
			}
		})
	}
}

func TestApplyDryRunIsIdempotent(t *testing.T) {
	originalWithLock := updateWithLock
	originalFetchLatestRelease := updateFetchLatestRelease
	originalDownloadFile := updateDownloadFile
	originalVerifyChecksum := updateVerifyChecksum
	originalVerifyGitHubAttestations := updateVerifyGitHubAttestations
	originalExtractCandidate := updateExtractCandidate
	originalValidateCandidate := updateValidateCandidate
	originalReplaceBinary := updateReplaceBinary
	t.Cleanup(func() {
		updateWithLock = originalWithLock
		updateFetchLatestRelease = originalFetchLatestRelease
		updateDownloadFile = originalDownloadFile
		updateVerifyChecksum = originalVerifyChecksum
		updateVerifyGitHubAttestations = originalVerifyGitHubAttestations
		updateExtractCandidate = originalExtractCandidate
		updateValidateCandidate = originalValidateCandidate
		updateReplaceBinary = originalReplaceBinary
	})

	runtimeAssetName := "agent-gate_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	statePath := filepath.Join(t.TempDir(), "update.json")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	candidatePath := filepath.Join(t.TempDir(), "agent-gate")
	if err := os.WriteFile(candidatePath, []byte("candidate"), 0o755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	updateWithLock = func(_ context.Context, fn func() error) error {
		return fn()
	}
	updateFetchLatestRelease = func(_ context.Context, _ Options) (release, error) {
		return release{
			HTMLURL: "https://example.invalid/release",
			TagName: "202606250140-71-dbb89ef",
			Assets: []releaseAsset{
				{
					Name:               runtimeAssetName,
					BrowserDownloadURL: "https://example.invalid/archive",
					Digest:             "sha256:deadbeef",
				},
			},
		}, nil
	}
	updateDownloadFile = func(_ context.Context, _ *http.Client, _ string, path string) error {
		return os.WriteFile(path, []byte("archive"), 0o600)
	}
	updateVerifyChecksum = func(_ context.Context, _ Options, _ release, _ releaseAsset, _ string) error {
		return nil
	}
	updateVerifyGitHubAttestations = func(_ context.Context, _ Options, _ release, _ releaseAsset, _ string) error {
		return nil
	}
	updateExtractCandidate = func(_ string) (string, func(), error) {
		return candidatePath, func() {}, nil
	}
	updateValidateCandidate = func(_ context.Context, _ string) error {
		return nil
	}
	updateReplaceBinary = func(_, _ string) error {
		t.Fatal("updateReplaceBinary() should not run during dry-run")
		return nil
	}

	options := Options{
		Config: &config.Config{
			Update: config.Update{
				Enabled:         nil,
				Mode:            config.UpdateModeApply,
				Interval:        "24h",
				Repo:            "agoodkind/agent-gate",
				AllowPrerelease: true,
			},
		},
		CacheDir:  cacheDir,
		StatePath: statePath,
		DryRun:    true,
	}

	firstResult, err := Apply(context.Background(), options)
	if err != nil {
		t.Fatalf("Apply() first error: %v", err)
	}
	secondResult, err := Apply(context.Background(), options)
	if err != nil {
		t.Fatalf("Apply() second error: %v", err)
	}

	for i, result := range []ApplyResult{firstResult, secondResult} {
		if !result.UpdateAvailable {
			t.Fatalf("result %d UpdateAvailable = false, want true", i)
		}
		if result.Applied {
			t.Fatalf("result %d Applied = true, want false", i)
		}
		if !result.DryRun {
			t.Fatalf("result %d DryRun = false, want true", i)
		}
		if result.LatestTag != "202606250140-71-dbb89ef" {
			t.Fatalf("result %d LatestTag = %q", i, result.LatestTag)
		}
	}

	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if state.LastResult != "dry_run" {
		t.Fatalf("LastResult = %q, want dry_run", state.LastResult)
	}
	if state.LatestTag != "202606250140-71-dbb89ef" {
		t.Fatalf("LatestTag = %q", state.LatestTag)
	}
	if state.LastError != "" {
		t.Fatalf("LastError = %q, want empty", state.LastError)
	}
}

func TestApplyCurrentIsIdempotent(t *testing.T) {
	originalWithLock := updateWithLock
	originalFetchLatestRelease := updateFetchLatestRelease
	originalDownloadFile := updateDownloadFile
	t.Cleanup(func() {
		updateWithLock = originalWithLock
		updateFetchLatestRelease = originalFetchLatestRelease
		updateDownloadFile = originalDownloadFile
	})

	runtimeAssetName := "agent-gate_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	statePath := filepath.Join(t.TempDir(), "update.json")
	downloadCallCount := 0

	updateWithLock = func(_ context.Context, fn func() error) error {
		return fn()
	}
	updateFetchLatestRelease = func(_ context.Context, _ Options) (release, error) {
		return release{
			HTMLURL: "https://example.invalid/release",
			TagName: version.Version,
			Assets: []releaseAsset{
				{
					Name:               runtimeAssetName,
					BrowserDownloadURL: "https://example.invalid/archive",
					Digest:             "sha256:deadbeef",
				},
			},
		}, nil
	}
	updateDownloadFile = func(_ context.Context, _ *http.Client, _, _ string) error {
		downloadCallCount++
		return nil
	}

	options := Options{
		Config: &config.Config{
			Update: config.Update{
				Enabled:         nil,
				Mode:            config.UpdateModeApply,
				Interval:        "24h",
				Repo:            "agoodkind/agent-gate",
				AllowPrerelease: true,
			},
		},
		StatePath: statePath,
	}

	firstResult, err := Apply(context.Background(), options)
	if err != nil {
		t.Fatalf("Apply() first error: %v", err)
	}
	secondResult, err := Apply(context.Background(), options)
	if err != nil {
		t.Fatalf("Apply() second error: %v", err)
	}

	for i, result := range []ApplyResult{firstResult, secondResult} {
		if result.UpdateAvailable {
			t.Fatalf("result %d UpdateAvailable = true, want false", i)
		}
		if result.Applied {
			t.Fatalf("result %d Applied = true, want false", i)
		}
	}
	if downloadCallCount != 0 {
		t.Fatalf("downloadCallCount = %d, want 0", downloadCallCount)
	}

	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if state.LastResult != "current" {
		t.Fatalf("LastResult = %q, want current", state.LastResult)
	}
	if state.AppliedTag != "" {
		t.Fatalf("AppliedTag = %q, want empty without prior applied state", state.AppliedTag)
	}
	if state.LastError != "" {
		t.Fatalf("LastError = %q, want empty", state.LastError)
	}
}

func TestApplyCurrentPreservesAppliedTag(t *testing.T) {
	originalWithLock := updateWithLock
	originalFetchLatestRelease := updateFetchLatestRelease
	t.Cleanup(func() {
		updateWithLock = originalWithLock
		updateFetchLatestRelease = originalFetchLatestRelease
	})

	runtimeAssetName := "agent-gate_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	statePath := filepath.Join(t.TempDir(), "update.json")
	if err := SaveState(statePath, State{AppliedTag: "202606250437-78-1cff09c"}); err != nil {
		t.Fatalf("SaveState() setup error: %v", err)
	}

	updateWithLock = func(_ context.Context, fn func() error) error {
		return fn()
	}
	updateFetchLatestRelease = func(_ context.Context, _ Options) (release, error) {
		return release{
			HTMLURL: "https://example.invalid/release",
			TagName: version.Version,
			Assets: []releaseAsset{
				{
					Name:               runtimeAssetName,
					BrowserDownloadURL: "https://example.invalid/archive",
					Digest:             "sha256:deadbeef",
				},
			},
		}, nil
	}

	options := Options{
		Config: &config.Config{
			Update: config.Update{
				Enabled:         nil,
				Mode:            config.UpdateModeApply,
				Interval:        "24h",
				Repo:            "agoodkind/agent-gate",
				AllowPrerelease: true,
			},
		},
		StatePath: statePath,
	}

	_, err := Apply(context.Background(), options)
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}

	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if state.AppliedTag != "202606250437-78-1cff09c" {
		t.Fatalf("AppliedTag = %q", state.AppliedTag)
	}
}
