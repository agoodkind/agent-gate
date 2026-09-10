package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/go-makefile/selfupdate"
)

func TestReloadKeepsReplacementUpdateSchedulerAlive(t *testing.T) {
	setDaemonTestDirs(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	requested := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/agoodkind/agent-gate/releases" {
			t.Errorf("unexpected update request: %s", request.URL.Path)
		}
		requested <- struct{}{}
		<-request.Context().Done()
		canceled <- struct{}{}
	}))
	t.Cleanup(backend.Close)
	t.Setenv("AGENT_GATE_UPDATE_API_BASE_URL", backend.URL)

	server, err := New(t.Context(), newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	server.configPath = filepath.Join(t.TempDir(), "candidate.toml")
	writeConfig(t, server.configPath, "[audit]\nenabled = false\n[update]\nenabled = true\nmode = \"check\"\ninterval = \"1h\"\n")
	initial, cancelInitial := context.WithCancel(t.Context())
	cancelInitial()
	server.StartUpdateScheduler(initial, func() { t.Error("check-only scheduler requested relaunch") })
	if err := selfupdate.SaveState(config.DefaultUpdateStatePath(), selfupdate.State{
		NextCheckAt: time.Now().Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.reloadConfig(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement update scheduler made no request after reload returned")
	}
	select {
	case <-canceled:
		t.Fatal("reload canceled the replacement update request")
	default:
	}
	server.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Server.Close did not cancel the active update request")
	}
	waitRuntimeCondition(t, func() bool {
		state, err := selfupdate.LoadState(config.DefaultUpdateStatePath())
		return err == nil && state.LastResult == "error"
	})
}
