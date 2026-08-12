package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/go-makefile/selfupdate"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAuthenticatedProxyAddsTokenAndPreservesRequest(t *testing.T) {
	t.Parallel()
	target, err := url.Parse("https://api.github.com")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	var receivedRequest *http.Request
	proxy := authenticatedProxy(target, "ci-token")
	proxy.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		receivedRequest = request.Clone(request.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"http://localhost/repos/fork/agent-gate/releases?per_page=100",
		nil,
	)

	proxy.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if receivedRequest == nil {
		t.Fatal("proxy did not forward request")
	}
	if got := receivedRequest.Header.Get("Authorization"); got != "Bearer ci-token" {
		t.Fatalf("Authorization = %q, want bearer token", got)
	}
	if receivedRequest.Host != "" {
		t.Fatalf("Host = %q, want URL host", receivedRequest.Host)
	}
	if got := receivedRequest.URL.String(); got != "https://api.github.com/repos/fork/agent-gate/releases?per_page=100" {
		t.Fatalf("URL = %q", got)
	}
}

func TestStartAuthenticatedProxyUsesLocalhost(t *testing.T) {
	t.Parallel()
	check := &updateCheck{environment: environment{token: "ci-token"}}
	if err := check.startAuthenticatedProxy(context.Background()); err != nil {
		t.Fatalf("startAuthenticatedProxy() error = %v", err)
	}
	t.Cleanup(check.apiProxy.Close)
	proxyURL, err := url.Parse(check.apiProxy.URL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if proxyURL.Hostname() != "localhost" {
		t.Fatalf("hostname = %q, want localhost", proxyURL.Hostname())
	}
}

func TestSelectReleasesForBranchBuild(t *testing.T) {
	t.Parallel()
	releases := []githubRelease{
		{TagName: "202608121600-13b-abcdef1"},
		{TagName: "202608111500-13a-12345678"},
	}
	environment := environment{
		commit:  "abcdef1234567890",
		refType: "branch",
	}

	selection, err := selectReleases(releases, environment)
	if err != nil {
		t.Fatalf("selectReleases() error = %v", err)
	}
	if selection.target.TagName != releases[0].TagName {
		t.Fatalf("target = %q, want %q", selection.target.TagName, releases[0].TagName)
	}
	if selection.previous.TagName != releases[1].TagName {
		t.Fatalf("previous = %q, want %q", selection.previous.TagName, releases[1].TagName)
	}
}

func TestSelectReleasesSkipsDrafts(t *testing.T) {
	t.Parallel()
	releases := []githubRelease{
		{TagName: "draft", Draft: true},
		{TagName: "202608121600-13b-abcdef12"},
		{TagName: "202608111500-13a-12345678"},
	}
	environment := environment{
		commit:  "abcdef1234567890",
		refType: "branch",
	}

	selection, err := selectReleases(releases, environment)
	if err != nil {
		t.Fatalf("selectReleases() error = %v", err)
	}
	if selection.previous.TagName != releases[2].TagName {
		t.Fatalf("previous = %q, want %q", selection.previous.TagName, releases[2].TagName)
	}
}

func TestReleaseAssetFindsCurrentPlatform(t *testing.T) {
	t.Parallel()
	release := githubRelease{Assets: []githubAsset{
		{Name: "agent-gate_linux_arm64.tar.gz", BrowserDownloadURL: "arm"},
		{Name: "agent-gate_linux_amd64.tar.gz", BrowserDownloadURL: "amd"},
	}}

	asset, err := releaseAsset(release, "linux", "amd64")
	if err != nil {
		t.Fatalf("releaseAsset() error = %v", err)
	}
	if asset.BrowserDownloadURL != "amd" {
		t.Fatalf("URL = %q, want %q", asset.BrowserDownloadURL, "amd")
	}
}

func TestPrepareDirectoriesUsesWorkflowRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	check := &updateCheck{
		environment: environment{repository: "fork-owner/agent-gate"},
		testRoot:    root,
		testHome:    filepath.Join(root, "home"),
		installBin:  filepath.Join(root, "bin", "agent-gate"),
		stateRoot:   filepath.Join(root, "state"),
		configRoot:  filepath.Join(root, "config"),
		cacheRoot:   filepath.Join(root, "cache"),
		runtimeRoot: filepath.Join(root, "run"),
	}

	if err := check.prepareDirectories(); err != nil {
		t.Fatalf("prepareDirectories() error = %v", err)
	}
	configPath := filepath.Join(check.configRoot, "agent-gate", "config.toml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(content), `repo = "fork-owner/agent-gate"`) {
		t.Fatalf("config = %q, want workflow repository", content)
	}
}

func TestStateReportsApplied(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "update.json")
	if err := selfupdate.SaveState(statePath, selfupdate.State{LastResult: "applied"}); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	applied, err := stateReportsApplied(statePath)
	if err != nil {
		t.Fatalf("stateReportsApplied() error = %v", err)
	}
	if !applied {
		t.Fatal("stateReportsApplied() = false, want true")
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()
	output := "version:   202608121600-13b-abcdef12\ncommit:    abcdef12\ndirty:     false\n"
	version, err := parseVersion(output)
	if err != nil {
		t.Fatalf("parseVersion() error = %v", err)
	}
	if version != "202608121600-13b-abcdef12" {
		t.Fatalf("parseVersion() = %q", version)
	}
}

func TestExtractBinaryRejectsTruncatedArchive(t *testing.T) {
	t.Parallel()
	destination := filepath.Join(t.TempDir(), "agent-gate")
	if err := extractBinary(bytes.NewBufferString("truncated"), destination); err == nil {
		t.Fatal("extractBinary() error = nil, want truncated archive rejection")
	}
}

func TestExtractBinaryRejectsDeclaredSizeBomb(t *testing.T) {
	t.Parallel()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "agent-gate",
		Mode:     0o700,
		Size:     maxBinaryBytes + 1,
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}

	destination := filepath.Join(t.TempDir(), "agent-gate")
	if err := extractBinary(&archive, destination); err == nil {
		t.Fatal("extractBinary() error = nil, want size rejection")
	}
}

func TestRemoveTestRootRejectsTempDirectory(t *testing.T) {
	t.Parallel()
	if err := removeTestRoot(os.TempDir()); err == nil {
		t.Fatal("removeTestRoot() error = nil, want refusal")
	}
}

func TestRemoveTestRootRemovesOwnedDirectory(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp(testRootParent, testRootPattern)
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	if err := removeTestRoot(root); err != nil {
		t.Fatalf("removeTestRoot() error = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
}
