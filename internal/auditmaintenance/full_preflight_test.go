package auditmaintenance_test

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/intake"
)

const fullCompactSafetyMargin = uint64(64 * 1024 * 1024)

func TestFullCompactDryRunDoesNotWriteDatabase(t *testing.T) {
	path := createFullCompactDatabase(t)
	before := snapshotFullCompactFiles(t, path)
	plan, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path: path,
		Service: installer.ServiceState{
			Platform: "launchd", Managed: true, Running: true, BinaryPath: "/opt/agent-gate",
		},
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err != nil {
		t.Fatalf("PreviewFullCompact: %v", err)
	}
	if !plan.IntegrityOK || plan.HookImpact != "operator-controlled offline interval" {
		t.Fatalf("plan = %#v", plan)
	}
	wantRequired := uint64(plan.DatabaseSize.DatabaseBytes+plan.DatabaseSize.WALBytes) +
		fullCompactSafetyMargin
	if plan.RequiredFreeBytes != wantRequired {
		t.Fatalf("required bytes = %d, want %d", plan.RequiredFreeBytes, wantRequired)
	}
	after := snapshotFullCompactFiles(t, path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("preflight changed database: before=%#v after=%#v", before, after)
	}
}

func TestFullCompactDryRunMeasuresLiveWALWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), intake.Record{
		EventID: "live-wal", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	beforeDatabase := readFullCompactFile(t, path)
	beforeWAL := readFullCompactFile(t, path+"-wal")
	plan, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path:      path,
		Service:   installer.ServiceState{Platform: "launchd", Managed: true, Running: true},
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DatabaseSize.WALBytes <= 0 {
		t.Fatalf("WAL bytes = %d, want positive", plan.DatabaseSize.WALBytes)
	}
	afterDatabase := readFullCompactFile(t, path)
	afterWAL := readFullCompactFile(t, path+"-wal")
	if beforeDatabase != afterDatabase || beforeWAL != afterWAL {
		t.Fatal("live-WAL preflight changed the database or write-ahead log")
	}
}

func TestFullCompactPlanUsesStableJSONFields(t *testing.T) {
	encoded, err := json.Marshal(auditmaintenance.FullCompactPlan{
		DatabasePath: "/tmp/audit.db",
		DatabaseSize: auditmaintenance.DatabaseSize{DatabaseBytes: 10, WALBytes: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"database_path"`, `"database_size"`, `"database_bytes"`, `"wal_bytes"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("JSON = %s, want %s", encoded, field)
		}
	}
}

func TestFullCompactPreflightRequiresManagedServiceControl(t *testing.T) {
	path := createFullCompactDatabase(t)
	_, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path: path, Service: installer.ServiceState{Platform: "launchd", Managed: false},
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "managed service") {
		t.Fatalf("PreviewFullCompact error = %v, want managed service", err)
	}
}

func TestFullCompactPreflightRejectsInsufficientSpace(t *testing.T) {
	path := createFullCompactDatabase(t)
	_, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path:      path,
		Service:   installer.ServiceState{Platform: "systemd", Managed: true},
		FreeBytes: func(string) (uint64, error) { return 1, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient free space") {
		t.Fatalf("PreviewFullCompact error = %v, want insufficient space", err)
	}
}

func TestFullCompactPreflightRejectsIntegrityFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path:      path,
		Service:   installer.ServiceState{Platform: "launchd", Managed: true},
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("PreviewFullCompact error = %v, want integrity failure", err)
	}
}

func TestFullCompactPreflightReportsOperatorControlledOfflineImpact(t *testing.T) {
	path := createFullCompactDatabase(t)
	plan, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path:      path,
		Service:   installer.ServiceState{Platform: "launchd", Managed: true, Running: true},
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HookImpact != "operator-controlled offline interval" {
		t.Fatalf("hook impact = %q", plan.HookImpact)
	}
}

func TestFullCompactPreflightRejectsSymlinkDatabasePath(t *testing.T) {
	target := createFullCompactDatabase(t)
	link := filepath.Join(t.TempDir(), "audit.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path:      link,
		Service:   installer.ServiceState{Platform: "launchd", Managed: true},
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("PreviewFullCompact error = %v, want symbolic link rejection", err)
	}
}

func TestFullCompactPreflightCanonicalizesSymlinkParent(t *testing.T) {
	targetDirectory := t.TempDir()
	linkedDirectory := filepath.Join(t.TempDir(), "audit")
	if err := os.Symlink(targetDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(linkedDirectory, "audit.db")
	store, err := intake.OpenSQLite(t.Context(), inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var measuredDirectory string
	plan, err := auditmaintenance.PreviewFullCompact(t.Context(), auditmaintenance.FullCompactOptions{
		Path:    inputPath,
		Service: installer.ServiceState{Platform: "launchd", Managed: true},
		FreeBytes: func(path string) (uint64, error) {
			measuredDirectory = path
			return 1 << 40, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(targetDirectory)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(canonicalTarget, "audit.db")
	if plan.DatabasePath != wantPath || measuredDirectory != canonicalTarget {
		t.Fatalf(
			"database path/directory = %q/%q, want %q/%q",
			plan.DatabasePath,
			measuredDirectory,
			wantPath,
			canonicalTarget,
		)
	}
}

func createFullCompactDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func snapshotFullCompactFiles(t *testing.T, path string) map[string][32]byte {
	t.Helper()
	result := make(map[string][32]byte)
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		content, err := os.ReadFile(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[candidate] = sha256.Sum256(content)
	}
	return result
}

func readFullCompactFile(t *testing.T, path string) [32]byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(content)
}
