package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestRunAuditStatusReportsFileSizes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := openFixtureIntake(t, t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Handle().Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runAudit([]string{"status", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d error=%s", code, stderr.String())
	}
	var status auditFileStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.DatabaseBytes != info.Size() || status.WALBytes != 0 {
		t.Fatalf("status=%+v file=%d", status, info.Size())
	}
}

func TestRunAuditRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"maintain", "compact"} {
		var stdout, stderr bytes.Buffer
		if code := runAudit([]string{command}, &stdout, &stderr); code != 2 {
			t.Fatalf("%s code=%d error=%s", command, code, stderr.String())
		}
	}
}

func setupAuditCommandEnvironment(t *testing.T, configBody string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
}

func createAuditCommandDatabase(t *testing.T) {
	t.Helper()
	store, err := openFixtureIntake(t, t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Handle().Close(); err != nil {
		t.Fatal(err)
	}
}
