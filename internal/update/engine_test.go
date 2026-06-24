package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
